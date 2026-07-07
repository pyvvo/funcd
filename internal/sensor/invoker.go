package sensor

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/activator"
	"github.com/green-0-rabbit/funcd/internal/eventing"
)

const contentTypeCE = "application/cloudevents+json"

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
	Client    *http.Client
}

// Invoke resolves fn's ready upstream (waking it if cold) and POSTs the CloudEvent. A transport/5xx/4xx
// failure is Unavailable; the caller records it on the Invocation (never silently dropped).
func (i *HTTPInvoker) Invoke(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, ev eventing.CloudEvent) error {
	ref := activator.FunctionRef{Namespace: ns, Name: fn}
	upstream, ready, err := i.Endpoints.Upstream(ctx, ref)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "resolve upstream for %s/%s", ns, fn)
	}
	if !ready || upstream == "" {
		if i.Waker == nil {
			return fault.Unavailablef(op, "function %s/%s has no ready upstream", ns, fn)
		}
		upstream, err = i.Waker.Wake(ctx, ref)
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "wake %s/%s", ns, fn)
		}
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return fault.Internalf(op, "marshal cloudevent: %v", err)
	}
	client := i.Client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, bytes.NewReader(body))
	if err != nil {
		return fault.Internalf(op, "build request: %v", err)
	}
	req.Header.Set("Content-Type", contentTypeCE)
	resp, err := client.Do(req)
	if err != nil {
		return fault.Unavailablef(op, "POST to %s/%s upstream: %v", ns, fn, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		return fault.Unavailablef(op, "function %s/%s returned status %d", ns, fn, resp.StatusCode)
	}
	return nil
}
