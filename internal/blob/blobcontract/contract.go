// Package blobcontract is the shared conformance suite for the blob.Bucket port
// (ADR-0007). RunContract asserts the storage-layer semantics that must hold for
// every backend — memory and file run it, proving driver-conformance-parity.
package blobcontract

import (
	"bytes"
	"context"
	"testing"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/blob"
)

// RunContract runs the backend-agnostic blob scenarios against a Bucket built by
// newBucket. newBucket must return a fresh, empty bucket on each call.
func RunContract(t *testing.T, newBucket func(t *testing.T) blob.Bucket) {
	t.Helper()
	t.Run("blob-roundtrip", func(t *testing.T) { testRoundtrip(t, newBucket(t)) })
	t.Run("not-found", func(t *testing.T) { testNotFound(t, newBucket(t)) })
	t.Run("delete-removes", func(t *testing.T) { testDeleteRemoves(t, newBucket(t)) })
	t.Run("list-by-prefix", func(t *testing.T) { testListByPrefix(t, newBucket(t)) })
	t.Run("signed-url-unsupported-locally", func(t *testing.T) { testSignedURLUnsupported(t, newBucket(t)) })
}

func testRoundtrip(t *testing.T, b blob.Bucket) {
	ctx := context.Background()
	val := []byte("hello funcd")
	if err := b.Put(ctx, "k1", val); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := b.Get(ctx, "k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, val) {
		t.Fatalf("Get: got %q want %q", got, val)
	}
	ok, err := b.Exists(ctx, "k1")
	if err != nil || !ok {
		t.Fatalf("Exists: got (%v,%v) want (true,nil)", ok, err)
	}
}

func testNotFound(t *testing.T, b blob.Bucket) {
	ctx := context.Background()
	if _, err := b.Get(ctx, "nope"); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("Get(absent): kind=%v want not_found", fault.KindOf(err))
	}
	if err := b.Delete(ctx, "nope"); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("Delete(absent): kind=%v want not_found", fault.KindOf(err))
	}
	ok, err := b.Exists(ctx, "nope")
	if err != nil || ok {
		t.Fatalf("Exists(absent): got (%v,%v) want (false,nil)", ok, err)
	}
}

func testDeleteRemoves(t *testing.T, b blob.Bucket) {
	ctx := context.Background()
	if err := b.Put(ctx, "d", []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := b.Delete(ctx, "d"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := b.Get(ctx, "d"); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("Get after delete: kind=%v want not_found", fault.KindOf(err))
	}
	if ok, _ := b.Exists(ctx, "d"); ok {
		t.Fatal("Exists after delete: true want false")
	}
}

func testListByPrefix(t *testing.T, b blob.Bucket) {
	ctx := context.Background()
	for _, k := range []string{"a/1", "a/2", "b/1"} {
		if err := b.Put(ctx, k, []byte("v")); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
	}
	items, err := b.List(ctx, "a/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 || items[0].Key != "a/1" || items[1].Key != "a/2" {
		keys := make([]string, len(items))
		for i, it := range items {
			keys[i] = it.Key
		}
		t.Fatalf("List(a/): got %v want [a/1 a/2]", keys)
	}
	if items[0].Size != 1 {
		t.Errorf("List: size=%d want 1", items[0].Size)
	}
}

func testSignedURLUnsupported(t *testing.T, b blob.Bucket) {
	ctx := context.Background()
	if err := b.Put(ctx, "s", []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// memory/file backends have no signer → fault.Unavailable (ADR-0007 §3).
	if _, err := b.SignedURL(ctx, "s", blob.SignOptions{Method: blob.SignGet}); fault.KindOf(err) != fault.Unavailable {
		t.Fatalf("SignedURL on local backend: kind=%v want unavailable", fault.KindOf(err))
	}
}
