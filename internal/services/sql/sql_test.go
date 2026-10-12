package sql_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/provider"
	sqlsvc "github.com/pyvvo/funcd/internal/services/sql"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

const masterKey = "0123456789abcdef0123456789abcdef"

func TestEnginePassword(t *testing.T) {
	a := sqlsvc.EnginePassword([]byte(masterKey), "default", "todo-db")
	require.Equal(t, a, sqlsvc.EnginePassword([]byte(masterKey), "default", "todo-db"), "derived, so stable")
	require.NotEqual(t, a, sqlsvc.EnginePassword([]byte(masterKey), "default", "audit-db"))
	require.NotEqual(t, a, sqlsvc.EnginePassword([]byte(masterKey), "team", "todo-db"))
	require.NotEqual(t, a, sqlsvc.EnginePassword([]byte("another master, 32 bytes long..."), "default", "todo-db"))
	require.Len(t, a, 43, "base64url of 32 bytes, unpadded")
}

func TestEngines(t *testing.T) {
	e := sqlsvc.NewEngines()
	_, ok := e.Address("default", "todo-db")
	require.False(t, ok)
	e.Set("default", "todo-db", "10.0.0.2:4001")
	addr, ok := e.Address("default", "todo-db")
	require.True(t, ok)
	require.Equal(t, "10.0.0.2:4001", addr)
	_, ok = e.Address("team", "todo-db")
	require.False(t, ok, "keyed by namespace too")
	e.Delete("default", "todo-db")
	_, ok = e.Address("default", "todo-db")
	require.False(t, ok)
}

// fakeRuntime is a provider.Runtime with provider.Volumes; ready decides Converge's answer.
type fakeRuntime struct {
	mu        sync.Mutex
	ready     bool
	addr      string
	teardowns int
	dirs      map[provider.ProviderRef]bool
	reclaims  int
}

func (f *fakeRuntime) Converge(_ context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs[spec.Ref] = true
	return provider.ProviderStatus{Ready: f.ready, Address: f.addr, Reason: "Starting"}, nil
}
func (f *fakeRuntime) Teardown(context.Context, provider.ProviderRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teardowns++
	return nil
}
func (f *fakeRuntime) Reclaim(_ context.Context, ref provider.ProviderRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.dirs, ref)
	f.reclaims++
	return nil
}
func (f *fakeRuntime) List(context.Context) ([]provider.ProviderRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []provider.ProviderRef
	for r := range f.dirs {
		out = append(out, r)
	}
	return out, nil
}

// plainRuntime offers no durable storage.
type plainRuntime struct{}

func (plainRuntime) Converge(context.Context, provider.ProviderSpec) (provider.ProviderStatus, error) {
	return provider.ProviderStatus{}, nil
}
func (plainRuntime) Teardown(context.Context, provider.ProviderRef) error { return nil }

type gate struct{ held bool }

func (g gate) Held() bool          { return g.held }
func (gate) ReleasedAt() time.Time { return time.Time{} }

// todoDB is the database every reconciler test works on.
func todoDB() provider.ProviderRef {
	return provider.ProviderRef{Namespace: "default", Name: "todo-db"}
}

func setup(t *testing.T, rt *fakeRuntime, held bool) (*sqlsvc.Reconciler, store.Store) {
	t.Helper()
	st := store.New(memory.New())
	r, err := sqlsvc.NewReconciler(sqlsvc.ReconcilerDeps{Store: st, Provider: rt, Engines: sqlsvc.NewEngines(), Master: []byte(masterKey), Hold: gate{held}})
	require.NoError(t, err)
	return r, st
}

func newRuntime(dirs ...provider.ProviderRef) *fakeRuntime {
	f := &fakeRuntime{dirs: map[provider.ProviderRef]bool{}, addr: "10.0.0.2:4001"}
	for _, d := range dirs {
		f.dirs[d] = true
	}
	return f
}

func create(t *testing.T, st store.Store, memoryBound string) *v1.SQLDatabase {
	t.Helper()
	d := &v1.SQLDatabase{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSQLDatabase.GVK().APIVersion(), Kind: v1.KindSQLDatabase}}
	d.Name, d.Namespace, d.ResourceGroup = todoDB().Name, todoDB().Namespace, "rg1"
	d.Spec.Resources.Memory = memoryBound
	obj, err := st.Create(context.Background(), d)
	require.NoError(t, err)
	return obj.(*v1.SQLDatabase)
}

func reconcile(t *testing.T, r *sqlsvc.Reconciler) controller.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), controller.Request{GVK: v1.KindSQLDatabase.GVK(), Namespace: todoDB().Namespace, Name: todoDB().Name})
	require.NoError(t, err)
	return res
}

func get(t *testing.T, st store.Store) *v1.SQLDatabase {
	t.Helper()
	obj, err := st.Get(context.Background(), v1.KindSQLDatabase.GVK(), todoDB().Namespace, todoDB().Name)
	require.NoError(t, err)
	return obj.(*v1.SQLDatabase)
}

func TestNewReconcilerNeedsVolumes(t *testing.T) {
	_, err := sqlsvc.NewReconciler(sqlsvc.ReconcilerDeps{Store: store.New(memory.New()), Provider: plainRuntime{}, Engines: sqlsvc.NewEngines(), Master: []byte(masterKey)})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

func TestReconcileStartingThenReady(t *testing.T) {
	rt := newRuntime()
	r, st := setup(t, rt, false)
	create(t, st, "")
	res := reconcile(t, r)
	require.Equal(t, 2*time.Second, res.RequeueAfter, "a starting engine is polled")
	d := get(t, st)
	c, _ := d.Status.Conditions.Get("Ready")
	require.Equal(t, v1.ConditionFalse, c.Status)
	require.Equal(t, "Starting", c.Reason)
	_, ok := r.Engines().Address(todoDB().Namespace, todoDB().Name)
	require.False(t, ok)

	rt.ready = true
	res = reconcile(t, r)
	require.Equal(t, controller.SupervisionPeriod, res.RequeueAfter)
	require.Equal(t, v1.PhaseReady, get(t, st).Status.Phase)
	addr, ok := r.Engines().Address(todoDB().Namespace, todoDB().Name)
	require.True(t, ok)
	require.Equal(t, "10.0.0.2:4001", addr)
	v := get(t, st).ResourceVersion
	reconcile(t, r)
	require.Equal(t, v, get(t, st).ResourceVersion, "an unchanged status is not written")
}

func TestGenerationChangeTearsDownOnce(t *testing.T) {
	rt := newRuntime()
	r, st := setup(t, rt, false)
	create(t, st, "")
	reconcile(t, r)
	d := get(t, st)
	d.Spec.Resources.Memory = "512Mi"
	_, err := st.Update(context.Background(), d)
	require.NoError(t, err)
	reconcile(t, r)
	reconcile(t, r)
	require.Equal(t, 1, rt.teardowns, "the engine never got Ready, and still only one teardown")
	require.Equal(t, get(t, st).Generation, get(t, st).Status.ObservedGeneration)
}

func TestDeleteReclaimsUnlessHeld(t *testing.T) {
	for _, held := range []bool{false, true} {
		rt := newRuntime()
		rt.ready = true
		r, st := setup(t, rt, held)
		create(t, st, "")
		reconcile(t, r)
		require.NoError(t, st.Delete(context.Background(), v1.KindSQLDatabase.GVK(), todoDB().Namespace, todoDB().Name, ""))
		reconcile(t, r)
		require.Equal(t, 1, rt.teardowns, "held=%v", held)
		require.Equal(t, !held, rt.reclaims == 1, "held=%v: reclaimed only when not held", held)
		_, ok := r.Engines().Address(todoDB().Namespace, todoDB().Name)
		require.False(t, ok)
	}
}

func TestReclaimOrphans(t *testing.T) {
	kept := provider.ProviderRef{Namespace: "default", Name: "kept-db"}
	rt := newRuntime(todoDB(), kept)
	r, st := setup(t, rt, true)
	d := &v1.SQLDatabase{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSQLDatabase.GVK().APIVersion(), Kind: v1.KindSQLDatabase}}
	d.Name, d.Namespace, d.ResourceGroup = kept.Name, kept.Namespace, "rg1"
	_, err := st.Create(context.Background(), d)
	require.NoError(t, err)

	orphans, err := r.Orphans(context.Background())
	require.NoError(t, err)
	require.Equal(t, []v1.ObjectRef{{Kind: v1.KindSQLDatabase, Namespace: "default", Name: "todo-db"}}, orphans)
	require.NoError(t, r.ReclaimOrphans(context.Background()))
	require.Zero(t, rt.reclaims, "a no-op while held")

	r2, err := sqlsvc.NewReconciler(sqlsvc.ReconcilerDeps{Store: st, Provider: rt, Engines: sqlsvc.NewEngines(), Master: []byte(masterKey)})
	require.NoError(t, err)
	require.NoError(t, r2.ReclaimOrphans(context.Background()))
	require.Equal(t, 1, rt.reclaims)
	require.False(t, rt.dirs[todoDB()])
	require.True(t, rt.dirs[kept], "a database with an object keeps its directory")
}

func TestProberHasTables(t *testing.T) {
	tables := 0
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		user, pass, _ := req.BasicAuth()
		if user != "funcd" || pass != sqlsvc.EnginePassword([]byte(masterKey), todoDB().Namespace, todoDB().Name) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		require.Equal(t, "/db/query", req.URL.Path)
		_, _ = fmt.Fprintf(w, `{"results":[{"columns":["count(*)"],"types":["integer"],"values":[[%d]]}]}`, tables)
	}))
	t.Cleanup(engine.Close)
	engines := sqlsvc.NewEngines()
	rt := newRuntime()
	p := sqlsvc.Prober{Engines: engines, Master: []byte(masterKey), Volumes: rt}
	ctx := context.Background()

	has, err := p.HasTables(ctx, todoDB().Namespace, todoDB().Name)
	require.NoError(t, err, "no engine, no directory: nothing was written")
	require.False(t, has)

	rt.dirs[todoDB()] = true
	_, err = p.HasTables(ctx, todoDB().Namespace, todoDB().Name)
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "a directory the engine cannot be asked about fails closed")

	engines.Set(todoDB().Namespace, todoDB().Name, strings.TrimPrefix(engine.URL, "http://"))
	has, err = p.HasTables(ctx, todoDB().Namespace, todoDB().Name)
	require.NoError(t, err)
	require.False(t, has)
	tables = 1
	has, err = p.HasTables(ctx, todoDB().Namespace, todoDB().Name)
	require.NoError(t, err)
	require.True(t, has)

	_, err = sqlsvc.Prober{Engines: engines, Master: []byte("wrong master, also thirty-two by"), Volumes: rt}.HasTables(ctx, todoDB().Namespace, todoDB().Name)
	require.Equal(t, fault.Internal, fault.KindOf(err), "a refused credential is funcd's fault")
	require.NotContains(t, err.Error(), sqlsvc.EnginePassword([]byte(masterKey), todoDB().Namespace, todoDB().Name))
}

// ADR-0233 Decision 1: status.bindings counts the Functions whose spec.sql names the database.
func TestReconcileCountsBindings(t *testing.T) {
	rt := newRuntime()
	rt.ready = true
	r, st := setup(t, rt, false)
	create(t, st, "")
	for _, name := range []v1.ObjectName{"todo-api", "todo-report", "other"} {
		fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
		fn.Name, fn.Namespace, fn.ResourceGroup = name, "default", "rg1"
		fn.Spec = v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: "oci-layout://x:1"}
		if name != "other" {
			fn.Spec.SQL = []v1.FunctionSQL{{Alias: "app", Database: todoDB().Name}}
		}
		_, err := st.Create(context.Background(), fn)
		require.NoError(t, err)
	}
	reconcile(t, r)
	require.Equal(t, 2, get(t, st).Status.Bindings)
	require.Len(t, r.MapFunction(context.Background(), &v1.Function{ObjectMeta: v1.ObjectMeta{Namespace: "default"}}), 1)
}

// ADR-0234 Decision 6: without durable storage a missing directory is no lost data; with it, a database that was Ready
// and lost its directory reports DataMissing on the first pass.
func TestDataMissingNeedsDurableStorage(t *testing.T) {
	for _, durable := range []bool{false, true} {
		rt := newRuntime()
		rt.ready = true
		st := store.New(memory.New())
		d := create(t, st, "")
		d.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionTrue})
		_, err := st.Update(context.Background(), d)
		require.NoError(t, err)
		r, err := sqlsvc.NewReconciler(sqlsvc.ReconcilerDeps{Store: st, Provider: rt, Engines: sqlsvc.NewEngines(), Master: []byte(masterKey), DurableStorage: durable})
		require.NoError(t, err)
		reconcile(t, r)
		c, ok := get(t, st).Status.Conditions.Get("DataMissing")
		require.Equal(t, durable, ok && c.Status == v1.ConditionTrue, "durable=%v", durable)
		reconcile(t, r)
		c, _ = get(t, st).Status.Conditions.Get("DataMissing")
		require.Equal(t, durable, c.Status == v1.ConditionTrue, "a later pass keeps it")
	}
}

// conflictOnce is a store whose first status write is lost to a Conflict.
type conflictOnce struct {
	store.Store
	failed bool
}

func (c *conflictOnce) Update(ctx context.Context, obj v1.Object) (v1.Object, error) {
	if !c.failed {
		c.failed = true
		return nil, fault.Conflictf("test", "lost to another write")
	}
	return c.Store.Update(ctx, obj)
}

// A DataMissing found on a pass whose status write is lost is written by the next pass, though that pass's engine
// directory exists again.
func TestDataMissingSurvivesALostWrite(t *testing.T) {
	rt := newRuntime()
	rt.ready = true
	base := store.New(memory.New())
	d := create(t, base, "")
	d.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionTrue})
	_, err := base.Update(context.Background(), d)
	require.NoError(t, err)
	st := &conflictOnce{Store: base}
	r, err := sqlsvc.NewReconciler(sqlsvc.ReconcilerDeps{Store: st, Provider: rt, Engines: sqlsvc.NewEngines(), Master: []byte(masterKey), DurableStorage: true})
	require.NoError(t, err)
	reconcile(t, r)
	_, ok := get(t, base).Status.Conditions.Get("DataMissing")
	require.False(t, ok, "the first write was lost")
	require.True(t, rt.dirs[todoDB()], "and the pass recreated the directory")
	reconcile(t, r)
	c, ok := get(t, base).Status.Conditions.Get("DataMissing")
	require.True(t, ok && c.Status == v1.ConditionTrue, "the next pass records it")
}

// A database deleted while its DataMissing was pending and created again under the same name reports nothing missing.
func TestDataMissingForgottenOnDelete(t *testing.T) {
	rt := newRuntime()
	rt.ready = true
	base := store.New(memory.New())
	d := create(t, base, "")
	d.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionTrue})
	_, err := base.Update(context.Background(), d)
	require.NoError(t, err)
	r, err := sqlsvc.NewReconciler(sqlsvc.ReconcilerDeps{Store: &conflictOnce{Store: base}, Provider: rt, Engines: sqlsvc.NewEngines(),
		Master: []byte(masterKey), DurableStorage: true})
	require.NoError(t, err)
	reconcile(t, r)
	require.NoError(t, base.Delete(context.Background(), v1.KindSQLDatabase.GVK(), todoDB().Namespace, todoDB().Name, ""))
	reconcile(t, r)
	create(t, base, "")
	reconcile(t, r)
	_, ok := get(t, base).Status.Conditions.Get("DataMissing")
	require.False(t, ok, "the new database lost nothing")
}

// A pass whose status write is lost to a concurrent write runs again at once, not a supervision period later: the
// change it computed, such as a cleared DataMissing, would otherwise wait that long.
func TestStatusWriteLostToConflictRequeuesAtOnce(t *testing.T) {
	rt := newRuntime(todoDB())
	rt.ready = true
	base := store.New(memory.New())
	create(t, base, "")
	r, err := sqlsvc.NewReconciler(sqlsvc.ReconcilerDeps{Store: &conflictOnce{Store: base}, Provider: rt, Engines: sqlsvc.NewEngines(), Master: []byte(masterKey)})
	require.NoError(t, err)
	require.Equal(t, controller.Result{Requeue: true}, reconcile(t, r), "the lost write is retried at once")
	require.Equal(t, controller.Result{RequeueAfter: controller.SupervisionPeriod}, reconcile(t, r), "then the steady supervision")
	_, ok := get(t, base).Status.Conditions.Get("Ready")
	require.True(t, ok)
}

// A Succeeded Restore created in the millisecond DataMissing turned True clears it; one created a millisecond before
// does not. Both times are the store's, in milliseconds.
func TestRestoreOfTheTransitionMillisecondClearsDataMissing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		before  time.Duration
		cleared bool
	}{
		{"same millisecond", 0, true},
		{"a millisecond before", time.Millisecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newRuntime()
			rt.ready = true
			st := store.New(memory.New())
			d := create(t, st, "")
			d.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionTrue})
			_, err := st.Update(context.Background(), d)
			require.NoError(t, err)
			r, err := sqlsvc.NewReconciler(sqlsvc.ReconcilerDeps{Store: st, Provider: rt, Engines: sqlsvc.NewEngines(), Master: []byte(masterKey), DurableStorage: true})
			require.NoError(t, err)
			reconcile(t, r)

			rs := &v1.Restore{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRestore.GVK().APIVersion(), Kind: v1.KindRestore}}
			rs.Name, rs.Namespace, rs.ResourceGroup = "back", todoDB().Namespace, "rg1"
			rs.Spec = v1.RestoreSpec{Backup: "b1", Source: v1.RestoreSource{CredentialsSecret: "reader", IdentitySecret: "owner"},
				Members: []v1.RestoreMember{{Kind: v1.KindSQLDatabase, Name: todoDB().Name, InPlace: true}}}
			rs.Status.Phase = v1.RunSucceeded
			obj, err := st.Create(context.Background(), rs)
			require.NoError(t, err)
			d = get(t, st)
			i := slices.IndexFunc(d.Status.Conditions, func(c v1.Condition) bool { return c.Type == "DataMissing" })
			require.NotEqual(t, -1, i)
			require.Equal(t, v1.ConditionTrue, d.Status.Conditions[i].Status)
			d.Status.Conditions[i].LastTransitionTime = v1.NewTimestamp(time.Time(obj.GetObjectMeta().CreationTime).Add(tc.before))
			_, err = st.Update(context.Background(), d)
			require.NoError(t, err)

			reconcile(t, r)
			c, _ := get(t, st).Status.Conditions.Get("DataMissing")
			require.Equal(t, tc.cleared, c.Status == v1.ConditionFalse, "DataMissing cleared")
		})
	}
}
