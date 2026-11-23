package local_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// runsByRef is the JobRunReader: each run by the name of the run its caller's ref names.
type runsByRef map[v1.ObjectName]v1.JobRun

func (r runsByRef) Run(_ context.Context, ref local.Ref) (v1.JobRun, error) {
	run, ok := r[ref.Run]
	if !ok || run.Spec.Job != ref.Name {
		return v1.JobRun{}, local.ErrNotAJobWorker.Errorf("test.Run", "no run %s", ref)
	}
	return run, nil
}

func jobHandler(ref local.Ref, runs local.JobRunReader) http.Handler {
	return local.NewHandler(ref, &fakeResolver{}, fakeInvoker{}, nil, nil, slog.New(slog.DiscardHandler),
		local.WithFacets(local.JobFacet(runs)))
}

func readRun(t *testing.T, h http.Handler) (int, local.JobRunInfo, string) {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/job/run", "")
	var body local.JobRunInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return rec.Code, body, rec.Body.String()
}

// ADR-0348 Decision 6: GET /job/run answers a Job's worker its own run, with the approval that admission recorded for
// its current preview, and null with none; a Function's worker, and a run that is gone, get 403.
func TestGetJobRunAnswersTheCallersOwnRun(t *testing.T) {
	t.Parallel()
	digest := "sha256:" + strings.Repeat("1", 64)
	approved := v1.JobRun{ObjectMeta: v1.ObjectMeta{Name: "tofu-1", Namespace: "team"},
		Spec: v1.JobRunSpec{Job: "tofu", Approve: digest},
		Status: v1.JobRunStatus{Phase: v1.JobRunRunning, JobRevision: "tofu-v3", StartedBy: "alice", ApprovedBy: "bob",
			ApprovedAt: v1.NewTimestamp(time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)), Preview: &v1.JobRunPreview{Digest: digest, Document: json.RawMessage(`{"summary":"2 to add"}`)}}}
	bypass := v1.JobRun{ObjectMeta: v1.ObjectMeta{Name: "tofu-2", Namespace: "team"}, Spec: v1.JobRunSpec{Job: "tofu"},
		Status: v1.JobRunStatus{Phase: v1.JobRunRunning, JobRevision: "tofu-v3", StartedBy: "alice"}}
	unrecorded := approved
	unrecorded.Name, unrecorded.Status.ApprovedAt = "tofu-3", v1.Timestamp{}
	runs := runsByRef{"tofu-1": approved, "tofu-2": bypass, "tofu-3": unrecorded}

	code, _, raw := readRun(t, jobHandler(runRef("tofu-1"), runs))
	require.Equal(t, http.StatusOK, code)
	require.JSONEq(t, `{"name": "tofu-1", "job": "tofu", "revision": "tofu-v3", "phase": "Running", "startedBy": "alice",
		"approval": {"approvedBy": "bob", "digest": "`+digest+`", "preview": {"summary": "2 to add"}}}`, raw)

	for _, name := range []v1.ObjectName{"tofu-2", "tofu-3"} {
		code, body, raw := readRun(t, jobHandler(runRef(name), runs))
		require.Equal(t, http.StatusOK, code)
		require.Contains(t, raw, `"approval":null`, "%s: approval is null, not left out", name)
		require.Nil(t, body.Approval, "%s: no approval that admission recorded for its preview", name)
	}

	for _, ref := range []local.Ref{local.FunctionRef("team", "tofu"), runRef("tofu-9")} {
		rec := do(t, jobHandler(ref, runs), http.MethodGet, "/job/run", "")
		require.Equal(t, http.StatusForbidden, rec.Code, "%s: %s", ref, rec.Body.String())
	}
}

// The Job facet's one endpoint declares its one cause (ADR-0330), and its segment passes CheckFacets.
func TestJobFacetIsDescribed(t *testing.T) {
	t.Parallel()
	require.NoError(t, local.CheckFacets(local.JobFacet(nil)))
	var found bool
	for _, e := range local.Endpoints() {
		if e.Pattern == "GET /job/run" {
			found = true
			require.Equal(t, []*fault.KnownCause{local.ErrNotAJobWorker}, e.Causes)
		}
	}
	require.True(t, found, "GET /job/run is among the endpoints")
}
