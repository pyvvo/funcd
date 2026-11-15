//go:build !e2e

package funcd_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/workernode/local"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// callCauseEnv runs a platform whose workers are the test's handlers and whose nested in-flight cap is 1, with the call
// matrices' Functions in its default namespace: crash, big and held, of protocol p, which their workers serve, crash
// cutting its reply off after its first byte, big replying past 1 MiB and held keeping each call until the test ends;
// other, of the other protocol, has no worker, and gone is deleted through the store once caller links it. caller links
// them as c, b, h, o and g, and denied links big as d, which a Policy forbids it to call. One call of caller to held is
// in flight once it returns.
func callCauseEnv(t *testing.T, p v1.FunctionProtocol) *causeEnv {
	t.Helper()
	workers := newFakeWorkers()
	st := store.New(memory.New())
	plat := answersPlatform(t, funcd.WithStore(st), funcd.WithRuntime(workers), funcd.WithRuntimeShim("node", "shim.mjs"),
		funcd.WithNestedInFlightCap(1))
	c, base := answersClient(), "http://"+plat.Addr()
	t.Cleanup(c.CloseIdleConnections)
	holding, release := make(chan struct{}, 1), make(chan struct{})
	workers.serve(t, "crash", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "64")
		_, _ = io.WriteString(w, "{")
		rc := http.NewResponseController(w)
		_ = rc.Flush()
		if conn, _, err := rc.Hijack(); err == nil {
			_ = conn.Close()
		}
	})
	workers.serve(t, "big", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `"`+strings.Repeat("x", 1<<20)+`"`)
	})
	workers.serve(t, "held", func(_ http.ResponseWriter, r *http.Request) {
		select {
		case holding <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	// Registered after the workers' servers, so it runs before each closes: a server waits for its calls to end.
	t.Cleanup(func() { close(release) })
	for _, name := range []v1.ObjectName{"crash", "big", "held"} {
		callable(t, c, base, name, "", func(fn *v1.Function) { fn.Spec.Protocol = p })
	}
	other := function("other")
	if p != v1.ProtocolHTTP {
		other.Spec.Protocol = v1.ProtocolHTTP
	}
	create(t, c, base, other)
	create(t, c, base, function("gone"))
	caller := function("caller")
	caller.Spec.Bindings.Links = []v1.FunctionLink{{Alias: "c", Target: "crash"}, {Alias: "b", Target: "big"},
		{Alias: "h", Target: "held", Timeout: v1.Duration(5 * time.Minute)}, {Alias: "o", Target: "other"},
		{Alias: "g", Target: "gone"}}
	create(t, c, base, caller)
	// The collector and an App's prune delete through the store, past link-deletion-protection (ADR-0170); no version,
	// since gone's reconciler may write its status meanwhile.
	require.NoError(t, st.Delete(context.Background(), v1.KindFunction.GVK(), "default", "gone", ""))
	denied := function("denied")
	denied.Spec.Bindings.Links = []v1.FunctionLink{{Alias: "d", Target: "big"}}
	create(t, c, base, denied)
	create(t, c, base, &v1.Policy{TypeMeta: v1.TypeMeta{APIVersion: v1.KindPolicy.GVK().APIVersion(), Kind: v1.KindPolicy},
		ObjectMeta: v1.ObjectMeta{Name: "no-calls", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.PolicySpec{Cedar: `forbid(principal == Function::"default/denied", action == Action::"link::invoke", resource);`}})

	sock, err := funcd.LocalAPISocketForTest(plat, "default", "caller")
	require.NoError(t, err)
	path := "/invoke/h"
	if p == v1.ProtocolHTTP {
		path = "/fetch/h/"
	}
	go holdCall(sock, path)
	select {
	case <-holding:
	case <-time.After(10 * time.Second):
		t.Fatal("the call to held did not reach its worker")
	}
	return &causeEnv{p: plat}
}

// holdCall makes a call on the local API at sock, which held's worker keeps until the test ends.
func holdCall(sock, path string) {
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://local"+path, strings.NewReader(`{}`))
	if err != nil {
		return
	}
	if resp, err := client.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// TestInvokeCauseMatrix is invoke's matrix: each cause that POST /invoke/{alias} declares, provoked by one request.
func TestInvokeCauseMatrix(t *testing.T) {
	t.Parallel()
	runCauseMatrix(t, invokeCauseRows(callCauseEnv(t, "")))
}

// invokeCauseRows is invoke's matrix on env.
func invokeCauseRows(env *causeEnv) []causeRow {
	const endpoint = "POST /invoke/{alias}"
	invoke := func(alias, body string) causeRequest {
		return causeRequest{method: http.MethodPost, path: "/invoke/" + alias, body: body}
	}
	row := func(cause *fault.KnownCause, caller v1.ObjectName, request causeRequest) causeRow {
		return causeRow{endpoint: endpoint, cause: cause, env: env, caller: caller, request: request}
	}
	return []causeRow{
		row(local.ErrBodyTooLarge, "caller", invoke("b", `"`+strings.Repeat("x", 1<<20)+`"`)),
		row(local.ErrBodyUnreadable, "caller", causeRequest{
			raw: "POST /invoke/b HTTP/1.1\r\nHost: local\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n"}),
		row(local.ErrNoLink, "caller", invoke("ghost", `{}`)),
		row(local.ErrLinkTargetMissing, "caller", invoke("g", `{}`)),
		row(local.ErrTargetServesHTTP, "caller", invoke("o", `{}`)),
		row(local.ErrLinkDenied, "denied", invoke("d", `{}`)),
		row(local.ErrNestedCapReached, "caller", invoke("h", `{}`)),
		row(local.ErrReplyCutOff, "caller", invoke("c", `{}`)),
		row(local.ErrReplyTooLarge, "caller", invoke("b", `{}`)),
	}
}

// TestFetchCauseMatrix is fetch's matrix: each cause that /fetch/ declares, provoked by one request.
func TestFetchCauseMatrix(t *testing.T) {
	t.Parallel()
	runCauseMatrix(t, fetchCauseRows(callCauseEnv(t, v1.ProtocolHTTP)))
}

// fetchCauseRows is fetch's matrix on env.
func fetchCauseRows(env *causeEnv) []causeRow {
	const endpoint = "/fetch/"
	fetch := func(path string) causeRequest { return causeRequest{method: http.MethodGet, path: "/fetch/" + path} }
	row := func(cause *fault.KnownCause, caller v1.ObjectName, request causeRequest) causeRow {
		return causeRow{endpoint: endpoint, cause: cause, env: env, caller: caller, request: request}
	}
	return []causeRow{
		row(local.ErrFetchPathMalformed, "caller", fetch("b")),
		row(local.ErrBodyTooLarge, "caller", causeRequest{method: http.MethodPost, path: "/fetch/b/",
			body: strings.Repeat("x", 1<<20+1)}),
		row(local.ErrBodyUnreadable, "caller", causeRequest{
			raw: "POST /fetch/b/ HTTP/1.1\r\nHost: local\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n"}),
		row(local.ErrNoLink, "caller", fetch("ghost/")),
		row(local.ErrLinkTargetMissing, "caller", fetch("g/")),
		row(local.ErrTargetNotHTTP, "caller", fetch("o/")),
		row(local.ErrLinkDenied, "denied", fetch("d/")),
		row(local.ErrNestedCapReached, "caller", fetch("h/")),
		row(local.ErrReplyCutOff, "caller", fetch("c/")),
		row(local.ErrReplyTooLarge, "caller", fetch("b/")),
	}
}
