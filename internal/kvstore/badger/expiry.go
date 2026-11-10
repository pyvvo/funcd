package badger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"golang.org/x/sync/errgroup"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/kvstore"
	"github.com/pyvvo/funcd/internal/platform/diskfull"
)

const (
	// timelineKey holds the instance's timeline, which keys every version's hash (ADR-0320 Decision 2). The backup
	// export leaves it out, so a restored instance mints another when it opens and every version changes.
	timelineKey = Reserved + "kv/timeline"
	// expiryPrefix indexes each key written with an expiry by that second, so the sweeper reads only what is due.
	expiryPrefix = Reserved + "kv/exp/"
	// expiryDigits is the width of the second in an index key, so the keys sort by it.
	expiryDigits = 20
)

func expiryKey(expiresAt uint64, key string) []byte {
	return fmt.Appendf(nil, "%s%0*d/%s", expiryPrefix, expiryDigits, expiresAt, key)
}

// dropIndexOf deletes the index key of key's live entry when it has an expiry: the entry a plain write replaces leaves
// the index with it, so the index holds one key per live key with an expiry.
func dropIndexOf(txn *badger.Txn, key string) error {
	item, err := txn.Get([]byte(key))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return nil
	}
	if err != nil || item.ExpiresAt() == 0 {
		return err
	}
	return txn.Delete(expiryKey(item.ExpiresAt(), key))
}

// indexUnder reads the index keys of the keys under prefix, from each key's newest entry, which Badger's native
// DropPrefix would leave behind; it reads the store's own keys, so a teardown stays O(store) (ADR-0066).
func (d *driver) indexUnder(prefix string) ([][]byte, error) {
	var index [][]byte
	err := d.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.AllVersions, opts.PrefetchValues, opts.Prefix = true, false, []byte(prefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		var last []byte
		for it.Seek(opts.Prefix); it.ValidForPrefix(opts.Prefix); it.Next() {
			item := it.Item()
			if bytes.Equal(item.Key(), last) {
				continue // an older entry of the key just read
			}
			last = item.KeyCopy(last[:0])
			if at := item.ExpiresAt(); at != 0 {
				index = append(index, expiryKey(at, string(last)))
			}
		}
		return nil
	})
	if err != nil {
		return nil, diskfull.Wrapf(err, "kvbadger.DropPrefix", "read the index keys under %q", prefix)
	}
	return index, nil
}

// dropIndexKeys deletes the index keys a DropPrefix of prefix left behind.
func (d *driver) dropIndexKeys(prefix string, index [][]byte) error {
	if len(index) == 0 {
		return nil
	}
	wb := d.db.NewWriteBatch()
	for _, k := range index {
		if err := wb.Delete(k); err != nil {
			wb.Cancel()
			return diskfull.Wrapf(err, "kvbadger.DropPrefix", "drop the index keys under %q", prefix)
		}
	}
	if err := wb.Flush(); err != nil {
		return diskfull.Wrapf(err, "kvbadger.DropPrefix", "drop the index keys under %q", prefix)
	}
	return nil
}

// loadTimeline reads the instance's timeline, minting and storing one when there is none: at first start, and after
// a restore, whose export left it out.
func loadTimeline(db *badger.DB) (string, error) {
	const op = "kvbadger.Open"
	var timeline string
	err := db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(timelineKey))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		v, err := item.ValueCopy(nil)
		timeline = string(v)
		return err
	})
	if err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "read the timeline")
	}
	if timeline != "" {
		return timeline, nil
	}
	timeline = kvstore.NewTimeline()
	if err := db.Update(func(txn *badger.Txn) error { return txn.Set([]byte(timelineKey), []byte(timeline)) }); err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "store the timeline")
	}
	return timeline, nil
}

type dueKey struct {
	key       string
	expiresAt uint64
}

// Sweep deletes up to limit keys that expired by now and skip does not name (kvstore.Sweeper), the oldest expiry first.
// Each is a gateway delete conditional on the entry that expired, so the change feed and the backup see it as any
// delete, and a renewal that won the race keeps its key.
func (d *driver) Sweep(ctx context.Context, now time.Time, limit int, skip func(string) bool) ([]string, bool, error) {
	due, err := d.due(ctx, now, limit, skip)
	if err != nil {
		return nil, false, err
	}
	results := make([]kvstore.Result, len(due))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(dropConcurrency)
	for i, e := range due {
		g.Go(func() error {
			res, err := d.submit(gctx, &writeReq{key: e.key, del: true, reap: e.expiresAt, versioned: true})
			results[i] = res
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, false, err
	}
	var swept []string
	for i, res := range results {
		if res.Done {
			swept = append(swept, due[i].key)
		}
	}
	return swept, len(due) == limit, nil
}

// due lists the index entries whose second passed by now and whose key skip does not name, oldest first, up to limit.
func (d *driver) due(ctx context.Context, now time.Time, limit int, skip func(string) bool) ([]dueKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []dueKey
	prefix := []byte(expiryPrefix)
	err := d.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues, opts.Prefix = false, prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix) && len(out) < limit; it.Next() {
			rest := string(it.Item().Key()[len(prefix):])
			if len(rest) <= expiryDigits || rest[expiryDigits] != '/' {
				continue
			}
			at, err := strconv.ParseUint(rest[:expiryDigits], 10, 64)
			if err != nil {
				continue
			}
			if !kvstore.Expired(at, now) {
				break
			}
			if key := rest[expiryDigits+1:]; !skip(key) {
				out = append(out, dueKey{key: key, expiresAt: at})
			}
		}
		return nil
	})
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "kvbadger.Sweep", "read the expiry index")
	}
	return out, nil
}
