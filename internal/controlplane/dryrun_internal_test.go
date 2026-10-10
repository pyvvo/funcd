package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// appNamespaceReader is an App admission that reads the namespace (ADR-0147), as app-parts does.
type appNamespaceReader struct{}

func (appNamespaceReader) Name() string           { return "app-namespace-reader" }
func (appNamespaceReader) Phase() admission.Phase { return admission.Validating }
func (appNamespaceReader) ReadsNamespace() bool   { return true }
func (appNamespaceReader) Handles(gvk v1.GroupVersionKind, _ admission.Operation) bool {
	return gvk.Kind == v1.KindApp
}
func (appNamespaceReader) Admit(_ context.Context, r admission.Request) (v1.Object, error) {
	return r.Object, nil
}

type grantAll struct{}

func (grantAll) Authorize(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{Allowed: true}, nil
}

// lockProbe is a planner that reports whether the namespace lock was free while it planned.
type lockProbe struct {
	locks *nsLocks
	calls atomic.Int64
	free  atomic.Bool
}

func (p *lockProbe) PlanApp(ctx context.Context, _ *v1.App) (v1.AppPlan, error) {
	p.calls.Add(1)
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if unlock, err := p.locks.lock(ctx, "team-a"); err == nil {
		p.free.Store(true)
		unlock()
	}
	return v1.AppPlan{Revision: "todo-1"}, nil
}

// ADR-0220 Decision 4: a dry run of a marked kind waits for the namespace lock as the real write does, and the App
// plan runs once the lock is released.
func TestDryRunWaitsForTheLockAndPlansAfter(t *testing.T) {
	st := store.New(memory.New())
	t.Cleanup(func() { _ = st.Close() })
	probe := &lockProbe{}
	h := NewStoreHandlers(st, grantAll{}, admission.NewPipeline(appNamespaceReader{}), nil, probe).(*storeHandlers)
	probe.locks = h.locks
	r := chi.NewRouter()
	r.Use(middleware.Authn(middleware.NewStaticCredentials(map[string]auth.Identity{"t": {Subject: "dev"}})))
	NewAPI(r, h)

	unlock, err := h.locks.lock(context.Background(), "team-a")
	require.NoError(t, err)
	body, err := json.Marshal(&v1.App{ObjectMeta: v1.ObjectMeta{Name: "todo", Namespace: "team-a", ResourceGroup: "rg1"}})
	require.NoError(t, err)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/apis/funcd.io/v1alpha1/namespaces/team-a/apps?dryRun=true", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer t")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		done <- rec
	}()
	require.Never(t, func() bool { return len(done) > 0 || probe.calls.Load() > 0 }, 200*time.Millisecond, 10*time.Millisecond,
		"the dry run waits for the namespace lock")
	unlock()
	var rec *httptest.ResponseRecorder
	select {
	case rec = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the dry run did not finish once the lock was free")
	}
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, int64(1), probe.calls.Load())
	require.True(t, probe.free.Load(), "the plan runs after the lock is released")
	var got v1.App
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, &v1.AppPlan{Revision: "todo-1"}, got.Status.Plan)
}
