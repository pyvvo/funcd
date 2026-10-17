package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
)

// scenario: http-no-events — a workflow step that names a Function serving HTTP fails with TargetServesHTTP before the
// step function is woken or called (ADR-0243 Decision 5).
func TestDispatchRefusesAStepThatServesHTTP(t *testing.T) {
	waker := &fakeWaker{upstream: "http://never"}
	d, err := NewHTTPDispatcher(DispatchDeps{
		Endpoints: fakeEndpoints{upstream: "http://never", ready: true}, Waker: waker, Grant: fakeGrant{allow: true},
		Protocol: func(context.Context, activator.FunctionRef) (v1.FunctionProtocol, error) { return v1.ProtocolHTTP, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Dispatch(context.Background(), dispatchReq("api"))
	if fault.KindOf(err) != fault.Conflict || !strings.Contains(err.Error(), activator.ReasonTargetServesHTTP) || !isPermanent(err) {
		t.Fatalf("want a permanent Conflict TargetServesHTTP, got %v", err)
	}
	if waker.called {
		t.Fatal("must not wake a step that serves HTTP")
	}
}

// scenario: http-no-events — a step that refs a Function serving HTTP, which has no contract, is untyped, so the
// Workflow is Ready and its runs reach the dispatcher, which refuses the step (ADR-0243 Decision 5).
func TestRefToAFunctionThatServesHTTPIsUntyped(t *testing.T) {
	s := newStore(t)
	fn := &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "api", Namespace: "default", ResourceGroup: "rg1"},
	}
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image, fn.Spec.Protocol = "nodejs22", "handle", "oci:api", v1.ProtocolHTTP
	if _, err := s.Create(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
	wf, _ := reconcileWF(t, s, fakeContracts{}, v1.WorkflowStep{Name: "purge", Function: &v1.FunctionStep{Ref: "api"}})
	if !ready(wf) {
		t.Fatalf("a ref to a Function that serves HTTP must not wait for its contract, conditions=%+v", wf.Status.Conditions)
	}
}
