// Package plugintest holds invokePlugins for the tests of the chain's entries (ADR-0302).
package plugintest

import (
	"bytes"
	"context"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/invoke"
)

// Recorder records each call's caller at Before.
type Recorder struct {
	mu      sync.Mutex
	callers []invoke.Caller
}

func (*Recorder) Name() string              { return "recorder" }
func (*Recorder) Applies(*invoke.Call) bool { return true }

func (r *Recorder) Before(_ context.Context, c *invoke.Call) *invoke.Refusal {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.callers = append(r.callers, c.Caller)
	return nil
}

// Callers returns the callers recorded so far, in order.
func (r *Recorder) Callers() []invoke.Caller {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.callers)
}

// Refuser refuses every call at Before with a 403 problem.
type Refuser struct{}

func (Refuser) Name() string              { return "refuser" }
func (Refuser) Applies(*invoke.Call) bool { return true }

func (Refuser) Before(context.Context, *invoke.Call) *invoke.Refusal {
	return &invoke.Refusal{Err: fault.Forbiddenf("plugintest", "refused by the test plugin")}
}

// BadBody is the body BadAnswer refuses.
const BadBody = `{"bad":true}`

// BadAnswer refuses, with a 500 problem, an answer whose body is BadBody.
type BadAnswer struct{}

func (BadAnswer) Name() string              { return "bad-answer" }
func (BadAnswer) Applies(*invoke.Call) bool { return true }

func (BadAnswer) After(_ context.Context, _ *invoke.Call, a *invoke.Answer) *invoke.Refusal {
	b, err := a.Body(1 << 10)
	if err != nil {
		return &invoke.Refusal{Err: err}
	}
	if bytes.Equal(bytes.TrimSpace(b), []byte(BadBody)) {
		return &invoke.Refusal{Err: fault.Internalf("plugintest", "the answer is bad")}
	}
	return nil
}

// Peeker sees every answer at After and reads no body.
type Peeker struct{ seen atomic.Int32 }

func (*Peeker) Name() string              { return "peeker" }
func (*Peeker) Applies(*invoke.Call) bool { return true }

func (p *Peeker) After(context.Context, *invoke.Call, *invoke.Answer) *invoke.Refusal {
	p.seen.Add(1)
	return nil
}

// Seen is how many answers the peeker saw.
func (p *Peeker) Seen() int { return int(p.seen.Load()) }
