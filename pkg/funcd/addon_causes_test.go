//go:build !e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/workernode/local"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// addonEngines is the engine runtime of the add-on matrix (ADR-0338 Decision 6): each object's engine is Ready at the
// address its name gives, but starting's, which never is. echo and login answer every call, big past the matrix's
// 1 KiB limit, slow never within its 300 ms timeout, and cut cuts each answer off.
type addonEngines struct{ addrs map[v1.ObjectName]string }

func newAddonEngines(t *testing.T) addonEngines {
	t.Helper()
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) }) // before slow's Close, which waits for the held calls
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nshort")
		_ = buf.Flush()
		_ = conn.Close()
	}))
	t.Cleanup(cut.Close)
	ok := answering(t, http.StatusOK, "ok")
	return addonEngines{addrs: map[v1.ObjectName]string{"echo": ok, "login": ok,
		"big":  answering(t, http.StatusOK, strings.Repeat("a", 2048)),
		"slow": strings.TrimPrefix(slow.URL, "http://"), "cut": strings.TrimPrefix(cut.URL, "http://")}}
}

func (e addonEngines) Converge(_ context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	if spec.Ref.Name == "starting" {
		return provider.ProviderStatus{Reason: "Starting"}, nil
	}
	return provider.ProviderStatus{Running: 1, Ready: true, Address: e.addrs[spec.Ref.Name]}, nil
}

func (addonEngines) Teardown(context.Context, provider.ProviderRef) error { return nil }

// addonCauseEnv runs a platform whose default namespace holds the add-on matrix's objects: the EchoService objects of
// addonEngines, served at /, with /private private, and the IdentityProvider login, served under /auth/v1. binder binds
// each by its name, and denied binds echo, which a Policy forbids it to call. With held, the namespace is held once
// every engine but starting's is Ready.
func addonCauseEnv(t *testing.T, held bool) *causeEnv {
	t.Helper()
	hd, err := hold.Open(t.TempDir())
	require.NoError(t, err)
	p := answersPlatform(t, funcd.WithAddonRuntime(newAddonEngines(t), v1.AddonRuntimeContainerd),
		funcd.WithAddonRegistries("ghcr.io"), funcd.WithAddonCallLimits(300*time.Millisecond, 1024), funcd.WithHold(hd))
	c, base := answersClient(), "http://"+p.Addr()
	echo := addonFixture("echo", "EchoService")
	echo.Spec.BasePath, echo.Spec.PrivatePaths = "", []string{"/private"}
	for _, a := range []*v1.Addon{addonFixture("rauthy", "IdentityProvider"), echo} {
		ask{what: "the Addon " + string(a.Name), method: http.MethodPost, path: addonsPath, token: answersAdmin,
			body: jsonOf(t, a), status: http.StatusOK}.run(t, c, base)
	}
	objects := map[v1.ObjectName]string{"login": "identityproviders"}
	create(t, c, base, identityFixture("login"))
	binder, denied := function("binder"), function("denied")
	binder.Spec.Bindings.Addon = []v1.FunctionAddon{{Alias: "login", Object: "login"}}
	for _, name := range []v1.ObjectName{"echo", "big", "slow", "cut", "starting"} {
		o := identityFixture(name)
		o.Kind = "EchoService"
		createAt(t, c, base, "echoservices", o)
		objects[name] = "echoservices"
		binder.Spec.Bindings.Addon = append(binder.Spec.Bindings.Addon, v1.FunctionAddon{Alias: string(name), Object: name})
	}
	denied.Spec.Bindings.Addon = []v1.FunctionAddon{{Alias: "echo", Object: "echo"}}
	create(t, c, base, binder)
	create(t, c, base, denied)
	create(t, c, base, &v1.Policy{TypeMeta: v1.TypeMeta{APIVersion: v1.KindPolicy.GVK().APIVersion(), Kind: v1.KindPolicy},
		ObjectMeta: v1.ObjectMeta{Name: "no-addon-calls", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.PolicySpec{Cedar: `forbid(principal == Function::"default/denied", action == Action::"addon::call", resource);`}})
	for name, plural := range objects {
		if name != "starting" {
			require.Eventually(t, func() bool { return addonPhase(c, base, plural, name) == v1.PhaseReady },
				10*time.Second, 10*time.Millisecond, "%s %s is Ready", plural, name)
		}
	}
	if held {
		require.NoError(t, hd.HoldNamespace("default", hold.Marker{Reason: hold.ReasonData, Since: v1.NewTimestamp(time.Now())}))
	}
	return &causeEnv{p: p}
}

// createAt creates obj at its kind's plural, as an add-on object is written.
func createAt(t *testing.T, c *http.Client, base, plural string, obj v1.Object) {
	t.Helper()
	ask{what: "the fixture " + string(obj.GetObjectMeta().Name), method: http.MethodPost,
		path: "/apis/funcd.io/v1alpha1/namespaces/default/" + plural, token: answersAdmin, body: jsonOf(t, obj),
		status: http.StatusOK}.run(t, c, base)
}

// addonPhase is the phase of the add-on object default/name at plural, "" when it cannot be read.
func addonPhase(c *http.Client, base, plural string, name v1.ObjectName) v1.Phase {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		base+"/apis/funcd.io/v1alpha1/namespaces/default/"+plural+"/"+string(name), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+answersAdmin)
	resp, err := c.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	var o v1.AddonObject
	if json.NewDecoder(resp.Body).Decode(&o) != nil {
		return ""
	}
	return o.Status.Phase
}

// TestAddonCauseMatrix is the add-on facet's matrix: each cause that its two patterns declare, provoked by one request.
func TestAddonCauseMatrix(t *testing.T) {
	t.Parallel()
	runCauseMatrix(t, addonCauseRows(addonEnvs{main: addonCauseEnv(t, false), held: addonCauseEnv(t, true)}))
}

// addonEnvs are the platforms of the add-on matrix: main, and held, whose namespace is held.
type addonEnvs struct {
	main, held *causeEnv
}

// addonCauseRows is the add-on matrix over envs: the same causes on the pattern with a path and on the alias alone,
// which calls /.
func addonCauseRows(envs addonEnvs) []causeRow {
	var rows []causeRow
	for _, e := range []struct {
		endpoint, suffix, refused string
	}{
		{"/addon/{alias}/{path...}", "/x", "/addon/echo/private"},
		{"/addon/{alias}", "", "/addon/login"},
	} {
		get := func(alias string) causeRequest {
			return causeRequest{method: http.MethodGet, path: "/addon/" + alias + e.suffix}
		}
		row := func(cause *fault.KnownCause, env *causeEnv, caller v1.ObjectName, request causeRequest) causeRow {
			return causeRow{endpoint: e.endpoint, cause: cause, env: env, caller: caller, request: request}
		}
		rows = append(rows,
			row(local.ErrAddonNoBinding, envs.main, "binder", get("other")),
			row(local.ErrAddonForbidden, envs.main, "denied", get("echo")),
			row(local.ErrAddonPathRefused, envs.main, "binder", causeRequest{method: http.MethodGet, path: e.refused}),
			row(local.ErrAddonHeld, envs.held, "binder", get("echo")),
			row(local.ErrAddonEngineUnavailable, envs.main, "binder", get("starting")),
			row(local.ErrAddonTimeout, envs.main, "binder", get("slow")),
			row(local.ErrBodyTooLarge, envs.main, "binder", causeRequest{method: http.MethodPost,
				path: "/addon/echo" + e.suffix, body: strings.Repeat("b", 2048)}),
			row(local.ErrBodyUnreadable, envs.main, "binder", causeRequest{
				raw: "POST /addon/echo" + e.suffix + " HTTP/1.1\r\nHost: local\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n"}),
			row(local.ErrAddonReplyTooLarge, envs.main, "binder", get("big")),
			row(local.ErrAddonReplyCutOff, envs.main, "binder", get("cut")),
		)
	}
	return rows
}
