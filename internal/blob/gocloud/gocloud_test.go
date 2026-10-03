package gocloud_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/blobcontract"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

// scenario: driver-conformance-parity — the gocloud driver passes the identical
// blob contract against both the memory (memblob) and file (fileblob) backends.
func TestScenario_DriverConformanceParity(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		blobcontract.RunContract(t, func(t *testing.T) blob.Bucket {
			b, err := gocloud.Open(context.Background(), "mem://")
			if err != nil {
				t.Fatalf("open mem bucket: %v", err)
			}
			t.Cleanup(func() { _ = b.Close() })
			return b
		})
	})
	t.Run("file", func(t *testing.T) {
		blobcontract.RunContract(t, func(t *testing.T) blob.Bucket {
			b, err := gocloud.Open(context.Background(), "file://"+t.TempDir())
			if err != nil {
				t.Fatalf("open file bucket: %v", err)
			}
			t.Cleanup(func() { _ = b.Close() })
			return b
		})
	})
}

// issue 331: FileURL keeps URL syntax in a directory name ('#', '?', '%') in the path, so an object written through
// the bucket lands in exactly that directory: not in the sibling a "%41" escape decodes to, not in the working directory.
func TestIssue331_FileURLBucketWritesIntoExactlyThatDirectory(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"a#b", "q?x", "pct%", "p%41q"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			t.Chdir(base)
			dir, decoy := filepath.Join(base, name), filepath.Join(base, "pAq")
			require.NoError(t, os.Mkdir(dir, 0o700))
			require.NoError(t, os.Mkdir(decoy, 0o700))
			b, err := gocloud.Open(ctx, gocloud.FileURL(dir))
			require.NoError(t, err)
			t.Cleanup(func() { _ = b.Close() })
			require.NoError(t, b.Put(ctx, "k", []byte("v")))
			got, err := os.ReadFile(filepath.Join(dir, "k"))
			require.NoError(t, err)
			require.Equal(t, "v", string(got))
			inDecoy, err := os.ReadDir(decoy)
			require.NoError(t, err)
			require.Empty(t, inDecoy)
			inBase, err := os.ReadDir(base)
			require.NoError(t, err)
			names := make([]string, 0, len(inBase))
			for _, e := range inBase {
				names = append(names, e.Name())
			}
			require.ElementsMatch(t, []string{name, "pAq"}, names)
		})
	}
}

// TestIssue160_UnstorableKeysAreInvalidAndNeverAlias: keys are opaque on every backend
// (ADR-0007 §1). The file backend maps a key to an OS path, so a key it cannot hold must
// fail fault.Invalid (the S3 gateway's 400), not Internal (a retried 500), and a key must
// never read or write another key's object.
func TestIssue160_UnstorableKeysAreInvalidAndNeverAlias(t *testing.T) {
	ctx := context.Background()
	backends := map[string]string{"memory": "mem://", "file": "file://"}
	for name, scheme := range backends {
		open := func(t *testing.T) blob.Bucket {
			t.Helper()
			url := scheme
			if name == "file" {
				url += t.TempDir()
			}
			b, err := gocloud.Open(ctx, url)
			require.NoError(t, err)
			t.Cleanup(func() { _ = b.Close() })
			return b
		}
		storedOrInvalid := func(t *testing.T, b blob.Bucket, key string) {
			t.Helper()
			data := []byte("v:" + key)
			if err := b.Put(ctx, key, data); err != nil {
				require.Equal(t, fault.Invalid, fault.KindOf(err), "Put: %v", err)
				return
			}
			got, err := b.Get(ctx, key)
			require.NoError(t, err)
			require.Equal(t, data, got)
		}
		t.Run(name, func(t *testing.T) {
			for label, key := range map[string]string{
				"attrs suffix":       "bronze/x.attrs",
				"300-byte segment":   "bronze/" + strings.Repeat("a", 300),
				"1024-byte key":      "bronze/" + strings.Repeat("a", 1017),
				"1999-byte deep key": "bronze/" + strings.Repeat("abcdefghi/", 199) + "x",
			} {
				t.Run(label, func(t *testing.T) { storedOrInvalid(t, open(t), key) })
			}
			t.Run("object over an existing prefix", func(t *testing.T) {
				b := open(t)
				require.NoError(t, b.Put(ctx, "bronze/x", []byte("x")))
				storedOrInvalid(t, b, "bronze")
			})
			t.Run("prefix under an existing object", func(t *testing.T) {
				b := open(t)
				require.NoError(t, b.Put(ctx, "bronze", []byte("x")))
				storedOrInvalid(t, b, "bronze/x")
			})
			for key, other := range map[string]string{
				"bronze/./dot": "bronze/dot",
				"./dot":        "dot",
				"bronze/x/..":  "bronze",
				"/dot":         "dot",
			} {
				t.Run("alias "+key, func(t *testing.T) {
					b := open(t)
					storedOrInvalid(t, b, key)
					_, err := b.Get(ctx, other)
					require.Equal(t, fault.NotFound, fault.KindOf(err), "Get(%q) read the object of %q", other, key)
				})
			}
		})
	}
}

// TestIssue375_EscapeSequenceKeysNeverAlias: fileblob hex-escapes some runes of a key as
// "__0x<hex>__" and decodes every such sequence when it lists, so a raw key holding one must
// fail fault.Invalid rather than read another key's object or list under another name.
func TestIssue375_EscapeSequenceKeysNeverAlias(t *testing.T) {
	ctx := context.Background()
	for name, scheme := range map[string]string{"memory": "mem://", "file": "file://"} {
		t.Run(name, func(t *testing.T) {
			url := scheme
			if name == "file" {
				url += t.TempDir()
			}
			b, err := gocloud.Open(ctx, url)
			require.NoError(t, err)
			t.Cleanup(func() { _ = b.Close() })

			require.NoError(t, b.Put(ctx, "a//b", []byte("v:a//b")))
			got, err := b.Get(ctx, "a/__0x2f__b")
			require.Error(t, err, "Get(a/__0x2f__b) read the object of a//b: %q", got)
			require.Contains(t, []fault.Kind{fault.NotFound, fault.Invalid}, fault.KindOf(err), "Get: %v", err)

			key := "c/__0x41__"
			if err := b.Put(ctx, key, []byte("v:"+key)); err != nil {
				require.Equal(t, fault.Invalid, fault.KindOf(err), "Put: %v", err)
				return
			}
			items, err := b.List(ctx, "c/")
			require.NoError(t, err)
			require.Len(t, items, 1)
			require.Equal(t, key, items[0].Key)
			data, err := b.Get(ctx, items[0].Key)
			require.NoError(t, err)
			require.Equal(t, []byte("v:"+key), data)
		})
	}
}

// TestIssue459_ListFindsKeysUnderEscapedPrefixes: fileblob stores a key under its escaped path
// ("a//b/c" in "a/__0x2f__b/c") but starts its List walk at the raw, cleaned directory part of the
// prefix, so a prefix holding "//", "../", a control rune or a trailing "/" — mid-path or at the
// end — must still list every key it prefixes and no sibling ("pq" under "p/"), as on the memory
// backend (ADR-0007 §1).
func TestIssue459_ListFindsKeysUnderEscapedPrefixes(t *testing.T) {
	ctx := context.Background()
	keys := []string{"a//b/c", "x/../y/z", "../w", "d../e", "f/", "f/g", "h\x01/i", "p/q", "pq"}
	prefixes := []string{
		"a//b/", "a//b/c", "a//", "a/", "x/../y/", "x/../y/z", "x/../", "x/", "../", "d../",
		"f/", "h\x01/", "h\x01/i", "p/", "",
	}
	for name, scheme := range map[string]string{"memory": "mem://", "file": "file://"} {
		t.Run(name, func(t *testing.T) {
			url := scheme
			if name == "file" {
				url += t.TempDir()
			}
			b, err := gocloud.Open(ctx, url)
			require.NoError(t, err)
			t.Cleanup(func() { _ = b.Close() })
			for _, k := range keys {
				require.NoError(t, b.Put(ctx, k, []byte(k)), "Put(%q)", k)
			}
			for _, p := range prefixes {
				want := []string{}
				for _, k := range keys {
					if strings.HasPrefix(k, p) {
						want = append(want, k)
					}
				}
				slices.Sort(want)
				items, err := b.List(ctx, p)
				if !assert.NoError(t, err, "List(%q)", p) {
					continue
				}
				got := []string{}
				for _, it := range items {
					got = append(got, it.Key)
				}
				assert.Equal(t, want, got, "List(%q)", p)
			}
		})
	}
}
