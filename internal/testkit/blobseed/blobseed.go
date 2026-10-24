// Package blobseed seeds a file:// bucket's directory with many objects for tests. It writes through gocloud's
// fileblob, without the syncs funcd's writer makes, so a test of thousands of keys stays within go test's limit; the
// objects read as written before ADR-0278, checked against their MD5 alone (ADR-0278 Decision 4). Only tests import
// it (depguard).
package blobseed

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gocloud.dev/blob/fileblob"
)

// Objects writes objects, key to bytes, into the file:// bucket rooted at dir.
func Objects(t testing.TB, dir string, objects map[string][]byte) {
	t.Helper()
	b, err := fileblob.OpenBucket(dir, &fileblob.Options{NoTempDir: true})
	require.NoError(t, err)
	defer func() { require.NoError(t, b.Close()) }()
	for key, data := range objects {
		require.NoError(t, b.WriteAll(context.Background(), key, data, nil), "seed %q", key)
	}
}
