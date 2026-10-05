package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/platform/httpx"
)

// attemptHeader carries the 1-based attempt number to the step function so it can
// dedupe under at-least-once (ADR-0094).
const attemptHeader = "X-Funcd-Attempt"

// stepEvent wraps a step's input in a CloudEvent envelope (ADR-0094: dispatch reuses the eventing
// seam). A materialized step function is an ordinary funcd Function: the shim reads `event.data`
// and validates it against the step's I/O contract, so the flowing input (the run input, or a
// parent's output — verbatim) travels as the event's `data`. The id is deterministic
// (<run>-<step>-<attempt>) so retries share it under at-least-once. A step with no input still
// sends `data: null` (a valid CloudEvent), never an empty body.
func stepEvent(req DispatchRequest) []byte {
	data := req.Input
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	body, _ := json.Marshal(struct {
		SpecVersion string          `json:"specversion"`
		Type        string          `json:"type"`
		Source      string          `json:"source"`
		ID          string          `json:"id"`
		Data        json.RawMessage `json:"data"`
	}{
		SpecVersion: "1.0",
		Type:        "dev.funcd.workflow.step.v1",
		Source:      string(req.Namespace) + "/" + string(req.Run),
		ID:          string(req.Run) + "-" + string(req.Step) + "-" + strconv.Itoa(req.Attempt),
		Data:        data,
	})
	return body
}

// Waker wakes a scaled-to-zero function and returns its ready upstream (ADR-0033). A
// structural match for eventing.Waker, declared locally so the workflow package does
// not import a sibling feature.
type Waker interface {
	Wake(ctx context.Context, fn activator.FunctionRef) (upstream string, err error)
}

// Granter authorizes a step dispatch: only functions declared in a run's pinned spec
// may be invoked (spec-as-grant, ADR-0094). An undeclared target is Forbidden. A non-nil pin is
// the target's revision pin (ADR-0190): it is granted only while that revision is the pinned one.
type Granter interface {
	Allow(ns v1.NamespaceName, target v1.ObjectName, pin *v1.RevisionPin) bool
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
		client = httpx.NodeClient(0)
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
	if pin := req.Revision; pin != nil && pin.Function != req.Target {
		return nil, fault.Forbiddenf(op, "target %q is not the function of the pin %s of run %q", req.Target, PinString(*pin), req.Run)
	}
	if d.grant != nil && !d.grant.Allow(req.Namespace, req.Target, req.Revision) {
		if req.Revision != nil { // ADR-0190 Decision 4: no fallback to latest
			return nil, fault.NotFoundf(op, "pinned revision %s of run %q is gone", PinString(*req.Revision), req.Run)
		}
		d.log.Warn("dispatch denied: undeclared target",
			"namespace", req.Namespace, "run", req.Run, "step", req.Step, "target", req.Target)
		return nil, fault.Forbiddenf(op, "target %q is not declared in run %q", req.Target, req.Run)
	}
	fn := activator.FunctionRef{Namespace: req.Namespace, Name: req.Target}
	if req.Revision != nil {
		fn.Revision, fn.UID = req.Revision.Revision, req.Revision.FunctionUID
	}
	upstream, err := d.upstream(ctx, fn)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, bytes.NewReader(stepEvent(req)))
	if err != nil {
		return nil, fault.Internalf(op, "build request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(attemptHeader, strconv.Itoa(req.Attempt))
	// ADR-0102: propagate the run's W3C trace context so the step-function invocation's span (ADR-0101)
	// joins the run's trace — one run = one trace. Empty trace-id ⇒ no header (additive/legacy). The
	// parent is the step's DAG predecessor (ADR-0105), so the step span nests along its edge.
	if req.TraceID != "" {
		httpReq.Header.Set("traceparent", "00-"+req.TraceID+"-"+req.ParentSpanID+"-01")
	}
	// ADR-0105: give the step function the span-id to USE (so a successor parents on it) + its fan-in links.
	if req.SpanID != "" {
		httpReq.Header.Set("X-Funcd-Span-Id", req.SpanID)
	}
	if len(req.Links) > 0 {
		httpReq.Header.Set("X-Funcd-Span-Links", strings.Join(req.Links, ","))
	}
	resp, err := d.client.Do(httpReq)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, op, "invoke %s/%s", req.Namespace, req.Target) // retryable
	}
	defer httpx.CloseBody(resp.Body)
	// Read only what the outcome needs: an output up to one byte past its payload limit (a longer one is
	// rejected anyway), the head of a rejection that its error keeps, and nothing of a retryable failure;
	// the deferred drain discards at most httpx.DrainLimit more.
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	rejected := resp.StatusCode >= 400 && resp.StatusCode < 500
	if !ok && !rejected {
		return nil, fault.Unavailablef(op, "%s/%s returned %d", req.Namespace, req.Target, resp.StatusCode) // retryable
	}
	var src io.Reader = resp.Body
	switch {
	case rejected:
		src = io.LimitReader(src, errBodyMax+1)
	case req.MaxOutput > 0:
		src = io.LimitReader(src, req.MaxOutput+1)
	}
	body, err := io.ReadAll(src)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, op, "read response from %s/%s", req.Namespace, req.Target)
	}
	if rejected {
		return nil, Permanent(fault.Invalidf(op, "%s/%s rejected the step (%d): %s", req.Namespace, req.Target, resp.StatusCode, truncate(body)))
	}
	return body, nil
}

// errBodyMax is how much of a rejection's body its error keeps.
const errBodyMax = 256

// upstream resolves fn's ready upstream. With a Waker every dispatch goes through Wake, warm
// ones too, so each step call counts as activity and idle reclaim takes a step function down
// only when no step has used it (ADR-0033 §5); Wake wakes a cold one.
func (d *HTTPDispatcher) upstream(ctx context.Context, fn activator.FunctionRef) (string, error) {
	const op = "workflow.dispatch"
	if d.waker != nil {
		upstream, err := d.waker.Wake(ctx, fn)
		if err != nil {
			return "", fault.Wrapf(err, fault.KindOf(err), op, "wake %s/%s", fn.Namespace, fn.Name)
		}
		return upstream, nil
	}
	upstream, ready, err := d.endpoints.Upstream(ctx, fn)
	if err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), op, "resolve upstream for %s/%s", fn.Namespace, fn.Name)
	}
	if !ready || upstream == "" {
		return "", fault.Unavailablef(op, "function %s/%s has no ready upstream", fn.Namespace, fn.Name)
	}
	return upstream, nil
}

// PinString names a revision pin in a fault message.
func PinString(p v1.RevisionPin) string {
	s := fmt.Sprintf("function %q revision %q (uid %s", p.Function, p.Revision, p.FunctionUID)
	if p.ImageDigest != "" {
		s += ", digest " + p.ImageDigest
	}
	return s + ")"
}

func truncate(b []byte) string {
	if len(b) > errBodyMax {
		return string(b[:errBodyMax]) + "…"
	}
	return string(b)
}
