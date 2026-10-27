package blob_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/quota"
)

// asked records the subjects a write asks the quota about.
type asked struct{ subjects []quota.Subject }

func (a *asked) Admit(_ context.Context, r quota.Request) (quota.Settle, error) {
	a.subjects = append(a.subjects, r.Subject)
	return func(bool) {}, nil
}

// A Bucket asks the quota only about a subject a size caps (ADR-0292 Decision 5): a key's leading segment that no
// capped prefix names is never tracked, and an uncapped Bucket never is.
func TestQuotedAsksOnlyCappedSubjects(t *testing.T) {
	ctx := context.Background()
	inner, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })
	bucket := quota.Subject{Kind: v1.KindBucket, Namespace: "default", Name: "media"}
	gold := bucket
	gold.Prefix = "gold"
	for _, c := range []struct {
		name   string
		capped map[string]bool
		key    string
		want   []quota.Subject
	}{
		{"an uncapped Bucket's root key", nil, "index.html", nil},
		{"an uncapped Bucket's prefix", nil, "raw/a", nil},
		{"a capped prefix", map[string]bool{"gold": true}, "gold/a", []quota.Subject{gold}},
		{"a root key beside a capped prefix", map[string]bool{"gold": true}, "index.html", nil},
		{"a capped prefix of a capped Bucket", map[string]bool{"": true, "gold": true}, "gold/a", []quota.Subject{gold, bucket}},
		{"a capped Bucket's root key", map[string]bool{"": true}, "index.html", []quota.Subject{bucket}},
	} {
		chk := &asked{}
		view := blob.Quoted(inner, chk, bucket, func(p string) bool { return c.capped[p] })
		require.NoError(t, view.Put(ctx, c.key, []byte("x"), blob.PutOptions{}), c.name)
		require.NoError(t, view.Delete(ctx, c.key), c.name)
		if len(c.want) == 0 {
			require.Empty(t, chk.subjects, c.name)
			continue
		}
		require.Equal(t, append(append([]quota.Subject{}, c.want...), c.want...), chk.subjects, "%s: the put, then the delete", c.name)
	}
}
