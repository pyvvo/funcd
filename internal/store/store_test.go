package store_test

import (
	"context"
	"testing"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
)

// xorEnc is a trivial reversible cipher standing in for the real at-rest
// encryptor (P-P/F15 supplies tink/envelope). It proves the store routes the
// configured kinds' values through the Encryptor seam.
type xorEnc struct{ key byte }

func (e xorEnc) Encrypt(_ context.Context, pt []byte) ([]byte, error) {
	return xorBytes(pt, e.key), nil
}
func (e xorEnc) Decrypt(_ context.Context, ct []byte) ([]byte, error) {
	return xorBytes(ct, e.key), nil
}

func xorBytes(b []byte, k byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[i] = b[i] ^ k
	}
	return out
}

// scenario: secret-encrypted-at-rest — with WithEncryptor for Secret, a Secret's
// stored bytes are ciphertext (a non-encrypting reader cannot decode them) and a
// Get through the encrypting store round-trips the plaintext.
func TestScenario_SecretEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	eng := memory.New()
	s := store.New(eng, store.WithEncryptor([]v1.Kind{v1.KindSecret}, xorEnc{key: 0x5A}))

	obj, ok := v1.NewObject(v1.KindSecret)
	if !ok {
		t.Fatal("NewObject(Secret) returned false")
	}
	sec, _ := obj.(*v1.Secret)
	sec.Name = "db-pw"
	sec.Namespace = "default"
	sec.ResourceGroup = "rg1"
	sec.Spec.Data = map[string][]byte{"password": []byte("s3cr3t")}
	if _, err := s.Create(ctx, sec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Round-trip through the encrypting store returns the plaintext.
	got, err := s.Get(ctx, v1.KindSecret.GVK(), "default", "db-pw")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	gs, _ := got.(*v1.Secret)
	if gs == nil || string(gs.Spec.Data["password"]) != "s3cr3t" {
		t.Fatalf("decrypt round-trip mismatch: %+v", got)
	}

	// At rest the bytes are ciphertext: a NON-encrypting store over the same
	// engine cannot decode the value (it is not plaintext JSON).
	plain := store.New(eng)
	if _, err := plain.Get(ctx, v1.KindSecret.GVK(), "default", "db-pw"); err == nil {
		t.Fatal("non-encrypting reader decoded ciphertext-at-rest; value was not encrypted")
	}

	// With no encryptor configured, a Config (not a Secret) is stored as plaintext
	// — confirming the seam is scoped to the named kinds, not blanket encryption.
	cfgObj, _ := v1.NewObject(v1.KindConfig)
	cfg, _ := cfgObj.(*v1.Config)
	cfg.Name = "plain"
	cfg.Namespace = "default"
	cfg.ResourceGroup = "rg1"
	cfg.Spec.Data = map[string]string{"k": "v"}
	if _, err := s.Create(ctx, cfg); err != nil {
		t.Fatalf("Create(config): %v", err)
	}
	if _, err := plain.Get(ctx, v1.KindConfig.GVK(), "default", "plain"); err != nil {
		t.Fatalf("non-encrypting reader should read the un-encrypted Config: %v", err)
	}
}
