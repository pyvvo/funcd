package funcd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/provider"
	sqlsvc "github.com/pyvvo/funcd/internal/services/sql"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// sqlRuntime is a fake SQL engine runtime (provider.Runtime and provider.Volumes): every engine is Ready at once,
// served by one httptest engine that tells databases apart by their password and counts each one's tables.
type sqlRuntime struct {
	mu        sync.Mutex
	engine    *httptest.Server
	specs     map[provider.ProviderRef]provider.ProviderSpec
	converges map[provider.ProviderRef]int
	teardowns map[provider.ProviderRef]int
	dirs      map[provider.ProviderRef]bool
	tables    map[string]int // by password
	moved     []quarantine.Entry
}

func newSQLRuntime(t *testing.T, dirs ...provider.ProviderRef) *sqlRuntime {
	r := &sqlRuntime{
		specs: map[provider.ProviderRef]provider.ProviderSpec{}, converges: map[provider.ProviderRef]int{},
		teardowns: map[provider.ProviderRef]int{}, dirs: map[provider.ProviderRef]bool{}, tables: map[string]int{},
	}
	for _, d := range dirs {
		r.dirs[d] = true
	}
	r.engine = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.engine.Close)
	return r
}

func (r *sqlRuntime) serve(w http.ResponseWriter, req *http.Request) {
	user, pass, ok := req.BasicAuth()
	r.mu.Lock()
	n, known := r.tables[pass]
	r.mu.Unlock()
	if !ok || user != "funcd" || !known || req.URL.Path != "/db/query" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	_, _ = fmt.Fprintf(w, `{"results":[{"columns":["count(*)"],"types":["integer"],"values":[[%d]]}]}`, n)
}

func (r *sqlRuntime) Converge(_ context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs[spec.Ref] = spec
	r.converges[spec.Ref]++
	r.dirs[spec.Ref] = true
	if _, ok := r.tables[spec.Env["FUNCD_SQL_PASSWORD"]]; !ok {
		r.tables[spec.Env["FUNCD_SQL_PASSWORD"]] = 0
	}
	return provider.ProviderStatus{Running: 1, Ready: true, Address: strings.TrimPrefix(r.engine.URL, "http://")}, nil
}

func (r *sqlRuntime) Teardown(_ context.Context, ref provider.ProviderRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.teardowns[ref]++
	return nil
}

func (r *sqlRuntime) Reclaim(_ context.Context, ref provider.ProviderRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.dirs, ref)
	return nil
}

func (r *sqlRuntime) Quarantine() quarantine.Store { return mapQuarantine{&r.mu, r.dirs, &r.moved} }

func (r *sqlRuntime) List(context.Context) ([]provider.ProviderRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []provider.ProviderRef
	for ref := range r.dirs {
		out = append(out, ref)
	}
	return out, nil
}

func (r *sqlRuntime) spec(ref provider.ProviderRef) (provider.ProviderSpec, int, int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.specs[ref], r.converges[ref], r.teardowns[ref], r.dirs[ref]
}

func (r *sqlRuntime) createTable(ref provider.ProviderRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tables[r.specs[ref].Env["FUNCD_SQL_PASSWORD"]]++
}

// sqlEnv is a running platform with the fake SQL runtime and an API client.
type sqlEnv struct {
	ctx context.Context
	p   *Platform
	rt  *sqlRuntime
	c   *sdk.Client
}

func startSQL(t *testing.T, rt provider.Runtime, opts ...Option) *sqlEnv {
	t.Helper()
	ctx := context.Background()
	p, err := New(append([]Option{InMemory(), WithSQLProviderRuntime(rt), WithPacing(Pacing{SupervisionPeriod: 100 * time.Millisecond})}, opts...)...)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(DevToken))
	require.NoError(t, err)
	e := &sqlEnv{ctx: ctx, p: p, c: c}
	e.rt, _ = rt.(*sqlRuntime)
	return e
}

func sqlDatabase(name string) *v1.SQLDatabase {
	d := &v1.SQLDatabase{TypeMeta: tmeta(v1.KindSQLDatabase)}
	d.Name, d.Namespace, d.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	return d
}

func sqlRef(name string) provider.ProviderRef {
	return provider.ProviderRef{Namespace: "default", Name: v1.ObjectName(name)}
}

func (e *sqlEnv) apply(t *testing.T, d *v1.SQLDatabase) error {
	t.Helper()
	_, err := e.c.Apply(e.ctx, d)
	return err
}

func (e *sqlEnv) get(t *testing.T, name string) (*v1.SQLDatabase, bool) {
	t.Helper()
	obj, err := e.p.cfg.store.Get(e.ctx, v1.KindSQLDatabase.GVK(), "default", v1.ObjectName(name))
	if fault.KindOf(err) == fault.NotFound {
		return nil, false
	}
	require.NoError(t, err)
	return obj.(*v1.SQLDatabase), true
}

func (e *sqlEnv) waitReady(t *testing.T, name string, generation int64) {
	t.Helper()
	require.Eventually(t, func() bool {
		d, ok := e.get(t, name)
		return ok && d.Status.Phase == v1.PhaseReady && d.Status.ObservedGeneration == generation
	}, 5*time.Second, 10*time.Millisecond, "%s is Ready at generation %d", name, generation)
}

// scenario: database-starts-its-engine — an applied SQLDatabase runs one engine from the rqlite image with a durable
// directory and turns Ready once the engine is Ready.
func TestScenarioDatabaseStartsItsEngine(t *testing.T) {
	t.Parallel()
	e := startSQL(t, newSQLRuntime(t))
	require.NoError(t, e.apply(t, sqlDatabase("todo-db")))
	e.waitReady(t, "todo-db", 1)
	spec, _, _, dir := e.rt.spec(sqlRef("todo-db"))
	require.True(t, dir, "the engine has a directory")
	require.Equal(t, "funcd/runtime-rqlite", spec.Image)
	require.Equal(t, 4001, spec.Port)
	require.Equal(t, 1, spec.Replicas)
	require.Equal(t, provider.ReadinessProbe{Path: "/readyz", ExpectStatus: http.StatusOK}, spec.Readiness)
	require.Equal(t, &provider.StorageSpec{Path: "/data", UID: 65532, GID: 65532}, spec.Storage)
	require.Equal(t, "256Mi", spec.Resources.Memory, "the default memory bound")
	require.Equal(t, "todo-db", spec.Env["FUNCD_SQL_NODE_ID"])
	require.NotEmpty(t, spec.Env["FUNCD_SQL_PASSWORD"])
	addr, ok := e.p.sqlReconciler.Engines().Address("default", "todo-db")
	require.True(t, ok, "the Ready engine is in the registry")
	require.Equal(t, strings.TrimPrefix(e.rt.engine.URL, "http://"), addr)
}

// scenario: one-engine-per-database — a second database gets its own engine, directory and credential.
func TestScenarioOneEnginePerDatabase(t *testing.T) {
	t.Parallel()
	e := startSQL(t, newSQLRuntime(t))
	require.NoError(t, e.apply(t, sqlDatabase("todo-db")))
	require.NoError(t, e.apply(t, sqlDatabase("audit-db")))
	e.waitReady(t, "todo-db", 1)
	e.waitReady(t, "audit-db", 1)
	todo, _, _, todoDir := e.rt.spec(sqlRef("todo-db"))
	audit, _, _, auditDir := e.rt.spec(sqlRef("audit-db"))
	require.True(t, todoDir && auditDir, "each engine has its own directory")
	require.NotEqual(t, todo.Env["FUNCD_SQL_PASSWORD"], audit.Env["FUNCD_SQL_PASSWORD"], "each engine has its own credential")
}

// scenario: delete-reclaims-engine-and-data — deleting an empty Ready database removes its engine and directory.
func TestScenarioDeleteReclaimsEngineAndData(t *testing.T) {
	t.Parallel()
	e := startSQL(t, newSQLRuntime(t))
	require.NoError(t, e.apply(t, sqlDatabase("todo-db")))
	e.waitReady(t, "todo-db", 1)
	require.NoError(t, e.c.Delete(e.ctx, v1.KindSQLDatabase, "default", "todo-db"))
	require.Eventually(t, func() bool {
		_, _, teardowns, dir := e.rt.spec(sqlRef("todo-db"))
		return teardowns > 0 && !dir
	}, 5*time.Second, 10*time.Millisecond, "the engine is torn down and its directory reclaimed")
	_, ok := e.p.sqlReconciler.Engines().Address("default", "todo-db")
	require.False(t, ok, "the deleted engine leaves the registry")
}

// scenario: delete-reclaims-engine-and-data (held half) — while the platform is held the directory of a database
// with no object stays and hold status lists it; the first boot after the release reclaims it.
func TestScenarioDeleteReclaimsEngineAndDataAfterHold(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, hold.Write(dir, hold.Marker{Reason: "restore", Since: v1.NewTimestamp(time.Now())}))
	h, err := hold.Open(dir)
	require.NoError(t, err)
	rt := newSQLRuntime(t, sqlRef("gone-db"))
	held := startSQL(t, rt, WithHold(h))
	orphans, err := holdPlatform{held.p}.SQLOrphans(held.ctx)
	require.NoError(t, err)
	require.Equal(t, []v1.ObjectRef{{Kind: v1.KindSQLDatabase, Namespace: "default", Name: "gone-db"}}, orphans)
	_, _, _, kept := rt.spec(sqlRef("gone-db"))
	require.True(t, kept, "a held boot keeps the directory")

	require.NoError(t, h.Release(time.Now()))
	startSQL(t, rt)
	require.Eventually(t, func() bool {
		_, _, _, still := rt.spec(sqlRef("gone-db"))
		return !still
	}, 5*time.Second, 10*time.Millisecond, "the first boot after the release reclaims the directory")
}

// neverStarts is the SQL runtime of a host that cannot run the engine, as the process runtime: Converge fails, and
// no directory exists.
type neverStarts struct{ teardowns int }

func (*neverStarts) Converge(context.Context, provider.ProviderSpec) (provider.ProviderStatus, error) {
	return provider.ProviderStatus{}, fault.Invalidf("test", "the process runtime offers no durable storage")
}
func (n *neverStarts) Teardown(context.Context, provider.ProviderRef) error {
	n.teardowns++
	return nil
}
func (*neverStarts) Reclaim(context.Context, provider.ProviderRef) error  { return nil }
func (*neverStarts) List(context.Context) ([]provider.ProviderRef, error) { return nil, nil }
func (*neverStarts) Quarantine() quarantine.Store                         { return noQuarantine() }

// scenario: delete-of-never-started-database — a database whose engine never started deletes through the API.
func TestScenarioDeleteOfNeverStartedDatabase(t *testing.T) {
	t.Parallel()
	e := startSQL(t, &neverStarts{})
	require.NoError(t, e.apply(t, sqlDatabase("todo-db")))
	require.Eventually(t, func() bool {
		d, ok := e.get(t, "todo-db")
		if !ok {
			return false
		}
		c, ok := d.Status.Conditions.Get("Ready")
		return ok && c.Status == v1.ConditionFalse && c.Reason == sqlsvc.ReasonEngineConvergeFailed
	}, 5*time.Second, 10*time.Millisecond, "the database reports its engine failing to start")
	require.NoError(t, e.c.Delete(e.ctx, v1.KindSQLDatabase, "default", "todo-db"))
	_, ok := e.get(t, "todo-db")
	require.False(t, ok)
}

// scenario: resources-change-restarts-engine — a new memory bound recreates the engine once, on the same directory.
func TestScenarioResourcesChangeRestartsEngine(t *testing.T) {
	t.Parallel()
	e := startSQL(t, newSQLRuntime(t))
	require.NoError(t, e.apply(t, sqlDatabase("todo-db")))
	e.waitReady(t, "todo-db", 1)
	e.rt.createTable(sqlRef("todo-db"))
	d := sqlDatabase("todo-db")
	d.Spec.Resources.Memory = "512Mi"
	require.NoError(t, e.apply(t, d))
	e.waitReady(t, "todo-db", 2)
	time.Sleep(300 * time.Millisecond) // three supervision periods: no second teardown may follow
	spec, _, teardowns, dir := e.rt.spec(sqlRef("todo-db"))
	require.Equal(t, 1, teardowns, "one change makes exactly one teardown")
	require.Equal(t, "512Mi", spec.Resources.Memory)
	require.True(t, dir, "the directory stays")
	has, err := e.p.sqlReconciler.Prober().HasTables(e.ctx, "default", "todo-db")
	require.NoError(t, err)
	require.True(t, has, "the data is still there")
}

// scenario: delete-refused-while-holding-data — an API delete of a database with a table answers 409 and keeps it.
func TestScenarioDeleteRefusedWhileHoldingData(t *testing.T) {
	t.Parallel()
	e := startSQL(t, newSQLRuntime(t))
	require.NoError(t, e.apply(t, sqlDatabase("todo-db")))
	e.waitReady(t, "todo-db", 1)
	e.rt.createTable(sqlRef("todo-db"))
	err := e.c.Delete(e.ctx, v1.KindSQLDatabase, "default", "todo-db")
	require.Equal(t, fault.Conflict, fault.KindOf(err), "409: %v", err)
	_, ok := e.get(t, "todo-db")
	require.True(t, ok, "the database stays")
	_, _, teardowns, dir := e.rt.spec(sqlRef("todo-db"))
	require.Zero(t, teardowns, "its engine stays")
	require.True(t, dir, "its data stays")
}

// scenario: database-quota — a namespace at its SQLDatabase cap answers 400 to one more.
func TestScenarioDatabaseQuota(t *testing.T) {
	t.Parallel()
	e := startSQL(t, newSQLRuntime(t), WithSQLDatabaseQuota(2))
	require.NoError(t, e.apply(t, sqlDatabase("a")))
	require.NoError(t, e.apply(t, sqlDatabase("b")))
	err := e.apply(t, sqlDatabase("c"))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "400: %v", err)
}
