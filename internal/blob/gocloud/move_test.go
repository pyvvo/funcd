package gocloud_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

// ADR-0288 Decision 2: MovePrefix moves every object under src to dst with its suffix, bytes and content type, on a
// file store, an in-memory store and S3; run again it moves nothing more, and bad prefixes are fault.Invalid.
func TestMovePrefix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stores := map[string]func(t *testing.T) blob.Bucket{
		"file": func(t *testing.T) blob.Bucket {
			b, err := gocloud.Open(ctx, gocloud.FileURL(t.TempDir()))
			require.NoError(t, err)
			return b
		},
		"mem": func(t *testing.T) blob.Bucket {
			b, err := gocloud.Open(ctx, "mem://")
			require.NoError(t, err)
			return b
		},
		"s3": func(t *testing.T) blob.Bucket {
			s := s3stub.New(t, nil)
			b, err := gocloud.OpenWith(ctx, s.URL(), gocloud.OpenOptions{CredentialsFile: s3stub.CredentialsFile(t, "AKID")})
			require.NoError(t, err)
			return b
		},
	}
	for name, open := range stores {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := open(t)
			t.Cleanup(func() { _ = b.Close() })
			for k, v := range map[string]string{"s3/ns/b/x": "1", "s3/ns/b/d/y": "2", "s3/ns/bb/z": "3"} {
				require.NoError(t, b.Put(ctx, k, []byte(v), blob.PutOptions{ContentType: "text/plain"}))
			}
			m, ok := b.(blob.PrefixMover)
			require.True(t, ok)
			const dst = "quarantine/bucket/ns/b/20261025T010000.000Z/data/"
			require.NoError(t, m.MovePrefix(ctx, "s3/ns/b/", dst))
			for k, v := range map[string]string{dst + "x": "1", dst + "d/y": "2", "s3/ns/bb/z": "3"} {
				got, err := b.Get(ctx, k)
				require.NoError(t, err, k)
				require.Equal(t, v, string(got), k)
				a, err := b.Attributes(ctx, k)
				require.NoError(t, err, k)
				require.Equal(t, "text/plain", a.ContentType, k)
			}
			left, err := b.List(ctx, "s3/ns/b/")
			require.NoError(t, err)
			require.Empty(t, left)
			require.NoError(t, m.MovePrefix(ctx, "s3/ns/b/", dst), "run again, nothing is left to move")
			for _, bad := range [][2]string{{"s3/ns/b", dst}, {"s3/ns/b/", "s3/ns/b/q/"}, {"", dst}} {
				require.Equal(t, fault.Invalid, fault.KindOf(m.MovePrefix(ctx, bad[0], bad[1])), bad)
			}
		})
	}
}
