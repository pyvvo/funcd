package aesgcm_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/secrets/aesgcm"
)

func key32() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

// scenario: encryptor-roundtrips.
func TestScenarioEncryptorRoundtrips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	enc, err := aesgcm.NewAESEncryptor(key32())
	require.NoError(t, err)

	plain := []byte("s3cr3t-value")
	ct1, err := enc.Encrypt(ctx, plain)
	require.NoError(t, err)
	require.NotEqual(t, plain, ct1, "ciphertext differs from plaintext")
	require.NotContains(t, string(ct1), "s3cr3t", "plaintext not present in ciphertext")

	got, err := enc.Decrypt(ctx, ct1)
	require.NoError(t, err)
	require.Equal(t, plain, got, "decrypt recovers the plaintext")

	// unique per call (random nonce)
	ct2, err := enc.Encrypt(ctx, plain)
	require.NoError(t, err)
	require.False(t, bytes.Equal(ct1, ct2), "two encrypts of the same plaintext differ (random nonce)")

	// tamper → fail
	tampered := append([]byte(nil), ct1...)
	tampered[len(tampered)-1] ^= 0xFF
	_, err = enc.Decrypt(ctx, tampered)
	require.Error(t, err, "tampered ciphertext fails authentication")
}

// scenario: encryptor-rejects-bad-key.
func TestScenarioEncryptorRejectsBadKey(t *testing.T) {
	t.Parallel()
	_, err := aesgcm.NewAESEncryptor(make([]byte, 16)) // AES-128 length, not 32
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

// The cookie seal opens only under its key and its additional data (ADR-0335 Decision 5).
func TestSealOpensOnlyUnderItsKeyAndAdditionalData(t *testing.T) {
	t.Parallel()
	ad := []byte("funcd_session.staging.sso")
	sealed, err := aesgcm.Seal(key32(), []byte(`{"id_token":"x"}`), ad)
	require.NoError(t, err)
	again, err := aesgcm.Seal(key32(), []byte(`{"id_token":"x"}`), ad)
	require.NoError(t, err)
	require.NotEqual(t, sealed, again, "a fresh nonce each call")

	plain, err := aesgcm.Open(key32(), sealed, ad)
	require.NoError(t, err)
	require.Equal(t, `{"id_token":"x"}`, string(plain))

	other := key32()
	other[0] ^= 0xFF
	for name, open := range map[string]func() ([]byte, error){
		"another key":           func() ([]byte, error) { return aesgcm.Open(other, sealed, ad) },
		"other additional data": func() ([]byte, error) { return aesgcm.Open(key32(), sealed, []byte("funcd_session.staging.public")) },
		"no additional data":    func() ([]byte, error) { return aesgcm.Open(key32(), sealed, nil) },
		"a changed byte": func() ([]byte, error) {
			b := append([]byte(nil), sealed...)
			b[len(b)-1] ^= 1
			return aesgcm.Open(key32(), b, ad)
		},
		"shorter than its nonce": func() ([]byte, error) { return aesgcm.Open(key32(), sealed[:4], ad) },
		"a 16-byte key":          func() ([]byte, error) { return aesgcm.Open(key32()[:16], sealed, ad) },
	} {
		_, err := open()
		require.Error(t, err, name)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
	}
	_, err = aesgcm.Seal(key32()[:16], []byte("x"), ad)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a 16-byte key seals nothing")
}
