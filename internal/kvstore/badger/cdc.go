package badger

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/bus"
)

// cdc implements the ADR-0066 CDC seam (ADR-0068) as a transactional outbox: every write appends a
// _cdc/<seq> log entry IN the data's own txn (so the log can never diverge from the data), and a
// durable-cursor tailer drains the log to a bus sink, resuming from a persisted cursor across a restart —
// no missed change. Badger's lossy Subscribe is never the feed.
type cdc struct {
	db        *badger.DB
	seq       *badger.Sequence
	sink      bus.Bus
	subject   bus.Subject
	retention time.Duration
}

const (
	cdcSeqKey       = Reserved + "cdc_seq"            // GetSequence lease counter (reserved, never listed)
	cdcLogPrefix    = Reserved + "cdc/"               // _cdc/<020d seq> outbox entries, seq-ordered
	cdcCursorKey    = Reserved + "cdc_cursor/default" // the single-consumer durable cursor
	cdcSeqBandwidth = 100                             // lease 100 seqs per persisted write (gaps on crash are fine)
	cdcPollInterval = 200 * time.Millisecond          // tail wake cadence when the log is drained
)

// CDCConfig configures the opt-in change-feed (ADR-0068). Subject is the bus subject to publish to (the
// configured sink); zero Retention ⇒ 24h.
type CDCConfig struct {
	Subject   bus.Subject
	Retention time.Duration
}

// NewCDC builds the change-feed over an opened KV Badger instance. A nil sink or empty subject means CDC
// was enabled without a sink — fault.Invalid (never a silent half-configured feed).
func NewCDC(db *badger.DB, sink bus.Bus, cfg CDCConfig) (CDC, error) {
	if sink == nil || cfg.Subject == "" {
		return nil, fault.Invalidf("kvbadger.NewCDC", "cdc enabled but sink is empty")
	}
	seq, err := db.GetSequence([]byte(cdcSeqKey), cdcSeqBandwidth)
	if err != nil {
		return nil, fault.Internalf("kvbadger.NewCDC", "lease cdc sequence: %v", err)
	}
	ret := cfg.Retention
	if ret <= 0 {
		ret = 24 * time.Hour
	}
	return &cdc{db: db, seq: seq, sink: sink, subject: cfg.Subject, retention: ret}, nil
}

// changeRecord is one outbox entry — the key, the op, and its seq — published to the bus.
type changeRecord struct {
	Key string `json:"key"`
	Op  Op     `json:"op"`
	Seq uint64 `json:"seq"`
}

func cdcLogKey(seq uint64) []byte { return []byte(fmt.Sprintf("%s%020d", cdcLogPrefix, seq)) }

// OnWrite appends the change-log entry in the SAME txn the gateway is committing the data write in, so the
// data key and its _cdc/<seq> entry are atomic (both or neither — the outbox property).
func (c *cdc) OnWrite(txn *badger.Txn, key string, op Op) error {
	next, err := c.seq.Next()
	if err != nil {
		return fault.Internalf("kvbadger.cdc.OnWrite", "next seq: %v", err)
	}
	seq := next + 1 // 1-based: seq 0 would be unreachable from a 0-initialised cursor (pending seeks cursor+1)
	rec, err := json.Marshal(changeRecord{Key: key, Op: op, Seq: seq})
	if err != nil {
		return fault.Internalf("kvbadger.cdc.OnWrite", "encode change: %v", err)
	}
	return txn.Set(cdcLogKey(seq), rec)
}

// Tail drains the outbox to the bus from the durable cursor until ctx is cancelled, reclaiming delivered
// entries on each pass (retention). It resumes from the persisted cursor on restart — zero loss.
func (c *cdc) Tail(ctx context.Context) error {
	for {
		n, err := c.drain(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if err := c.gc(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		if n == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(cdcPollInterval):
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// drain publishes every pending entry (seq > cursor) in order, persisting the cursor after each successful
// publish — so a publish failure (or ctx cancel) stops with the cursor at the last delivered seq, and the
// next pass re-reads it and continues without loss. Returns how many were published.
func (c *cdc) drain(ctx context.Context) (int, error) {
	cursor, err := c.cursor(ctx)
	if err != nil {
		return 0, err
	}
	pending, err := c.pending(ctx, cursor)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, e := range pending {
		if err := ctx.Err(); err != nil {
			return published, err
		}
		if err := c.sink.Publish(ctx, c.subject, e.rec); err != nil {
			return published, err // cursor unchanged for this entry → re-delivered next pass
		}
		if err := c.setCursor(ctx, e.seq); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}

type pendingEntry struct {
	seq uint64
	rec []byte
}

// pending reads, in seq order, every outbox entry with seq > cursor.
func (c *cdc) pending(ctx context.Context, cursor uint64) ([]pendingEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []pendingEntry
	p := []byte(cdcLogPrefix)
	err := c.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		start := cdcLogKey(cursor + 1)
		for it.Seek(start); it.ValidForPrefix(p); it.Next() {
			item := it.Item()
			val, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			var rec changeRecord
			if err := json.Unmarshal(val, &rec); err != nil {
				return err
			}
			out = append(out, pendingEntry{seq: rec.Seq, rec: val})
		}
		return nil
	})
	if err != nil {
		return nil, fault.Internalf("kvbadger.cdc.pending", "%v", err)
	}
	return out, nil
}

// gc reclaims outbox entries the consumer has passed (seq <= cursor) — min-cursor retention bounding the
// log. (Single consumer: the cursor is the min.)
func (c *cdc) gc(ctx context.Context) error {
	cursor, err := c.cursor(ctx)
	if err != nil {
		return err
	}
	if cursor == 0 {
		return nil
	}
	var stale [][]byte
	p := []byte(cdcLogPrefix)
	err = c.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			k := it.Item().KeyCopy(nil)
			if seqOf(k) <= cursor {
				stale = append(stale, k)
			}
		}
		return nil
	})
	if err != nil {
		return fault.Internalf("kvbadger.cdc.gc", "scan: %v", err)
	}
	for _, k := range stale {
		if err := c.db.Update(func(txn *badger.Txn) error { return txn.Delete(k) }); err != nil {
			return fault.Internalf("kvbadger.cdc.gc", "delete: %v", err)
		}
	}
	return nil
}

// seqOf parses the trailing seq number out of a _cdc/<020d seq> key.
func seqOf(key []byte) uint64 {
	s := string(key)
	if len(s) <= len(cdcLogPrefix) {
		return 0
	}
	var v uint64
	_, _ = fmt.Sscanf(s[len(cdcLogPrefix):], "%020d", &v)
	return v
}

func (c *cdc) cursor(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var v uint64
	err := c.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(cdcCursorKey))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			if len(val) == 8 {
				v = binary.BigEndian.Uint64(val)
			}
			return nil
		})
	})
	if err != nil {
		return 0, fault.Internalf("kvbadger.cdc.cursor", "%v", err)
	}
	return v, nil
}

func (c *cdc) setCursor(ctx context.Context, v uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	if err := c.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(cdcCursorKey), buf[:])
	}); err != nil {
		return fault.Internalf("kvbadger.cdc.setCursor", "%v", err)
	}
	return nil
}

// release returns the leased sequence band so a clean restart resumes tightly (best-effort; gaps are
// acceptable for ordering). The driver calls it on Close.
func (c *cdc) release() {
	if c.seq != nil {
		_ = c.seq.Release()
	}
}

// RunCDC drives the tailer until ctx is cancelled (the daemon starts it in a goroutine when CDC is
// enabled). A nil/non-*cdc c is a no-op.
func RunCDC(ctx context.Context, c CDC, logger *slog.Logger) {
	cc, ok := c.(*cdc)
	if !ok || cc == nil {
		return
	}
	if err := cc.Tail(ctx); err != nil && ctx.Err() == nil {
		logger.Error("kv cdc tail failed", "err", err)
	}
}
