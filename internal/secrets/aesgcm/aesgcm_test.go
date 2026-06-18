package aesgcm_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/secrets/aesgcm"
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
