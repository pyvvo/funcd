package blobmirror_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/blobmirror"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

func memStore(t *testing.T) blob.Bucket {
	t.Helper()
	b, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// scenario: mirror-restore — generation n restored into an empty store returns every object with its bytes, content
// type and metadata (an unsealed generation's bytes as is, an age header included); a non-empty destination is
// fault.Conflict, a missing object fault.NotFound before the first write, an altered one fault.Invalid with the
// destination emptied.
func TestScenarioMirrorRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t, nil)
	ageLike := "age-encryption.org/v1\nuser bytes, not a sealed file"
	f.put("s3/ns/b/a", "A")
	f.put("s3/ns/b/age", ageLike)
	f.put("s3/ns/b/z", "Z")
	m := f.run()
	src := f.reader()

	dst := memStore(t)
	got, err := blobmirror.Restore(ctx, src, m.Generation, dst, envelope.Opener(nil))
	require.NoError(t, err)
	require.Equal(t, m.Generation, got.Generation)
	require.Equal(t, map[string]string{"s3/ns/b/a": "A", "s3/ns/b/age": ageLike, "s3/ns/b/z": "Z"}, contents(t, dst))
	a, err := dst.Attributes(ctx, "s3/ns/b/a")
	require.NoError(t, err)
	require.Equal(t, "text/plain", a.ContentType)
	require.Equal(t, map[string]string{"of": "s3/ns/b/a"}, a.Metadata)

	_, err = blobmirror.Restore(ctx, src, m.Generation, dst, envelope.Opener(nil))
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a non-empty destination")
	_, err = blobmirror.Restore(ctx, src, 9, memStore(t), envelope.Opener(nil))
	require.Equal(t, fault.NotFound, fault.KindOf(err), "no such generation")

	objects := f.stub.Keys("blob/0000000001/objects/")
	var zPart string
	for _, k := range objects {
		data, _, _, _ := f.stub.Object(k)
		if string(data) == "Z" {
			zPart = k
		}
	}
	require.NotEmpty(t, zPart)
	f.stub.Seed(zPart, []byte("altered"))
	empty := memStore(t)
	_, err = blobmirror.Restore(ctx, src, m.Generation, empty, envelope.Opener(nil))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "an altered object")
	require.Empty(t, contents(t, empty), "the restore removed what it wrote")

	require.NoError(t, src.Delete(ctx, zPart))
	_, err = blobmirror.Restore(ctx, src, m.Generation, empty, envelope.Opener(nil))
	require.Equal(t, fault.NotFound, fault.KindOf(err), "a missing part")
	require.Contains(t, err.Error(), `"s3/ns/b/z"`)
	require.Empty(t, contents(t, empty), "nothing written before the check")
}

// A sealed generation's index and objects open through one call of open with the manifest's recipients.
func TestRestoreOpensOnce(t *testing.T) {
	t.Parallel()
	s, ids := sealer(t)
	f := newFixture(t, func(c *blobmirror.Config) { c.Seal, c.Recipients = s.Seal(), s.Keys().Recipients })
	for _, k := range []string{"a", "b", "c"} {
		f.put(k, strings.Repeat(k, 100))
	}
	m := f.run()
	require.Equal(t, s.Keys().Recipients, m.Recipients)
	for _, k := range f.stub.Keys("blob/") {
		data, _, _, _ := f.stub.Object(k)
		require.NotContains(t, string(data), "aaaa", "%s is sealed", k)
	}
	calls := 0
	open := func(recipients []string) (backup.Unseal, error) {
		calls++
		require.Equal(t, m.Recipients, recipients)
		return envelope.Opener(ids)(recipients)
	}
	require.Equal(t, map[string]string{"a": strings.Repeat("a", 100), "b": strings.Repeat("b", 100), "c": strings.Repeat("c", 100)},
		f.restored(m.Generation, open))
	require.Equal(t, 1, calls)

	_, err := blobmirror.Restore(context.Background(), f.reader(), m.Generation, memStore(t), envelope.Opener(nil))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "no identity opens it")
}
