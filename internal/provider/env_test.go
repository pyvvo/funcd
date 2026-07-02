package provider_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/provider"
	"github.com/green-0-rabbit/funcd/internal/store"
	storemem "github.com/green-0-rabbit/funcd/internal/store/memory"
)

// fakeSecretResolver is a consumer-side SecretResolver double (no mock framework): it records the
// identity/names it was called with and returns a canned env or error.
type fakeSecretResolver struct {
	env    map[string]string
	err    error
	gotID  auth.Identity
	gotNs  v1.NamespaceName
	gotArg []string
}

func (f *fakeSecretResolver) ResolveEnv(_ context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error) {
	f.gotID, f.gotNs, f.gotArg = id, ns, names
	if f.err != nil {
		return nil, f.err
	}
	return f.env, nil
}

func createConfigMap(t *testing.T, st store.Store, ns v1.NamespaceName, name string, data map[string]string) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindConfigMap)
	require.True(t, ok)
	cm := obj.(*v1.ConfigMap)
	cm.Name = v1.ObjectName(name)
	cm.Namespace = ns
	cm.ResourceGroup = "rg1"
	cm.Spec.Data = data
	_, err := st.Create(context.Background(), cm)
	require.NoError(t, err)
}

func devIdentity(ns v1.NamespaceName) auth.Identity {
	return auth.Identity{Subject: "system:secret-injector:" + string(ns), Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{ns}}
}

// scenario: provider-env-resolves-secrets-and-config — config (ConfigMap DUCKDB_*) + secrets
// (Secret QUACK_TOKEN) merge into one guarded env, config first then secrets (a secret may
// override a config default).
func TestResolveEnv_secrets_and_config(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const ns v1.NamespaceName = "team-a"
	st := store.New(storemem.New())
	createConfigMap(t, st, ns, "engine-cfg", map[string]string{
		"DUCKDB_MEMORY_LIMIT": "4GB",
		"SHARED":              "from-config",
	})
	sr := &fakeSecretResolver{env: map[string]string{
		"QUACK_TOKEN": "tok",
		"SHARED":      "from-secret", // resolved after config → overrides
	}}

	env, err := provider.ResolveEnv(ctx, provider.EnvDeps{
		Secrets:  sr,
		Store:    st,
		Identity: devIdentity,
	}, ns, []v1.ObjectName{"engine-cfg"}, []v1.ObjectName{"quack"})
	require.NoError(t, err)

	require.Equal(t, "4GB", env["DUCKDB_MEMORY_LIMIT"])
	require.Equal(t, "tok", env["QUACK_TOKEN"])
	require.Equal(t, "from-secret", env["SHARED"], "secrets are merged after config → a secret overrides a config default")
	require.Equal(t, devIdentity(ns), sr.gotID, "the secret read uses the secret-injector identity")
	require.Equal(t, []string{"quack"}, sr.gotArg)
}

// scenario: reserved-key-dropped-once — a FUNCD_-prefixed ConfigMap Data key is dropped via the
// single shared guard.
func TestResolveEnv_reserved_key_dropped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const ns v1.NamespaceName = "team-a"
	st := store.New(storemem.New())
	createConfigMap(t, st, ns, "engine-cfg", map[string]string{
		"FUNCD_ARTIFACT": "/evil/override", // reserved → dropped
		"SAFE":           "ok",
	})

	env, err := provider.ResolveEnv(ctx, provider.EnvDeps{Store: st, Identity: devIdentity},
		ns, []v1.ObjectName{"engine-cfg"}, nil)
	require.NoError(t, err)

	require.Equal(t, "ok", env["SAFE"])
	require.NotContains(t, env, "FUNCD_ARTIFACT", "a reserved FUNCD_ config key is dropped by the shared guard")
}

// declared secrets with no resolver wired → fault.Invalid (unchanged catalog fail-closed semantics).
func TestResolveEnv_secrets_without_resolver_is_invalid(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(storemem.New())
	_, err := provider.ResolveEnv(ctx, provider.EnvDeps{Store: st, Identity: devIdentity},
		"team-a", nil, []v1.ObjectName{"quack"})
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}
