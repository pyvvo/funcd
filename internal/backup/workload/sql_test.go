package workload_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

// brokenStream reads data, then fails as a stream with an error trailer does.
type brokenStream struct{ r io.Reader }

func (b brokenStream) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if errors.Is(err, io.EOF) {
		return n, fault.Unavailablef("test", "the backup stream broke")
	}
	return n, err
}

// ADR-0234 Decision 2: a database's file is stored as records of 8 MiB keyed 000001, 000002, …; a broken stream or a
// database with no Ready engine fails the run with no manifest.
func TestWriteSQLDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := s3stub.New(t, nil)
	target, err := gocloud.OpenWith(ctx, s.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDOWNER", Secret: "s"}})
	require.NoError(t, err)
	file := bytes.Repeat([]byte("q"), 8<<20+5)
	src := workload.Sources{SQL: func(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (io.ReadCloser, error) {
		switch name {
		case "todo-db":
			return io.NopCloser(bytes.NewReader(file)), nil
		case "broken-db":
			return io.NopCloser(brokenStream{strings.NewReader("partial")}), nil
		}
		return nil, fault.Wrapf(workload.ErrEngineNotReady, fault.Unavailable, "test", "%s/%s", ns, name)
	}}
	const path = "orders/nightly-1-uid/"
	m, err := workload.Write(ctx, "orders", []workload.Member{{Kind: v1.KindSQLDatabase, Name: "todo-db"}}, src,
		workload.Options{Target: target, Path: path})
	require.NoError(t, err)
	require.Equal(t, "sql/todo-db", m.Members[0].Files[0].Name)
	next := workload.Records(ctx, target, path, m.Members[0].Files[0], nil)
	var keys []string
	var got []byte
	for {
		rec, rerr := next()
		if errors.Is(rerr, io.EOF) {
			break
		}
		require.NoError(t, rerr)
		keys = append(keys, string(rec.Key))
		got = append(got, rec.Value...)
	}
	require.Equal(t, []string{"000001", "000002"}, keys)
	require.Equal(t, file, got)

	for _, name := range []v1.ObjectName{"broken-db", "stopped-db"} {
		p := "orders/" + string(name) + "-uid/"
		_, err = workload.Write(ctx, "orders", []workload.Member{{Kind: v1.KindSQLDatabase, Name: name}}, src,
			workload.Options{Target: target, Path: p})
		require.Error(t, err, "%s", name)
		require.NotContains(t, s.PutKeys(p), p+"manifest.yaml", "%s: no manifest", name)
	}
	_, err = workload.Write(ctx, "orders", []workload.Member{{Kind: v1.KindSQLDatabase, Name: "stopped-db"}}, src,
		workload.Options{Target: target, Path: "orders/again-uid/"})
	require.ErrorIs(t, err, workload.ErrEngineNotReady)
	_, err = workload.Write(ctx, "orders", []workload.Member{{Kind: v1.KindSQLDatabase, Name: "todo-db"}}, workload.Sources{},
		workload.Options{Target: target, Path: "orders/none-uid/"})
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "no SQL source wired")
}

// A scope's databases resolve to SQLDatabase members after the other kinds; a missing one is NotFound.
func TestResolveDatabases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(storemem.New())
	db := &v1.SQLDatabase{TypeMeta: tm(v1.KindSQLDatabase), ObjectMeta: meta("todo-db", "rg")}
	_, err := st.Create(ctx, db)
	require.NoError(t, err)
	ms, err := workload.Resolve(ctx, st, "orders", v1.BackupScope{Databases: []v1.ObjectName{"todo-db"}})
	require.NoError(t, err)
	require.Equal(t, []workload.Member{{Kind: v1.KindSQLDatabase, Name: "todo-db"}}, ms)
	ms, err = workload.Resolve(ctx, st, "orders", v1.BackupScope{ResourceGroup: "rg"})
	require.NoError(t, err)
	require.Equal(t, []workload.Member{{Kind: v1.KindSQLDatabase, Name: "todo-db"}}, ms, "a ResourceGroup scope includes its databases")
	_, err = workload.Resolve(ctx, st, "orders", v1.BackupScope{Databases: []v1.ObjectName{"gone-db"}})
	require.Equal(t, fault.NotFound, fault.KindOf(err))
}
