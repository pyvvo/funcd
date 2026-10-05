package sensor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/eventing"
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
	Client    *http.Client // nil: a client of the invoker's own, built on the first call

	defaultOnce   sync.Once
	defaultClient *http.Client
}

// Invoke resolves fn's ready upstream (waking it if cold) and POSTs the CloudEvent. A transport/5xx/4xx
// failure is Unavailable, carrying the start of the function's answer; the caller records it on the
// Invocation (never silently dropped).
func (i *HTTPInvoker) Invoke(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, ev eventing.CloudEvent) error {
	ref := activator.FunctionRef{Namespace: ns, Name: fn}
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
	body, err := json.Marshal(ev)
	if err != nil {
		return fault.Internalf(op, "marshal cloudevent: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, bytes.NewReader(body))
	if err != nil {
		return fault.Internalf(op, "build request: %v", err)
	}
	req.Header.Set("Content-Type", contentTypeCE)
	resp, err := i.client().Do(req)
	if err != nil {
		return fault.Unavailablef(op, "POST to %s/%s upstream: %v", ns, fn, err)
	}
	defer httpx.CloseBody(resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		answer, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		if answer = bytes.TrimSpace(answer); len(answer) == 0 {
			return fault.Unavailablef(op, "function %s/%s returned status %d", ns, fn, resp.StatusCode)
		}
		return fault.Unavailablef(op, "function %s/%s returned status %d: %s", ns, fn, resp.StatusCode, capMsg(string(answer)))
	}
	return nil
}

// client returns Client, or the invoker's default, built once so its calls reuse their connections.
func (i *HTTPInvoker) client() *http.Client {
	if i.Client != nil {
		return i.Client
	}
	i.defaultOnce.Do(func() { i.defaultClient = httpx.NodeClient(0) })
	return i.defaultClient
}
