package dataplane_test

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/invoke"
	"github.com/pyvvo/funcd/internal/testkit/plugintest"
)

// chainWith is the data plane's built-in chain with extra plugins after it (ADR-0302).
func chainWith(extra ...invoke.Plugin) dataplane.Option {
	return dataplane.WithChain(invoke.NewChain(append(dataplane.Plugins(nil, nil, nil, 0), extra...)...))
}

// call POSTs {} to /function/api in the default namespace, as an external call or, with internal, a fn-to-fn one.
func call(h http.Handler, internal bool, header map[string]string) *httptest.ResponseRecorder {
	return callWith(h, internal, `{}`, header)
}

// callBody is call with its own body.
func callBody(h http.Handler, internal bool, body string) *httptest.ResponseRecorder {
	return callWith(h, internal, body, nil)
}

func callWith(h http.Handler, internal bool, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "http://any/function/api", strings.NewReader(body))
	req.Header.Set("X-Funcd-Namespace", "default")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	if internal {
		req = req.WithContext(dataplane.WithInternal(req.Context()))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// warmAPI marks Function api warm, so the activator forwards without a wake.
func warmAPI() map[v1.ObjectName]string { return map[v1.ObjectName]string{"api": "u"} }

// scenario: a-refused-call-wakes-nothing (the data-plane entry)
func TestScenarioARefusedCallWakesNothing(t *testing.T) {
	t.Parallel()
	up := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("the worker was called") })
	h, st, scaler := authDoorUpstream(t, nil, nil, creds{}, up, chainWith(plugintest.Refuser{}))
	seedFunction(t, st, "api")
	for _, internal := range []bool{false, true} {
		require.Equal(t, http.StatusForbidden, call(h, internal, nil).Code)
	}
	require.Zero(t, scaler.count(), "a refused call wakes nothing")
}

// scenario: every-caller-runs-the-chain (the data-plane entry: External and FnToFn)
func TestScenarioEveryCallerRunsTheChain(t *testing.T) {
	t.Parallel()
	rec := &plugintest.Recorder{}
	var mu sync.Mutex
	var atWorker []int
	up := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		atWorker = append(atWorker, len(rec.Callers()))
		mu.Unlock()
	})
	h, st, _ := authDoorUpstream(t, warmAPI(), nil, creds{}, up, chainWith(rec))
	seedFunction(t, st, "api")
	require.Equal(t, http.StatusOK, call(h, false, nil).Code)
	require.Equal(t, http.StatusOK, call(h, true, nil).Code)
	require.Equal(t, []invoke.Caller{invoke.External, invoke.FnToFn}, rec.Callers())
	require.Equal(t, []int{1, 2}, atWorker, "each call was recorded before the worker received it")
}

// scenario: an-after-plugin-replaces-the-answer (the data-plane entry: External and FnToFn)
func TestScenarioAnAfterPluginReplacesTheAnswer(t *testing.T) {
	t.Parallel()
	up := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.Header.Get("X-Test-Answer")) })
	h, st, _ := authDoorUpstream(t, warmAPI(), nil, creds{}, up, chainWith(plugintest.BadAnswer{}))
	seedFunction(t, st, "api")
	for _, internal := range []bool{false, true} {
		bad := call(h, internal, map[string]string{"X-Test-Answer": plugintest.BadBody})
		require.Equal(t, http.StatusInternalServerError, bad.Code)
		require.Contains(t, bad.Body.String(), "the answer is bad")
		good := call(h, internal, map[string]string{"X-Test-Answer": `{"ok":true}`})
		require.Equal(t, http.StatusOK, good.Code)
		require.Equal(t, `{"ok":true}`, good.Body.String())
	}
}

// scenario: an-unread-answer-streams
func TestScenarioAnUnreadAnswerStreams(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	up := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: two\n\n")
	})
	peek := &plugintest.Peeker{}
	h, st, _ := authDoorUpstream(t, warmAPI(), nil, creds{}, up, chainWith(peek))
	seedProtocol(t, st, "api", v1.ProtocolHTTP)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/function/api", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	first := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		require.Equal(t, "data: one\n", line)
	case <-time.After(5 * time.Second):
		t.Fatal("the first event did not reach the caller before the stream ended")
	}
	once.Do(func() { close(release) })
	require.Equal(t, 1, peek.Seen())
}

// scenario: the-by-name-form-keeps-its-stance
func TestScenarioTheByNameFormKeepsItsStance(t *testing.T) {
	t.Parallel()
	up := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("the worker was called") })
	h, st, scaler := authDoorUpstream(t, nil, nil, creds{}, up)
	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "api", "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/x"
	fn.Spec.Ingress = &v1.Ingress{Auth: &v1.RouteAuth{Mode: v1.AuthAuthenticated}}
	_, err := st.Create(context.Background(), fn)
	require.NoError(t, err)

	require.Equal(t, http.StatusUnauthorized, authReq(t, h, "/function/api", "default", "").StatusCode,
		"an open namespace's by-name call takes the Function's own stance")
	seedNSAuth(t, st, "secure", v1.ExposureImplicit, v1.AuthAuthenticated)
	require.Equal(t, http.StatusUnauthorized, authReq(t, h, "/function/nope", "secure", "").StatusCode,
		"a missing Function answers 401, not 404, to an anonymous caller")
	require.Zero(t, scaler.count())
}

// The built-in plugins run in ADR-0302's order, at their points, on exactly the callers Decision 4 names.
func TestPluginsKeepTheirCallers(t *testing.T) {
	t.Parallel()
	callers := map[string][]invoke.Caller{
		"authn": {invoke.External}, "hold": {invoke.External, invoke.FnToFn}, "rate": {invoke.External},
		"credentials": {invoke.External}, "prefix": {invoke.External}, "envelope": {invoke.External}, "deadline": {invoke.External},
	}
	httpFn := &v1.Function{}
	httpFn.Spec.Protocol = v1.ProtocolHTTP
	var names []string
	for _, p := range dataplane.Plugins(nil, nil, nil, 0) {
		names = append(names, p.Name())
		_, admit := p.(invoke.AdmitPlugin)
		_, before := p.(invoke.BeforePlugin)
		require.Equal(t, p.Name() == "authn", admit && !before, "%s runs at Admit only if it is authn", p.Name())
		for _, caller := range []invoke.Caller{invoke.External, invoke.FnToFn, invoke.WorkflowStep, invoke.SensorAction, invoke.AppHook} {
			c := &invoke.Call{Caller: caller, Stance: v1.AuthAuthenticated, Function: httpFn, Request: httptest.NewRequest(http.MethodPost, "/", nil)}
			require.Equal(t, slices.Contains(callers[p.Name()], caller), p.Applies(c), "%s on a %v call", p.Name(), caller)
		}
	}
	require.Equal(t, []string{"authn", "hold", "rate", "credentials", "prefix", "envelope", "deadline"}, names)
}

// The answer check never runs on a 101: the tunnel after it is not an answer, so an After plugin that reads bodies
// leaves a WebSocket working.
func TestAnswerCheckSkipsAnUpgrade(t *testing.T) {
	t.Parallel()
	_, srv, _ := wsDataPlane(t, 0, chainWith(plugintest.BadAnswer{}))
	requireEcho(t, dialFunction(t, srv, "mode=echo"), "hi")
}
