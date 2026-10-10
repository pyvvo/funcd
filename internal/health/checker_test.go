package health_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/health"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

const ns v1.NamespaceName = "default"

// readCheck is a binding Facade's CheckRead: it fails the aliases in errs and records every alias it checks.
type readCheck struct {
	mu    sync.Mutex
	errs  map[string]error
	calls []string
}

func (r *readCheck) CheckRead(_ context.Context, _ v1.NamespaceName, _ v1.ObjectName, alias string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, alias)
	return r.errs[alias]
}

// linkPDP allows link::invoke on every target but those in deny, and records the targets it was asked about.
type linkPDP struct {
	deny  map[v1.ObjectName]bool
	asked []v1.ObjectName
}

func (p *linkPDP) Authorize(_ context.Context, req auth.Request) (auth.Decision, error) {
	if req.Action != auth.ActionLinkInvoke {
		return auth.Decision{Allowed: false, Reason: "unexpected action " + string(req.Action)}, nil
	}
	p.asked = append(p.asked, req.Resource.Name)
	if p.deny[req.Resource.Name] {
		return auth.Decision{Allowed: false, Reason: "forbid policy"}, nil
	}
	return auth.Decision{Allowed: true}, nil
}

// noInvoke is an Invoker, the path to a worker and the activator, that fails the test when it is called.
type noInvoke struct{ t *testing.T }

func (n noInvoke) Invoke(context.Context, local.Ref, []byte, time.Duration) ([]byte, error) {
	n.t.Error("the dependency check must never call a worker or wake one")
	return nil, fault.Internalf("noInvoke", "called")
}

type fixture struct {
	t     *testing.T
	st    store.Store
	kv    *readCheck
	blob  *readCheck
	pdp   *linkPDP
	kvP   *switchProbe
	blobP *switchProbe
	prob  *health.Prober
	check local.DependencyChecker
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		t: t, st: store.New(memory.New()),
		kv: &readCheck{errs: map[string]error{}}, blob: &readCheck{errs: map[string]error{}},
		pdp: &linkPDP{deny: map[v1.ObjectName]bool{}}, kvP: &switchProbe{}, blobP: &switchProbe{},
	}
	f.prob = newProber(t, nil, time.Hour, time.Second, map[health.Target]health.Probe{
		health.TargetKV: f.kvP.probe, health.TargetBlob: f.blobP.probe,
	})
	f.check = health.NewChecker(health.CheckerDeps{
		Store: f.st, KV: f.kv, Blob: f.blob, Links: local.NewResolver(f.st), Authz: f.pdp, Health: f.prob,
	})
	return f
}

// fn stores Function name, changed by change, with phase.
func (f *fixture) fn(name v1.ObjectName, phase v1.Phase, change func(*v1.Function)) {
	f.t.Helper()
	fn := &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: ns, ResourceGroup: "rg1"},
		Spec:       v1.FunctionSpec{Runtime: "nodejs22", Handler: "index.handler", Image: "oci-layout://x:1"},
	}
	if change != nil {
		change(fn)
	}
	created, err := f.st.Create(context.Background(), fn)
	require.NoError(f.t, err)
	if phase != "" {
		c := created.(*v1.Function)
		c.Status.Phase = phase
		_, err = f.st.Update(context.Background(), c)
		require.NoError(f.t, err)
	}
}

func (f *fixture) checkOf(name v1.ObjectName) *local.DependencyReport {
	return f.check.Check(context.Background(), local.Ref{Namespace: ns, Function: name})
}

// api binds KV table store, blob prefix files and links mailer.
func api(fn *v1.Function) {
	fn.Spec.KV = []v1.FunctionKV{{Alias: "store", Store: "todo-store", Table: "todos"}, {Alias: "audit", Store: "todo-store", Table: "audit"}}
	fn.Spec.Blob = []v1.FunctionBlob{{Alias: "files", Bucket: "todo-files", Prefix: "attachments"}}
	fn.Spec.Links = []v1.FunctionLink{{Alias: "mail", Target: "mailer"}}
}

// Every binding passes: its resolve and read authorization, the storage probes and the link target, which is Idle.
func TestCheckerPassesWhenEveryBindingPasses(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.fn("todo-api", "", api)
	f.fn("mailer", v1.PhaseIdle, nil)
	require.Nil(t, f.checkOf("todo-api"))
	require.Equal(t, []string{"store", "audit"}, f.kv.calls)
	require.Equal(t, []string{"files"}, f.blob.calls)
	require.Equal(t, []v1.ObjectName{"mailer"}, f.pdp.asked)
	require.Nil(t, f.checkOf("ghost"), "a caller the store no longer holds has nothing to check")
}

// The check goes spec.kv, then spec.blob, then spec.links, and stops at the first failure.
func TestCheckerStopsAtTheFirstFailureInOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.fn("todo-api", "", api)
	f.kv.errs["audit"] = fault.Forbiddenf("services.kv.resolveAuth", "kv::read denied on todo-store/audit")
	f.blob.errs["files"] = fault.NotFoundf("services.blob.resolveAuth", "no bucket todo-files")

	require.Equal(t, &local.DependencyReport{Kind: "kv", Binding: "audit", Reason: "Forbidden", Message: "kv::read denied on todo-store/audit"},
		f.checkOf("todo-api"))
	require.Empty(t, f.blob.calls, "blob is not checked after a kv failure")

	delete(f.kv.errs, "audit")
	require.Equal(t, &local.DependencyReport{Kind: "blob", Binding: "files", Reason: "NotFound", Message: "no bucket todo-files"},
		f.checkOf("todo-api"))
	require.Empty(t, f.pdp.asked, "links are not checked after a blob failure")
}

// A binding whose storage probe fails reports StorageUnreachable with the probe's error; a Function with no binding on
// that storage passes.
func TestCheckerReportsUnreachableStorage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.fn("todo-api", "", api)
	f.fn("mailer", "", nil)
	f.fn("files-only", "", func(fn *v1.Function) {
		fn.Spec.Blob = []v1.FunctionBlob{{Alias: "files", Bucket: "todo-files", Prefix: "attachments"}}
	})
	f.kvP.fail(fault.Unavailablef("kvstore.badger", "the engine is closed"))
	health.ProbeAll(context.Background(), f.prob)

	require.Equal(t, &local.DependencyReport{Kind: "kv", Binding: "store", Reason: "StorageUnreachable", Message: "kvstore.badger: the engine is closed"},
		f.checkOf("todo-api"))
	require.Nil(t, f.checkOf("files-only"))

	f.kvP.pass()
	f.blobP.fail(fault.Unavailablef("blob", "unreachable"))
	health.ProbeAll(context.Background(), f.prob)
	rep := f.checkOf("todo-api")
	require.Equal(t, "blob", rep.Kind)
	require.Equal(t, "StorageUnreachable", rep.Reason)
}

// A link passes while its target exists and is not Failed, whatever else its phase, so mutual links never block each
// other; a missing target, a Failed one and a denied link::invoke fail.
func TestCheckerLinks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		target v1.Phase
		create bool
		deny   bool
		want   *local.DependencyReport
	}{
		{"idle target", v1.PhaseIdle, true, false, nil},
		{"deploying target", v1.PhaseDeploying, true, false, nil},
		{"degraded target", v1.PhaseDegraded, true, false, nil},
		{"never-booted target", "", true, false, nil},
		{"missing target", "", false, false, &local.DependencyReport{Kind: "link", Binding: "mail", Reason: "NotFound", Message: "link target default/mailer does not exist"}},
		{"failed target", v1.PhaseFailed, true, false, &local.DependencyReport{Kind: "link", Binding: "mail", Reason: "NotReady", Message: "link target default/mailer is Failed"}},
		{"denied link::invoke", v1.PhaseReady, true, true, &local.DependencyReport{Kind: "link", Binding: "mail", Reason: "Forbidden",
			Message: "caller default/todo-api is not authorized to invoke default/mailer: forbid policy"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.fn("todo-api", "", func(fn *v1.Function) { fn.Spec.Links = []v1.FunctionLink{{Alias: "mail", Target: "mailer"}} })
			if tc.create {
				f.fn("mailer", tc.target, nil)
			}
			f.pdp.deny["mailer"] = tc.deny
			require.Equal(t, tc.want, f.checkOf("todo-api"))
		})
	}
	t.Run("mutual links", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.fn("a", v1.PhaseDeploying, func(fn *v1.Function) { fn.Spec.Links = []v1.FunctionLink{{Alias: "b", Target: "b"}} })
		f.fn("b", v1.PhaseDeploying, func(fn *v1.Function) { fn.Spec.Links = []v1.FunctionLink{{Alias: "a", Target: "a"}} })
		require.Nil(t, f.checkOf("a"))
		require.Nil(t, f.checkOf("b"))
	})
}

// A binding reached once the check's budget is spent reports Timeout; a store error reports the socket Unreachable.
func TestCheckerBudgetAndStoreErrors(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.fn("todo-api", "", api)
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	rep := f.check.Check(ctx, local.Ref{Namespace: ns, Function: "todo-api"})
	require.Equal(t, "Timeout", rep.Reason)
	require.Contains(t, []string{"kv", "socket"}, rep.Kind)
	require.Empty(t, f.kv.calls, "no Facade call past the budget")

	broken := health.NewChecker(health.CheckerDeps{Store: failingStore{}})
	require.Equal(t, &local.DependencyReport{Kind: "socket", Reason: "Unreachable", Message: "the metastore is closed"},
		broken.Check(context.Background(), local.Ref{Namespace: ns, Function: "todo-api"}))
}

type failingStore struct{}

func (failingStore) Get(context.Context, v1.GroupVersionKind, v1.NamespaceName, v1.ObjectName) (v1.Object, error) {
	return nil, fault.Unavailablef("store.Get", "the metastore is closed")
}

// GET /health/dependencies on a sandbox's local API answers from the checker within DependencyCheckBudget, never
// calling a worker: 200 while every binding passes, else 503 with the report.
func TestDependencyEndpointNeverCallsAWorker(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.fn("todo-api", "", api)
	f.fn("mailer", v1.PhaseIdle, nil)
	h := local.NewHandler(local.Ref{Namespace: ns, Function: "todo-api"}, local.NewResolver(f.st), noInvoke{t}, f.pdp, nil, nil,
		f.check, slog.New(slog.DiscardHandler))

	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		start := time.Now()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/dependencies", http.NoBody))
		require.Less(t, time.Since(start), local.DependencyCheckBudget+50*time.Millisecond)
		return rec
	}
	require.Equal(t, http.StatusOK, get().Code)

	f.kv.errs["audit"] = fault.Forbiddenf("services.kv.resolveAuth", "kv::read denied")
	rec := get()
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var rep local.DependencyReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rep))
	require.Equal(t, local.DependencyReport{Kind: "kv", Binding: "audit", Reason: "Forbidden", Message: "kv::read denied"}, rep)
}
