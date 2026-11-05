package badger

import (
	"strings"
	"testing"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

// A failure of a transaction keeps Badger's text out of the Msg, so a 500's detail holds no cause and the log's full
// error does.
func TestTxnFaultsHoldNoCause(t *testing.T) {
	db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	bt := db.NewTransaction(true)
	bt.Discard()
	tx := &txn{bt: bt, write: true}
	const cause = "discarded"
	for name, call := range map[string]func() error{
		"get":    func() error { _, _, err := tx.Get("b", "k"); return err },
		"put":    func() error { return tx.Put("b", "k", []byte("v")) },
		"delete": func() error { return tx.Delete("b", "k") },
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			require.Error(t, err)
			require.True(t, strings.Contains(err.Error(), cause), "the full error keeps the cause: %v", err)
			require.NotContains(t, fault.ToProblem(err).Detail, cause)
		})
	}
}
