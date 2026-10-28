package funcd

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// floorOf is a free-space reader whose value a test sets.
func floorOf(free *atomic.Int64) Option {
	return WithFreeSpaceFloor(func() (int64, error) { return free.Load(), nil }, 2<<30)
}

func floorBucket() *v1.Bucket {
	return quotaBucket(v1.BucketSpec{Prefixes: []v1.BucketPrefix{
		{Name: "raw", Owner: v1.FunctionOwner("writer")},
		{Name: "gold", Owner: v1.LakeOwner("analytics")},
	}})
}

// scenario: floor-refuses-workload-writes — below storage.minFreeSize an S3 upload, a ctx.blob put and a Lake's
// catalog save get 507 while a read and a delete still succeed (ADR-0296); scenario: floor-lifts — once space is freed
// the next upload lands without a restart.
func TestScenarioFloorRefusesWorkloadWrites(t *testing.T) {
	ctx := context.Background()
	var free atomic.Int64
	free.Store(3 << 30)
	p, client := quotaGatewayWith(t, []Option{floorOf(&free)}, floorBucket(), quotaFunction("writer"))
	writer, engine := client(v1.KindFunction, "writer"), client(v1.KindLake, "analytics")

	require.NoError(t, put(ctx, writer, "raw/a", 10))
	free.Store(1536 << 20)
	require.Eventually(t, func() bool { return put(ctx, writer, "raw/b", 10) != nil }, 5*time.Second, 50*time.Millisecond,
		"the next reading, at most a second later, refuses")
	requireQuotaExceeded(t, put(ctx, writer, "raw/b", 10))
	_, err := engine.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String(v1.LakeCatalogBucket),
		Key: aws.String("media/gold/_ducklake/catalog.db"), Body: bytes.NewReader(make([]byte, 64))})
	requireQuotaExceeded(t, err)
	view, ok := s3BucketFor(p.cfg.blob, p.cfg.store, p.checker)("default", "media")
	require.True(t, ok)
	require.Equal(t, fault.InsufficientStorage, fault.KindOf(view.Put(ctx, "raw/c", []byte("x"), blob.PutOptions{})),
		"ctx.blob writes through the same view")

	got, err := view.Get(ctx, "raw/a")
	require.NoError(t, err, "a read still works")
	require.Len(t, got, 10)
	_, err = writer.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String("media"), Key: aws.String("raw/a")})
	require.NoError(t, err, "a delete still works")

	free.Store(3 << 30)
	require.Eventually(t, func() bool { return put(ctx, writer, "raw/b", 10) == nil }, 5*time.Second, 50*time.Millisecond,
		"above the floor again, an upload lands with no restart")
}

// scenario: platform-writes-continue — below the floor an operator's apply still succeeds: the control plane writes
// into the margin (ADR-0296 Decision 6).
func TestScenarioPlatformWritesContinue(t *testing.T) {
	var free atomic.Int64
	free.Store(1 << 30)
	p, err := New(InMemory(), floorOf(&free))
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(DevToken))
	require.NoError(t, err)
	ctx := context.Background()

	_, err = c.Apply(ctx, floorBucket())
	require.NoError(t, err, "an apply below the floor")
	view, ok := s3BucketFor(p.cfg.blob, p.cfg.store, p.checker)("default", "media")
	require.True(t, ok)
	require.Equal(t, fault.InsufficientStorage, fault.KindOf(view.Put(ctx, "raw/a", []byte("x"), blob.PutOptions{})),
		"while a workload write is refused")
}

// scenario: floor-off — with storage.minFreeSize "0" no write is refused for the filesystem's free space.
func TestScenarioFloorOff(t *testing.T) {
	ctx := context.Background()
	_, client := quotaGatewayWith(t, []Option{WithFreeSpaceFloor(func() (int64, error) { return 0, nil }, 0)},
		floorBucket(), quotaFunction("writer"))
	require.NoError(t, put(ctx, client(v1.KindFunction, "writer"), "raw/a", 10))
}
