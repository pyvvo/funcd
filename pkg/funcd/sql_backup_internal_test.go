package funcd

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/provider"
)

// fileEngine is a fake engine that holds a database's SQLite file as opaque bytes: /db/backup streams it, /db/load and
// /boot replace it, and the object count is 0 for an empty or a 4 KiB file (what Empty loads), else 1.
type fileEngine struct {
	srv      *httptest.Server
	password string

	mu          sync.Mutex
	data        []byte
	breakStream bool
	failLoad    bool
}

func (e *fileEngine) serve(w http.ResponseWriter, r *http.Request) {
	if user, pass, ok := r.BasicAuth(); !ok || user != "funcd" || pass != e.password {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	switch r.URL.Path {
	case "/db/backup":
		if r.URL.Query().Get("fmt") != "delete" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Trailer", "X-Stream-Error")
		_, _ = w.Write(e.data)
		if e.breakStream {
			w.Header().Set("X-Stream-Error", "disk I/O error")
		}
	case "/boot":
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		e.data = body
	case "/db/load":
		if e.failLoad {
			_, _ = w.Write([]byte(`{"error":"load refused"}`))
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		e.data = body
		_, _ = w.Write([]byte(`{"results":[]}`))
	case "/db/query":
		n := 1
		if len(e.data) == 0 || len(e.data) == 4096 {
			n = 0
		}
		_, _ = w.Write([]byte(`{"results":[{"columns":["count(*)"],"values":[[` + string(rune('0'+n)) + `]]}]}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (e *fileEngine) file() []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	return bytes.Clone(e.data)
}

func (e *fileEngine) set(f func(*fileEngine)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f(e)
}

// fileEngines is a SQL runtime of fileEngines, one per database; a database in notReady never gets a Ready engine.
// dirs is the engine directories List reports, as a durable storage root would hold them.
type fileEngines struct {
	t        *testing.T
	mu       sync.Mutex
	engines  map[provider.ProviderRef]*fileEngine
	notReady map[v1.ObjectName]bool
	dirs     map[provider.ProviderRef]bool
	moved    []quarantine.Entry
}

func newFileEngines(t *testing.T) *fileEngines {
	return &fileEngines{t: t, engines: map[provider.ProviderRef]*fileEngine{}, notReady: map[v1.ObjectName]bool{},
		dirs: map[provider.ProviderRef]bool{}}
}

func (f *fileEngines) Converge(_ context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs[spec.Ref] = true
	if f.notReady[spec.Ref.Name] {
		return provider.ProviderStatus{Reason: "Starting"}, nil
	}
	e, ok := f.engines[spec.Ref]
	if !ok {
		e = &fileEngine{password: spec.Env["FUNCD_SQL_PASSWORD"]}
		e.srv = httptest.NewServer(http.HandlerFunc(e.serve))
		f.t.Cleanup(e.srv.Close)
		f.engines[spec.Ref] = e
	}
	return provider.ProviderStatus{Running: 1, Ready: true, Address: strings.TrimPrefix(e.srv.URL, "http://")}, nil
}

func (*fileEngines) Teardown(context.Context, provider.ProviderRef) error { return nil }

func (f *fileEngines) Reclaim(_ context.Context, ref provider.ProviderRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.dirs, ref)
	return nil
}

func (f *fileEngines) List(context.Context) ([]provider.ProviderRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []provider.ProviderRef
	for ref := range f.dirs {
		out = append(out, ref)
	}
	return out, nil
}

func (f *fileEngines) engine(t *testing.T, name v1.ObjectName) *fileEngine {
	t.Helper()
	var e *fileEngine
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		e = f.engines[provider.ProviderRef{Namespace: "orders", Name: name}]
		return e != nil
	}, 10*time.Second, 10*time.Millisecond, "the engine of %s starts", name)
	return e
}

// sqlRig is the backup rig with SQL databases served by fileEngines, the Ready ones listed.
func sqlRig(t *testing.T, engines *fileEngines, dbs ...v1.ObjectName) *wbRig {
	t.Helper()
	r := newWBRig(t, wbOptions{opts: []Option{WithSQLProviderRuntime(engines)}, setup: func(r *wbRig) {
		for _, name := range dbs {
			r.seed(&v1.SQLDatabase{TypeMeta: tmeta(v1.KindSQLDatabase), ObjectMeta: ordersMeta(string(name))})
		}
	}})
	for _, name := range dbs {
		if !engines.notReady[name] {
			r.waitSQLReady(name)
		}
	}
	return r
}

func (r *wbRig) waitSQLReady(name v1.ObjectName) {
	r.t.Helper()
	require.Eventually(r.t, func() bool {
		obj, err := r.st.Get(r.ctx, v1.KindSQLDatabase.GVK(), "orders", name)
		return err == nil && obj.(*v1.SQLDatabase).Status.Phase == v1.PhaseReady
	}, 10*time.Second, 10*time.Millisecond, "%s is Ready", name)
}

// dbSchedule is a schedule whose scope is the databases names.
func (r *wbRig) dbSchedule(names ...v1.ObjectName) *v1.BackupSchedule {
	return r.schedule("orders-db", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{Databases: names}
	})
}

// sqlite is a stand-in SQLite file larger than an empty one.
func sqlite(rows string) []byte {
	return []byte("SQLite format 3\x00" + rows + strings.Repeat(".", 8000))
}

// scenario: scheduled-backup-copies-database — a schedule naming todo-db copies its file as member SQLDatabase/todo-db,
// file sql/todo-db, sealed to the schedule's recipients.
func TestScenarioScheduledBackupCopiesDatabase(t *testing.T) {
	t.Parallel()
	engines := newFileEngines(t)
	r := sqlRig(t, engines, "todo-db")
	engines.engine(t, "todo-db").set(func(e *fileEngine) { e.data = sqlite("milk,eggs") })
	r.create(r.dev, r.dbSchedule("todo-db"))
	r.create(r.dev, r.backupObj("b1", "orders-db"))
	b := r.waitBackup("b1", v1.RunSucceeded)
	require.Equal(t, []v1.BackupMember{{Kind: v1.KindSQLDatabase, Name: "todo-db", Bytes: b.Status.Members[0].Bytes}}, b.Status.Members)
	require.Equal(t, map[string]string{"000001": string(sqlite("milk,eggs"))}, r.records(b.Status.Path, v1.KindSQLDatabase, "todo-db", r.recovery),
		"the file opens with either recipient's identity")
}

// scenario: app-backup-includes-database — an App declaring todo-db and a backupSchedules entry with an empty scope:
// the schedule backs up the App, and its Backup holds SQLDatabase/todo-db.
func TestScenarioAppBackupIncludesDatabase(t *testing.T) {
	t.Parallel()
	engines := newFileEngines(t)
	r := sqlRig(t, engines)
	spec := r.schedule("x", "0 3 * * *", func(s *v1.BackupScheduleSpec) { s.Scope = v1.BackupScope{} }).Spec
	r.create(r.dev, &v1.App{TypeMeta: tmeta(v1.KindApp), ObjectMeta: ordersMeta("todo"), Spec: v1.AppSpec{
		SQL:             []v1.SQLDatabaseEntry{{Name: "todo-db"}},
		BackupSchedules: []v1.AppBackupSchedule{{Name: "todo-nightly", BackupScheduleSpec: spec}},
	}})
	r.waitSQLReady("todo-db")
	engines.engine(t, "todo-db").set(func(e *fileEngine) { e.data = sqlite("milk") })
	require.Eventually(t, func() bool {
		_, err := r.st.Get(r.ctx, v1.KindBackupSchedule.GVK(), "orders", "todo-nightly")
		return err == nil
	}, 10*time.Second, 10*time.Millisecond, "the App writes its schedule")
	r.create(r.dev, r.backupObj("b1", "todo-nightly"))
	b := r.waitBackup("b1", v1.RunSucceeded)
	require.Equal(t, []v1.BackupMember{{Kind: v1.KindSQLDatabase, Name: "todo-db", Bytes: b.Status.Members[0].Bytes}}, b.Status.Members)
	require.Equal(t, map[string]string{"000001": string(sqlite("milk"))}, r.records(b.Status.Path, v1.KindSQLDatabase, "todo-db", r.recovery))
}

// scenario: restore-into-new-name — a Restore of SQLDatabase/todo-db as the empty todo-db-restored brings the backup's
// file there and leaves todo-db unchanged.
func TestScenarioRestoreIntoNewName(t *testing.T) {
	t.Parallel()
	engines := newFileEngines(t)
	r := sqlRig(t, engines, "todo-db", "todo-db-restored")
	src := engines.engine(t, "todo-db")
	src.set(func(e *fileEngine) { e.data = sqlite("milk,eggs") })
	r.create(r.dev, r.dbSchedule("todo-db"))
	r.create(r.dev, r.backupObj("b1", "orders-db"))
	r.waitBackup("b1", v1.RunSucceeded)
	src.set(func(e *fileEngine) { e.data = sqlite("milk,eggs,bread") })
	r.create(r.dev, r.restoreObj("back", "b1", v1.RestoreMember{Kind: v1.KindSQLDatabase, Name: "todo-db", As: "todo-db-restored"}))
	r.waitRestore("back", v1.RunSucceeded)
	require.Equal(t, sqlite("milk,eggs"), engines.engine(t, "todo-db-restored").file())
	require.Equal(t, sqlite("milk,eggs,bread"), src.file(), "the source is unchanged")
}

// scenario: restore-refuses-non-empty — a destination with a table, or one a Function binds, fails NotEmpty and loads
// nothing.
func TestScenarioRestoreRefusesNonEmpty(t *testing.T) {
	t.Parallel()
	engines := newFileEngines(t)
	r := sqlRig(t, engines, "todo-db", "full-db", "bound-db")
	engines.engine(t, "todo-db").set(func(e *fileEngine) { e.data = sqlite("milk") })
	engines.engine(t, "full-db").set(func(e *fileEngine) { e.data = sqlite("other") })
	fn := &v1.Function{TypeMeta: tmeta(v1.KindFunction), ObjectMeta: ordersMeta("reader"),
		Spec: v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: "file:///nonexistent.mjs", Bindings: v1.FunctionBindings{SQL: []v1.FunctionSQL{{Alias: "app", Database: "bound-db"}}}}}
	r.seed(fn)
	r.create(r.dev, r.dbSchedule("todo-db"))
	r.create(r.dev, r.backupObj("b1", "orders-db"))
	r.waitBackup("b1", v1.RunSucceeded)
	for _, dest := range []v1.ObjectName{"full-db", "bound-db"} {
		name := "into-" + string(dest)
		r.create(r.dev, r.restoreObj(name, "b1", v1.RestoreMember{Kind: v1.KindSQLDatabase, Name: "todo-db", As: dest}))
		rs := r.waitRestore(v1.ObjectName(name), v1.RunFailed)
		require.Equal(t, "NotEmpty", reason(rs.Status.Status), "%s", dest)
	}
	require.Equal(t, sqlite("other"), engines.engine(t, "full-db").file(), "nothing is loaded")
	require.Empty(t, engines.engine(t, "bound-db").file())
}

// scenario: restore-in-place — an administrator's in-place Restore of todo-db leaves exactly the backup's file.
func TestScenarioRestoreInPlaceDatabase(t *testing.T) {
	t.Parallel()
	engines := newFileEngines(t)
	r := sqlRig(t, engines, "todo-db")
	db := engines.engine(t, "todo-db")
	db.set(func(e *fileEngine) { e.data = sqlite("milk") })
	r.create(r.dev, r.dbSchedule("todo-db"))
	r.create(r.dev, r.backupObj("b1", "orders-db"))
	r.waitBackup("b1", v1.RunSucceeded)
	db.set(func(e *fileEngine) { e.data = sqlite("milk,changed") })
	r.create(r.admin, r.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindSQLDatabase, Name: "todo-db", InPlace: true}))
	r.waitRestore("rewind", v1.RunSucceeded)
	require.Equal(t, sqlite("milk"), db.file())
}

// scenario: failed-restore-empties-database — when a later member fails, a database the Restore already loaded is left
// with no table.
func TestScenarioFailedRestoreEmptiesDatabase(t *testing.T) {
	t.Parallel()
	engines := newFileEngines(t)
	r := sqlRig(t, engines, "todo-db", "audit-db", "todo-db-restored", "audit-db-restored")
	engines.engine(t, "todo-db").set(func(e *fileEngine) { e.data = sqlite("milk") })
	engines.engine(t, "audit-db").set(func(e *fileEngine) { e.data = sqlite("log") })
	engines.engine(t, "audit-db-restored").set(func(e *fileEngine) { e.failLoad = true })
	r.create(r.dev, r.dbSchedule("todo-db", "audit-db"))
	r.create(r.dev, r.backupObj("b1", "orders-db"))
	r.waitBackup("b1", v1.RunSucceeded)
	rs := r.restoreObj("both", "b1", v1.RestoreMember{Kind: v1.KindSQLDatabase, Name: "todo-db", As: "todo-db-restored"})
	rs.Spec.Members = append(rs.Spec.Members, v1.RestoreMember{Kind: v1.KindSQLDatabase, Name: "audit-db", As: "audit-db-restored"})
	r.create(r.dev, rs)
	r.waitRestore("both", v1.RunFailed)
	require.Len(t, engines.engine(t, "todo-db-restored").file(), 4096, "the loaded database is emptied")
}

// scenario: backup-fails-while-engine-not-ready — a database with no Ready engine fails the Backup EngineNotReady.
func TestScenarioBackupFailsWhileEngineNotReady(t *testing.T) {
	t.Parallel()
	engines := newFileEngines(t)
	engines.notReady["todo-db"] = true
	r := sqlRig(t, engines, "todo-db")
	r.create(r.dev, r.dbSchedule("todo-db"))
	r.create(r.dev, r.backupObj("b1", "orders-db"))
	b := r.waitBackup("b1", v1.RunFailed)
	require.Equal(t, "EngineNotReady", reason(b.Status.Status))
	c, _ := b.Status.Conditions.Get("Ready")
	require.Contains(t, c.Message, "todo-db")
}

// scenario: truncated-stream-fails-backup — a stream that ends with X-Stream-Error fails the Backup, with no manifest.
func TestScenarioTruncatedStreamFailsBackup(t *testing.T) {
	t.Parallel()
	engines := newFileEngines(t)
	r := sqlRig(t, engines, "todo-db")
	engines.engine(t, "todo-db").set(func(e *fileEngine) { e.data, e.breakStream = sqlite("milk"), true })
	r.create(r.dev, r.dbSchedule("todo-db"))
	r.create(r.dev, r.backupObj("b1", "orders-db"))
	b := r.waitBackup("b1", v1.RunFailed)
	c, _ := b.Status.Conditions.Get("Ready")
	require.Contains(t, c.Message, "disk I/O error")
	require.Empty(t, b.Status.Members, "nothing is recorded for the database")
}

// scenario: missing-data-reported — after a restart on a host that lost the engine directory, a database that was Ready
// is Ready and empty with DataMissing True; a Succeeded Restore into it clears it.
func TestScenarioMissingDataReported(t *testing.T) {
	t.Parallel()
	engines := newFileEngines(t)
	r := newWBRig(t, wbOptions{opts: []Option{WithSQLProviderRuntime(engines), WithProviderStorage(t.TempDir())}, setup: func(r *wbRig) {
		r.seed(&v1.SQLDatabase{TypeMeta: tmeta(v1.KindSQLDatabase), ObjectMeta: ordersMeta("todo-db")})
	}})
	r.waitSQLReady("todo-db")
	engines.engine(t, "todo-db").set(func(e *fileEngine) { e.data = sqlite("milk") })
	r.create(r.dev, r.dbSchedule("todo-db"))
	r.create(r.dev, r.backupObj("b1", "orders-db"))
	r.waitBackup("b1", v1.RunSucceeded)

	engines.mu.Lock()
	clear(engines.dirs)
	clear(engines.engines)
	engines.mu.Unlock()
	r.restart()
	missing := func() (v1.Condition, bool) {
		obj, err := r.st.Get(r.ctx, v1.KindSQLDatabase.GVK(), "orders", "todo-db")
		require.NoError(t, err)
		return obj.(*v1.SQLDatabase).Status.Conditions.Get("DataMissing")
	}
	require.Eventually(t, func() bool { c, ok := missing(); return ok && c.Status == v1.ConditionTrue }, 10*time.Second, 10*time.Millisecond,
		"DataMissing is True")
	c, _ := missing()
	require.Contains(t, c.Message, "workload Restore")
	r.waitSQLReady("todo-db")
	require.Empty(t, engines.engine(t, "todo-db").file(), "the database starts empty")

	r.create(r.admin, r.restoreObj("back", "b1", v1.RestoreMember{Kind: v1.KindSQLDatabase, Name: "todo-db", InPlace: true}))
	r.waitRestore("back", v1.RunSucceeded)
	require.Eventually(t, func() bool { c, ok := missing(); return ok && c.Status == v1.ConditionFalse }, 10*time.Second, 10*time.Millisecond,
		"a Succeeded Restore clears DataMissing")
}
