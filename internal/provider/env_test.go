package provider_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/provider"
	"github.com/green-0-rabbit/funcd/internal/store"
	storemem "github.com/green-0-rabbit/funcd/internal/store/memory"
)

// fakeSecretResolver is a consumer-side SecretResolver double: it returns a canned env.
type fakeSecretResolver struct{ env map[string]string }

func (f *fakeSecretResolver) ResolveEnv(context.Context, auth.Identity, v1.NamespaceName, []string) (map[string]string, error) {
	return f.env, nil
}

func devIdentity(ns v1.NamespaceName) auth.Identity {
	return auth.Identity{Subject: "system:secret-injector:" + string(ns), Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{ns}}
}

// provider.ResolveEnv is a thin facade over envresolve.ResolveEnv (ADR-0093); this proves the
// facade wires config-then-secrets through to the relocated resolver (the resolver's own behavior
// is exhaustively covered by the internal/envresolve suite).
func TestResolveEnv_delegates_to_envresolve(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const ns v1.NamespaceName = "team-a"
	st := store.New(storemem.New())

	obj, ok := v1.NewObject(v1.KindConfigMap)
	require.True(t, ok)
	cm := obj.(*v1.ConfigMap)
	cm.Name, cm.Namespace, cm.ResourceGroup = "engine-cfg", ns, "rg1"
	cm.Spec.Data = map[string]string{"DUCKDB_MEMORY_LIMIT": "4GB", "SHARED": "from-config"}
	_, err := st.Create(ctx, cm)
	require.NoError(t, err)

	sr := &fakeSecretResolver{env: map[string]string{"QUACK_TOKEN": "tok", "SHARED": "from-secret"}}
	env, err := provider.ResolveEnv(ctx, provider.EnvDeps{Secrets: sr, Store: st, Identity: devIdentity},
		ns, []v1.ObjectName{"engine-cfg"}, []v1.ObjectName{"quack"})
	require.NoError(t, err)
	require.Equal(t, "4GB", env["DUCKDB_MEMORY_LIMIT"])
	require.Equal(t, "tok", env["QUACK_TOKEN"])
	require.Equal(t, "from-secret", env["SHARED"], "secrets merged after config → a secret overrides a config default")
}
