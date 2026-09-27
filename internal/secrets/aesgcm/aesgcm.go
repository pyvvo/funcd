// Package aesgcm is the V1 at-rest secrets encryptor (ADR-0022): an AES-256-GCM
// store.Encryptor that fills the ADR-0006 seam. Each Encrypt uses a fresh random nonce
// prepended to the ciphertext; Decrypt authenticates (GCM) and fails on tamper. Stdlib
// AEAD only — the envelope/KMS/OpenBAO production drivers swap in behind store.Encryptor.
package aesgcm

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"io"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/store"
)

// keyLen is the AES-256 key length in bytes.
const keyLen = 32

type encryptor struct {
	gcm cipher.AEAD
}

// NewAESEncryptor returns an AES-256-GCM store.Encryptor. key must be exactly 32 bytes
// (a misconfigured key must not silently weaken crypto).
func NewAESEncryptor(key []byte) (store.Encryptor, error) {
	const op = "secrets.aesgcm.NewAESEncryptor"
	if len(key) != keyLen {
		return nil, fault.Invalidf(op, "key must be %d bytes (AES-256), got %d", keyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "new cipher")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "new gcm")
	}
	return &encryptor{gcm: gcm}, nil
}

// Encrypt seals plaintext with a fresh random nonce, returning nonce||ciphertext.
func (e *encryptor) Encrypt(_ context.Context, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, e.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "secrets.aesgcm.Encrypt", "read nonce")
	}
	// Seal appends the ciphertext to the nonce (dst == nonce), yielding nonce||ciphertext.
	return e.gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt splits the nonce, authenticates, and returns the plaintext; tamper → fault.Invalid.
func (e *encryptor) Decrypt(_ context.Context, data []byte) ([]byte, error) {
	const op = "secrets.aesgcm.Decrypt"
	ns := e.gcm.NonceSize()
	if len(data) < ns {
		return nil, fault.Invalidf(op, "ciphertext too short (%d < nonce %d)", len(data), ns)
	}
	nonce, ciphertext := data[:ns], data[ns:]
	plaintext, err := e.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "decrypt/authenticate (tampered or wrong key)")
	}
	return plaintext, nil
}
