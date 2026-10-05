package funcd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// Issue #58 (ADR-0080 bucket-deletion-protection): the production wiring probes the substrate the
// s3gateway writes (s3/<ns>/<bucket>/<prefix>/…), so a Bucket whose prefix still holds objects can be
// neither deleted nor stripped of that prefix; once drained, both are allowed.
func TestIssue58_BucketWithObjectsIsDeletionProtected(t *testing.T) {
	p, err := New(InMemory())
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-doneCh })

	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(DevToken))
	require.NoError(t, err)
	ctx := context.Background()

	lake := func(prefixes ...string) *v1.Bucket {
		b := &v1.Bucket{
			TypeMeta:   v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket},
			ObjectMeta: v1.ObjectMeta{Name: "lake", Namespace: "default", ResourceGroup: "rg1"},
		}
		for _, name := range prefixes {
			b.Spec.Prefixes = append(b.Spec.Prefixes, v1.BucketPrefix{Name: name, Owner: "writer"})
		}
		return b
	}
	_, err = c.Apply(ctx, lake("raw", "web"))
	require.NoError(t, err)

	const key = "s3/default/lake/web/index.html"
	require.NoError(t, p.cfg.blob.Put(ctx, key, []byte("<title>v1</title>"), blob.PutOptions{}))

	err = c.Delete(ctx, v1.KindBucket, "default", "lake")
	require.Equal(t, fault.Conflict, fault.KindOf(err), "deleting a Bucket whose prefix still holds objects ⇒ Conflict, got %v", err)
	_, err = c.Apply(ctx, lake("raw"))
	require.Equal(t, fault.Conflict, fault.KindOf(err), "dropping a prefix that still holds objects ⇒ Conflict, got %v", err)

	require.NoError(t, p.cfg.blob.Delete(ctx, key))
	_, err = c.Apply(ctx, lake("raw"))
	require.NoError(t, err, "a drained prefix can be dropped")
	require.NoError(t, c.Delete(ctx, v1.KindBucket, "default", "lake"), "a drained Bucket is deletable")
}
