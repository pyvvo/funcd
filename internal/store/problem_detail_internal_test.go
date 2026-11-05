package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

const encryptCause = "dial tcp 10.0.0.9:8200: connect: connection refused"

// failingEncryptor fails each encrypt and decrypt.
type failingEncryptor struct{}

func (failingEncryptor) Encrypt(context.Context, []byte) ([]byte, error) {
	return nil, errors.New(encryptCause)
}

func (failingEncryptor) Decrypt(context.Context, []byte) ([]byte, error) {
	return nil, errors.New(encryptCause)
}

// documentOf is a Function that encodes as doc, or fails to encode when doc is nil.
type documentOf struct {
	*v1.Function
	doc []byte
}

func (d documentOf) MarshalJSON() ([]byte, error) {
	if d.doc == nil {
		return nil, errors.New(encryptCause)
	}
	return d.doc, nil
}

// A failure of the store's codec keeps its error's text out of the Msg, so a 500's detail holds no cause and the log's
// full error does.
func TestCodecFaultsHoldNoCause(t *testing.T) {
	ctx := context.Background()
	plain := &store{}
	sealed := &store{enc: map[v1.Kind]Encryptor{v1.KindSecret: failingEncryptor{}}}
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	badTimeout := []byte(`{"spec": {"timeout": "not-a-duration"}}`)
	for _, tc := range []struct {
		name  string
		call  func() error
		cause string
	}{
		{"encode's marshal", func() error { _, err := plain.encode(ctx, v1.KindFunction, documentOf{Function: fn}); return err }, encryptCause},
		{"encode's encrypt", func() error { _, err := sealed.encode(ctx, v1.KindSecret, &v1.Secret{}); return err }, encryptCause},
		{"decode's decrypt", func() error { _, err := sealed.decode(ctx, v1.KindSecret, []byte("x")); return err }, encryptCause},
		{"decode's unmarshal", func() error { _, err := plain.decode(ctx, v1.KindFunction, badTimeout); return err }, "not-a-duration"},
		{"clone's marshal", func() error { _, err := cloneObject(documentOf{Function: fn}); return err }, encryptCause},
		{"clone's unmarshal", func() error { _, err := cloneObject(documentOf{Function: fn, doc: badTimeout}); return err }, "not-a-duration"},
		{"specOf's marshal", func() error { _, err := specOf(documentOf{Function: fn}); return err }, encryptCause},
		{"specOf's unmarshal", func() error { _, err := specOf(documentOf{Function: fn, doc: []byte(`[1]`)}); return err }, "cannot unmarshal"},
		{"a corrupt revision", func() error { _, err := parseRevision([]byte("not-a-number")); return err }, "not-a-number"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.cause) {
				t.Fatalf("the error %q has lost its cause %q", err, tc.cause)
			}
			if detail := fault.ToProblem(err).Detail; strings.Contains(detail, tc.cause) {
				t.Fatalf("detail = %q holds the cause %q", detail, tc.cause)
			}
		})
	}
}

// A stored object whose resourceVersion does not parse fails the watch's first events the same way.
func TestCorruptResourceVersionHoldsNoCause(t *testing.T) {
	ctx := context.Background()
	engine := newFakeEngine()
	s := New(engine).(*store)
	const corrupt = "not-a-version"
	cm := &v1.ConfigMap{TypeMeta: v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap}}
	cm.Name, cm.Namespace, cm.ResourceVersion = "c", "default", corrupt
	raw, err := s.encode(ctx, v1.KindConfigMap, cm)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	gvk := v1.KindConfigMap.GVK()
	if err := engine.Update(ctx, func(tx Txn) error { return tx.Put(gvk.String(), keyFor("default", "c"), raw) }); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err = s.initialEvents(ctx, gvk, "default", 0)
	if err == nil || !strings.Contains(err.Error(), corrupt) {
		t.Fatalf("err = %v, want the corrupt version in the full error", err)
	}
	if detail := fault.ToProblem(err).Detail; strings.Contains(detail, corrupt) {
		t.Fatalf("detail = %q holds the cause", detail)
	}
}
