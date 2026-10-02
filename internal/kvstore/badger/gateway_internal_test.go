package badger

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// oneBatch queues reqs before the gateway starts, so its greedy drain takes them as ONE group-commit batch
// (the co-batching concurrent Puts reach by timing), and returns the driver and each request's result.
func oneBatch(t *testing.T, reqs []*writeReq) (*driver, []error) {
	t.Helper()
	db, err := openDB(t.TempDir(), false)
	require.NoError(t, err)
	d := &driver{db: db, reqs: make(chan *writeReq, len(reqs)), stop: make(chan struct{})}
	t.Cleanup(func() { _ = d.Close() })
	for _, r := range reqs {
		r.done = make(chan error, 1)
		d.reqs <- r
	}
	d.wg.Add(1)
	go d.gateway(newConfig(nil).batchMax)
	errs := make([]error, len(reqs))
	for i, r := range reqs {
		errs[i] = <-r.done
	}
	return d, errs
}

// TestIssue31_OverBudgetBatchCommitsEveryWrite: eight 900 KiB values, each under the 1 MiB store cap,
// overflow one Badger txn together. Every write commits, and a later rewrite of the first key still wins.
func TestIssue31_OverBudgetBatchCommitsEveryWrite(t *testing.T) {
	const n = 8
	reqs := make([]*writeReq, 0, n+1)
	for i := range n {
		reqs = append(reqs, &writeReq{key: fmt.Sprintf("ns/s/k%d", i), val: bytes.Repeat([]byte{byte('a' + i)}, 900<<10)})
	}
	reqs = append(reqs, &writeReq{key: "ns/s/k0", val: []byte("last")})

	d, errs := oneBatch(t, reqs)
	for i, err := range errs {
		require.NoError(t, err, "write %d", i)
	}
	ctx := context.Background()
	for _, r := range reqs[1:] {
		v, found, err := d.Get(ctx, r.key)
		require.NoError(t, err)
		require.True(t, found, r.key)
		require.Equal(t, r.val, v, r.key)
	}
}

// TestIssue31_RejectedWriteFailsAlone: a key over Badger's key-size limit fails its own write, never the
// writes of other callers that share its batch.
func TestIssue31_RejectedWriteFailsAlone(t *testing.T) {
	reqs := []*writeReq{
		{key: "a/s/k", val: []byte("a")},
		{key: "b/s/" + strings.Repeat("x", 70000), val: []byte("b")},
		{key: "c/s/k", val: []byte("c")},
	}

	d, errs := oneBatch(t, reqs)
	require.ErrorContains(t, errs[1], "exceeded")
	require.NoError(t, errs[0])
	require.NoError(t, errs[2])
	ctx := context.Background()
	for _, r := range []*writeReq{reqs[0], reqs[2]} {
		v, found, err := d.Get(ctx, r.key)
		require.NoError(t, err)
		require.True(t, found, r.key)
		require.Equal(t, r.val, v, r.key)
	}
}
