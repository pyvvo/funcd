package e2e_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"maps"
	"os/exec"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Issue 565: the identity rule (CLAUDE.md) covers binary files too, but check-hygiene and the reviews
// read text only, so engine.tar.gz got committed with the packer's owner names and AppleDouble entries
// (repacked with --numeric-owner in #295).
func TestIssue565_TrackedArchivesCarryNoOwnerNames(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var leaks []string
	for _, f := range trackedFiles(t, root, "*.tar", "*.tar.gz", "*.tgz") {
		// The staged blob, not the working tree: a local build overwrites these with real archives
		// (build-runtime-images, build-catalog-engine) under skip-worktree, and those are never committed.
		cmd := exec.Command("git", "cat-file", "blob", ":"+f)
		cmd.Dir = root
		data, err := cmd.Output()
		require.NoError(t, err, "git cat-file %s", f)
		if bytes.HasPrefix(data, []byte("PLACEHOLDER")) {
			continue
		}
		var r io.Reader = bytes.NewReader(data)
		if !strings.HasSuffix(f, ".tar") {
			zr, err := gzip.NewReader(r)
			require.NoError(t, err, "gunzip %s", f)
			r = zr
		}
		hits, err := tarLeaks(r)
		require.NoError(t, err, "read %s as a tar archive", f)
		for _, h := range hits {
			leaks = append(leaks, f+": "+h)
		}
	}
	require.Empty(t, leaks, "owner names or macOS metadata in a committed archive; "+
		"repack it with --numeric-owner and no xattrs or macOS metadata, as scripts/fetch-catalog-engine.sh does")
}

func TestIssue565_TarLeaksReportsEachLeak(t *testing.T) {
	t.Parallel()
	leaks, err := tarLeaks(tarOf(t,
		&tar.Header{Name: "duckdb", Mode: 0o755, Uname: "packer", Gname: "staff"},
		&tar.Header{Name: "._x", Mode: 0o644},
		&tar.Header{Name: "VERSION", Mode: 0o644, PAXRecords: map[string]string{
			"SCHILY.xattr.com.apple.provenance":     "1",
			"LIBARCHIVE.xattr.com.apple.quarantine": "1",
		}},
	))
	require.NoError(t, err)
	require.Equal(t, []string{
		"duckdb: user name",
		"duckdb: group name",
		"._x: AppleDouble entry",
		"VERSION: PAX record LIBARCHIVE.xattr.com.apple.quarantine",
		"VERSION: PAX record SCHILY.xattr.com.apple.provenance",
	}, leaks)

	leaks, err = tarLeaks(tarOf(t,
		&tar.Header{Name: "duckdb", Mode: 0o755},
		&tar.Header{Name: "VERSION", Mode: 0o644, PAXRecords: map[string]string{"comment": "numeric owners"}},
	))
	require.NoError(t, err)
	require.Empty(t, leaks)

	_, err = tarLeaks(bytes.NewReader(bytes.Repeat([]byte("x"), 1024)))
	require.Error(t, err)
}

// tarLeaks lists, entry by entry, each owner name, AppleDouble (._*) file and extended-attribute PAX
// record in the tar stream r. It names the field, never the value: the value is what must not leak.
func tarLeaks(r io.Reader) ([]string, error) {
	var leaks []string
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return leaks, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Uname != "" {
			leaks = append(leaks, h.Name+": user name")
		}
		if h.Gname != "" {
			leaks = append(leaks, h.Name+": group name")
		}
		if strings.HasPrefix(path.Base(h.Name), "._") {
			leaks = append(leaks, h.Name+": AppleDouble entry")
		}
		for _, k := range slices.Sorted(maps.Keys(h.PAXRecords)) {
			if strings.HasPrefix(k, "LIBARCHIVE.xattr.") || strings.HasPrefix(k, "SCHILY.xattr.") {
				leaks = append(leaks, h.Name+": PAX record "+k)
			}
		}
	}
}

func tarOf(t *testing.T, hdrs ...*tar.Header) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range hdrs {
		require.NoError(t, tw.WriteHeader(h))
	}
	require.NoError(t, tw.Close())
	return &buf
}
