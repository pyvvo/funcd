package lake_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controller"
	lakegw "github.com/pyvvo/funcd/internal/lake/gateway"
	"github.com/pyvvo/funcd/internal/provider"
	lakesvc "github.com/pyvvo/funcd/internal/services/lake"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/poll"
)

func readyStatus() provider.ProviderStatus {
	return provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080", Report: "saved 2026-11-03T22:58:20+00:00"}
}

func stoppingLake(t *testing.T) store.Store {
	t.Helper()
	st := store.New(storemem.New())
	seedLakeBucket(t, st)
	_, err := st.Create(context.Background(), mkLake("lake"))
	require.NoError(t, err)
	return st
}

func storedLake(t *testing.T, st store.Store) *v1.Lake {
	t.Helper()
	obj, err := st.Get(context.Background(), v1.KindLake.GVK(), "default", "lake")
	require.NoError(t, err)
	return obj.(*v1.Lake)
}

func readyReason(t *testing.T, cs *v1.Lake) string {
	t.Helper()
	c, ok := cs.Status.Conditions.Get("Ready")
	require.True(t, ok)
	return c.Reason
}

type engineStop struct {
	release func()
	err     error
}

// stopInBackground runs StopEngine on default/lake and reports on the channel it returns.
func stopInBackground(r *lakesvc.Reconciler) <-chan engineStop {
	stopped := make(chan engineStop, 1)
	go func() {
		release, err := r.StopEngine(context.Background(), "default", "lake")
		stopped <- engineStop{release, err}
	}()
	return stopped
}

func stillStopping(stopped <-chan engineStop) bool {
	return poll.Never(func() bool { return len(stopped) > 0 }, 100*time.Millisecond, 5*time.Millisecond)
}

// requireSuspended posts to the Lake proxy at url, which answers as Suspend set it to, and not by what is behind it.
func requireSuspended(t *testing.T, url string) {
	t.Helper()
	resp, err := http.Post(url+"/quack", "application/octet-stream", strings.NewReader("q"))
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Contains(t, string(body), "lake not ready")
}

// hookedUpdate is a store that runs before ahead of each Update, which may answer it. A test sets before while no pass runs.
type hookedUpdate struct {
	store.Store
	before func() error
}

func (s *hookedUpdate) Update(ctx context.Context, o v1.Object) (v1.Object, error) {
	if s.before != nil {
		if err := s.before(); err != nil {
			return nil, err
		}
	}
	return s.Store.Update(ctx, o)
}

// TestIssue1384_StopEngineKeepsTheLakeEngineless: while a Restore loads a Lake no engine of it runs and no pass starts
// one, its proxy answers 503 and CatalogSaved says nothing of a catalog the load replaces; once the Restore releases it,
// the next pass starts an engine, which reads the copy, on the same proxy URL.
// oracle: docs/design/lake/restore-stop.fizz#NoStaleEngineOverTheCopy
// oracle: docs/design/lake/restore-stop.fizz#AnEngineComesBack
func TestIssue1384_StopEngineKeepsTheLakeEngineless(t *testing.T) {
	t.Parallel()
	st := stoppingLake(t)
	prov := &fakeProvider{status: readyStatus()}
	mgr := lakegw.NewManager("", lakegw.NewLakeKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) { d.Proxy = mgr })
	reconcileOnce(t, r, "lake")
	before := storedLake(t, st)
	require.Equal(t, v1.PhaseReady, before.Status.Phase)
	_, saved := before.Status.Conditions.Get("CatalogSaved")
	require.True(t, saved)
	starts := len(prov.converged)

	release, err := r.StopEngine(context.Background(), "default", "lake")
	require.NoError(t, err)
	require.Equal(t, []provider.ProviderRef{{Namespace: "default", Name: "lake"}}, prov.tornDown, "StopEngine returns once no engine runs")
	got := storedLake(t, st)
	require.Equal(t, v1.PhasePending, got.Status.Phase)
	require.Equal(t, "Restoring", readyReason(t, got))
	require.Empty(t, got.Status.Endpoint)
	_, saved = got.Status.Conditions.Get("CatalogSaved")
	require.False(t, saved, "a fresh engine reports nothing, so no old save outlives the catalog it spoke of")
	requireSuspended(t, "http://"+before.Status.Endpoint)

	reconcileOnce(t, r, "lake")
	reconcileOnce(t, r, "lake")
	require.Len(t, prov.converged, starts, "no pass starts an engine while the Lake loads")
	require.Len(t, prov.tornDown, 1)

	release()
	release()
	prov.status.Report = ""
	reconcileOnce(t, r, "lake")
	require.Len(t, prov.converged, starts+1, "the next pass starts an engine again")
	got = storedLake(t, st)
	require.Equal(t, v1.PhaseReady, got.Status.Phase)
	require.Equal(t, before.Status.Endpoint, got.Status.Endpoint, "the URL consumers hold stays")
	_, saved = got.Status.Conditions.Get("CatalogSaved")
	require.False(t, saved, "nothing says saved until the new engine saves")
	prov.status.Report = readyStatus().Report
	reconcileOnce(t, r, "lake")
	c, saved := storedLake(t, st).Status.Conditions.Get("CatalogSaved")
	require.True(t, saved)
	require.Equal(t, v1.ConditionTrue, c.Status)
}

// gatedProxy is a Lake proxy manager whose Ensure tells entered and waits for proceed before it retargets the proxy.
type gatedProxy struct {
	*lakegw.Manager
	entered, proceed chan struct{}
}

func (g *gatedProxy) Ensure(lake auth.EntityRef, upstream, engineToken, saveURL string) (string, error) {
	close(g.entered)
	<-g.proceed
	return g.Manager.Ensure(lake, upstream, engineToken, saveURL)
}

// TestIssue1384_StopEngineWaitsForAPassCreatingAnEngine: a pass that began before the Restore asked ends first: past its
// Converge it still has the proxy's Ensure and its status write to do, and a late Ensure would lift the suspension and point
// the proxy at a stopped engine. StopEngine then stops the engine that pass started.
// oracle: docs/design/lake/restore-stop.fizz#TheProxyStaysSuspended
func TestIssue1384_StopEngineWaitsForAPassCreatingAnEngine(t *testing.T) {
	t.Parallel()
	st := stoppingLake(t)
	prov := &fakeProvider{status: readyStatus()}
	mgr := lakegw.NewManager("", lakegw.NewLakeKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	proxy := &gatedProxy{Manager: mgr, entered: make(chan struct{}), proceed: make(chan struct{})}
	r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) { d.Proxy = proxy })
	passed := make(chan error, 1)
	go func() {
		_, err := r.Reconcile(context.Background(), controller.Request{GVK: v1.KindLake.GVK(), Namespace: "default", Name: "lake"})
		passed <- err
	}()
	select {
	case <-proxy.entered:
	case err := <-passed:
		t.Fatalf("the pass ended without reaching Ensure: %v", err)
	}
	require.Len(t, prov.converged, 1, "the pass is past its Converge")
	stopped := stopInBackground(r)
	require.True(t, stillStopping(stopped), "StopEngine returned while a pass was running")
	close(proxy.proceed)
	require.NoError(t, <-passed)
	s := <-stopped
	require.NoError(t, s.err)
	defer s.release()
	require.Len(t, prov.tornDown, 1, "the engine that pass started is stopped too")
	got := storedLake(t, st)
	require.Equal(t, v1.PhasePending, got.Status.Phase, "the pass's Ready does not outlive the stop")
	require.Equal(t, "Restoring", readyReason(t, got))
	url, ok := mgr.ProxyURL("default", "lake")
	require.True(t, ok)
	requireSuspended(t, "http://"+url)
}

// slowStop is a fakeProvider whose Teardown tells stopping and waits for proceed: a stop under the hold takes its grace.
type slowStop struct {
	fakeProvider
	stopping, proceed chan struct{}
}

func (s *slowStop) Teardown(ctx context.Context, ref provider.ProviderRef) error {
	close(s.stopping)
	<-s.proceed
	return s.fakeProvider.Teardown(ctx, ref)
}

// TestIssue1384_StopEngineReturnsOnceTheEngineIsGone: a load that began while the engine stopped would lose to the engine's
// next retried save, so StopEngine waits for the stop, and a status write lost to a Conflict goes again. A pass that comes
// meanwhile stops nothing.
// oracle: docs/design/lake/restore-stop.fizz#NoStaleEngineOverTheCopy
func TestIssue1384_StopEngineReturnsOnceTheEngineIsGone(t *testing.T) {
	t.Parallel()
	st := &hookedUpdate{Store: stoppingLake(t)}
	prov := &slowStop{fakeProvider: fakeProvider{status: readyStatus()}, stopping: make(chan struct{}), proceed: make(chan struct{})}
	r := newReconciler(t, st, prov, nil)
	reconcileOnce(t, r, "lake")
	var writes atomic.Int32
	st.before = func() error {
		if writes.Add(1) == 1 {
			return fault.Conflictf("test", "an apply changed the Lake")
		}
		return nil
	}
	stopped := stopInBackground(r)
	select {
	case <-prov.stopping:
	case s := <-stopped:
		t.Fatalf("StopEngine returned and stopped nothing: %v", s.err)
	}
	require.EqualValues(t, 2, writes.Load())
	require.Equal(t, "Restoring", readyReason(t, storedLake(t, st)))
	require.True(t, stillStopping(stopped), "StopEngine returned while its engine was stopping")
	reconcileOnce(t, r, "lake")
	close(prov.proceed)
	s := <-stopped
	require.NoError(t, s.err)
	require.Len(t, prov.tornDown, 1, "by the stop alone")
	s.release()
}

// The mark and the suspended proxy hold the Lake, so a status write that never lands keeps no engine up.
func TestIssue1384_StopEngineStopsTheEngineWhateverItsStatusWriteAnswers(t *testing.T) {
	t.Parallel()
	for name, refusal := range map[string]error{
		"every write lost to a Conflict": fault.Conflictf("test", "an apply changed the Lake"),
		"the store refuses":              fault.Unavailablef("test", "the store refused"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			st := &hookedUpdate{Store: stoppingLake(t)}
			prov := &fakeProvider{status: readyStatus()}
			r := newReconciler(t, st, prov, nil)
			reconcileOnce(t, r, "lake")
			st.before = func() error { return refusal }
			release, err := r.StopEngine(context.Background(), "default", "lake")
			require.NoError(t, err)
			defer release()
			require.Len(t, prov.tornDown, 1)
		})
	}
}

// TestIssue1384_StopEngineKeepsTheEdgeEntry: holdNotReady clears a Lake's edge entry, and with none its external path would
// answer 404. StopEngine and a marked pass leave it: under the hold ADR-0290's 503 answers there, and after the lift the
// suspended proxy behind it.
func TestIssue1384_StopEngineKeepsTheEdgeEntry(t *testing.T) {
	t.Parallel()
	st := store.New(storemem.New())
	seedLakeBucket(t, st)
	cs := mkLake("lake")
	cs.Spec.Ingress = &v1.Ingress{Enabled: new(true), Path: "/lake/lake"}
	_, err := st.Create(context.Background(), cs)
	require.NoError(t, err)
	mgr := lakegw.NewManager("", lakegw.NewLakeKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	routes := newRecordingRoutes()
	r := newReconciler(t, st, &fakeProvider{status: readyStatus()}, func(d *lakesvc.ReconcilerDeps) {
		d.Proxy = mgr
		d.Routes = routes
	})
	reconcileOnce(t, r, "lake")
	require.Len(t, routes.get(lakeRouteSource), 1)

	release, err := r.StopEngine(context.Background(), "default", "lake")
	require.NoError(t, err)
	defer release()
	reconcileOnce(t, r, "lake")
	entries := routes.get(lakeRouteSource)
	require.Len(t, entries, 1, "the entry stays")
	requireSuspended(t, entries[0].Rules[0].Upstream)
}

// A pass while a Lake is stopped only requeues: it touches no engine and no status, and leaves a deleted Lake to the pass
// after the release.
func TestIssue1384_AMarkedPassOnlyRequeues(t *testing.T) {
	t.Parallel()
	st := stoppingLake(t)
	prov := &fakeProvider{status: readyStatus()}
	r := newReconciler(t, st, prov, nil)
	reconcileOnce(t, r, "lake")
	starts := len(prov.converged)
	release, err := r.StopEngine(context.Background(), "default", "lake")
	require.NoError(t, err)
	defer release()

	before := storedLake(t, st)
	res, err := r.Reconcile(context.Background(), controller.Request{GVK: v1.KindLake.GVK(), Namespace: "default", Name: "lake"})
	require.NoError(t, err)
	require.NotZero(t, res.RequeueAfter, "it comes back, or no engine would after the release")
	require.Equal(t, before, storedLake(t, st), "it writes nothing")
	require.Len(t, prov.converged, starts)
	require.Len(t, prov.tornDown, 1, "only the stop tears the engine down")

	require.NoError(t, st.Delete(context.Background(), v1.KindLake.GVK(), "default", "lake", ""))
	reconcileOnce(t, r, "lake")
	require.Len(t, prov.tornDown, 1)
	release()
	reconcileOnce(t, r, "lake")
	require.Len(t, prov.tornDown, 2, "the delete's path runs once the Lake is released")
}

// failingStop is a fakeProvider whose Teardown fails.
type failingStop struct{ fakeProvider }

func (*failingStop) Teardown(context.Context, provider.ProviderRef) error {
	return fault.Unavailablef("test", "the engine would not stop")
}

// A stop that fails releases the mark, so the Lake is not left engineless with no Restore to release it.
func TestIssue1384_AFailedStopReleasesTheLake(t *testing.T) {
	t.Parallel()
	st := stoppingLake(t)
	prov := &failingStop{fakeProvider{status: readyStatus()}}
	r := newReconciler(t, st, prov, nil)
	reconcileOnce(t, r, "lake")
	starts := len(prov.converged)
	_, err := r.StopEngine(context.Background(), "default", "lake")
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "%v", err)
	reconcileOnce(t, r, "lake")
	require.Len(t, prov.converged, starts+1)
}

// A Lake deleted between a Restore's plan and its stop has its engine stopped all the same.
func TestIssue1384_StopEngineOfADeletedLakeStopsItsEngine(t *testing.T) {
	t.Parallel()
	st := stoppingLake(t)
	prov := &fakeProvider{status: readyStatus()}
	r := newReconciler(t, st, prov, nil)
	reconcileOnce(t, r, "lake")
	require.NoError(t, st.Delete(context.Background(), v1.KindLake.GVK(), "default", "lake", ""))
	release, err := r.StopEngine(context.Background(), "default", "lake")
	require.NoError(t, err)
	defer release()
	require.Len(t, prov.tornDown, 1)
}
