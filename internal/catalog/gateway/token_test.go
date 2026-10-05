package gateway

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
)

// testRandomPart returns a random part shaped like the identity reconciler's: base64 of 32 random bytes.
func testRandomPart(t *testing.T) string {
	t.Helper()
	var b [32]byte
	_, err := rand.Read(b[:])
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(b[:])
}

// nonCanonicalPrefix returns an access key id that decodes to (ns, name) but is not the canonical
// IdentityAccessKey: the last base32 character differs only in its unused trailing bits.
func nonCanonicalPrefix(t *testing.T, ns, name string) string {
	t.Helper()
	canonical := s3gateway.IdentityAccessKey(ns, name)
	for _, c := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567" {
		alt := canonical[:len(canonical)-1] + string(c)
		if alt == canonical {
			continue
		}
		if gotNS, gotName, ok := s3gateway.DecodeIdentityAccess(alt); ok && gotNS == ns && gotName == name {
			return alt
		}
	}
	require.FailNow(t, "no non-canonical encoding found", "%s/%s", ns, name)
	return ""
}

func TestIdentityCatalogTokenRoundTrip(t *testing.T) {
	t.Parallel()
	random := testRandomPart(t)
	require.Len(t, random, randomPartLen)

	tok := IdentityCatalogToken("data", "analyst", random)
	prefix := s3gateway.IdentityAccessKey("data", "analyst")
	require.Equal(t, prefix+"."+random, tok)
	ns, name, ok := decodeIdentityCatalogToken(tok)
	require.True(t, ok)
	require.Equal(t, v1.NamespaceName("data"), ns)
	require.Equal(t, v1.ObjectName("analyst"), name)

	long := strings.Repeat("a", 63)
	maxTok := IdentityCatalogToken(v1.NamespaceName(long), v1.ObjectName(long), random)
	require.Len(t, maxTok, 255, "a 63/63 owner gives the maximum token length")
	ns, name, ok = decodeIdentityCatalogToken(maxTok)
	require.True(t, ok)
	require.Equal(t, v1.NamespaceName(long), ns)
	require.Equal(t, v1.ObjectName(long), name)

	refused := map[string]string{
		"no separator":           prefix + random,
		"empty random part":      prefix + ".",
		"random part too short":  prefix + "." + random[:randomPartLen-1],
		"random part too long":   prefix + "." + random + "A",
		"function access key":    s3gateway.DeriveKeypair([]byte("node-master"), v1.KindFunction, "data", "analyst").AccessKey + "." + random,
		"bad base32":             "FUNCID!!!!" + "." + random,
		"empty owner body":       "FUNCID." + random,
		"non-DNS namespace":      s3gateway.IdentityAccessKey("Data", "analyst") + "." + random,
		"non-DNS name":           s3gateway.IdentityAccessKey("data", "an_alyst") + "." + random,
		"name over 63":           s3gateway.IdentityAccessKey("data", strings.Repeat("a", 64)) + "." + random,
		"non-canonical bits":     nonCanonicalPrefix(t, "data", "analyst") + "." + random,
		"non-canonical newline":  prefix[:10] + "\n" + prefix[10:] + "." + random,
		"bare access key id":     prefix,
		"old 44-character token": random,
	}
	for label, token := range refused {
		_, _, ok := decodeIdentityCatalogToken(token)
		require.False(t, ok, label)
	}
}
