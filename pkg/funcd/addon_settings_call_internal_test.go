package funcd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/invoke"
	"github.com/pyvvo/funcd/internal/sensor"
	addonsvc "github.com/pyvvo/funcd/internal/services/addon"
)

// ADR-0341: a settings kind's Function applies a target's settings, called from the target's pass.

const identityProvidersPath = "/apis/funcd.io/v1alpha1/namespaces/%s/identityproviders"

// scenario: settings-functions-refused — a target's spec.functions that names a kind its Addon does not declare, or
// one kind twice, a settings object's spec.functions, and an App's part whose ref names a Function the App does not
// declare: 400 naming the entry, and nothing is stored.
func TestScenarioSettingsFunctionsRefused(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthySettingsAddon("rauthy"))
	for name, c := range map[string]struct {
		fns  []v1.AddonObjectFunction
		want string
	}{
		"a kind the Addon does not declare": {[]v1.AddonObjectFunction{{Kind: "RauthyGroup", Ref: "groups"}},
			`spec.functions[0].kind "RauthyGroup" is neither a settings kind of Addon "rauthy" nor the object's own engineless kind`},
		"one kind twice": {[]v1.AddonObjectFunction{{Kind: "RauthyPasswordPolicy", Ref: "a"}, {Kind: "RauthyPasswordPolicy", Ref: "b"}},
			`spec.functions[1].kind "RauthyPasswordPolicy" is named by spec.functions[0] too`},
	} {
		o := identityProvider("default")
		o.Spec.Functions = c.fns
		status, detail := e.send(t, http.MethodPost, fmt.Sprintf(identityProvidersPath, "default"), addonDevToken, o)
		require.Equal(t, http.StatusBadRequest, status, "%s: %s", name, detail)
		require.Contains(t, detail, c.want, name)
	}
	_, stored := e.object(t, "default", "login")
	require.False(t, stored, "nothing is stored")

	p := policy("default", "strict", "login", 8, 16)
	p.Spec.Functions = []v1.AddonObjectFunction{{Kind: "RauthyPasswordPolicy", Ref: "rauthy-settings"}}
	status, detail := e.send(t, http.MethodPost, fmt.Sprintf(policiesPath, "default"), addonDevToken, p)
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "runs no engine")

	a := loginApp("default", "web-auth")
	a.Spec.Addons[0].Functions = []v1.AddonObjectFunction{{Kind: "RauthyPasswordPolicy", Ref: "web-auth-settings"}}
	status, detail = e.send(t, http.MethodPost, fmt.Sprintf(appsPath, "default"), addonDevToken, a)
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, `spec.addons[0].functions[0].ref`)
	fn := settingsFn("default", "web-auth-login")
	a.Spec.Functions = []v1.AppFunction{{Name: "web-auth-settings", FunctionSpec: fn.Spec}}
	status, detail = e.send(t, http.MethodPost, fmt.Sprintf(appsPath, "default"), addonDevToken, a)
	require.Equal(t, http.StatusOK, status, "the App declares its Function: %s", detail)
}

// upstream is one Function's ready upstream at a fixed URL.
type upstream string

func (u upstream) Upstream(context.Context, activator.FunctionRef) (string, bool, error) {
	return string(u), true, nil
}

// ADR-0341 Decision 2: the call reaches the Function as a CloudEvent of type funcd.addon.settings from
// funcd://<namespace>/addon/<target>, the call its data, and the answer is the reply; an answer past 1 MiB, one that is
// no reply, a failed answer, and a Function that serves HTTP fail the call.
func TestAddonSettingsCallerSendsTheCall(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got eventing.CloudEvent
	status, answer := http.StatusOK, `{"results":[{"name":"strict","ok":true},{"name":"lax","ok":false,"reason":"TooShort"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	caller := addonSettingsCaller{inv: &sensor.HTTPInvoker{Endpoints: upstream(srv.URL), Caller: invoke.AddonSettings}}
	call := addonsvc.SettingsCall{Kind: "RauthyPasswordPolicy", Cause: "change",
		Target: addonsvc.SettingsTarget{Kind: "IdentityProvider", Name: "login", Alias: "idp"},
		Objects: []addonsvc.SettingsObject{{Name: "strict", UID: "7bc5ed56", Generation: 3,
			Config: json.RawMessage(`{"provider":"login","lengthMin":16}`)}}}
	ctx := context.Background()
	reply, err := caller.Call(ctx, "default", "rauthy-settings", call)
	require.NoError(t, err)
	require.Equal(t, addonsvc.SettingsReply{Results: []addonsvc.SettingsResult{{Name: "strict", OK: true},
		{Name: "lax", Reason: "TooShort"}}}, reply)
	mu.Lock()
	require.Equal(t, "funcd.addon.settings", got.Type)
	require.Equal(t, "funcd://default/addon/login", got.Source)
	require.JSONEq(t, `{"kind":"RauthyPasswordPolicy","cause":"change","target":{"kind":"IdentityProvider","name":"login",`+
		`"alias":"idp"},"objects":[{"name":"strict","uid":"7bc5ed56","generation":3,"config":{"provider":"login","lengthMin":16}}]}`,
		string(got.Data))
	mu.Unlock()

	for name, c := range map[string]struct {
		status int
		answer string
	}{
		"an answer past 1 MiB": {http.StatusOK, `{"results":[]}` + strings.Repeat(" ", settingsReplyLimit)},
		"no reply":             {http.StatusOK, "done"},
		"a failed answer":      {http.StatusInternalServerError, `{"error":"rauthy refused"}`},
	} {
		mu.Lock()
		status, answer = c.status, c.answer
		mu.Unlock()
		_, err := caller.Call(ctx, "default", "rauthy-settings", call)
		require.Error(t, err, name)
	}
	httpFn := addonSettingsCaller{inv: &sensor.HTTPInvoker{Endpoints: upstream(srv.URL), Caller: invoke.AddonSettings,
		Protocol: func(context.Context, activator.FunctionRef) (v1.FunctionProtocol, error) { return v1.ProtocolHTTP, nil }}}
	_, err = httpFn.Call(ctx, "default", "rauthy-settings", call)
	require.Error(t, err, "a Function that serves HTTP")
}

// ADR-0345 Decision 2: an engineless kind's call reaches its Function as a CloudEvent of type funcd.addon.objects from
// funcd://<namespace>/addon/<function>, the group's list its data, and the answer is the reply; a Function that serves
// HTTP fails the call.
func TestAddonObjectsCallerSendsTheCall(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got eventing.CloudEvent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"results":[{"name":"bunny-edge","ok":false,"reason":"AwaitingApproval"}]}`)
	}))
	t.Cleanup(srv.Close)
	caller := addonObjectsCaller{inv: &sensor.HTTPInvoker{Endpoints: upstream(srv.URL), Caller: invoke.AddonObjects}}
	call := addonsvc.ObjectsCall{Kind: "InfraStack", Cause: "change", Objects: []addonsvc.CallObject{{Name: "bunny-edge",
		UID: "9c2e41aa", Generation: 3, Deleting: true, Config: json.RawMessage(`{"job":"bunny-edge"}`)}}}
	ctx := context.Background()
	reply, err := caller.Call(ctx, "default", "infrastack-reconciler", call)
	require.NoError(t, err)
	require.Equal(t, addonsvc.SettingsReply{Results: []addonsvc.SettingsResult{{Name: "bunny-edge", Reason: "AwaitingApproval"}}}, reply)
	mu.Lock()
	require.Equal(t, "funcd.addon.objects", got.Type)
	require.Equal(t, "funcd://default/addon/infrastack-reconciler", got.Source)
	require.JSONEq(t, `{"kind":"InfraStack","cause":"change","objects":[{"name":"bunny-edge","uid":"9c2e41aa","generation":3,`+
		`"deleting":true,"config":{"job":"bunny-edge"}}]}`, string(got.Data))
	mu.Unlock()
	httpFn := addonObjectsCaller{inv: &sensor.HTTPInvoker{Endpoints: upstream(srv.URL), Caller: invoke.AddonObjects,
		Protocol: func(context.Context, activator.FunctionRef) (v1.FunctionProtocol, error) { return v1.ProtocolHTTP, nil }}}
	_, err = httpFn.Call(ctx, "default", "infrastack-reconciler", call)
	require.Error(t, err, "a Function that serves HTTP")
}
