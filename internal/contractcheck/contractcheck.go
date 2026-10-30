// Package contractcheck is funcd's check of every call against its contract (ADR-0303): two invokePlugins of the
// ADR-0302 chain, contract-input at Before and contract-output at After, with ADR-0301's rules.
package contractcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/internal/invoke"
)

// MaxCheckedBytes is the largest input or result the check reads (Decision 6): the limit external bodies and
// fn-to-fn calls already have.
const MaxCheckedBytes = 1 << 20

// The checks' bounds (Decision 6): the pattern budget of one check, and the most digits a number, or its exponent's
// magnitude, may have.
const (
	CallBudget = time.Second
	MaxDigits  = 1000
)

// Pair is one revision's compiled contract.
type Pair struct{ Input, Output *contract.Validator }

// Source returns the compiled contract of the revision that serves a call to ref, or nil when it delivers none. fn is
// the Function the data plane read, or nil on a client path.
type Source interface {
	ContractOf(ctx context.Context, ref activator.FunctionRef, fn *v1.Function) (*Pair, error)
}

// Plugins returns contract-input (Before) and contract-output (After).
func Plugins(src Source) []invoke.Plugin {
	return []invoke.Plugin{inputPlugin{src: src}, outputPlugin{src: src}}
}

// applies is every caller of a cloudevent Function; on a client path the Function is unread, and the clients refuse
// an http target before the chain (RefuseHTTP).
func applies(c *invoke.Call) bool {
	return c.Function == nil || c.Function.Spec.Protocol.Normalized() != v1.ProtocolHTTP
}

type inputPlugin struct{ src Source }

func (inputPlugin) Name() string                { return "contract-input" }
func (inputPlugin) Applies(c *invoke.Call) bool { return applies(c) }

func (p inputPlugin) Before(ctx context.Context, c *invoke.Call) *invoke.Refusal {
	const op = "contractcheck.input"
	pair, rf := contractOf(ctx, p.src, c)
	if rf != nil {
		return rf
	}
	// Decision 2: the output check uses the contract the input was checked against.
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), pairKey{}, pairBox{pair: pair}))
	if pair == nil || pair.Input == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, MaxCheckedBytes+1))
	c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), c.Request.Body))
	if err != nil {
		return &invoke.Refusal{Err: fault.Invalidf(op, "reading request body: %v", err)}
	}
	if len(body) > MaxCheckedBytes {
		return &invoke.Refusal{Err: fault.PayloadTooLargef(op, "the input exceeds the %d-byte contract check", MaxCheckedBytes)}
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil {
		return nil // not a JSON object: the shim refuses it with 400, as before
	}
	data := envelope["data"]
	if data == nil {
		data = json.RawMessage("null")
	}
	return check(pair.Input, data, http.StatusUnprocessableEntity, "event data does not match the input contract")
}

type outputPlugin struct{ src Source }

func (outputPlugin) Name() string                { return "contract-output" }
func (outputPlugin) Applies(c *invoke.Call) bool { return applies(c) }

func (p outputPlugin) After(ctx context.Context, c *invoke.Call, a *invoke.Answer) *invoke.Refusal {
	const op = "contractcheck.output"
	if a.Status != http.StatusOK && a.Status != http.StatusNoContent {
		return nil
	}
	var pair *Pair
	if box, ok := c.Request.Context().Value(pairKey{}).(pairBox); ok {
		pair = box.pair
	} else {
		var rf *invoke.Refusal
		if pair, rf = contractOf(ctx, p.src, c); rf != nil {
			return rf
		}
	}
	if pair == nil || pair.Output == nil {
		return nil
	}
	result := []byte("null")
	if a.Status == http.StatusOK {
		b, err := a.Body(MaxCheckedBytes)
		if fault.KindOf(err) == fault.PayloadTooLarge {
			return &invoke.Refusal{Err: fault.Internalf(op, "the handler's result exceeds the %d-byte contract check", MaxCheckedBytes)}
		} else if err != nil {
			return &invoke.Refusal{Err: err}
		}
		result = b
	}
	return check(pair.Output, result, http.StatusInternalServerError, "handler result does not match the output contract")
}

// pairKey carries, on a call's request, the contract its input plugin resolved; a pairBox holds nil too.
type pairKey struct{}

type pairBox struct{ pair *Pair }

// contractOf resolves the call's contract; a failure refuses the call with 503, fail-closed (Decision 2).
func contractOf(ctx context.Context, src Source, c *invoke.Call) (*Pair, *invoke.Refusal) {
	pair, err := src.ContractOf(ctx, c.Target, c.Function)
	if err != nil {
		return nil, &invoke.Refusal{Err: fault.Wrapf(err, fault.Unavailable, "contractcheck",
			"the contract of %s/%s cannot be read", c.Target.Namespace, c.Target.Name)}
	}
	return pair, nil
}

// check answers ADR-0058's {error, details} with status when doc breaks v's contract.
func check(v *contract.Validator, doc []byte, status int, message string) *invoke.Refusal {
	details, err := v.Validate(doc)
	if err != nil {
		details = []contract.Detail{{Path: "", Message: "the document is not JSON"}}
	}
	if len(details) == 0 {
		return nil
	}
	body, _ := json.Marshal(struct {
		Error   string            `json:"error"`
		Details []contract.Detail `json:"details"`
	}{Error: message, Details: details})
	return &invoke.Refusal{Status: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}
}
