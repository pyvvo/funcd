package controlplane_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	"github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

// captureReader records the last Query it was asked and echoes one line back (so a test can assert what
// the run-log querier resolved status.traceId / --step into).
type captureReader struct{ last *logread.Query }

func (c *captureReader) Read(_ context.Context, q logread.Query) ([]logread.Line, error) {
	*c.last = q
	return []logread.Line{{Time: time.Unix(0, 1).UTC(), Severity: "INFO", Body: "line", Namespace: q.Namespace, Function: q.Function, TraceID: q.TraceID}}, nil
}

func newRunLogsServer(t *testing.T, seed func(store.Store)) (http.Handler, *logread.Query) {
	t.Helper()
	return newRunLogsServerWithRuns(t, func(s store.Store, _ runstate.Store) {
		if seed != nil {
			seed(s)
		}
	})
}

// newRunLogsServerWithRuns is newRunLogsServer whose seed also writes the engine's run records.
func newRunLogsServerWithRuns(t *testing.T, seed func(store.Store, runstate.Store)) (http.Handler, *logread.Query) {
	t.Helper()
	s := store.New(memory.New())
	runs, err := badger.New(badger.Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = runs.Close() })
	seed(s, runs)
	last := &logread.Query{}
	q := controlplane.NewWorkflowRunLogQuerier(s, runs, &captureReader{last: last})
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		devToken:   {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
		adminToken: {Subject: "ops", Role: auth.RoleAdmin},
	})
	h, err := controlplane.NewServer(controlplane.Deps{
		Store:       s,
		Authorizer:  rbac.New(),
		Credentials: creds,
		RunLogs:     q,
	})
	require.NoError(t, err)
	return h, last
}

func seedRun(t *testing.T, s store.Store, ns, name, workflow, traceID string) {
	t.Helper()
	run := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: v1.NamespaceName(ns), ResourceGroup: "rg"},
		Spec:       v1.WorkflowRunSpec{Workflow: v1.ObjectName(workflow)},
	}
	run.Status.TraceID = traceID
	if _, err := s.Create(context.Background(), run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
}

func runLogsPath(ns, run string) string {
	return "/apis/funcd.io/v1alpha1/namespaces/" + ns + "/workflowruns/" + run + "/logs"
}

// scenario: run-logs-resolves-trace — the server resolves the run's status.traceId and reads the
// namespace-wide, trace-filtered logs.
func TestScenarioRunLogsResolvesTrace(t *testing.T) {
	srv, last := newRunLogsServer(t, func(s store.Store) {
		seedRun(t, s, "team-a", "run-1", "wf", "trace-xyz")
	})
	rec := do(t, srv, http.MethodGet, runLogsPath("team-a", "run-1"), devToken, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "trace-xyz", last.TraceID, "querier must pass the resolved trace-id to the reader")
	require.Equal(t, "team-a", last.Namespace)
	require.Equal(t, "", last.Function, "no --step ⇒ namespace-wide (Function empty)")
}

// scenario: rbac-scoped — a caller not authorized on the run's namespace gets 403.
func TestScenarioRunLogsRBACScoped(t *testing.T) {
	srv, _ := newRunLogsServer(t, func(s store.Store) {
		seedRun(t, s, "team-b", "run-x", "wf", "trace-1")
	})
	rec := do(t, srv, http.MethodGet, runLogsPath("team-b", "run-x"), devToken, nil) // dev is bound to team-a
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

// empty status.traceId (a legacy/traceless run) ⇒ an empty result, not an error.
func TestScenarioRunLogsEmptyTrace(t *testing.T) {
	srv, _ := newRunLogsServer(t, func(s store.Store) {
		seedRun(t, s, "team-a", "run-legacy", "wf", "")
	})
	rec := do(t, srv, http.MethodGet, runLogsPath("team-a", "run-legacy"), devToken, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Items []logread.Line `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Empty(t, out.Items, "a traceless run returns no lines, not an error")
}

// scenario: run-logs-step — --step narrows to that step's function (an image step ⇒ <workflow>-<step>).
func TestScenarioRunLogsStepResolves(t *testing.T) {
	srv, last := newRunLogsServer(t, func(s store.Store) {
		wf := &v1.Workflow{
			TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
			ObjectMeta: v1.ObjectMeta{Name: "wf", Namespace: "team-a", ResourceGroup: "rg"},
			Spec:       v1.WorkflowSpec{Steps: []v1.WorkflowStep{{Name: "s1", Function: &v1.FunctionStep{Image: "registry.example.com/img:v1"}}}},
		}
		if _, err := s.Create(context.Background(), wf); err != nil {
			t.Fatalf("seed workflow: %v", err)
		}
		seedRun(t, s, "team-a", "run-s", "wf", "trace-step")
	})
	rec := do(t, srv, http.MethodGet, runLogsPath("team-a", "run-s")+"?step=s1", devToken, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "wf-s1", last.Function, "--step of an image step must resolve to the materialized function")
	require.Equal(t, "trace-step", last.TraceID, "--step read still carries the trace filter")
}

// the route is absent (404) when no run-log querier is configured.
func TestRunLogsRouteAbsentWhenUnset(t *testing.T) {
	srv := newLogsServer(t, nil) // no RunLogs dep
	rec := do(t, srv, http.MethodGet, runLogsPath("team-a", "run-1"), adminToken, nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestIssue723_StepResolvesFromPinnedSpec: --step resolves against the spec the run pinned at start
// (ADR-0106, ADR-0094), not the live Workflow, so an edited or deleted Workflow does not move the read.
// A record left by an earlier run of the same name is not this run's pin.
func TestIssue723_StepResolvesFromPinnedSpec(t *testing.T) {
	cases := []struct {
		name   string
		pinned v1.FunctionStep
		recUID v1.UID
		live   *v1.FunctionStep // the live Workflow's step; nil ⇒ the Workflow is deleted
		want   string
	}{
		{name: "ref step, workflow edited", pinned: v1.FunctionStep{Ref: "f1"}, live: &v1.FunctionStep{Ref: "f2"}, want: "f1"},
		{name: "image step, workflow edited to a ref", pinned: v1.FunctionStep{Image: "registry.example.com/img:v1"}, live: &v1.FunctionStep{Ref: "f2"}, want: "wf-s1"},
		{name: "ref step, workflow deleted", pinned: v1.FunctionStep{Ref: "f1"}, want: "f1"},
		{name: "record of an earlier run", pinned: v1.FunctionStep{Ref: "f1"}, recUID: "earlier-run", live: &v1.FunctionStep{Ref: "f2"}, want: "f2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			srv, last := newRunLogsServerWithRuns(t, func(s store.Store, runs runstate.Store) {
				seedRun(t, s, "team-a", "run-s", "wf", "trace-step")
				require.NoError(t, runs.Put(ctx, &runstate.Record{
					Namespace: "team-a", Name: "run-s", Workflow: "wf", RunUID: tc.recUID, TraceID: "trace-step",
					Spec: v1.WorkflowSpec{Steps: []v1.WorkflowStep{{Name: "s1", Function: &tc.pinned}}},
				}))
				if tc.live == nil {
					return
				}
				_, err := s.Create(ctx, &v1.Workflow{
					TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
					ObjectMeta: v1.ObjectMeta{Name: "wf", Namespace: "team-a", ResourceGroup: "rg"},
					Spec:       v1.WorkflowSpec{Steps: []v1.WorkflowStep{{Name: "s1", Function: tc.live}}},
				})
				require.NoError(t, err)
			})
			rec := do(t, srv, http.MethodGet, runLogsPath("team-a", "run-s")+"?step=s1", devToken, nil)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, tc.want, last.Function)
			require.Equal(t, "trace-step", last.TraceID)
		})
	}
}
