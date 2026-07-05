package workflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/activator"
)

type fakeEndpoints struct {
	upstream string
	ready    bool
}

func (f fakeEndpoints) Upstream(context.Context, activator.FunctionRef) (string, bool, error) {
	return f.upstream, f.ready, nil
}

type fakeWaker struct {
	upstream string
	called   bool
}

func (f *fakeWaker) Wake(context.Context, activator.FunctionRef) (string, error) {
	f.called = true
	return f.upstream, nil
}

type fakeGrant struct{ allow bool }

func (f fakeGrant) Allow(v1.NamespaceName, v1.ObjectName) bool { return f.allow }

func echoServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(attemptHeader) == "" {
			t.Error("missing X-Funcd-Attempt header")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dispatchReq(target string) DispatchRequest {
	return DispatchRequest{Namespace: "default", Run: "r1", Step: "s", Target: v1.ObjectName(target), Attempt: 1, Input: json.RawMessage(`{}`)}
}

// scenario: scale-to-zero-step-wakes — a not-ready function is woken, then invoked.
func TestDispatchWakesColdStep(t *testing.T) {
	srv := echoServer(t, 200, `{"ok":true}`)
	waker := &fakeWaker{upstream: srv.URL}
	d, err := NewHTTPDispatcher(DispatchDeps{
		Endpoints: fakeEndpoints{ready: false}, Waker: waker, Grant: fakeGrant{allow: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Dispatch(context.Background(), dispatchReq("s-fn"))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !waker.called {
		t.Fatal("cold function should have been woken")
	}
	if string(out) != `{"ok":true}` {
		t.Fatalf("output = %s", out)
	}
}

// scenario: undeclared-target-impossible — an ungranted target is Forbidden, no call.
func TestDispatchFailClosed(t *testing.T) {
	waker := &fakeWaker{upstream: "http://never"}
	d, _ := NewHTTPDispatcher(DispatchDeps{
		Endpoints: fakeEndpoints{upstream: "http://never", ready: true}, Waker: waker, Grant: fakeGrant{allow: false},
	})
	_, err := d.Dispatch(context.Background(), dispatchReq("evil"))
	if fault.KindOf(err) != fault.Forbidden {
		t.Fatalf("want Forbidden, got %v", err)
	}
	if waker.called {
		t.Fatal("must not touch the function when denied")
	}
}

// 4xx ⇒ permanent (not retried); 5xx ⇒ retryable.
func TestDispatchStatusClassification(t *testing.T) {
	t.Run("4xx-permanent", func(t *testing.T) {
		srv := echoServer(t, 422, "bad input")
		d, _ := NewHTTPDispatcher(DispatchDeps{Endpoints: fakeEndpoints{upstream: srv.URL, ready: true}, Grant: fakeGrant{allow: true}})
		_, err := d.Dispatch(context.Background(), dispatchReq("s"))
		if !isPermanent(err) {
			t.Fatalf("4xx should be permanent, got %v", err)
		}
	})
	t.Run("5xx-retryable", func(t *testing.T) {
		srv := echoServer(t, 503, "overloaded")
		d, _ := NewHTTPDispatcher(DispatchDeps{Endpoints: fakeEndpoints{upstream: srv.URL, ready: true}, Grant: fakeGrant{allow: true}})
		_, err := d.Dispatch(context.Background(), dispatchReq("s"))
		if err == nil || isPermanent(err) {
			t.Fatalf("5xx should be a retryable (non-permanent) error, got %v", err)
		}
	})
}
