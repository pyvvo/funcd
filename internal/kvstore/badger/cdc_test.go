package badger

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/bus"
)

// fakeBus is an in-memory bus.Bus recording published change records, able to simulate a consumer outage
// (failAfter): the first failAfter publishes succeed, the rest error — enough to prove zero-loss resume.
type fakeBus struct {
	mu        sync.Mutex
	pubs      []changeRecord
	failAfter int // 0 ⇒ never fail
}

func (b *fakeBus) Publish(_ context.Context, _ bus.Subject, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failAfter > 0 && len(b.pubs) >= b.failAfter {
		return fault.Unavailablef("fakeBus", "consumer down")
	}
	var rec changeRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return err
	}
	b.pubs = append(b.pubs, rec)
	return nil
}

func (b *fakeBus) Subscribe(context.Context, bus.Subject) (bus.Subscription, error) {
	return nil, fault.Invalidf("fakeBus.Subscribe", "unsupported")
}
func (b *fakeBus) EnsureStream(context.Context, bus.StreamConfig) error { return nil }
func (b *fakeBus) Consume(context.Context, bus.ConsumeConfig) (bus.Consumer, error) {
	return nil, fault.Invalidf("fakeBus.Consume", "unsupported")
}
func (b *fakeBus) Close() error { return nil }

func (b *fakeBus) seqs() []uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]uint64, len(b.pubs))
	for i, r := range b.pubs {
		out[i] = r.Seq
	}
	return out
}

// countPrefix counts keys under prefix in the raw db.
func countPrefix(t *testing.T, db *badger.DB, prefix string) int {
	t.Helper()
	n := 0
	require.NoError(t, db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		p := []byte(prefix)
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			n++
		}
		return nil
	}))
	return n
}

func putN(t *testing.T, kv interface {
	Put(context.Context, string, []byte) error
}, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		require.NoError(t, kv.Put(context.Background(), fmt.Sprintf("k/%06d", i), []byte("v")))
	}
}

// scenario: cdc-default-off — no CDC config ⇒ no _cdc/ entries written, nothing published.
func TestScenarioCDCDefaultOff(t *testing.T) {
	kv, seams, err := OpenWithSeams(t.TempDir(), nil, nil)
	require.NoError(t, err)
	defer func() { _ = kv.(*driver).Close() }()
	require.Nil(t, seams.CDC, "no CDC seam when unconfigured")

	putN(t, kv, 5)
	require.Equal(t, 0, countPrefix(t, kv.(*driver).db, cdcLogPrefix), "no outbox writes when CDC is off")
}

// scenario: cdc-enabled-requires-sink — enabled with a nil sink or empty subject ⇒ fault.Invalid.
func TestScenarioCDCEnabledRequiresSink(t *testing.T) {
	db := openRawDB(t, t.TempDir())
	_, err := NewCDC(db, nil, CDCConfig{Subject: "kv.changes"})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "nil sink ⇒ invalid")
	_, err = NewCDC(db, &fakeBus{}, CDCConfig{Subject: ""})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "empty subject ⇒ invalid")
}

// scenario: cdc-log-entry-atomic-with-write — each Put writes the data key AND its _cdc/<seq> entry in the
// same txn (both present), so the outbox count matches the write count.
func TestScenarioCDCLogEntryAtomicWithWrite(t *testing.T) {
	fb := &fakeBus{}
	kv, seams, err := OpenWithSeamsFor(t.TempDir(), nil, BackupConfig{}, fb, CDCConfig{Subject: "kv.changes"})
	require.NoError(t, err)
	defer func() { _ = kv.(*driver).Close() }()
	require.NotNil(t, seams.CDC)

	putN(t, kv, 4)
	d := kv.(*driver)
	require.Equal(t, 4, countPrefix(t, d.db, cdcLogPrefix), "one outbox entry per write, atomic with the data")
	require.Equal(t, 4, countPrefix(t, d.db, "k/"), "the data keys are present")
}

// scenario: cdc-survives-consumer-restart — a consumer killed mid-stream resumes from the durable cursor;
// every change is delivered, zero loss, zero dup.
func TestScenarioCDCSurvivesConsumerRestart(t *testing.T) {
	fb := &fakeBus{failAfter: 6} // the 7th publish fails — the "consumer goes down" mid-stream
	kv, seams, err := OpenWithSeamsFor(t.TempDir(), nil, BackupConfig{}, fb, CDCConfig{Subject: "kv.changes"})
	require.NoError(t, err)
	defer func() { _ = kv.(*driver).Close() }()
	cc := seams.CDC.(*cdc)
	ctx := context.Background()

	const total = 15
	putN(t, kv, total)

	_, err = cc.drain(ctx)
	require.Error(t, err, "the consumer went down mid-stream")
	require.Len(t, fb.seqs(), 6, "delivered up to the failure")

	fb.failAfter = 0 // the consumer comes back
	_, err = cc.drain(ctx)
	require.NoError(t, err)

	got := fb.seqs()
	require.Len(t, got, total, "every change delivered after resume — zero loss")
	seen := map[uint64]bool{}
	for _, s := range got {
		require.False(t, seen[s], "seq %d delivered twice — a dup", s)
		seen[s] = true
	}
	require.Len(t, seen, total, "all distinct — zero dup")
}

// scenario: cdc-retention-bounds-log — entries the consumer has passed (seq <= cursor) are reclaimed.
func TestScenarioCDCRetentionBoundsLog(t *testing.T) {
	fb := &fakeBus{}
	kv, seams, err := OpenWithSeamsFor(t.TempDir(), nil, BackupConfig{}, fb, CDCConfig{Subject: "kv.changes"})
	require.NoError(t, err)
	defer func() { _ = kv.(*driver).Close() }()
	cc := seams.CDC.(*cdc)
	ctx := context.Background()

	putN(t, kv, 10)
	d := kv.(*driver)
	require.Equal(t, 10, countPrefix(t, d.db, cdcLogPrefix), "outbox holds all changes before retention")

	_, err = cc.drain(ctx) // deliver everything → cursor = last seq
	require.NoError(t, err)
	require.NoError(t, cc.gc(ctx))
	require.Equal(t, 0, countPrefix(t, d.db, cdcLogPrefix), "delivered entries reclaimed — the log does not grow unbounded")
}
