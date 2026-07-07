package s3gateway

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/versity/versitygw/auth"
)

// TestDeriveKeypairDeterministic (ADR-0085) — DeriveKeypair is pure and stable.
func TestDeriveKeypairDeterministic(t *testing.T) {
	t.Parallel()
	m := []byte("master")
	a := DeriveKeypair(m, "ns", "fn")
	b := DeriveKeypair(m, "ns", "fn")
	require.Equal(t, a, b)
	require.NotEmpty(t, a.AccessKey)
	require.NotEmpty(t, a.SecretKey)
}

// TestDecodeAccessRoundTrip (ADR-0085) — an in-platform access key decodes back to its
// Ref; an external access key is rejected.
func TestDecodeAccessRoundTrip(t *testing.T) {
	t.Parallel()
	kp := DeriveKeypair([]byte("m"), "default", "analytics")
	ns, fn, ok := decodeAccess(kp.AccessKey)
	require.True(t, ok)
	require.Equal(t, "default", ns)
	require.Equal(t, "analytics", fn)

	_, _, ok = decodeAccess("EXTERNALKEY123")
	require.False(t, ok, "a non-FUNCD access key is external")
}

// TestIAMGetUserAccount (ADR-0085) — in-platform derives the secret; external resolves
// from the store; unknown is not-found.
func TestIAMGetUserAccount(t *testing.T) {
	t.Parallel()
	master := []byte("node-master")
	ext := mapExternal{"EXT": {secret: "s3cr3t", ns: "tenantA"}}
	im := &iam{master: master, external: ext}

	kp := DeriveKeypair(master, "default", "fn1")
	acct, err := im.GetUserAccount(kp.AccessKey)
	require.NoError(t, err)
	require.Equal(t, kp.SecretKey, acct.Secret, "the derived secret is recomputed in the IAM")

	eacct, err := im.GetUserAccount("EXT")
	require.NoError(t, err)
	require.Equal(t, "s3cr3t", eacct.Secret)

	_, err = im.GetUserAccount("NOPE")
	require.Error(t, err, "an unknown access key is not found")
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
