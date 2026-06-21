package admission_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controlplane/admission"
)

// fakeAdmission is a configurable test double implementing the Admission port.
type fakeAdmission struct {
	name    string
	phase   admission.Phase
	handles func(v1.GroupVersionKind, admission.Operation) bool
	admit   func(context.Context, admission.Request) (v1.Object, error)
}

func (f fakeAdmission) Name() string          { return f.name }
func (f fakeAdmission) Phase() admission.Phase { return f.phase }

func (f fakeAdmission) Handles(g v1.GroupVersionKind, o admission.Operation) bool {
	if f.handles == nil {
		return true
	}
	return f.handles(g, o)
}

func (f fakeAdmission) Admit(ctx context.Context, r admission.Request) (v1.Object, error) {
	return f.admit(ctx, r)
}

func nsObj() *v1.Namespace { return &v1.Namespace{} }

// scenario: mutating-precedes-validating — a Mutating admission's change is visible to a Validating
// one, regardless of registration order (the two-phase guarantee).
func TestScenarioMutatingPrecedesValidating(t *testing.T) {
	gvk := v1.KindNamespace.GVK()
	mut := fakeAdmission{name: "defaulter", phase: admission.Mutating,
		admit: func(_ context.Context, r admission.Request) (v1.Object, error) {
			r.Object.GetObjectMeta().Name = "defaulted"
			return r.Object, nil
		}}
	var saw v1.ObjectName
	val := fakeAdmission{name: "checker", phase: admission.Validating,
		admit: func(_ context.Context, r admission.Request) (v1.Object, error) {
			saw = r.Object.GetObjectMeta().Name
			return r.Object, nil
		}}
	// Register validating FIRST to prove phase order wins over registration order.
	p := admission.NewPipeline(val, mut)
	out, err := p.Admit(context.Background(), admission.Request{
		Operation: admission.Create, GVK: gvk, Object: nsObj(),
	})
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("defaulted"), out.GetObjectMeta().Name, "mutation carried to output")
	require.Equal(t, v1.ObjectName("defaulted"), saw, "validating admission saw the mutated object")
}

// scenario: first-denial-short-circuits — the first registered admission's fault is returned and the
// second never runs (deterministic order).
func TestScenarioFirstDenialShortCircuits(t *testing.T) {
	gvk := v1.KindNamespace.GVK()
	first := fakeAdmission{name: "first", phase: admission.Validating,
		admit: func(_ context.Context, _ admission.Request) (v1.Object, error) {
			return nil, fault.Invalidf("first", "denied by first")
		}}
	secondRan := false
	second := fakeAdmission{name: "second", phase: admission.Validating,
		admit: func(_ context.Context, r admission.Request) (v1.Object, error) {
			secondRan = true
			return r.Object, nil
		}}
	p := admission.NewPipeline(first, second)
	_, err := p.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk, Object: nsObj()})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), "denied by first")
	require.False(t, secondRan, "the second admission must not run after the first denies")
}

// scenario: delete-runs-registered-admission — a Delete admission runs on delete; with none, Handles
// reports false so the caller can skip the Old-object fetch.
func TestScenarioDeleteRunsRegisteredAdmission(t *testing.T) {
	gvk := v1.KindNamespace.GVK()
	ran := false
	del := fakeAdmission{name: "del", phase: admission.Validating,
		handles: func(_ v1.GroupVersionKind, o admission.Operation) bool { return o == admission.Delete },
		admit: func(_ context.Context, r admission.Request) (v1.Object, error) {
			ran = true
			return r.Object, nil
		}}
	p := admission.NewPipeline(del)
	require.True(t, p.Handles(gvk, admission.Delete))
	_, err := p.Admit(context.Background(), admission.Request{Operation: admission.Delete, GVK: gvk})
	require.NoError(t, err)
	require.True(t, ran, "the Delete admission ran")

	require.False(t, admission.NewPipeline().Handles(gvk, admission.Delete), "no admission ⇒ Handles false")
}

// the Handles filter — a small matrix over operations for a Create/Update admission.
func TestHandlesMatrix(t *testing.T) {
	gvk := v1.KindFunction.GVK()
	cu := fakeAdmission{name: "cu", phase: admission.Validating,
		handles: func(_ v1.GroupVersionKind, o admission.Operation) bool {
			return o == admission.Create || o == admission.Update
		}}
	p := admission.NewPipeline(cu)
	for _, tc := range []struct {
		op   admission.Operation
		want bool
	}{
		{admission.Create, true},
		{admission.Update, true},
		{admission.Delete, false},
	} {
		t.Run(string(tc.op), func(t *testing.T) {
			require.Equal(t, tc.want, p.Handles(gvk, tc.op))
		})
	}
}
