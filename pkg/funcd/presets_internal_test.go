package funcd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/bus/nats"
)

// A WithBus or WithBlob after InMemory closes the preset's bus or bucket, which nothing else would: the platform closes
// only the drivers it ends up with, and the replaced in-memory bus kept its NATS server running and its JetStream dir in
// TMPDIR (internal/testkit/bench's file backend left one per run). A driver the caller passed is never closed.
func TestReplacedPresetDriversAreClosed(t *testing.T) {
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("TMPDIR", dir)
	busDirs := func() int {
		left, err := filepath.Glob(filepath.Join(dir, "funcd-bus-mem-*"))
		require.NoError(t, err)
		return len(left)
	}
	ctx := context.Background()
	open := func() *config {
		c := &config{}
		require.NoError(t, InMemory()(c))
		return c
	}

	c := open()
	require.Equal(t, 1, busDirs())
	mine, err := nats.Open(ctx, nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mine.Close() })
	require.NoError(t, WithBus(mine)(c))
	require.Equal(t, 1, busDirs(), "the preset's bus is closed, the caller's is open")
	other, err := nats.Open(ctx, nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close() })
	require.NoError(t, WithBus(other)(c))
	require.Equal(t, 2, busDirs(), "a bus the caller passed is never closed")
	require.NoError(t, c.bus.Close())
	require.NoError(t, mine.Close())

	c = open()
	preset := c.blob
	bucket, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bucket.Close() })
	require.NoError(t, WithBlob(bucket)(c))
	require.Error(t, preset.Put(ctx, "k", []byte("v"), blob.PutOptions{}), "the preset's bucket is closed")
	require.NoError(t, c.bus.Close())
}
