package sql_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup/workload"
	sqlsvc "github.com/pyvvo/funcd/internal/services/sql"
)

// backupEngine serves /db/backup?fmt=delete (with the error trailer when broken), /db/load and the object count, as
// user funcd with the database's password.
type backupEngine struct {
	data    []byte
	broken  bool
	refused bool
	objects int
	loads   int
	boots   int
}

func (e *backupEngine) serve(t *testing.T) *httptest.Server {
	pw := sqlsvc.EnginePassword([]byte(masterKey), "default", "todo-db")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "funcd" || p != pw {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/db/backup":
			require.Equal(t, "delete", r.URL.Query().Get("fmt"))
			w.Header().Set("Trailer", "X-Stream-Error")
			_, _ = w.Write(e.data)
			if e.broken {
				w.Header().Set("X-Stream-Error", "disk I/O error")
			}
		case "/db/load":
			if e.refused {
				_, _ = w.Write([]byte(`{"results":[{"error":"file is not a database"}]}`))
				return
			}
			e.data, _ = io.ReadAll(r.Body)
			e.loads++
			_, _ = w.Write([]byte(`{"results":[]}`))
		case "/boot":
			e.data, _ = io.ReadAll(r.Body)
			e.boots++
		case "/db/query":
			_, _ = w.Write([]byte(`{"results":[{"columns":["count(*)"],"values":[[` + string(rune('0'+e.objects)) + `]]}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func backups(addr string) sqlsvc.Backups {
	engines := sqlsvc.NewEngines()
	if addr != "" {
		engines.Set("default", "todo-db", addr)
	}
	return sqlsvc.Backups{Engines: engines, Master: []byte(masterKey)}
}

func TestBackupsCopyLoadEmpty(t *testing.T) {
	ctx := context.Background()
	e := &backupEngine{data: []byte("SQLite format 3\x00rows")}
	b := backups(strings.TrimPrefix(e.serve(t).URL, "http://"))

	rc, err := b.Source(ctx, "default", "todo-db")
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	require.Equal(t, e.data, got)

	e.broken = true
	rc, err = b.Source(ctx, "default", "todo-db")
	require.NoError(t, err)
	_, err = io.ReadAll(rc)
	require.ErrorContains(t, err, "disk I/O error", "a broken stream never reads as a complete file")

	require.NoError(t, b.Load(ctx, "default", "todo-db", bytes.NewReader([]byte("restored"))))
	require.Equal(t, []byte("restored"), e.data)
	require.Equal(t, [2]int{1, 1}, [2]int{e.loads, e.boots}, "a load empties the database, then boots the file")
	require.NoError(t, b.Empty(ctx, "default", "todo-db"))
	require.Len(t, e.data, 4096, "Empty loads the empty SQLite file")
	require.True(t, bytes.HasPrefix(e.data, []byte("SQLite format 3\x00")))
	e.refused = true
	require.Equal(t, fault.Invalid, fault.KindOf(b.Load(ctx, "default", "todo-db", strings.NewReader("junk"))), "a refused empty stops the load")

	has, err := b.HasTables(ctx, "default", "todo-db")
	require.NoError(t, err)
	require.False(t, has)
	e.objects = 1
	has, err = b.HasTables(ctx, "default", "todo-db")
	require.NoError(t, err)
	require.True(t, has)
}

func TestBackupsEngineNotReady(t *testing.T) {
	ctx := context.Background()
	b := backups("")
	_, err := b.Source(ctx, "default", "todo-db")
	require.ErrorIs(t, err, workload.ErrEngineNotReady)
	require.ErrorIs(t, b.Load(ctx, "default", "todo-db", strings.NewReader("x")), workload.ErrEngineNotReady)
	_, err = b.HasTables(ctx, "default", "todo-db")
	require.ErrorIs(t, err, workload.ErrEngineNotReady)

	closedURL := "http://127.0.0.1:1" // nothing listens there: a closed server's port could be bound again
	addr := strings.TrimPrefix(closedURL, "http://")
	_, err = backups(addr).Source(ctx, "default", "todo-db")
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.NotContains(t, err.Error(), addr, "the engine's address never appears in an error")

	wrong := &backupEngine{}
	srv := wrong.serve(t)
	other := sqlsvc.Backups{Engines: sqlsvc.NewEngines(), Master: []byte("another master, thirty-two bytes")}
	other.Engines.Set("default", "todo-db", strings.TrimPrefix(srv.URL, "http://"))
	_, err = other.Source(ctx, "default", "todo-db")
	require.Equal(t, fault.Internal, fault.KindOf(err), "a refused credential is funcd's fault")
}

// A backup stream cut mid-copy fails Unavailable, naming the database and never the engine's address.
func TestBackupsBrokenConnection(t *testing.T) {
	ctx := context.Background()
	pw := sqlsvc.EnginePassword([]byte(masterKey), "default", "todo-db")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, p, _ := r.BasicAuth(); p != pw {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Length", "1000000")
		_, _ = w.Write([]byte("SQLite format 3\x00"))
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		require.NoError(t, err)
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")
	rc, err := backups(addr).Source(ctx, "default", "todo-db")
	require.NoError(t, err)
	_, err = io.ReadAll(rc)
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.ErrorContains(t, err, "default/todo-db")
	require.NotContains(t, err.Error(), addr)
	host, _, _ := strings.Cut(addr, ":")
	require.NotContains(t, err.Error(), host+":", "no host:port of either end")
}

// failingFile is a backup file whose read fails partway, as a broken part on the target does.
type failingFile struct{ sent bool }

func (f *failingFile) Read(p []byte) (int, error) {
	if !f.sent {
		f.sent = true
		return copy(p, "SQLite format 3\x00"), nil
	}
	return 0, fault.Invalidf("test", "a torn record in the backup")
}

// A load whose backup file fails to read reports that cause, not an engine that does not answer.
func TestBackupsLoadKeepsTheFileError(t *testing.T) {
	e := &backupEngine{}
	b := backups(strings.TrimPrefix(e.serve(t).URL, "http://"))
	err := b.Load(context.Background(), "default", "todo-db", &failingFile{})
	require.ErrorContains(t, err, "a torn record in the backup")
}
