package funcd

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/kvstore"
	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
)

// sweepRecorder records the keys its driver's Sweep deletes.
type sweepRecorder struct {
	kvstore.KV
	mu    sync.Mutex
	swept []string
}

func (r *sweepRecorder) Sweep(ctx context.Context, now time.Time, limit int, skip func(string) bool) ([]string, bool, error) {
	keys, more, err := r.KV.(kvstore.Sweeper).Sweep(ctx, now, limit, skip)
	r.mu.Lock()
	r.swept = append(r.swept, keys...)
	r.mu.Unlock()
	return keys, more, err
}

func (r *sweepRecorder) sweptKey(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Contains(r.swept, key)
}

// scenario: sweep-deletes-what-expired (platform) — the KV provider runs the sweep from its Run, every
// kvstore.sweepInterval, so a key that expired is deleted with no call asking for it (ADR-0320 Decision 5).
// oracle: docs/design/storage/kv-claims.fizz#ExpiredIsSwept
func TestKVProviderSweepsExpiredKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rec := &sweepRecorder{KV: kvmemory.New()}
	_, err := rec.KV.(kvstore.Conditional).PutIf(ctx, "default/s/t/lease", []byte("x"), kvstore.Write{TTL: time.Second})
	require.NoError(t, err)
	p := eventingPlatform(t, WithKVStore(rec), WithKVSweepInterval(100*time.Millisecond))
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { return rec.sweptKey("default/s/t/lease") }, 5*time.Second, 20*time.Millisecond,
		"the platform's sweep deletes the expired key")
}
