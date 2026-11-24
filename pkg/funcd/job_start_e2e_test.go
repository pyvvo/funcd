//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// The scenarios of ADR-0349 on the process driver and the real Node shim: a Function planner starts and reads the runs
// of the Jobs it binds through ctx.job. Every Job here sets approval.bypass, so its runs run at once.

// plannerSrc starts a run of the Job its event's alias binds, under the event's name, or reads run get; a refusal answers
// the shim error's status and message.
const plannerSrc = `export async function handle(ctx, event) {
  const d = event.data ?? {};
  try {
    if (d.get) return { state: await ctx.job.get(d.alias ?? 'stack', d.get) };
    return { run: await ctx.job.start(d.alias ?? 'stack', d.input ?? {}, d.name) };
  } catch (e) {
    return { status: e.status, message: String(e.message) };
  }
}
`

// plannerReply is what plannerSrc answers.
type plannerReply struct {
	Run     string          `json:"run"`
	State   json.RawMessage `json:"state"`
	Status  int             `json:"status"`
	Message string          `json:"message"`
}

// plansSrc is the handler of a bound Job: it answers the op of its input.
const plansSrc = "export function handle(ctx, event) { return { op: event.data?.op ?? null }; }\n"

func bypass(s *v1.JobSpec) { s.Approval.Bypass = true }

// deployPlanner deploys plannerSrc as Function planner, binding each of jobs under its alias.
func (h *shimRig) deployPlanner(t *testing.T, jobs ...v1.FunctionJob) {
	t.Helper()
	h.deploy(t, "planner", nodeFn(plannerSrc).with(func(fn *v1.Function) { fn.Spec.Bindings.Job = jobs }))
	waitReady(t, h.c, "planner")
}

// plan calls planner with the event data in and decodes its answer.
func (h *shimRig) plan(t *testing.T, in string) plannerReply {
	t.Helper()
	r := h.post("planner", in)
	require.Equal(t, http.StatusOK, r.status, r.body)
	var out plannerReply
	require.NoError(t, json.Unmarshal([]byte(r.body), &out), r.body)
	return out
}

// runsOf lists the runs of the default namespace whose Job is job.
func (h *shimRig) runsOf(t *testing.T, job string) []v1.JobRun {
	t.Helper()
	objs, err := h.c.List(context.Background(), v1.KindJobRun, "default")
	require.NoError(t, err)
	var out []v1.JobRun
	for _, o := range objs {
		if r := o.(*v1.JobRun); string(r.Spec.Job) == job {
			out = append(out, *r)
		}
	}
	return out
}

// stack binds the Job tofu under the alias stack.
func stack() v1.FunctionJob { return v1.FunctionJob{Alias: "stack", Job: "tofu"} }

// scenario: function-starts-job-run — planner's ctx.job.start("stack", {"op": "plan"}) answers tofu-<id> at once, and
// that run exists, started by Function::"default/planner".
func TestScenarioFunctionStartsJobRun(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deployJob(t, "tofu", plansSrc, "", bypass)
	h.deployPlanner(t, stack())

	got := h.plan(t, `{"input": {"op": "plan"}}`)
	require.Regexp(t, `^tofu-[a-z0-9]+$`, got.Run, got.Message)
	run := h.getJobRun(t, got.Run)
	require.Equal(t, `Function::"default/planner"`, run.Status.StartedBy)
	require.Equal(t, v1.ObjectName("tofu"), run.Spec.Job)
	require.JSONEq(t, `{"op": "plan"}`, string(run.Spec.Input))
}

// scenario: function-reads-job-run — a later ctx.job.get("stack", run) answers the run's phase and reason and, once it
// Succeeded, its result.
func TestScenarioFunctionReadsJobRun(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deployJob(t, "tofu", plansSrc, "", bypass)
	h.waitJob(t, "tofu")
	h.deployPlanner(t, stack())

	name := h.plan(t, `{"input": {"op": "plan"}}`).Run
	require.NotEmpty(t, name)
	var state struct {
		Name      string          `json:"name"`
		Phase     string          `json:"phase"`
		Reason    string          `json:"reason"`
		StartedAt *string         `json:"startedAt"`
		EndedAt   *string         `json:"endedAt"`
		Result    json.RawMessage `json:"result"`
	}
	require.Eventually(t, func() bool {
		got := h.plan(t, `{"get": "`+name+`"}`)
		require.Zero(t, got.Status, got.Message)
		require.NoError(t, json.Unmarshal(got.State, &state), string(got.State))
		return state.Phase == string(v1.JobRunSucceeded)
	}, runWithin, 100*time.Millisecond, "the run ends Succeeded, as ctx.job.get reads it")
	require.Equal(t, name, state.Name)
	require.NotNil(t, state.StartedAt)
	require.NotNil(t, state.EndedAt)
	require.JSONEq(t, `{"op": "plan"}`, string(state.Result))
}

// scenario: job-start-without-binding — ctx.job.start("other", {}) on an alias planner does not bind answers 403, and
// no run is created.
func TestScenarioJobStartWithoutBinding(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deployJob(t, "tofu", plansSrc, "", bypass)
	h.deployPlanner(t, stack())

	got := h.plan(t, `{"alias": "other"}`)
	require.Equal(t, http.StatusForbidden, got.Status, got.Message)
	require.Empty(t, got.Run)
	require.Empty(t, h.runsOf(t, "tofu"), "no run is created")
}

// scenario: job-start-forbidden-by-policy — a namespace Policy that forbids job::start to planner answers 403.
func TestScenarioJobStartForbiddenByPolicy(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deployJob(t, "tofu", plansSrc, "", bypass)
	_, err := h.c.Apply(context.Background(), &v1.Policy{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindPolicy.GVK().APIVersion(), Kind: v1.KindPolicy},
		ObjectMeta: v1.ObjectMeta{Name: "no-tofu-starts", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.PolicySpec{Cedar: `forbid(principal == Function::"default/planner", action == Action::"job::start", resource);`}})
	require.NoError(t, err)
	h.deployPlanner(t, stack())

	got := h.plan(t, `{"input": {"op": "plan"}}`)
	require.Equal(t, http.StatusForbidden, got.Status, got.Message)
	require.Empty(t, h.runsOf(t, "tofu"), "no run is created")
}

// scenario: job-start-input-refused — an input off the Job's contract answers 422, naming the path, with the body that a
// Function's call answers for the same refusal (the decider's ruling of 2026-11-24).
func TestScenarioJobStartInputRefused(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deployJob(t, "tofu", plansSrc,
		`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`, bypass)
	require.Eventually(t, func() bool { return h.waitJob(t, "tofu").Status.Contract != nil }, runWithin, 20*time.Millisecond)
	h.deployPlanner(t, stack())

	got := h.plan(t, `{"input": {}}`)
	require.Equal(t, http.StatusUnprocessableEntity, got.Status, got.Message)
	require.Contains(t, got.Message, `"error":"run input does not match the input schema of Job tofu (InputSchemaMismatch)"`)
	require.Contains(t, got.Message, `"path":"/name"`, "the refusal names the path")
	require.Empty(t, h.runsOf(t, "tofu"))
}

// scenario: job-start-named-twice — the run tofu-gen-7 started twice with one input, as a retried call does, answers
// tofu-gen-7 to both calls, and one run exists.
func TestScenarioJobStartNamedTwice(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deployJob(t, "tofu", plansSrc, "", bypass)
	h.deployPlanner(t, stack())

	// &, < and > reach the store escaped, as json.Marshal writes them, and keep the input equal
	in := `{"input": {"op": "plan", "stack": "net&dev <a>"}, "name": "tofu-gen-7"}`
	first, again := h.plan(t, in), h.plan(t, `{"name": "tofu-gen-7", "input": {"stack": "net&dev <a>", "op": "plan"}}`)
	// oracle: docs/design/job/start.fizz#CallerSucceeds
	require.Equal(t, "tofu-gen-7", first.Run, first.Message)
	require.Equal(t, "tofu-gen-7", again.Run, "the call sent again succeeds: %s", again.Message)
	// oracle: docs/design/job/start.fizz#AtMostOneRun
	require.Len(t, h.runsOf(t, "tofu"), 1, "one run exists")
}

// scenario: job-start-name-taken — a run tofu-gen-7 of another Job, or one with another input, answers 409.
func TestScenarioJobStartNameTaken(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deployJob(t, "tofu", plansSrc, "", bypass)
	h.deployJob(t, "backfill", plansSrc, "", bypass)
	_, err := h.c.Create(context.Background(), &v1.JobRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindJobRun.GVK().APIVersion(), Kind: v1.KindJobRun},
		ObjectMeta: v1.ObjectMeta{Name: "tofu-gen-7", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.JobRunSpec{Job: "backfill", Input: json.RawMessage(`{"op": "plan"}`)}})
	require.NoError(t, err)
	h.deployPlanner(t, stack())

	got := h.plan(t, `{"name": "tofu-gen-7", "input": {"op": "plan"}}`)
	require.Equal(t, http.StatusConflict, got.Status, "a run of another Job holds the name, with the same input: %s", got.Message)
	require.Equal(t, "tofu-gen-8", h.plan(t, `{"name": "tofu-gen-8", "input": {"op": "plan"}}`).Run)
	got = h.plan(t, `{"name": "tofu-gen-8", "input": {"op": "apply"}}`)
	require.Equal(t, http.StatusConflict, got.Status, "a run with another input holds the name: %s", got.Message)
}

// scenario: job-start-job-gone — once the bound Job is deleted, ctx.job.start answers 404.
func TestScenarioJobStartJobGone(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deployJob(t, "tofu", plansSrc, "", bypass)
	h.deployPlanner(t, stack())
	require.NoError(t, h.c.Delete(context.Background(), v1.KindJob, "default", "tofu"))
	require.Eventually(t, func() bool {
		_, err := h.c.Get(context.Background(), v1.KindJob, "default", "tofu")
		return fault.KindOf(err) == fault.NotFound
	}, runWithin, 20*time.Millisecond)

	got := h.plan(t, `{"input": {"op": "plan"}}`)
	require.Equal(t, http.StatusNotFound, got.Status, got.Message)
}

// scenario: job-get-other-job — a read of a run of a Job planner does not bind answers 404.
func TestScenarioJobGetOtherJob(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deployJob(t, "tofu", plansSrc, "", bypass)
	h.deployJob(t, "backfill", plansSrc, "", bypass)
	h.deployPlanner(t, stack())
	other := h.startRun(t, "backfill", `{}`)

	got := h.plan(t, `{"get": "`+other+`"}`)
	require.Equal(t, http.StatusNotFound, got.Status, got.Message)
}
