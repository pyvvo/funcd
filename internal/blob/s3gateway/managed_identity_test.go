package s3gateway

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/versity/versitygw/auth"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

type fakeManagedExternal struct {
	secret, ns string
	ok         bool
}

func (f fakeManagedExternal) Lookup(string) (string, string, bool) { return f.secret, f.ns, f.ok }

// scenario: identity-access-key deterministic + distinct from a Function key + round-trips (ADR-0135).
func TestIdentityAccessKeyDeterministicAndDistinct(t *testing.T) {
	t.Parallel()
	a1 := IdentityAccessKey("data", "dropper")
	require.Equal(t, a1, IdentityAccessKey("data", "dropper"), "deterministic")
	require.True(t, strings.HasPrefix(a1, identityAccessKeyPrefix))
	require.False(t, strings.HasPrefix(a1, accessKeyPrefix), "an Identity key does not carry the Function prefix")

	// It is NOT decoded as a Function key (so it falls through to external.Lookup) and round-trips.
	_, _, fnOK := decodeAccess(a1)
	require.False(t, fnOK)
	ns, name, ok := DecodeIdentityAccess(a1)
	require.True(t, ok)
	require.Equal(t, "data", ns)
	require.Equal(t, "dropper", name)

	// A Function key is not an Identity key.
	fnKey := DeriveKeypair([]byte("m"), "data", "fn").AccessKey
	_, _, idOK := DecodeIdentityAccess(fnKey)
	require.False(t, idOK)
}

// scenario: external-caller-resolves-to-identity — an Identity-issued key maps to the Identity principal.
func TestPrincipalForMapsIdentityKey(t *testing.T) {
	t.Parallel()
	access := IdentityAccessKey("data", "dropper")
	pr, err := principalFor(auth.Account{Access: access}, fakeManagedExternal{secret: "s", ns: "data", ok: true})
	require.NoError(t, err)
	require.Equal(t, v1.KindIdentity, pr.ref.Type)
	require.Equal(t, v1.NamespaceName("data"), pr.ref.Namespace)
	require.Equal(t, v1.ObjectName("dropper"), pr.ref.Name)

	// A Function key still resolves to a Function principal (unchanged).
	fnKey := DeriveKeypair([]byte("m"), "data", "fn").AccessKey
	fpr, err := principalFor(auth.Account{Access: fnKey}, fakeManagedExternal{ok: false})
	require.NoError(t, err)
	require.Equal(t, v1.KindFunction, fpr.ref.Type)
}
