package local_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// fakeDeps is a DependencyChecker: it answers reports[caller] and records each caller; hang blocks until released.
type fakeDeps struct {
	mu      sync.Mutex
	reports map[local.Ref]*local.DependencyReport
	callers []local.Ref
	hang    chan struct{}
}

func (d *fakeDeps) Check(_ context.Context, caller local.Ref) *local.DependencyReport {
	d.mu.Lock()
	d.callers = append(d.callers, caller)
	rep, hang := d.reports[caller], d.hang
	d.mu.Unlock()
	if hang != nil {
		<-hang
	}
	return rep
}

func (d *fakeDeps) seen() []local.Ref {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.callers
}

func depsHandler(caller local.Ref, deps local.DependencyChecker) http.Handler {
	return local.NewHandler(caller, &fakeResolver{}, fakeInvoker{}, nil, nil, nil, deps, slog.New(slog.DiscardHandler))
}

// GET /health/dependencies answers the fixed caller's check: 200 when it passes, 503 with the report as JSON when it
// fails, 503 Timeout past DependencyCheckBudget; with no checker the route does not exist, so a shim reads 404 as a pass.
func TestDependencyEndpoint(t *testing.T) {
	t.Parallel()
	caller := local.Ref{Namespace: "team-a", Function: "todo-api"}
	audit := &local.DependencyReport{Kind: "kv", Binding: "audit", Reason: "Forbidden", Message: "kv::read denied"}

	require.Equal(t, http.StatusNotFound, do(t, depsHandler(caller, nil), http.MethodGet, "/health/dependencies", "").Code)

	deps := &fakeDeps{reports: map[local.Ref]*local.DependencyReport{}}
	h := depsHandler(caller, deps)
	rec := do(t, h, http.MethodGet, "/health/dependencies", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, rec.Body.String())
	require.Equal(t, []local.Ref{caller}, deps.seen(), "the socket's fixed caller, never one from the request")

	deps.reports[caller] = audit
	rec = do(t, h, http.MethodGet, "/health/dependencies", "")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var got local.DependencyReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, *audit, got)

	require.Equal(t, http.StatusMethodNotAllowed, do(t, h, http.MethodPost, "/health/dependencies", "{}").Code)

	hung := &fakeDeps{hang: make(chan struct{})}
	t.Cleanup(func() { close(hung.hang) })
	start := time.Now()
	rec = do(t, depsHandler(caller, hung), http.MethodGet, "/health/dependencies", "")
	require.Less(t, time.Since(start), local.DependencyCheckBudget+50*time.Millisecond, "answered within the budget")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "socket", got.Kind)
	require.Equal(t, "Timeout", got.Reason)
}

// On a pool's shared socket the check runs for the member named in X-Funcd-Member; a request naming none, or one not
// in the pool, is refused 403 before the checker is asked.
func TestPoolSocketDependencies(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "fl")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	deps := &fakeDeps{reports: map[local.Ref]*local.DependencyReport{
		{Namespace: "team-a", Function: "b"}: {Kind: "blob", Binding: "files", Reason: "StorageUnreachable", Message: "down"},
	}}
	rec := &poolCalls{}
	m := local.NewManager(dir, poolStore{rec}, poolInvoker{rec}, nil, nil, nil, deps, slog.New(slog.DiscardHandler))
	t.Cleanup(m.Close)
	sock, err := m.PoolSocketFor("team-a", testPool, []v1.ObjectName{"a", "b"})
	require.NoError(t, err)
	c := unixClient(t, sock)

	require.Equal(t, http.StatusForbidden, callAs(t, c, http.MethodGet, "/health/dependencies"))
	require.Equal(t, http.StatusForbidden, callAs(t, c, http.MethodGet, "/health/dependencies", "z"))
	require.Empty(t, deps.seen(), "a refused request asks no checker")

	require.Equal(t, http.StatusOK, callAs(t, c, http.MethodGet, "/health/dependencies", "a"))
	require.Equal(t, http.StatusServiceUnavailable, callAs(t, c, http.MethodGet, "/health/dependencies", "b"))
	require.Equal(t, []local.Ref{{Namespace: "team-a", Function: "a"}, {Namespace: "team-a", Function: "b"}}, deps.seen())
	require.Empty(t, rec.take(), "the check calls no worker, store or port through the local API")
}
