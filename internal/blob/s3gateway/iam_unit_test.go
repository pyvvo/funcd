package s3gateway

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/versity/versitygw/auth"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// TestDeriveKeypairDeterministic (ADR-0085) — DeriveKeypair is pure and stable.
func TestDeriveKeypairDeterministic(t *testing.T) {
	t.Parallel()
	m := []byte("master")
	a := DeriveKeypair(m, v1.KindFunction, "ns", "fn")
	b := DeriveKeypair(m, v1.KindFunction, "ns", "fn")
	require.Equal(t, a, b)
	require.NotEmpty(t, a.AccessKey)
	require.NotEmpty(t, a.SecretKey)
}

// TestDecodeAccessRoundTrip (ADR-0085) — an in-platform access key decodes back to its
// (kind, ns, name); an external access key is rejected.
func TestDecodeAccessRoundTrip(t *testing.T) {
	t.Parallel()
	for _, kind := range []v1.Kind{v1.KindFunction, v1.KindCatalogService} {
		kp := DeriveKeypair([]byte("m"), kind, "default", "analytics")
		gotKind, ns, name, ok := decodeAccess(kp.AccessKey)
		require.True(t, ok)
		require.Equal(t, kind, gotKind)
		require.Equal(t, "default", ns)
		require.Equal(t, "analytics", name)
	}

	_, _, _, ok := decodeAccess("EXTERNALKEY123")
	require.False(t, ok, "a non-FUNCD access key is external")
}

// TestDeriveKeypairKindsDiffer (ADR-0175) — a Function and a CatalogService of one name hold different keys, and
// each key decodes to its own kind.
func TestDeriveKeypairKindsDiffer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ ns, name string }{{"default", "lake"}, {"data", "a"}, {"ns-with-long-name", "catalog-engine"}} {
		fn := DeriveKeypair([]byte("m"), v1.KindFunction, tc.ns, tc.name)
		cs := DeriveKeypair([]byte("m"), v1.KindCatalogService, tc.ns, tc.name)
		require.NotEqual(t, fn.AccessKey, cs.AccessKey, "%s/%s", tc.ns, tc.name)
		require.NotEqual(t, fn.SecretKey, cs.SecretKey, "%s/%s", tc.ns, tc.name)
		fnKind, _, _, ok := decodeAccess(fn.AccessKey)
		require.True(t, ok)
		require.Equal(t, v1.KindFunction, fnKind)
		csKind, _, _, ok := decodeAccess(cs.AccessKey)
		require.True(t, ok)
		require.Equal(t, v1.KindCatalogService, csKind)
	}
	_, _, _, ok := decodeAccess(DeriveKeypair([]byte("m"), v1.KindIdentity, "default", "lake").AccessKey)
	require.False(t, ok, "a kind with no key code yields an undecodable key")
}

// TestIAMGetUserAccount (ADR-0085) — in-platform derives the secret; external resolves
// from the store; unknown is not-found.
func TestIAMGetUserAccount(t *testing.T) {
	t.Parallel()
	master := []byte("node-master")
	ext := mapExternal{"EXT": {secret: "s3cr3t", ns: "tenantA"}}
	im := &iam{master: master, external: ext}

	kp := DeriveKeypair(master, v1.KindFunction, "default", "fn1")
	acct, err := im.GetUserAccount(kp.AccessKey)
	require.NoError(t, err)
	require.Equal(t, kp.SecretKey, acct.Secret, "the derived secret is recomputed in the IAM")

	eacct, err := im.GetUserAccount("EXT")
	require.NoError(t, err)
	require.Equal(t, "s3cr3t", eacct.Secret)

	// An unknown/malformed key must return the versity sentinel auth.ErrNoSuchUser so the SigV4
	// middleware maps it to a clean 403 InvalidAccessKeyId (not a 500 InternalError the SDK retries).
	_, err = im.GetUserAccount("NOPE")
	require.ErrorIs(t, err, auth.ErrNoSuchUser, "an unknown access key fails closed with the 403-mapped sentinel")
}

// TestIAMMutatorsNotSupported (ADR-0085) — the three mutators reject (derived accounts
// have no mutable state); ListUserAccounts + Shutdown are inert.
func TestIAMMutatorsNotSupported(t *testing.T) {
	t.Parallel()
	im := &iam{master: []byte("m")}
	require.Error(t, im.CreateAccount(auth.Account{Access: "x", Secret: "y"}))
	require.Error(t, im.UpdateUserAccount("x", auth.MutableProps{}))
	require.Error(t, im.DeleteUserAccount("x"))
	list, err := im.ListUserAccounts()
	require.NoError(t, err)
	require.Empty(t, list)
	require.NoError(t, im.Shutdown())
}

type mapExternal map[string]struct {
	secret string
	ns     string
}

func (m mapExternal) Lookup(access string) (string, string, bool) {
	e, ok := m[access]
	return e.secret, e.ns, ok
}
