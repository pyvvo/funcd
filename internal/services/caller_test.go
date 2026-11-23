package services_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/services"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// ADR-0351 Decision 2: a caller's bindings are those of its own kind, a Job's never the Function's of its name nor the
// other way; a caller of another kind, the zero value included, is Forbidden, and one not stored is NotFound. The
// principal names the kind.
func TestCallerBindingsAreTheCallersOwn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	t.Cleanup(func() { _ = st.Close() })
	meta := v1.ObjectMeta{Name: "tofu", Namespace: "team", ResourceGroup: "rg1"}
	tm := func(k v1.Kind) v1.TypeMeta { return v1.TypeMeta{APIVersion: k.GVK().APIVersion(), Kind: k} }
	for _, o := range []v1.Object{
		&v1.Job{TypeMeta: tm(v1.KindJob), ObjectMeta: meta, Spec: v1.JobSpec{Runtime: "nodejs22", Handler: "handle",
			Image: "oci://x/tofu:1", Bindings: v1.FunctionBindings{Secret: []v1.ObjectName{"job-creds"}}}},
		&v1.Function{TypeMeta: tm(v1.KindFunction), ObjectMeta: meta, Spec: v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle",
			Image: "oci://x/fn:1", Bindings: v1.FunctionBindings{Secret: []v1.ObjectName{"fn-creds"}}}},
	} {
		_, err := st.Create(ctx, o)
		require.NoError(t, err)
	}
	for caller, want := range map[v1.WorkloadRef]v1.ObjectName{v1.JobOwner("tofu"): "job-creds", v1.FunctionOwner("tofu"): "fn-creds"} {
		b, err := services.CallerBindings(ctx, st, "team", caller)
		require.NoError(t, err)
		require.Equal(t, []v1.ObjectName{want}, b.Secret, "%s", caller)
		require.Equal(t, &auth.EntityRef{Type: caller.Kind, Namespace: "team", Name: "tofu"}, services.Principal("team", caller))
	}
	for _, caller := range []v1.WorkloadRef{{}, v1.LakeOwner("tofu"), {Kind: v1.KindJobRun, Name: "tofu"}} {
		_, err := services.CallerBindings(ctx, st, "team", caller)
		require.Equal(t, fault.Forbidden, fault.KindOf(err), "%s: %v", caller, err)
	}
	_, err := services.CallerBindings(ctx, st, "team", v1.JobOwner("gone"))
	require.Equal(t, fault.NotFound, fault.KindOf(err))
}
