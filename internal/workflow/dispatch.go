package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/activator"
)

// attemptHeader carries the 1-based attempt number to the step function so it can
// dedupe under at-least-once (ADR-0094).
const attemptHeader = "X-Funcd-Attempt"

// Waker wakes a scaled-to-zero function and returns its ready upstream (ADR-0033). A
// structural match for eventing.Waker, declared locally so the workflow package does
// not import a sibling feature.
type Waker interface {
	Wake(ctx context.Context, fn activator.FunctionRef) (upstream string, err error)
}

// Granter authorizes a step dispatch: only functions declared in a run's pinned spec
// may be invoked (spec-as-grant, ADR-0094). An undeclared target is Forbidden.
type Granter interface {
	Allow(ns v1.NamespaceName, target v1.ObjectName) bool
}

// HTTPDispatcher is the production Dispatcher: it resolves a step function's ready
// upstream (waking a scaled-to-zero one), POSTs the step input, and returns the
// response body as the step output. A 4xx is a permanent failure (contract
// rejection / Forbidden); a 5xx or transport error is retryable. It enforces
// spec-as-grant fail-closed before dispatching.
type HTTPDispatcher struct {
	endpoints activator.Endpoints
	waker     Waker
	grant     Granter
	client    *http.Client
	log       *slog.Logger
}

// DispatchDeps wires the HTTP dispatcher.
type DispatchDeps struct {
	Endpoints activator.Endpoints
	Waker     Waker
	Grant     Granter
	Client    *http.Client
	Logger    *slog.Logger
}

// NewHTTPDispatcher builds the production dispatcher.
func NewHTTPDispatcher(d DispatchDeps) (*HTTPDispatcher, error) {
	if d.Endpoints == nil {
		return nil, fault.Invalidf("workflow.dispatch", "Endpoints is required")
	}
	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	return &HTTPDispatcher{
		endpoints: d.Endpoints, waker: d.Waker, grant: d.Grant, client: client,
		log: log.With("component", "workflow.dispatch"),
	}, nil
}

// Dispatch resolves, wakes, and invokes the step function.
func (d *HTTPDispatcher) Dispatch(ctx context.Context, req DispatchRequest) (json.RawMessage, error) {
	const op = "workflow.dispatch"
	// Fail-closed: only a target declared in the run's pinned spec may be invoked.
	if d.grant != nil && !d.grant.Allow(req.Namespace, req.Target) {
		d.log.Warn("dispatch denied: undeclared target",
			"namespace", req.Namespace, "run", req.Run, "step", req.Step, "target", req.Target)
		return nil, fault.Forbiddenf(op, "target %q is not declared in run %q", req.Target, req.Run)
	}
	fn := activator.FunctionRef{Namespace: req.Namespace, Name: req.Target}
	upstream, ready, err := d.endpoints.Upstream(ctx, fn)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "resolve upstream for %s/%s", req.Namespace, req.Target)
	}
	if !ready || upstream == "" {
		if d.waker == nil {
			return nil, fault.Unavailablef(op, "function %s/%s has no ready upstream", req.Namespace, req.Target)
		}
		upstream, err = d.waker.Wake(ctx, fn)
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "wake %s/%s", req.Namespace, req.Target)
		}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, bytes.NewReader(req.Input))
	if err != nil {
		return nil, fault.Internalf(op, "build request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(attemptHeader, strconv.Itoa(req.Attempt))
	resp, err := d.client.Do(httpReq)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, op, "invoke %s/%s", req.Namespace, req.Target) // retryable
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, op, "read response from %s/%s", req.Namespace, req.Target)
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return body, nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return nil, Permanent(fault.Invalidf(op, "%s/%s rejected the step (%d): %s", req.Namespace, req.Target, resp.StatusCode, truncate(body)))
	default:
		return nil, fault.Unavailablef(op, "%s/%s returned %d", req.Namespace, req.Target, resp.StatusCode) // retryable
	}
}

func truncate(b []byte) string {
	const max = 256
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
