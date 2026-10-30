// Package plugintest holds invokePlugins and a contract source for the tests of the chain's entries (ADR-0302,
// ADR-0303).
package plugintest

import (
	"bytes"
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/internal/contractcheck"
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

// Contract is a contractcheck.Source that serves one compiled contract for every call.
type Contract struct{ Pair *contractcheck.Pair }

func (s Contract) ContractOf(context.Context, activator.FunctionRef, *v1.Function) (*contractcheck.Pair, error) {
	return s.Pair, nil
}

// CompileContract compiles an input and an output side with the bounds funcd checks calls with (ADR-0303).
func CompileContract(t testing.TB, input, output string) Contract {
	t.Helper()
	bounds := contract.WithCallBounds(contractcheck.CallBudget, contractcheck.MaxDigits)
	in, err := contract.Compile([]byte(input), bounds)
	if err != nil {
		t.Fatalf("compile the input side: %v", err)
	}
	out, err := contract.Compile([]byte(output), bounds)
	if err != nil {
		t.Fatalf("compile the output side: %v", err)
	}
	return Contract{Pair: &contractcheck.Pair{Input: in, Output: out}}
}

// EmailInput is an input contract with one required email field, the field #1339's email case reads.
const EmailInput = `{"type": "object", "properties": {"email": {"type": "string", "format": "email"}},
	"required": ["email"], "additionalProperties": false}`
