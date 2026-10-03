package envresolve_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/envresolve"
	"github.com/pyvvo/funcd/internal/secrets"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
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

// scenario: env-resolves-secrets-and-config — config (ConfigMap DUCKDB_*) + secrets (Secret
// QUACK_TOKEN) merge into one guarded env, config first then secrets (a secret may override a
// config default).
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

	env, err := envresolve.ResolveEnv(ctx, envresolve.Deps{
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

// scenario: reserved-key-dropped — a FUNCD_-prefixed ConfigMap Data key is dropped via the
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

	env, err := envresolve.ResolveEnv(ctx, envresolve.Deps{Store: st, Identity: devIdentity},
		ns, []v1.ObjectName{"engine-cfg"}, nil)
	require.NoError(t, err)

	require.Equal(t, "ok", env["SAFE"])
	require.NotContains(t, env, "FUNCD_ARTIFACT", "a reserved FUNCD_ config key is dropped by the shared guard")
}

// declared secrets with no resolver wired → fault.Invalid (unchanged catalog fail-closed semantics),
// attributed to the secret side (ErrSecret).
func TestResolveEnv_secrets_without_resolver_is_invalid(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(storemem.New())
	_, err := envresolve.ResolveEnv(ctx, envresolve.Deps{Store: st, Identity: devIdentity},
		"team-a", nil, []v1.ObjectName{"quack"})
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.True(t, errors.Is(err, envresolve.ErrSecret), "a declared-but-unwired secret is attributed to the secret side")
}

// A missing ConfigMap fails with the config sentinel (ErrConfig) AND preserves the underlying
// fault kind (NotFound) — so the Function reconciler can pick ConfigResolveFailed while the fault
// kind still flows.
func TestResolveEnv_missing_configmap_is_ErrConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(storemem.New())
	_, err := envresolve.ResolveEnv(ctx, envresolve.Deps{Store: st, Identity: devIdentity},
		"team-a", []v1.ObjectName{"nope"}, nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, envresolve.ErrConfig), "a missing ConfigMap is attributed to the config side")
	require.False(t, errors.Is(err, envresolve.ErrSecret))
	require.Equal(t, fault.NotFound, fault.KindOf(err), "the underlying store NotFound kind is preserved through the join")
}

// A secret-resolution failure fails with the secret sentinel (ErrSecret) AND preserves the fault
// kind (Forbidden), never crossing into ErrConfig.
func TestResolveEnv_secret_failure_is_ErrSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(storemem.New())
	sr := &fakeSecretResolver{err: fault.Forbiddenf("secrets", "read denied")}
	_, err := envresolve.ResolveEnv(ctx, envresolve.Deps{Secrets: sr, Store: st, Identity: devIdentity},
		"team-a", nil, []v1.ObjectName{"quack"})
	require.Error(t, err)
	require.True(t, errors.Is(err, envresolve.ErrSecret), "a secret-resolution failure is attributed to the secret side")
	require.False(t, errors.Is(err, envresolve.ErrConfig))
	require.Equal(t, fault.Forbidden, fault.KindOf(err), "the underlying PDP Forbidden kind is preserved through the join")
}

// Env delivery cannot carry a NUL byte (exec rejects it), so a NUL in a bound ConfigMap or Secret
// value fails resolution closed on its own side, naming the object and the key, never the value.
func TestIssue356_NULValueRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const ns v1.NamespaceName = "team-a"
	st := store.New(storemem.New())
	createConfigMap(t, st, ns, "nul-cfg", map[string]string{"CFG_NUL": "a\x00b"})
	obj, ok := v1.NewObject(v1.KindSecret)
	require.True(t, ok)
	sec := obj.(*v1.Secret)
	sec.Name, sec.Namespace, sec.ResourceGroup = "nul-sec", ns, "rg1"
	sec.Spec.Data = map[string][]byte{"SEC_NUL": []byte("a\x00b")}
	_, err := st.Create(ctx, sec)
	require.NoError(t, err)
	sr, err := secrets.NewResolver(secrets.Deps{Store: st, Authorizer: rbac.New()})
	require.NoError(t, err)
	deps := envresolve.Deps{Secrets: sr, Store: st, Identity: devIdentity}

	env, err := envresolve.ResolveEnv(ctx, deps, ns, []v1.ObjectName{"nul-cfg"}, nil)
	require.Error(t, err, "a NUL ConfigMap value must be rejected, got env %q", env)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorIs(t, err, envresolve.ErrConfig)
	require.Contains(t, err.Error(), `"nul-cfg"`)
	require.Contains(t, err.Error(), `"CFG_NUL"`)
	require.NotContains(t, err.Error(), "a\x00b")

	env, err = envresolve.ResolveEnv(ctx, deps, ns, nil, []v1.ObjectName{"nul-sec"})
	require.Error(t, err, "a NUL Secret value must be rejected, got env %q", env)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorIs(t, err, envresolve.ErrSecret)
	require.Contains(t, err.Error(), `"nul-sec"`)
	require.Contains(t, err.Error(), `"SEC_NUL"`)
	require.NotContains(t, err.Error(), "a\x00b")
}

// With several values env delivery cannot carry, the error names the first bad key in sorted order on
// every resolve, so a Function's Ready message does not change between reconciles.
func TestIssue449_FirstBadKeyInSortedOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const ns v1.NamespaceName = "team-a"
	st := store.New(storemem.New())
	cfg := map[string]string{"A_OK": "fine"}
	sec := map[string][]byte{"A_OK": []byte("fine")}
	for i := range 16 {
		k := fmt.Sprintf("BAD_%02d", i)
		cfg[k] = "a\x00b"
		sec[k] = []byte("\xff")
	}
	createConfigMap(t, st, ns, "bad-cfg", cfg)
	obj, ok := v1.NewObject(v1.KindSecret)
	require.True(t, ok)
	s := obj.(*v1.Secret)
	s.Name, s.Namespace, s.ResourceGroup = "bad-sec", ns, "rg1"
	s.Spec.Data = sec
	_, err := st.Create(ctx, s)
	require.NoError(t, err)
	sr, err := secrets.NewResolver(secrets.Deps{Store: st, Authorizer: rbac.New()})
	require.NoError(t, err)
	deps := envresolve.Deps{Secrets: sr, Store: st, Identity: devIdentity}

	for _, tc := range []struct {
		name            string
		config, secrets []v1.ObjectName
		side            error
	}{
		{name: "config", config: []v1.ObjectName{"bad-cfg"}, side: envresolve.ErrConfig},
		{name: "secret", secrets: []v1.ObjectName{"bad-sec"}, side: envresolve.ErrSecret},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for range 50 {
				_, err := envresolve.ResolveEnv(ctx, deps, ns, tc.config, tc.secrets)
				require.ErrorIs(t, err, tc.side)
				require.Contains(t, err.Error(), `key "BAD_00"`)
			}
		})
	}
}
