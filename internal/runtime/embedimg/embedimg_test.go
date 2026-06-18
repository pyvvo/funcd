package embedimg

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"
)

// scenario: embedded-image-no-registry — the curated runtime image is loaded from the
// embedded copy (returned as a non-empty reader), never pulled from a registry. The real
// import into containerd is the deferred Linux lane; here we assert the embed is wired and
// non-empty for each curated runtime (the ADR-0054 non-gated unit).
func TestEmbeddedImageNoRegistry(t *testing.T) {
	for _, rt := range []string{"nodejs22", "python314"} {
		r, ok := Tar(rt)
		if !ok {
			t.Fatalf("Tar(%q): ok=false, want true (runtime must have an embedded tar)", rt)
		}
		if r == nil {
			t.Fatalf("Tar(%q): nil reader", rt)
		}
		b, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("Tar(%q): read: %v", rt, err)
		}
		if len(b) == 0 {
			t.Fatalf("Tar(%q): empty reader, want non-empty embedded tar", rt)
		}
	}
}

// scenario: embedded-image-no-registry (negative) — an unknown runtime has no embedded tar,
// so Tar reports ok=false (the Manager then falls through to ImageOverride; neither ⇒ NotFound).
func TestUnknownRuntimeHasNoEmbeddedTar(t *testing.T) {
	if r, ok := Tar("ruby33"); ok || r != nil {
		t.Fatalf(`Tar("ruby33"): got (reader!=nil=%v, ok=%v), want (nil, false)`, r != nil, ok)
	}
}

// scenario: embedded-image-no-registry — the containerd driver maps a curated image ref to its
// embedded tar so it can import it into the function's own containerd namespace (no registry
// pull). A ref that does not match the curated pattern reports ok=false (→ driver Pulls it).
func TestTarForImageRef(t *testing.T) {
	curated := []struct {
		ref string
		rt  string
	}{
		{"funcd/runtime-nodejs22:latest", "nodejs22"},
		{"docker.io/funcd/runtime-nodejs22:latest", "nodejs22"},
		{"funcd/runtime-python314:latest", "python314"},
		{"runtime-python314:v1", "python314"},
		{"funcd/runtime-nodejs22", "nodejs22"}, // no tag
	}
	for _, c := range curated {
		r, ok := TarForImageRef(c.ref)
		if !ok || r == nil {
			t.Fatalf("TarForImageRef(%q): got (nil=%v, ok=%v), want a reader for runtime %q", c.ref, r == nil, ok, c.rt)
		}
		want, _ := Tar(c.rt)
		gb, _ := io.ReadAll(r)
		wb, _ := io.ReadAll(want)
		if len(gb) == 0 || len(gb) != len(wb) {
			t.Fatalf("TarForImageRef(%q): %d bytes, want Tar(%q)'s %d bytes", c.ref, len(gb), c.rt, len(wb))
		}
	}

	nonCurated := []string{
		"funcd/runtime-ruby33:latest", // unknown runtime
		"docker.io/library/alpine:3.20",
		"ghcr.io/acme/custom:latest",
	}
	for _, ref := range nonCurated {
		if _, ok := TarForImageRef(ref); ok {
			t.Fatalf("TarForImageRef(%q): ok=true, want false (non-curated ref → driver Pulls)", ref)
		}
	}
}

// Each call returns an independent reader over the same bytes (concurrent-callers safe).
func TestTarReturnsFreshReader(t *testing.T) {
	r1, _ := Tar("nodejs22")
	b1, _ := io.ReadAll(r1)
	r2, _ := Tar("nodejs22")
	b2, _ := io.ReadAll(r2)
	if len(b1) == 0 || len(b1) != len(b2) {
		t.Fatalf("readers diverged: first=%d bytes, second=%d bytes", len(b1), len(b2))
	}
}

// TestTarReaderGunzipsGzippedEmbed is the regression for the embedded-image Import bug: real
// curated tars are `docker save | gzip` output (gzip-framed), and containerd's client.Import
// rejects a gzip frame ("archive/tar: invalid tar header"). tarReader must hand back the
// DECOMPRESSED tar for a gzipped embed, and pass plain bytes through unchanged.
func TestTarReaderGunzipsGzippedEmbed(t *testing.T) {
	payload := []byte("raw-tar-bytes-\x00\x00-stand-in")

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	gz := buf.Bytes()
	if !isGzip(gz) {
		t.Fatal("setup: gzipped bytes must carry the gzip magic")
	}
	got, err := io.ReadAll(tarReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("tarReader(gzipped) returned the gzip frame, not the decompressed tar (len %d vs %d)", len(got), len(payload))
	}

	plain := []byte("already a raw tar stream")
	if got, _ := io.ReadAll(tarReader(plain)); !bytes.Equal(got, plain) {
		t.Errorf("tarReader(plain) = %q, want unchanged %q", got, plain)
	}
}

// TestIsGzip guards the gzip-magic detection tarReader relies on.
func TestIsGzip(t *testing.T) {
	if !isGzip([]byte{0x1f, 0x8b, 0x08}) {
		t.Error("0x1f 0x8b must be detected as gzip")
	}
	if isGzip([]byte("plain tar bytes")) {
		t.Error("plain bytes must not be detected as gzip")
	}
	if isGzip([]byte{0x1f}) {
		t.Error("a single byte is not gzip")
	}
}
