package sql

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeletesOnly(t *testing.T) {
	t.Parallel()
	for text, want := range map[string]bool{
		"DELETE FROM t WHERE id = 1":                         true,
		"  -- purge\n delete from t":                         true,
		"/* old */ DROP TABLE t;":                            true,
		"DROP INDEX i; -- done":                              true,
		"DELETE FROM t WHERE name = 'a;b'":                   true,
		`DELETE FROM "t;x" WHERE [c;d] = 1 /* ; */`:          true,
		"DELETE FROM t WHERE 0; INSERT INTO t VALUES (1)":    false,
		"WITH old AS (SELECT 1) DELETE FROM t":               false,
		"INSERT INTO t VALUES (1)":                           false,
		"DELETED_AT":                                         false,
		"UPDATE t SET x = 1":                                 false,
		"DELETE FROM t WHERE name = 'it''s'; INSERT INTO t":  false,
		"DELETE FROM t WHERE name = 'unterminated; INSERT x": true,
	} {
		stmt, err := json.Marshal([]string{text})
		require.NoError(t, err)
		require.Equal(t, want, deletesOnly([]json.RawMessage{stmt}), text)
	}
	del := json.RawMessage(`["DELETE FROM t"]`)
	ins := json.RawMessage(`["INSERT INTO t VALUES (?)", 1]`)
	require.False(t, deletesOnly([]json.RawMessage{del, ins}), "one write among the statements makes them a write")
	require.False(t, deletesOnly(nil))
}
