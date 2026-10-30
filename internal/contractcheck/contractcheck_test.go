package contractcheck_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/internal/contractcheck"
	"github.com/pyvvo/funcd/internal/invoke"
	"github.com/pyvvo/funcd/internal/testkit/plugintest"
)

func chainOf(t *testing.T, input, output string) *invoke.Chain {
	t.Helper()
	return invoke.NewChain(contractcheck.Plugins(plugintest.CompileContract(t, input, output))...)
}

func callWith(fn *v1.Function, body string) *invoke.Call {
	return &invoke.Call{Caller: invoke.External, Function: fn,
		Request: httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))}
}

// answerOf is the error and details of a refusal's own body.
func answerOf(t *testing.T, rf *invoke.Refusal) (string, []contract.Detail) {
	t.Helper()
	require.Equal(t, "application/json", rf.Header.Get("Content-Type"))
	var body struct {
		Error   string            `json:"error"`
		Details []contract.Detail `json:"details"`
	}
	require.NoError(t, json.Unmarshal(rf.Body, &body))
	return body.Error, body.Details
}

func runtimeFn(runtime v1.RuntimeName) *v1.Function {
	fn := &v1.Function{}
	fn.Spec.Runtime = runtime
	return fn
}

// The input check refuses a call whose data breaks the contract, and leaves the body whole for the worker.
func TestInputCheck(t *testing.T) {
	t.Parallel()
	ch := chainOf(t, plugintest.EmailInput, `{}`)
	ok := callWith(nil, `{"specversion": "1.0", "data": {"email": "ops@localhost"}}`)
	require.Nil(t, ch.Before(context.Background(), ok))
	rest, _ := io.ReadAll(ok.Request.Body)
	require.Equal(t, `{"specversion": "1.0", "data": {"email": "ops@localhost"}}`, string(rest))

	rf := ch.Before(context.Background(), callWith(nil, `{"data": {"email": "a b@example.com"}}`))
	require.NotNil(t, rf)
	require.Equal(t, http.StatusUnprocessableEntity, rf.Status)
	msg, details := answerOf(t, rf)
	require.Equal(t, "event data does not match the input contract", msg)
	require.Len(t, details, 1)
	require.Equal(t, "/email", details[0].Path)

	require.Nil(t, ch.Before(context.Background(), callWith(nil, `not json`)), "the shim answers a non-object body with 400")
	require.Nil(t, ch.Before(context.Background(), callWith(nil, `[1, 2]`)))
}

// An http Function carries no contract, so neither check applies to its calls.
func TestHTTPFunctionsAreNotChecked(t *testing.T) {
	t.Parallel()
	fn := &v1.Function{}
	fn.Spec.Protocol = v1.ProtocolHTTP
	for _, p := range contractcheck.Plugins(plugintest.Contract{}) {
		require.False(t, p.Applies(&invoke.Call{Caller: invoke.External, Function: fn}), p.Name())
		require.True(t, p.Applies(&invoke.Call{Caller: invoke.WorkflowStep}), p.Name())
	}
}

// funcd answers #1339's cases before any shim, whatever the Function's runtime.
func TestTheAnswerDoesNotDependOnTheRuntime(t *testing.T) {
	t.Parallel()
	ch := chainOf(t, `{"type": "object", "properties": {"id": {"type": "string", "format": "uuid"}}, "additionalProperties": false}`, `{}`)
	body := `{"data": {"id": "urn:uuid:550e8400-e29b-41d4-a716-446655440000"}}`
	nodeAnswer := ch.Before(context.Background(), callWith(runtimeFn("nodejs22"), body))
	pythonAnswer := ch.Before(context.Background(), callWith(runtimeFn("python314"), body))
	require.NotNil(t, nodeAnswer)
	require.Equal(t, nodeAnswer, pythonAnswer)
}

// scenario: a-slow-check-is-cut
func TestScenarioASlowCheckIsCut(t *testing.T) {
	t.Parallel()
	ch := chainOf(t, `{"type": "array", "items": {"type": "string", "pattern": "^(a+)+$"}}`, `{}`)
	items := make([]string, 1000)
	for i := range items {
		items[i] = strings.Repeat("a", 40) + "!"
	}
	data, err := json.Marshal(items)
	require.NoError(t, err)
	start := time.Now()
	rf := ch.Before(context.Background(), callWith(nil, `{"data": `+string(data)+`}`))
	require.Less(t, time.Since(start), 3*time.Second)
	require.NotNil(t, rf)
	require.Equal(t, http.StatusUnprocessableEntity, rf.Status)
	_, details := answerOf(t, rf)
	require.Equal(t, "", details[0].Path)
	require.Contains(t, details[0].Message, "budget", "the root detail names the budget")
}

// scenario: a-body-over-the-limit-is-refused
func TestScenarioABodyOverTheLimitIsRefused(t *testing.T) {
	t.Parallel()
	ch := chainOf(t, `{"type": "object", "properties": {"n": {"type": "number"}}}`, `{}`)
	big := `{"data": {"s": "` + strings.Repeat("x", 2<<20) + `"}}`
	rf := ch.Before(context.Background(), callWith(nil, big))
	require.NotNil(t, rf)
	require.Equal(t, fault.PayloadTooLarge, fault.KindOf(rf))

	res := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(bytes.Repeat([]byte(" "), 2<<20)))}
	rf = ch.After(context.Background(), callWith(nil, `{}`), invoke.NewAnswer(res))
	require.NotNil(t, rf)
	require.Equal(t, http.StatusInternalServerError, rf.StatusCode())

	for _, n := range []string{"1e1001", strings.Repeat("7", 1001)} {
		rf = ch.Before(context.Background(), callWith(nil, `{"data": {"n": `+n+`}}`))
		require.NotNil(t, rf, n)
		_, details := answerOf(t, rf)
		require.Equal(t, "/n", details[0].Path)
	}
	require.Nil(t, ch.Before(context.Background(), callWith(nil, `{"data": {"n": 1e1000}}`)))
}

// countingSource counts its lookups.
type countingSource struct {
	plugintest.Contract
	lookups int
}

func (s *countingSource) ContractOf(ctx context.Context, ref activator.FunctionRef, fn *v1.Function) (*contractcheck.Pair, error) {
	s.lookups++
	return s.Contract.ContractOf(ctx, ref, fn)
}

// A call's contract is resolved once, at Before, and the output check reuses it (Decision 2).
func TestOneLookupPerCall(t *testing.T) {
	t.Parallel()
	src := &countingSource{Contract: plugintest.CompileContract(t, `{}`, `{"type": "null"}`)}
	ch := invoke.NewChain(contractcheck.Plugins(src)...)
	c := callWith(nil, `{"data": null}`)
	require.Nil(t, ch.Before(context.Background(), c))
	rf := ch.After(context.Background(), c, invoke.NewAnswer(&http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"x": 1}`))}))
	require.NotNil(t, rf, "a void contract refuses a body")
	require.Equal(t, 1, src.lookups)
}

// A failed lookup refuses the call with 503, fail-closed (Decision 2).
func TestALookupFailureRefusesTheCall(t *testing.T) {
	t.Parallel()
	ch := invoke.NewChain(contractcheck.Plugins(failingSource{})...)
	rf := ch.Before(context.Background(), callWith(nil, `{}`))
	require.NotNil(t, rf)
	require.Equal(t, http.StatusServiceUnavailable, rf.StatusCode())
}

type failingSource struct{}

func (failingSource) ContractOf(context.Context, activator.FunctionRef, *v1.Function) (*contractcheck.Pair, error) {
	return nil, fault.Internalf("test", "the artifact cannot be read")
}
