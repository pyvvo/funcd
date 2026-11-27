package embedimg

import (
	"io"
	"strings"
	"testing"
)

// ADR-0359 Decisions 3 and 4: WorkerTar reads the two worker images with the dev reference each carries, and no other
// runtime's; the committed placeholders, whose references are empty, are no image to run. It changes the package's
// references, so it runs alone.
func TestWorkerTar(t *testing.T) {
	saved := nodejs22Ref
	t.Cleanup(func() { nodejs22Ref = saved })

	nodejs22Ref = " funcd/runtime-nodejs22:dev-0123456789ab\n"
	r, ref, ok := WorkerTar("nodejs22")
	if !ok || ref != "funcd/runtime-nodejs22:dev-0123456789ab" {
		t.Fatalf("WorkerTar(nodejs22) = %q, %v; want its trimmed reference", ref, ok)
	}
	if b, err := io.ReadAll(r); err != nil || len(b) == 0 {
		t.Fatalf("WorkerTar(nodejs22) read %d bytes, %v", len(b), err)
	}

	nodejs22Ref = ""
	if _, _, ok := WorkerTar("nodejs22"); ok {
		t.Fatal("a placeholder, with no reference, is no image to run")
	}
	for _, rt := range []string{"duckdb", "rqlite", "nodejs20", ""} {
		if _, _, ok := WorkerTar(rt); ok {
			t.Fatalf("WorkerTar(%q): ok, want no worker image", rt)
		}
	}
	if strings.TrimSpace(python314Ref) == "" {
		if _, _, ok := WorkerTar("python314"); ok {
			t.Fatal("python314's committed placeholder is no image to run")
		}
	}
}
