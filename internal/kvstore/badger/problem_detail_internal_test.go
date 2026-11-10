package badger

import (
	"context"
	"testing"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

const closedCause = "DB Closed"

// A failed read keeps Badger's text out of the Msg, so a 500's detail holds no cause and the log's full error does.
func TestReadFaultsHoldNoCause(t *testing.T) {
	ctx := context.Background()
	kv, err := Open(t.TempDir(), WithSyncWrites(false), WithValueLogGCInterval(0))
	require.NoError(t, err)
	d := kv.(*driver)
	require.NoError(t, d.Close())
	for name, call := range map[string]func() error{
		"get":   func() error { _, _, err := d.Get(ctx, "k"); return err },
		"list":  func() error { _, err := d.List(ctx, ""); return err },
		"usage": func() error { _, err := d.Usage(ctx, ""); return err },
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			require.ErrorContains(t, err, closedCause)
			require.NotContains(t, fault.ToProblem(err).Detail, closedCause)
		})
	}
}

// The change log's write on a Put's path fails the same way.
func TestChangeLogFaultHoldsNoCause(t *testing.T) {
	db, err := badger.Open(badger.DefaultOptions(t.TempDir()).WithLogger(nil))
	require.NoError(t, err)
	seq, err := db.GetSequence([]byte("seq"), 1)
	require.NoError(t, err)
	_, err = seq.Next()
	require.NoError(t, err)
	require.NoError(t, db.Close())
	c := &cdc{db: db, seq: seq}
	err = c.OnWrite(nil, "k", OpPut, 0)
	require.ErrorContains(t, err, closedCause)
	require.NotContains(t, fault.ToProblem(err).Detail, closedCause)
}
