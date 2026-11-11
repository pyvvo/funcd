package secret_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	secretsvc "github.com/pyvvo/funcd/internal/services/secret"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// recordingResolver answers every Secret with one key and records the principal each call carries.
type recordingResolver struct{ principals []*auth.EntityRef }

func (r *recordingResolver) ResolveEnv(_ context.Context, id auth.Identity, _ v1.NamespaceName, names []string) (map[string]string, error) {
	r.principals = append(r.principals, id.Principal)
	out := map[string]string{}
	for _, n := range names {
		out[n] = "v"
	}
	return out, nil
}

func (r *recordingResolver) CheckRead(_ context.Context, id auth.Identity, _ v1.NamespaceName, _ string) error {
	r.principals = append(r.principals, id.Principal)
	return nil
}

// The facade reads, and answers the readiness check, as the calling Function (ADR-0321 Decisions 3 and 4).
func TestFacadeAsksAsTheCallingFunction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "reports", "team-a", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///unused.mjs"
	fn.Spec.Bindings.Secret = []v1.ObjectName{"api-keys"}
	_, err := st.Create(ctx, fn)
	require.NoError(t, err)
	res := &recordingResolver{}
	f, err := secretsvc.NewFacade(secretsvc.Deps{Store: st, Resolver: res})
	require.NoError(t, err)

	keys, err := f.List(ctx, "team-a", "reports")
	require.NoError(t, err)
	require.Equal(t, []string{"api-keys"}, keys)
	require.NoError(t, f.CheckRead(ctx, "team-a", "reports", "api-keys"))
	want := &auth.EntityRef{Type: v1.KindFunction, Namespace: "team-a", Name: "reports"}
	require.Equal(t, []*auth.EntityRef{want, want}, res.principals)
}
