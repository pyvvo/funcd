package secrets_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/auth/rbac"
	"github.com/green-0-rabbit/funcd/internal/secrets"
	"github.com/green-0-rabbit/funcd/internal/secrets/aesgcm"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
)

func key32() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func createSecret(t *testing.T, st store.Store, name, k, v string) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindSecret)
	require.True(t, ok)
	sec := obj.(*v1.Secret)
	sec.Name = v1.ObjectName(name)
	sec.Namespace = "team-a"
	sec.ResourceGroup = "rg1"
	sec.Spec.Type = v1.SecretTypeOpaque
	sec.Spec.Data = map[string][]byte{k: []byte(v)}
	_, err := st.Create(context.Background(), sec)
	require.NoError(t, err)
}

// scenario: secret-stored-encrypted-at-rest.
func TestScenarioSecretStoredEncryptedAtRest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	enc, err := aesgcm.NewAESEncryptor(key32())
	require.NoError(t, err)
	eng := memory.New()
	st := store.New(eng, store.WithEncryptor([]v1.Kind{v1.KindSecret}, enc))

	createSecret(t, st, "creds", "API_KEY", "s3cr3t")

	// read-back returns plaintext (the store decrypts transparently)
	obj, err := st.Get(ctx, v1.KindSecret.GVK(), "team-a", "creds")
	require.NoError(t, err)
	require.Equal(t, []byte("s3cr3t"), obj.(*v1.Secret).Spec.Data["API_KEY"])

	// raw at rest is ciphertext — neither the plaintext nor its JSON base64 appears
	var raw []byte
	require.NoError(t, eng.View(ctx, func(tx store.Txn) error {
		b, found, gerr := tx.Get(v1.KindSecret.GVK().String(), "team-a/creds")
		require.True(t, found)
		raw = b
		return gerr
	}))
	require.NotContains(t, string(raw), "s3cr3t", "plaintext is not stored")
	require.NotContains(t, string(raw), base64.StdEncoding.EncodeToString([]byte("s3cr3t")), "JSON-encoded secret is not stored")
}

func dev(ns v1.NamespaceName) auth.Identity {
	return auth.Identity{Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{ns}}
}

// scenario: resolver-returns-bound-secret-env.
func TestScenarioResolverReturnsBoundSecretEnv(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	enc, _ := aesgcm.NewAESEncryptor(key32())
	st := store.New(memory.New(), store.WithEncryptor([]v1.Kind{v1.KindSecret}, enc))
	createSecret(t, st, "creds", "API_KEY", "s3cr3t")

	r, err := secrets.NewResolver(secrets.Deps{Store: st, Authorizer: rbac.New()})
	require.NoError(t, err)

	env, err := r.ResolveEnv(ctx, dev("team-a"), "team-a", []string{"creds"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"API_KEY": "s3cr3t"}, env, "decrypted secret delivered as env vars")
}

// scenario: resolver-authorizes.
func TestScenarioResolverAuthorizes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	r, err := secrets.NewResolver(secrets.Deps{Store: st, Authorizer: rbac.New()})
	require.NoError(t, err)

	// a developer scoped to team-a resolving in team-b → Forbidden (before any read).
	_, err = r.ResolveEnv(ctx, dev("team-a"), "team-b", []string{"creds"})
	require.Equal(t, fault.Forbidden, fault.KindOf(err))
}
