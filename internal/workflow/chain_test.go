package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/pyvvo/funcd/internal/contractcheck"
	"github.com/pyvvo/funcd/internal/invoke"
	"github.com/pyvvo/funcd/internal/testkit/plugintest"
)

// scenario: a-refused-call-wakes-nothing (the dispatcher entry)
func TestScenarioARefusedCallWakesNothing(t *testing.T) {
	t.Parallel()
	waker := &fakeWaker{upstream: "http://unused.invalid"}
	d, err := NewHTTPDispatcher(DispatchDeps{Endpoints: fakeEndpoints{}, Waker: waker, Grant: fakeGrant{allow: true},
		Chain: invoke.NewChain(plugintest.Refuser{})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Dispatch(context.Background(), dispatchReq("s-fn"))
	var rf *invoke.Refusal
	if !errors.As(err, &rf) || !isPermanent(err) {
		t.Fatalf("Dispatch = %v, want the refusal, permanent (a 403)", err)
	}
	if waker.called {
		t.Fatal("a refused step woke its function")
	}
}

// scenario: every-caller-runs-the-chain (the dispatcher entry)
func TestScenarioEveryCallerRunsTheChain(t *testing.T) {
	t.Parallel()
	rec := &plugintest.Recorder{}
	srv := echoServer(t, 200, `{"ok":true}`)
	d, err := NewHTTPDispatcher(DispatchDeps{Endpoints: fakeEndpoints{upstream: srv.URL, ready: true}, Grant: fakeGrant{allow: true},
		Chain: invoke.NewChain(rec)})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := d.Dispatch(context.Background(), dispatchReq("s-fn")); err != nil || string(out) != `{"ok":true}` {
		t.Fatalf("Dispatch = %s, %v", out, err)
	}
	if got := rec.Callers(); !slices.Equal(got, []invoke.Caller{invoke.WorkflowStep}) {
		t.Fatalf("callers = %v, want [WorkflowStep]", got)
	}
}

// scenario: an-after-plugin-replaces-the-answer (the dispatcher entry): the plugin's 500 problem is retryable, as a
// worker's own 500 is.
func TestScenarioAnAfterPluginReplacesTheAnswer(t *testing.T) {
	t.Parallel()
	for body, refused := range map[string]bool{plugintest.BadBody: true, `{"ok":true}`: false} {
		srv := echoServer(t, 200, body)
		d, err := NewHTTPDispatcher(DispatchDeps{Endpoints: fakeEndpoints{upstream: srv.URL, ready: true}, Grant: fakeGrant{allow: true},
			Chain: invoke.NewChain(plugintest.BadAnswer{})})
		if err != nil {
			t.Fatal(err)
		}
		out, err := d.Dispatch(context.Background(), dispatchReq("s-fn"))
		var rf *invoke.Refusal
		switch {
		case refused && (!errors.As(err, &rf) || isPermanent(err)):
			t.Fatalf("Dispatch(%s) = %v, want the refusal, retryable", body, err)
		case !refused && (err != nil || string(out) != body):
			t.Fatalf("Dispatch(%s) = %s, %v, want the answer", body, out, err)
		}
	}
}

// scenario: every-path-checks-the-input (the dispatcher entry): the contract's 422 fails the step permanently, as the
// shim's 422 did, and the step's function is never woken.
func TestScenarioEveryPathChecksTheInput(t *testing.T) {
	t.Parallel()
	waker := &fakeWaker{upstream: "http://unused.invalid"}
	d, err := NewHTTPDispatcher(DispatchDeps{Endpoints: fakeEndpoints{}, Waker: waker, Grant: fakeGrant{allow: true},
		Chain: invoke.NewChain(contractcheck.Plugins(plugintest.CompileContract(t, plugintest.EmailInput, `{}`))...)})
	if err != nil {
		t.Fatal(err)
	}
	req := dispatchReq("s-fn")
	req.Input = json.RawMessage(`{"email": "a b@example.com"}`)
	_, err = d.Dispatch(context.Background(), req)
	if !isPermanent(err) || !strings.Contains(err.Error(), "rejected the step (422)") || !strings.Contains(err.Error(), "/email") {
		t.Fatalf("Dispatch = %v, want a permanent 422 naming /email", err)
	}
	if waker.called {
		t.Fatal("a refused step woke its function")
	}
}
