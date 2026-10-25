package volume

import (
	"archive/tar"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
)

func qnow() time.Time { return time.Date(2026, 10, 25, 3, 4, 5, 678_000_000, time.UTC) }

// reserveWithFile reserves id and writes a file into its leaf.
func reserveWithFile(t *testing.T, m *manager, id ID, body string) {
	t.Helper()
	dir, err := m.Reserve(t.Context(), id, "u1", 1<<20, v1.DeletionDelete)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orders.db"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestQuarantineMovesLeafThenMeta (ADR-0288 Decision 2): a local volume's leaf and meta move into
// .quarantine/volume/<ns>/<function>/<volume>/<since>/ as data and meta, its booking is freed, the scan no longer
// sees it, and the entry is listed kept.
func TestQuarantineMovesLeafThenMeta(t *testing.T) {
	ctx := t.Context()
	m := newManager(t, Options{CapacityBytes: 1 << 20})
	id := local("cart", "state")
	reserveWithFile(t, m, id, "rows")

	since, err := m.Quarantine().Move(ctx, id, qnow())
	if err != nil {
		t.Fatal(err)
	}
	if !since.Equal(qnow()) {
		t.Fatalf("since = %s, want %s", since, qnow())
	}
	entry := filepath.Join(m.o.Root, ".quarantine", "volume", "shop", "cart", "state", "20261025T030405.678Z")
	if b, err := os.ReadFile(filepath.Join(entry, "data", "orders.db")); err != nil || string(b) != "rows" {
		t.Fatalf("the entry's data = %q, %v; want the leaf's file", b, err)
	}
	if _, err := os.Stat(filepath.Join(entry, "meta")); err != nil {
		t.Fatalf("the meta did not follow the leaf: %v", err)
	}
	if _, err := os.Lstat(m.leaf(id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the leaf is still in place: %v", err)
	}
	if infos, err := m.List(ctx); err != nil || len(infos) != 0 {
		t.Fatalf("List = %v, %v; want no volume: the scan skips the quarantine", infos, err)
	}
	if err := m.Book(ctx, local("thumbs", "state"), 1<<20); err != nil {
		t.Fatalf("the quarantined volume still holds its booking: %v", err)
	}
	got, err := m.Quarantine().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Quarantined{{ID: id, Since: qnow(), State: quarantine.Kept, Data: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List = %+v, want %+v", got, want)
	}
	if _, err := m.Quarantine().Move(ctx, id, qnow().Add(time.Second)); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("a second Move = %v, want NotFound: nothing is left", err)
	}
	if _, err := m.Quarantine().Move(ctx, ID{Namespace: "shop", Function: "cart", Name: "cache", Worker: "w1"}, qnow()); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("an emptyDir's Move = %v, want Invalid", err)
	}
}

// TestQuarantineMoveCutBetweenRenames (ADR-0288 Decision 2): after a crash that moved the leaf but not the meta, the
// entry reads moving and the scan still sees the meta; the next Move puts the meta into that entry, which then reads
// kept. A meta with neither a leaf nor an entry holds no data: Move removes it and answers NotFound.
func TestQuarantineMoveCutBetweenRenames(t *testing.T) {
	ctx := t.Context()
	m := newManager(t, Options{})
	id := local("cart", "state")
	reserveWithFile(t, m, id, "rows")
	q := m.Quarantine().(volumeQuarantine)
	entry := q.entryDir(id, qnow())
	if err := os.MkdirAll(entry, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(m.leaf(id), filepath.Join(entry, "data")); err != nil {
		t.Fatal(err)
	}
	if got, err := q.List(ctx); err != nil || len(got) != 1 || got[0].State != quarantine.Moving {
		t.Fatalf("List = %+v, %v; want one moving entry", got, err)
	}
	if infos, err := m.List(ctx); err != nil || len(infos) != 1 {
		t.Fatalf("List = %v, %v; want the meta the cut move left", infos, err)
	}

	since, err := q.Move(ctx, id, qnow().Add(time.Hour))
	if err != nil || !since.Equal(qnow()) {
		t.Fatalf("Move = %s, %v; want the cut entry's since %s", since, err, qnow())
	}
	if got, err := q.List(ctx); err != nil || len(got) != 1 || got[0].State != quarantine.Kept {
		t.Fatalf("List = %+v, %v; want the entry kept once both are there", got, err)
	}

	stray := local("thumbs", "state")
	if err := os.MkdirAll(m.fnDir(stray), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.writeMeta(stray, meta{Owner: "u2", SizeBytes: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Move(ctx, stray, qnow()); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("a meta alone = %v, want NotFound", err)
	}
	if _, err := os.Lstat(m.metaPath(stray)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the stray meta is still there: %v", err)
	}
}

// TestQuarantineMarkTarRemove (ADR-0288 Decision 6): Mark writes the entry's state, Tar streams its data, and Remove
// deletes the entry and the parents it left empty; an entry's state wins over its layout.
func TestQuarantineMarkTarRemove(t *testing.T) {
	ctx := t.Context()
	m := newManager(t, Options{})
	id := local("cart", "state")
	reserveWithFile(t, m, id, "rows")
	q := m.Quarantine()
	since, err := q.Move(ctx, id, qnow())
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Mark(ctx, id, since, quarantine.Returning); err != nil {
		t.Fatal(err)
	}
	if got, _ := q.List(ctx); len(got) != 1 || got[0].State != quarantine.Returning {
		t.Fatalf("List = %+v, want the entry returning", got)
	}
	if err := q.Mark(ctx, id, since.Add(time.Second), quarantine.Returning); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("Mark of no entry = %v, want NotFound", err)
	}

	rc, err := q.Tar(ctx, id, since)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(rc)
	var names []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	_ = rc.Close()
	if !reflect.DeepEqual(names, []string{"orders.db"}) {
		t.Fatalf("Tar = %v, want the leaf's one file", names)
	}

	if err := q.Remove(ctx, id, since); err != nil {
		t.Fatal(err)
	}
	if got, _ := q.List(ctx); len(got) != 0 {
		t.Fatalf("List = %+v after Remove, want none", got)
	}
	if _, err := os.Lstat(filepath.Join(m.o.Root, ".quarantine", "volume", "shop")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Remove left the empty parents: %v", err)
	}
}
