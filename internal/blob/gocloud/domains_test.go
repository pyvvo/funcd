package gocloud_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

// ADR-0208 Decision 3: the same normalized endpoint and bucket, or one resolved directory inside the other, is the
// store; the same endpoint alone, or the same device, is the provider.
func TestCompareDomains(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	store := filepath.Join(base, "blob")
	require.NoError(t, os.MkdirAll(store, 0o700))
	require.NoError(t, os.Symlink(store, filepath.Join(base, "link")))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "other"), 0o700))
	ep := "&endpoint=https://S3.Example.com:443/"
	for name, c := range map[string]struct {
		store, target string
		want          gocloud.Overlap
	}{
		"same bucket, endpoint normalized": {"s3://b?region=r" + ep, "s3://b?region=r&endpoint=https://s3.example.com", gocloud.OverlapStore},
		"same bucket, another prefix":      {"s3://b?region=r&prefix=x/" + ep, "s3://b?region=r&prefix=y/" + ep, gocloud.OverlapStore},
		"another bucket":                   {"s3://b?region=r" + ep, "s3://c?region=r" + ep, gocloud.OverlapProvider},
		"another endpoint":                 {"s3://b?region=r" + ep, "s3://b?region=r&endpoint=http://minio:9000", gocloud.OverlapNone},
		"http default port":                {"s3://b?endpoint=HTTP://minio:80", "s3://c?endpoint=http://minio", gocloud.OverlapProvider},
		"AWS, same region":                 {"s3://b?region=eu-west-1", "s3://b?region=EU-WEST-1", gocloud.OverlapStore},
		"AWS, another region":              {"s3://b?region=eu-west-1", "s3://b?region=us-east-1", gocloud.OverlapNone},
		"AWS, no region":                   {"s3://b", "s3://c", gocloud.OverlapProvider},
		"directory inside, missing":        {gocloud.FileURL(store), gocloud.FileURL(filepath.Join(store, "a", "b")), gocloud.OverlapStore},
		"directory above":                  {gocloud.FileURL(store), gocloud.FileURL(base) + "?dir_file_mode=448", gocloud.OverlapStore},
		"through a symlink":                {gocloud.FileURL(store), gocloud.FileURL(filepath.Join(base, "link", "x")), gocloud.OverlapStore},
		"same device":                      {gocloud.FileURL(store), gocloud.FileURL(filepath.Join(base, "other")), gocloud.OverlapProvider},
		"file and s3":                      {gocloud.FileURL(store), "s3://b", gocloud.OverlapNone},
		"memory":                           {"mem://", "mem://", gocloud.OverlapNone},
	} {
		got, err := gocloud.CompareDomains(c.store, c.target)
		require.NoError(t, err, name)
		require.Equal(t, c.want, got, name)
	}
	for _, bad := range []string{"s3://b?%zz", "s3://b?endpoint=http://%zz", "%"} {
		_, err := gocloud.CompareDomains("s3://b", bad)
		require.Equal(t, fault.Invalid, fault.KindOf(err), bad)
	}
}

// ADR-0208 Decision 5: WalkFileKeys visits the keys under a prefix in key order with their data files, decoding
// escapes, skipping .attrs sidecars and .funcd-tmp files, and fails on a directory it cannot read.
func TestWalkFileKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	b, err := gocloud.Open(ctx, gocloud.FileURL(dir))
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	keys := []string{"a/b", "a//c", "a/d\x01e", "a/sub/f", "z"}
	for _, k := range keys {
		require.NoError(t, b.Put(ctx, k, []byte("data of "+k), blob.PutOptions{ContentType: "text/plain"}))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a", "g.1234.funcd-tmp"), []byte("x"), 0o600))

	type visit struct{ key, data string }
	walk := func(prefix string) ([]visit, error) {
		var got []visit
		err := gocloud.WalkFileKeys(ctx, dir, prefix, func(key, path string) error {
			data, err := os.ReadFile(path) //nolint:gosec // the test's own bucket
			got = append(got, visit{key, string(data)})
			return err
		})
		return got, err
	}
	got, err := walk("a/")
	require.NoError(t, err)
	require.Equal(t, []visit{
		{"a//c", "data of a//c"}, {"a/b", "data of a/b"}, {"a/d\x01e", "data of a/d\x01e"}, {"a/sub/f", "data of a/sub/f"},
	}, got)
	all, err := walk("")
	require.NoError(t, err)
	require.Len(t, all, len(keys))
	none, err := walk("missing/")
	require.NoError(t, err)
	require.Empty(t, none)

	if os.Geteuid() == 0 {
		return // root reads a mode-0 directory
	}
	sub := filepath.Join(dir, "a", "sub")
	require.NoError(t, os.Chmod(sub, 0))
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })
	_, err = walk("a/")
	require.Error(t, err, "an unreadable directory fails the walk")
}
