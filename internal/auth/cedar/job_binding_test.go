package cedar_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/cedar"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// planners stores, in namespace team, a Function planner that binds the Job tofu under the alias stack, a Function
// other with no job binding, and the Jobs tofu and backfill.
func planners(t *testing.T) store.Store {
	t.Helper()
	st := store.New(memory.New())
	meta := func(name v1.ObjectName) v1.ObjectMeta {
		return v1.ObjectMeta{Name: name, Namespace: "team", ResourceGroup: "rg1"}
	}
	tm := func(k v1.Kind) v1.TypeMeta { return v1.TypeMeta{APIVersion: k.GVK().APIVersion(), Kind: k} }
	fn := func(name v1.ObjectName, jobs ...v1.FunctionJob) *v1.Function {
		return &v1.Function{TypeMeta: tm(v1.KindFunction), ObjectMeta: meta(name), Spec: v1.FunctionSpec{Runtime: "nodejs22",
			Handler: "handle", Image: "oci://x/fn:1", Bindings: v1.FunctionBindings{Job: jobs}}}
	}
	job := func(name v1.ObjectName) *v1.Job {
		return &v1.Job{TypeMeta: tm(v1.KindJob), ObjectMeta: meta(name), Spec: v1.JobSpec{Runtime: "nodejs22", Handler: "handle", Image: "oci://x/job:1"}}
	}
	for _, o := range []v1.Object{fn("planner", v1.FunctionJob{Alias: "stack", Job: "tofu"}), fn("other"), job("tofu"), job("backfill")} {
		_, err := st.Create(context.Background(), o)
		require.NoError(t, err, "seed %s", o.GetName())
	}
	return st
}

func onJob(name v1.ObjectName) *auth.EntityRef {
	return &auth.EntityRef{Type: v1.KindJob, Namespace: "team", Name: name}
}

func canAct(t *testing.T, a auth.Authorizer, principal auth.EntityRef, action auth.Action, job v1.ObjectName) bool {
	t.Helper()
	return authorize(t, a, &principal, action, onJob(job)).Allowed
}

// ADR-0349 Decision 1: a Function's job binding grants it job::start and job::read on the bound Job alone; a Function
// with no binding, and a Job, which binds none, are denied both.
func TestJobBindingGrantsStartAndRead(t *testing.T) {
	t.Parallel()
	a := jobDriver(t, planners(t))
	planner := auth.EntityRef{Type: v1.KindFunction, Namespace: "team", Name: "planner"}
	other := auth.EntityRef{Type: v1.KindFunction, Namespace: "team", Name: "other"}
	asJob := auth.EntityRef{Type: v1.KindJob, Namespace: "team", Name: "tofu"}
	for _, action := range []auth.Action{auth.ActionJobStart, auth.ActionJobRead} {
		require.True(t, canAct(t, a, planner, action, "tofu"), "%s on the bound Job", action)
		require.False(t, canAct(t, a, planner, action, "backfill"), "%s on a Job no binding names", action)
		require.False(t, canAct(t, a, other, action, "tofu"), "%s with no binding", action)
		require.False(t, canAct(t, a, asJob, action, "tofu"), "%s by a Job", action)
	}
}

// ADR-0349 Decision 1: a namespace Policy's forbid revokes job::start or job::read alone, and a Policy may name both.
func TestPolicyForbidRevokesAJobAction(t *testing.T) {
	t.Parallel()
	for _, revoked := range []auth.Action{auth.ActionJobStart, auth.ActionJobRead} {
		text := `forbid(principal == Function::"team/planner", action == Action::"` + string(revoked) + `", resource == Job::"team/tofu");`
		require.NoError(t, cedar.ValidateCedarInNamespace("team", text), "a Policy may name %s on a Job", revoked)
		pol := v1.Policy{ObjectMeta: v1.ObjectMeta{Name: "no-tofu", Namespace: "team", ResourceGroup: "rg1"}, Spec: v1.PolicySpec{Cedar: text}}
		a := jobDriver(t, planners(t), pol)
		planner := auth.EntityRef{Type: v1.KindFunction, Namespace: "team", Name: "planner"}
		for _, action := range []auth.Action{auth.ActionJobStart, auth.ActionJobRead} {
			require.Equal(t, action != revoked, canAct(t, a, planner, action, "tofu"), "%s with %s forbidden", action, revoked)
		}
	}
	require.True(t, cedar.KnownAction(string(auth.ActionJobStart)))
	require.True(t, cedar.KnownAction(string(auth.ActionJobRead)))
}
