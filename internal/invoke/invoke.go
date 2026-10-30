// Package invoke is the chain every call to a function's worker runs (ADR-0302): invokePlugins, steps compiled into
// funcd, at three points of a call. Admit runs before anything about the Function is read, Before before the worker
// is called or woken, and After once it answered, before the caller sees the answer.
package invoke

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
)

// Caller is who makes a call.
type Caller int

const (
	External     Caller = iota + 1 // the data-plane listener, by name or through a Route
	FnToFn                         // a Function's own call (ADR-0064)
	WorkflowStep                   // a workflow step (ADR-0094)
	SensorAction                   // a Sensor's function action (ADR-0108)
	AppHook                        // an App's lifecycle hook (ADR-0214)
)

// String names the caller in logs and test failures.
func (c Caller) String() string {
	switch c {
	case External:
		return "External"
	case FnToFn:
		return "FnToFn"
	case WorkflowStep:
		return "WorkflowStep"
	case SensorAction:
		return "SensorAction"
	case AppHook:
		return "AppHook"
	}
	return "Caller(?)"
}

// Call is one call to a function's worker.
type Call struct {
	Caller    Caller
	Target    activator.FunctionRef // namespace, name and a step's revision pin (ADR-0190)
	Stance    v1.AuthMode           // External: the stance the route resolved (ADR-0113); "" otherwise
	Remainder string                // External and Function: the function-relative path
	Received  time.Time             // External: when the listener received the call (ADR-0151)
	Request   *http.Request         // what the worker gets; a Before plugin may replace it
	Function  *v1.Function          // set by the data plane before Before; nil at Admit and on the other paths
}

// Refusal ends a call before its worker, or replaces the worker's answer: an RFC 9457 problem from Err, or, when
// Status is set, that status with Body, for an answer an ADR fixes in another shape (ADR-0058's contract answers).
type Refusal struct {
	Err    error
	Header http.Header // set before the answer, e.g. Retry-After
	Status int
	Body   []byte
}

// Error is Err's text, else the status and the body, which a client path keeps in a step's or an Invocation's error.
func (r *Refusal) Error() string {
	if r.Err != nil {
		return r.Err.Error()
	}
	return strconv.Itoa(r.Status) + " " + http.StatusText(r.Status) + ": " + string(r.Body)
}

// Unwrap returns Err, so fault.KindOf reads a problem refusal's kind.
func (r *Refusal) Unwrap() error { return r.Err }

// StatusCode is the status the refusal answers with.
func (r *Refusal) StatusCode() int {
	if r.Status != 0 {
		return r.Status
	}
	return fault.StatusOf(r.Err)
}

// WriteAnswer writes the refusal to w: its headers, then its problem, or its status and body. It sets no
// Content-Type of its own, so a refusal with a body carries one in Header.
func (r *Refusal) WriteAnswer(w http.ResponseWriter) {
	for k, vs := range r.Header {
		w.Header()[k] = slices.Clone(vs)
	}
	if r.Status == 0 {
		fault.WriteProblem(w, r.Err)
		return
	}
	w.WriteHeader(r.Status)
	_, _ = w.Write(r.Body)
}

// Answer is the worker's answer at the After point.
type Answer struct {
	Status int
	Header http.Header
	res    *http.Response
	rest   io.ReadCloser // the worker's body, once Body has read from it
	body   []byte        // what Body has read so far
	eof    bool          // body is the whole body
	err    error
}

// NewAnswer wraps res. Body puts what it read back on res.Body, so the caller still gets the whole body.
func NewAnswer(res *http.Response) *Answer {
	return &Answer{Status: res.StatusCode, Header: res.Header, res: res}
}

// Body reads the answer's body, up to limit bytes; a longer body returns a fault.PayloadTooLarge. An answer whose
// body no plugin reads is never buffered, so it streams. A later call with a larger limit reads on.
func (a *Answer) Body(limit int64) ([]byte, error) {
	const op = "invoke.Answer.Body"
	if a.Status < http.StatusOK {
		return nil, fault.Invalidf(op, "a %d answer has no body to read", a.Status)
	}
	if a.rest == nil {
		a.rest = a.res.Body
	}
	if want := limit + 1 - int64(len(a.body)); a.err == nil && !a.eof && want > 0 {
		more, err := io.ReadAll(io.LimitReader(a.rest, want))
		a.body, a.err, a.eof = append(a.body, more...), err, err == nil && int64(len(more)) < want
		a.res.Body = readCloser{io.MultiReader(bytes.NewReader(a.body), a.rest), a.rest}
	}
	if a.err != nil {
		return nil, fault.Wrapf(a.err, fault.Unavailable, op, "read the worker's answer")
	}
	if int64(len(a.body)) > limit {
		return nil, fault.PayloadTooLargef(op, "the worker's answer exceeds %d bytes", limit)
	}
	return a.body, nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

// Plugin is an invokePlugin; it also implements one or more of AdmitPlugin, BeforePlugin and AfterPlugin. Applies
// reads the call's facts, never its body.
type Plugin interface {
	Name() string
	Applies(c *Call) bool
}

// AdmitPlugin runs before anything about the Function is read.
type AdmitPlugin interface {
	Plugin
	Admit(ctx context.Context, c *Call) *Refusal
}

// BeforePlugin runs before the worker is called or woken.
type BeforePlugin interface {
	Plugin
	Before(ctx context.Context, c *Call) *Refusal
}

// AfterPlugin runs once the worker answered, before the caller sees the answer.
type AfterPlugin interface {
	Plugin
	After(ctx context.Context, c *Call, a *Answer) *Refusal
}

// Chain runs its plugins in order. It is safe for concurrent use; a nil Chain runs nothing.
type Chain struct {
	plugins []Plugin
}

// NewChain returns the chain of plugins, in their order.
func NewChain(plugins ...Plugin) *Chain {
	return &Chain{plugins: slices.Clone(plugins)}
}

// Admit runs each applicable AdmitPlugin; the first refusal ends the call.
func (ch *Chain) Admit(ctx context.Context, c *Call) *Refusal {
	for _, p := range ch.list() {
		if ap, ok := p.(AdmitPlugin); ok && p.Applies(c) {
			if r := ap.Admit(ctx, c); r != nil {
				return r
			}
		}
	}
	return nil
}

// Before runs each applicable BeforePlugin; the first refusal ends the call before its worker.
func (ch *Chain) Before(ctx context.Context, c *Call) *Refusal {
	for _, p := range ch.list() {
		if bp, ok := p.(BeforePlugin); ok && p.Applies(c) {
			if r := bp.Before(ctx, c); r != nil {
				return r
			}
		}
	}
	return nil
}

// After runs each applicable AfterPlugin; the first refusal replaces the worker's answer.
func (ch *Chain) After(ctx context.Context, c *Call, a *Answer) *Refusal {
	for _, p := range ch.list() {
		if ap, ok := p.(AfterPlugin); ok && p.Applies(c) {
			if r := ap.After(ctx, c, a); r != nil {
				return r
			}
		}
	}
	return nil
}

func (ch *Chain) list() []Plugin {
	if ch == nil {
		return nil
	}
	return ch.plugins
}
