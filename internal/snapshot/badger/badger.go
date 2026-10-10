// Package badger snapshots and loads a whole Badger instance for the platform stores built on one (ADR-0202),
// the metastore engine and the run state, so each reads in one transaction alike.
package badger

import (
	"bytes"
	"context"
	"errors"
	"io"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/snapshot"
)

// Snapshot emits every key and its latest value, in key order, from ONE read transaction with ONE iterator:
// never DB.Backup or a Stream, whose producers each read at their own time (#806).
func Snapshot(ctx context.Context, db *badger.DB, emit func(snapshot.Record) error) error {
	const op = "badger.Snapshot"
	if err := ctx.Err(); err != nil {
		return err
	}
	return db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			item := it.Item()
			val, err := item.ValueCopy(nil)
			if err != nil {
				return fault.Internalf(op, "value of %q: %v", item.Key(), err)
			}
			if err := emit(snapshot.Record{Key: item.KeyCopy(nil), Value: val}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Load writes the records next returns, until io.EOF, into db through a WriteBatch, which commits whenever
// Badger's transaction limit is reached. A non-empty db gets fault.Conflict before any write. skip, when not
// nil, names a record to leave out, or fails the Load on a malformed one.
func Load(ctx context.Context, db *badger.DB, next func() (snapshot.Record, error), skip func(key []byte) (bool, error)) error {
	const op = "badger.Load"
	if err := ctx.Err(); err != nil {
		return err
	}
	var empty bool
	if err := db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		it.Rewind()
		empty = !it.Valid()
		return nil
	}); err != nil {
		return fault.Internalf(op, "check the store is empty: %v", err)
	}
	if !empty {
		return fault.Conflictf(op, "the store is not empty")
	}
	wb := db.NewWriteBatch()
	defer wb.Cancel()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "read the next record")
		}
		if skip != nil {
			left, serr := skip(r.Key)
			if serr != nil {
				return serr
			}
			if left {
				continue
			}
		}
		if err := wb.Set(bytes.Clone(r.Key), bytes.Clone(r.Value)); err != nil {
			return fault.Internalf(op, "write %q: %v", r.Key, err)
		}
	}
	if err := wb.Flush(); err != nil {
		return fault.Internalf(op, "flush: %v", err)
	}
	return nil
}
