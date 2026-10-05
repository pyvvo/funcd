// Package blobcontract is the shared conformance suite for the blob.Bucket port
// (ADR-0007). RunContract asserts the storage-layer semantics that must hold for
// every backend — memory and file run it, proving driver-conformance-parity.
package blobcontract

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // the port's content digest is MD5 (ADR-0159), not a security primitive
	"slices"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
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
	// Additive (ADR-0080): RangeReader is an OPTIONAL capability — skipped for a driver
	// that does not implement it, so existing backends keep passing unchanged.
	t.Run("range-reader-optional", func(t *testing.T) { testRangeReader(t, newBucket(t)) })
	t.Run("attributes-digest", func(t *testing.T) { testAttributesDigest(t, newBucket(t)) })
	t.Run("put-options-roundtrip", func(t *testing.T) { testPutOptionsRoundtrip(t, newBucket(t)) })
	t.Run("attributes-not-found", func(t *testing.T) { testAttributesNotFound(t, newBucket(t)) })
	t.Run("list-after", func(t *testing.T) { testListAfter(t, newBucket(t)) })
}

// testListAfter: ListAfter returns the keys under prefix strictly after `after`, sorted, at most limit, with
// more true exactly when a further key under prefix exists, and the fields List fills (ADR-0184).
func testListAfter(t *testing.T, b blob.Bucket) {
	ctx := context.Background()
	for _, k := range []string{"la/d", "la/b", "lb/x", "la/a", "l", "la/c", "first q", "first pagez", "first page", "first a", "fir"} {
		if err := b.Put(ctx, k, []byte(k), blob.PutOptions{}); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
	}
	cases := []struct {
		prefix, after string
		limit         int
		want          []string
		more          bool
	}{
		{"la/", "", 2, []string{"la/a", "la/b"}, true},
		{"la/", "la/b", 2, []string{"la/c", "la/d"}, false},
		{"la/", "la/bb", 10, []string{"la/c", "la/d"}, false},
		{"la/", "la/c", 1, []string{"la/d"}, false},
		{"la/", "la/d", 1, nil, false},
		{"la/", "a", 10, []string{"la/a", "la/b", "la/c", "la/d"}, false},
		{"la/", "zzz", 10, nil, false},
		// "first page" is gocloud's FirstPageToken; as after it is a key like any other.
		{"first", "first page", 1, []string{"first pagez"}, true},
		{"first", "first page", 5, []string{"first pagez", "first q"}, false},
	}
	for _, c := range cases {
		items, more, err := b.ListAfter(ctx, c.prefix, c.after, c.limit)
		if err != nil {
			t.Fatalf("ListAfter(%q, %q, %d): %v", c.prefix, c.after, c.limit, err)
		}
		if got := keysOf(items); !slices.Equal(got, c.want) || more != c.more {
			t.Fatalf("ListAfter(%q, %q, %d): got (%v, more %v) want (%v, more %v)", c.prefix, c.after, c.limit, got, more, c.want, c.more)
		}
	}

	// A page's last key resumes the listing, as memblob's page token does.
	var paged []string
	for after := ""; ; {
		items, more, err := b.ListAfter(ctx, "la/", after, 1)
		if err != nil {
			t.Fatalf("ListAfter(la/, %q, 1): %v", after, err)
		}
		paged = append(paged, keysOf(items)...)
		if !more {
			break
		}
		after = items[len(items)-1].Key
	}
	if want := []string{"la/a", "la/b", "la/c", "la/d"}; !slices.Equal(paged, want) {
		t.Fatalf("paging la/ by one: got %v want %v", paged, want)
	}

	listed, err := b.List(ctx, "la/")
	if err != nil {
		t.Fatalf("List(la/): %v", err)
	}
	ranged, _, err := b.ListAfter(ctx, "la/", "", 10)
	if err != nil {
		t.Fatalf("ListAfter(la/): %v", err)
	}
	for i := range listed {
		l, r := listed[i], ranged[i]
		if l.Key != r.Key || l.Size != r.Size || !bytes.Equal(l.MD5, r.MD5) || !l.ModTime.Equal(r.ModTime) {
			t.Fatalf("ListAfter entry %+v differs from List's %+v", r, l)
		}
	}

	for _, limit := range []int{0, -1} {
		if _, _, err := b.ListAfter(ctx, "la/", "", limit); fault.KindOf(err) != fault.Invalid {
			t.Fatalf("ListAfter(limit %d): kind=%v want invalid", limit, fault.KindOf(err))
		}
	}
}

func keysOf(items []blob.Attributes) []string {
	var keys []string
	for _, it := range items {
		keys = append(keys, it.Key)
	}
	return keys
}

// testAttributesDigest: Attributes and every List entry carry the MD5 of the written bytes (ADR-0159). It
// SKIPS a driver that has no digest for the object (s3://), whose MD5 stays empty.
func testAttributesDigest(t *testing.T, b blob.Bucket) {
	ctx := context.Background()
	val := []byte("digest me")
	want := md5.Sum(val) //nolint:gosec // content digest, not security
	if err := b.Put(ctx, "dg/k", val, blob.PutOptions{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	a, err := b.Attributes(ctx, "dg/k")
	if err != nil {
		t.Fatalf("Attributes: %v", err)
	}
	if len(a.MD5) == 0 {
		t.Skip("driver has no content digest for the object")
	}
	if !bytes.Equal(a.MD5, want[:]) || a.Key != "dg/k" || a.Size != int64(len(val)) {
		t.Fatalf("Attributes: got (key %q, size %d, md5 %x) want (dg/k, %d, %x)", a.Key, a.Size, a.MD5, len(val), want)
	}
	items, err := b.List(ctx, "dg/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || !bytes.Equal(items[0].MD5, want[:]) {
		t.Fatalf("List(dg/): got %+v want one entry with md5 %x", items, want)
	}
}

// testPutOptionsRoundtrip: the content type and user metadata given to Put come back from Attributes.
func testPutOptionsRoundtrip(t *testing.T, b blob.Bucket) {
	ctx := context.Background()
	opts := blob.PutOptions{ContentType: "application/json", Metadata: map[string]string{"owner": "etl"}}
	if err := b.Put(ctx, "po", []byte(`{"a":1}`), opts); err != nil {
		t.Fatalf("Put: %v", err)
	}
	a, err := b.Attributes(ctx, "po")
	if err != nil {
		t.Fatalf("Attributes: %v", err)
	}
	if a.ContentType != "application/json" || a.Metadata["owner"] != "etl" || len(a.Metadata) != 1 {
		t.Fatalf("Attributes: got (%q, %v) want (application/json, map[owner:etl])", a.ContentType, a.Metadata)
	}
}

func testAttributesNotFound(t *testing.T, b blob.Bucket) {
	if _, err := b.Attributes(context.Background(), "nope"); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("Attributes(absent): kind=%v want not_found", fault.KindOf(err))
	}
}

// testRangeReader exercises the optional blob.RangeReader capability (ADR-0080): a
// ranged read returns exactly bytes [offset, offset+length), a negative length reads to
// end, and an absent key is fault.NotFound. The case SKIPS a driver that does not
// implement RangeReader (the fallback path is tested at the s3gateway, not the port).
func testRangeReader(t *testing.T, b blob.Bucket) {
	rr, ok := b.(blob.RangeReader)
	if !ok {
		t.Skip("driver does not implement blob.RangeReader (optional capability)")
	}
	ctx := context.Background()
	val := []byte("0123456789")
	if err := b.Put(ctx, "r", val, blob.PutOptions{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := rr.GetRange(ctx, "r", 2, 4)
	if err != nil {
		t.Fatalf("GetRange(2,4): %v", err)
	}
	if !bytes.Equal(got, []byte("2345")) {
		t.Fatalf("GetRange(2,4): got %q want %q", got, "2345")
	}
	toEnd, err := rr.GetRange(ctx, "r", 7, -1)
	if err != nil {
		t.Fatalf("GetRange(7,-1): %v", err)
	}
	if !bytes.Equal(toEnd, []byte("789")) {
		t.Fatalf("GetRange(7,-1): got %q want %q", toEnd, "789")
	}
	if _, err := rr.GetRange(ctx, "absent", 0, 1); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("GetRange(absent): kind=%v want not_found", fault.KindOf(err))
	}
}

func testRoundtrip(t *testing.T, b blob.Bucket) {
	ctx := context.Background()
	val := []byte("hello funcd")
	if err := b.Put(ctx, "k1", val, blob.PutOptions{}); err != nil {
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
	if err := b.Put(ctx, "d", []byte("x"), blob.PutOptions{}); err != nil {
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
	// insert out of lexical order so the asserted [a/1 a/2] order proves the adapter sorts.
	for _, k := range []string{"a/2", "b/1", "a/1"} {
		if err := b.Put(ctx, k, []byte("v"), blob.PutOptions{}); err != nil {
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
	if err := b.Put(ctx, "s", []byte("x"), blob.PutOptions{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// memory/file backends have no signer → fault.Unavailable (ADR-0007 §3).
	if _, err := b.SignedURL(ctx, "s", blob.SignOptions{Method: blob.SignGet}); fault.KindOf(err) != fault.Unavailable {
		t.Fatalf("SignedURL on local backend: kind=%v want unavailable", fault.KindOf(err))
	}
}
