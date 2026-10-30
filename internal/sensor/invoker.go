package sensor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/invoke"
	"github.com/pyvvo/funcd/internal/platform/httpx"
)

const contentTypeCE = "application/cloudevents+json"

// maxErrorBody bounds how much of a failed function's answer is read into the delivery error.
const maxErrorBody = 1 << 10

// Waker wakes a scaled-to-zero function and returns its ready upstream (ADR-0033; the activator provides it).
type Waker interface {
	Wake(ctx context.Context, fn activator.FunctionRef) (upstream string, err error)
}

// HTTPInvoker invokes a Function with a CloudEvent, waking a cold target first (ADR-0033). This is the
// invoke/wake logic ADR-0108 REMOVED from the eventing Source, re-created here where the action lives —
// the Sensor's `function:` action. Built in pkg/funcd with the activator's Endpoints + Waker.
type HTTPInvoker struct {
	Endpoints activator.Endpoints
	Waker     Waker
	// Protocol, when set, refuses a target that serves HTTP before any request (ADR-0243 Decision 5).
	Protocol activator.ProtocolFunc
	Client   *http.Client // nil: a client of the invoker's own, built on the first call
	// Chain is the invokePlugin chain each call runs around its wake (ADR-0302); nil runs nothing.
	Chain *invoke.Chain
	// Caller is who the calls are: invoke.AppHook for the App reconciler's hooks; unset means invoke.SensorAction.
	Caller invoke.Caller

	defaultOnce   sync.Once
	defaultClient *http.Client
}

// Invoke runs the call's invokePlugin chain around the call (ADR-0302): Admit and Before, then it resolves fn's ready
// upstream (waking it if cold), POSTs the CloudEvent and runs After. A refusal, or a transport/5xx/4xx failure, is
// Unavailable, carrying the start of the answer; the caller records it on the Invocation (never silently dropped).
func (i *HTTPInvoker) Invoke(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, ev eventing.CloudEvent) error {
	ref := activator.FunctionRef{Namespace: ns, Name: fn}
	if err := activator.RefuseHTTP(ctx, i.Protocol, ref, op); err != nil {
		return err
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return fault.Internalf(op, "marshal cloudevent: %v", err)
	}
	// The upstream is known only after the wake, and Before runs first, so the URL is set below.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://upstream.invalid/", bytes.NewReader(body))
	if err != nil {
		return fault.Internalf(op, "build request: %v", err)
	}
	req.Header.Set("Content-Type", contentTypeCE)
	call := &invoke.Call{Caller: i.caller(), Target: ref, Request: req}
	if rf := i.Chain.Admit(ctx, call); rf != nil {
		return refused(ns, fn, rf)
	}
	if rf := i.Chain.Before(ctx, call); rf != nil {
		return refused(ns, fn, rf)
	}
	var upstream string
	if i.Waker != nil {
		// Wake a warm target too: Wake records the call as activity, so idle reclaim never fires while
		// actions keep arriving (issue #48); a ready target returns its upstream at once.
		up, err := i.Waker.Wake(ctx, ref)
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "wake %s/%s", ns, fn)
		}
		upstream = up
	} else {
		up, ready, err := i.Endpoints.Upstream(ctx, ref)
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "resolve upstream for %s/%s", ns, fn)
		}
		if !ready || up == "" {
			return fault.Unavailablef(op, "function %s/%s has no ready upstream", ns, fn)
		}
		upstream = up
	}
	target, err := url.Parse(upstream)
	if err != nil {
		return fault.Internalf(op, "build request: %v", err)
	}
	call.Request.URL, call.Request.Host = target, ""
	resp, err := i.client().Do(call.Request)
	if err != nil {
		return fault.Unavailablef(op, "POST to %s/%s upstream: %v", ns, fn, err)
	}
	defer httpx.CloseBody(resp.Body)
	if rf := i.Chain.After(ctx, call, invoke.NewAnswer(resp)); rf != nil {
		return refused(ns, fn, rf)
	}
	return answerError(ns, fn, resp.StatusCode, resp.Body)
}

// answerError is nil for a status below 400, else Unavailable with the start of the answer.
func answerError(ns v1.NamespaceName, fn v1.ObjectName, status int, body io.Reader) error {
	if status < http.StatusBadRequest {
		return nil
	}
	answer, _ := io.ReadAll(io.LimitReader(body, maxErrorBody))
	if answer = bytes.TrimSpace(answer); len(answer) == 0 {
		return fault.Unavailablef(op, "function %s/%s returned status %d", ns, fn, status)
	}
	return fault.Unavailablef(op, "function %s/%s returned status %d: %s", ns, fn, status, capMsg(string(answer)))
}

// refused maps a refusal as ADR-0302 Decision 3 says: one with its own status and body as the worker's answer, a
// problem as Unavailable wrapping it.
func refused(ns v1.NamespaceName, fn v1.ObjectName, rf *invoke.Refusal) error {
	if rf.Status != 0 {
		return answerError(ns, fn, rf.Status, bytes.NewReader(rf.Body))
	}
	return fault.Wrapf(rf, fault.Unavailable, op, "the call to %s/%s was refused", ns, fn)
}

func (i *HTTPInvoker) caller() invoke.Caller {
	if i.Caller == 0 {
		return invoke.SensorAction
	}
	return i.Caller
}

// client returns Client, or the invoker's default, built once so its calls reuse their connections.
func (i *HTTPInvoker) client() *http.Client {
	if i.Client != nil {
		return i.Client
	}
	i.defaultOnce.Do(func() { i.defaultClient = httpx.NodeClient(0) })
	return i.defaultClient
}
