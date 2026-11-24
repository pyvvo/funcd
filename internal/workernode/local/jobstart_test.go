package local_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// planner binds the Job tofu under the alias stack; denied lists the actions Cedar denies it.
type plannerGrants struct{ denied []auth.Action }

func (g plannerGrants) Grant(_ context.Context, caller local.Ref, alias string, action auth.Action) (v1.ObjectName, error) {
	if caller.Kind != v1.KindFunction || caller.Name != "planner" || alias != "stack" {
		return "", local.ErrJobNoBinding.Errorf("test.Grant", "no alias %q", alias)
	}
	if slices.Contains(g.denied, action) {
		return "", local.ErrJobDenied.Errorf("test.Grant", "%s forbidden", action)
	}
	return "tofu", nil
}

// startedRuns records each start and answers the runs it holds, by name.
type startedRuns struct {
	mu     sync.Mutex
	starts []startCall
	runs   map[v1.ObjectName]v1.JobRun
	err    error
}

type startCall struct {
	caller local.Ref
	job    v1.ObjectName
	name   v1.ObjectName
	input  string
}

func (s *startedRuns) Start(_ context.Context, caller local.Ref, job, name v1.ObjectName, input json.RawMessage) (v1.ObjectName, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts = append(s.starts, startCall{caller: caller, job: job, name: name, input: string(input)})
	if s.err != nil {
		return "", false, s.err
	}
	if name == "" {
		return "tofu-1a2b3c4d", true, nil
	}
	if _, held := s.runs[name]; held {
		return name, false, nil
	}
	return name, true, nil
}

func (s *startedRuns) Get(_ context.Context, ns v1.NamespaceName, job, run v1.ObjectName) (v1.JobRun, error) {
	r, ok := s.runs[run]
	if !ok || ns != "team" || r.Spec.Job != job {
		return v1.JobRun{}, local.ErrJobRunNotFound.Errorf("test.Get", "no run %q of Job %q", run, job)
	}
	return r, nil
}

func (s *startedRuns) calls() []startCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.starts)
}

func startHandler(ref local.Ref, s local.JobRunStarter, g local.JobGrants) http.Handler {
	return local.NewHandler(ref, &fakeResolver{}, fakeInvoker{}, nil, nil, slog.New(slog.DiscardHandler),
		local.WithFacets(local.JobFacet(runsByRef{}, s, g)))
}

// plannerRef is the Function that binds the Job tofu under the alias stack.
func plannerRef() local.Ref { return local.FunctionRef("team", "planner") }

// ADR-0349 Decisions 2 and 4: a start of a bound Job answers 201 with the run's name, a start the starter finds held
// with the same input 200, and the starter's refusals pass through; an alias the caller does not bind, a deny and a
// malformed body start nothing.
func TestJobStartAnswers(t *testing.T) {
	t.Parallel()
	s := &startedRuns{runs: map[v1.ObjectName]v1.JobRun{"tofu-gen-7": {}}}
	h := startHandler(plannerRef(), s, plannerGrants{})

	rec := do(t, h, http.MethodPost, "/job/stack", `{"input": {"op": "plan"}}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"run": "tofu-1a2b3c4d"}`, rec.Body.String())
	rec = do(t, h, http.MethodPost, "/job/stack", `{"input": {"op": "plan"}, "name": "tofu-gen-7"}`)
	require.Equal(t, http.StatusOK, rec.Code, "a start sent again answers the run it made: %s", rec.Body.String())
	require.JSONEq(t, `{"run": "tofu-gen-7"}`, rec.Body.String())
	rec = do(t, h, http.MethodPost, "/job/stack", `{"input": null}`)
	require.Equal(t, http.StatusCreated, rec.Code)
	require.Equal(t, []startCall{
		{caller: plannerRef(), job: "tofu", input: `{"op": "plan"}`},
		{caller: plannerRef(), job: "tofu", name: "tofu-gen-7", input: `{"op": "plan"}`},
		{caller: plannerRef(), job: "tofu"},
	}, s.calls(), "the Job is the binding's, the caller the sandbox's, and null is no input")

	for name, c := range map[string]struct {
		h    http.Handler
		body string
		code int
	}{
		"no binding":   {startHandler(plannerRef(), s, plannerGrants{}), `{}`, http.StatusForbidden},
		"denied":       {startHandler(plannerRef(), s, plannerGrants{denied: []auth.Action{auth.ActionJobStart}}), `{}`, http.StatusForbidden},
		"a Job caller": {startHandler(runRef("tofu-1"), s, plannerGrants{}), `{}`, http.StatusForbidden},
		// The local API answers an Invalid cause with 400, as it answers every body it refuses (fault.ToProblem).
		"not JSON":      {h, `{"input":`, http.StatusBadRequest},
		"a bad name":    {h, `{"name": "Tofu_7"}`, http.StatusBadRequest},
		"name not text": {h, `{"name": 7}`, http.StatusBadRequest},
	} {
		before := len(s.calls())
		path := "/job/stack"
		if name == "no binding" {
			path = "/job/other"
		}
		rec := do(t, c.h, http.MethodPost, path, c.body)
		require.Equal(t, c.code, rec.Code, "%s: %s", name, rec.Body.String())
		require.Len(t, s.calls(), before, "%s: nothing is started", name)
	}

	for _, refusal := range []*fault.KnownCause{local.ErrJobNotFound, local.ErrJobRunNameTaken, local.ErrJobInputTooLarge} {
		err := refusal.Errorf("test.Start", "refused")
		rec := do(t, startHandler(plannerRef(), &startedRuns{err: err}, plannerGrants{}), http.MethodPost, "/job/stack", `{"input": {}}`)
		require.Equal(t, fault.StatusOf(err), rec.Code, "%s: %s", refusal.Name(), rec.Body.String())
	}

	// ADR-0349 Decision 4 with the decider's ruling of 2026-11-24: an input off the Job's contract is answered as a
	// Function's call answers it, the 422 and the body relayed as they are.
	violation := `{"error": "run input does not match the input schema of Job tofu (InputSchemaMismatch)",` +
		` "details": [{"path": "/name", "message": "missing property \"name\""}]}`
	off := &startedRuns{err: &local.UpstreamError{Status: http.StatusUnprocessableEntity, Body: []byte(violation)}}
	rec = do(t, startHandler(plannerRef(), off, plannerGrants{}), http.MethodPost, "/job/stack", `{"input": {}}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.JSONEq(t, violation, rec.Body.String())
}

// ADR-0349 Decisions 3 and 4: a read of a run of the bound Job answers its name, phase, reason and times, and its
// result only once it Succeeded; a run of another Job, an unbound alias and a deny answer 404 and 403.
func TestJobReadAnswers(t *testing.T) {
	t.Parallel()
	started := v1.NewTimestamp(time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC))
	ended := v1.NewTimestamp(time.Date(2026, 11, 24, 9, 1, 0, 0, time.UTC))
	done := v1.JobRun{ObjectMeta: v1.ObjectMeta{Name: "tofu-1"}, Spec: v1.JobRunSpec{Job: "tofu"},
		Status: v1.JobRunStatus{Phase: v1.JobRunSucceeded, StartedAt: started, EndedAt: ended, Result: json.RawMessage(`{"added": 2}`)}}
	done.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionTrue})
	failed := v1.JobRun{ObjectMeta: v1.ObjectMeta{Name: "tofu-2"}, Spec: v1.JobRunSpec{Job: "tofu"},
		Status: v1.JobRunStatus{Phase: v1.JobRunFailed, StartedAt: started, EndedAt: ended, Result: json.RawMessage(`{"partial": true}`)}}
	failed.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionFalse, Reason: v1.JobRunHandlerFailed})
	fresh := v1.JobRun{ObjectMeta: v1.ObjectMeta{Name: "tofu-3"}, Spec: v1.JobRunSpec{Job: "tofu"}}
	other := v1.JobRun{ObjectMeta: v1.ObjectMeta{Name: "backfill-1"}, Spec: v1.JobRunSpec{Job: "backfill"}}
	s := &startedRuns{runs: map[v1.ObjectName]v1.JobRun{"tofu-1": done, "tofu-2": failed, "tofu-3": fresh, "backfill-1": other}}
	h := startHandler(plannerRef(), s, plannerGrants{})

	rec := do(t, h, http.MethodGet, "/job/stack/tofu-1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"name": "tofu-1", "phase": "Succeeded", "reason": "", "startedAt": "2026-11-24T09:00:00.000Z",
		"endedAt": "2026-11-24T09:01:00.000Z", "result": {"added": 2}}`, rec.Body.String())
	rec = do(t, h, http.MethodGet, "/job/stack/tofu-2", "")
	require.JSONEq(t, `{"name": "tofu-2", "phase": "Failed", "reason": "HandlerFailed", "startedAt": "2026-11-24T09:00:00.000Z",
		"endedAt": "2026-11-24T09:01:00.000Z", "result": null}`, rec.Body.String(), "a result only once Succeeded")
	rec = do(t, h, http.MethodGet, "/job/stack/tofu-3", "")
	require.JSONEq(t, `{"name": "tofu-3", "phase": "Pending", "reason": "", "startedAt": null, "endedAt": null,
		"result": null}`, rec.Body.String(), "a run no pass reached is Pending")

	require.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/job/stack/backfill-1", "").Code, "another Job's run")
	require.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/job/stack/tofu-9", "").Code, "a run that is gone")
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodGet, "/job/other/tofu-1", "").Code, "no binding")
	denied := startHandler(plannerRef(), s, plannerGrants{denied: []auth.Action{auth.ActionJobRead}})
	require.Equal(t, http.StatusForbidden, do(t, denied, http.MethodGet, "/job/stack/tofu-1", "").Code, "a deny")
	require.Equal(t, http.StatusCreated, do(t, denied, http.MethodPost, "/job/stack", `{}`).Code,
		"a forbid of job::read leaves job::start")
}

// The Job facet's start and read declare their causes (ADR-0330).
func TestJobStartIsDescribed(t *testing.T) {
	t.Parallel()
	causes := map[string][]*fault.KnownCause{}
	for _, e := range local.Endpoints() {
		causes[e.Pattern] = e.Causes
	}
	require.Subset(t, causes["POST /job/{alias}"], []*fault.KnownCause{local.ErrJobNoBinding, local.ErrJobDenied,
		local.ErrJobNotFound, local.ErrJobRunNameTaken, local.ErrJobInputTooLarge, local.ErrJobStartBodyInvalid})
	require.ElementsMatch(t, []*fault.KnownCause{local.ErrJobNoBinding, local.ErrJobDenied, local.ErrJobRunNotFound},
		causes["GET /job/{alias}/{run}"])
}
