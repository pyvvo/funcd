package gocloud_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/blobcontract"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

// TestCreateIfAbsentIsAtomic: one of 16 concurrent creates of one key wins on mem:// and file:// (ADR-0203).
func TestCreateIfAbsentIsAtomic(t *testing.T) {
	ctx := context.Background()
	for name, url := range map[string]func(t *testing.T) string{
		"memory": func(*testing.T) string { return "mem://" },
		"file":   func(t *testing.T) string { return gocloud.FileURL(t.TempDir()) },
	} {
		t.Run(name, func(t *testing.T) {
			b, err := gocloud.Open(ctx, url(t))
			require.NoError(t, err)
			t.Cleanup(func() { _ = b.Close() })
			blobcontract.CreateIfAbsentIsAtomic(t, b)
		})
	}
}

// TestFileTempHidden: a crash's create-if-absent temp file is never listed and its name is reserved; a create
// refuses a key fileblob would store under another path or outside the root, and attributes it cannot keep.
func TestFileTempHidden(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	b, err := gocloud.Open(ctx, gocloud.FileURL(dir))
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	const crashed = "g/k.0123456789abcdef.funcd-tmp"
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "g"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, filepath.FromSlash(crashed)), []byte("partial"), 0o600))
	require.NoError(t, b.Put(ctx, "g/other", []byte("x"), blob.PutOptions{}))
	require.NoError(t, b.Put(ctx, "g/k", []byte("v"), blob.PutOptions{IfNotExist: true}))

	for _, prefix := range []string{"", "g/", "g/k"} {
		items, err := b.List(ctx, prefix)
		require.NoError(t, err)
		ranged, _, err := b.ListAfter(ctx, prefix, "", 10)
		require.NoError(t, err)
		require.Equal(t, keys(items), keys(ranged))
		for _, it := range items {
			require.NotEqual(t, crashed, it.Key, "List(%q) shows a temp file", prefix)
		}
	}
	got, err := b.Get(ctx, "g/k")
	require.NoError(t, err)
	require.Equal(t, "v", string(got))
	_, err = b.Exists(ctx, crashed)
	require.Equal(t, fault.Invalid, fault.KindOf(err))

	for _, key := range []string{"x.funcd-tmp", "a//b", "a/./b", "../x", "a/../../x", "a/", "/a", "..", "a\x01b"} {
		err := b.Put(ctx, key, []byte("v"), blob.PutOptions{IfNotExist: true})
		require.Equalf(t, fault.Invalid, fault.KindOf(err), "create of %q: %v", key, err)
	}
	for _, opts := range []blob.PutOptions{
		{IfNotExist: true, ContentType: "text/plain"},
		{IfNotExist: true, Metadata: map[string]string{"a": "b"}},
	} {
		require.Equal(t, fault.Invalid, fault.KindOf(b.Put(ctx, "attrs", []byte("v"), opts)))
	}
	entries, err := os.ReadDir(filepath.Join(dir, "g"))
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	require.ElementsMatch(t, []string{"k", "k.0123456789abcdef.funcd-tmp", "other", "other.attrs"}, names,
		"a create leaves no temp file of its own")
}

// TestS3ConditionalConflictRetried: on s3:// a create answered 409 ConditionalRequestConflict is retried up to 3
// times, then fault.Unavailable; 412 is fault.Conflict only under IfNotExist.
func TestS3ConditionalConflictRetried(t *testing.T) {
	ctx := context.Background()
	s3TestEnv(t)
	cases := []struct {
		name       string
		status     int
		code       string
		failures   int32
		ifNotExist bool
		want       fault.Kind
		puts       int32
	}{
		{"three conflicts then created", http.StatusConflict, "ConditionalRequestConflict", 3, true, "", 4},
		{"conflicts stop at three retries", http.StatusConflict, "ConditionalRequestConflict", 100, true, fault.Unavailable, 4},
		{"412 under IfNotExist", http.StatusPreconditionFailed, "PreconditionFailed", 100, true, fault.Conflict, 1},
		{"412 without IfNotExist", http.StatusPreconditionFailed, "PreconditionFailed", 100, false, fault.Internal, 1},
		{"409 without IfNotExist", http.StatusConflict, "ConditionalRequestConflict", 100, false, fault.Internal, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var puts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.Method != http.MethodPut {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if puts.Add(1) <= c.failures {
					w.WriteHeader(c.status)
					_, _ = io.WriteString(w, "<Error><Code>"+c.code+"</Code><Message>refused</Message></Error>")
					return
				}
				w.Header().Set("ETag", `"e"`)
			}))
			t.Cleanup(srv.Close)
			b, err := gocloud.Open(ctx, "s3://bkt?region=us-east-1&use_path_style=true&endpoint="+srv.URL)
			require.NoError(t, err)
			t.Cleanup(func() { _ = b.Close() })
			err = b.Put(ctx, "k", []byte("v"), blob.PutOptions{IfNotExist: c.ifNotExist})
			if c.want == "" {
				require.NoError(t, err)
			} else {
				require.Equalf(t, c.want, fault.KindOf(err), "%v", err)
			}
			require.Equal(t, c.puts, puts.Load())
		})
	}
}

// s3TestEnv gives the AWS SDK static keys and keeps the host's AWS files and instance metadata out of the test.
func s3TestEnv(t *testing.T) {
	none := filepath.Join(t.TempDir(), "none")
	for k, v := range map[string]string{
		"AWS_ACCESS_KEY_ID":           "AKIDTEST",
		"AWS_SECRET_ACCESS_KEY":       "secret",
		"AWS_SESSION_TOKEN":           "",
		"AWS_PROFILE":                 "",
		"AWS_CONFIG_FILE":             none,
		"AWS_SHARED_CREDENTIALS_FILE": none,
		"AWS_EC2_METADATA_DISABLED":   "true",
	} {
		t.Setenv(k, v)
	}
}

func keys(items []blob.Attributes) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Key)
	}
	return out
}
