package admission_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
)

func runObj(input string) *v1.WorkflowRun {
	r := &v1.WorkflowRun{TypeMeta: v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun}}
	r.Name, r.Namespace, r.ResourceGroup = "run", "default", "rg1"
	r.Spec.Workflow = "wf"
	if input != "" {
		r.Spec.Input = json.RawMessage(input)
	}
	return r
}

// scenario core: workflowrun-payload — a run whose spec.input exceeds the cap is rejected; one within the
// cap is admitted; limit ≤ 0 disables the check.
// scenario: workflowrun-payload-over-cap (ADR-0148) — the denial is PayloadTooLarge, rendered 413.
func TestWorkflowRunPayloadAdmission(t *testing.T) {
	t.Parallel()
	a := admission.NewWorkflowRunPayloadAdmission(64)
	require.True(t, a.Handles(v1.KindWorkflowRun.GVK(), admission.Create))
	require.True(t, a.Handles(v1.KindWorkflowRun.GVK(), admission.Update))
	require.False(t, a.Handles(v1.KindFunction.GVK(), admission.Create))

	ctx := context.Background()
	_, err := a.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindWorkflowRun.GVK(), Object: runObj(`{"ok":1}`)})
	require.NoError(t, err, "a small input is admitted")

	big := `{"data":"` + strings.Repeat("x", 200) + `"}`
	_, err = a.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindWorkflowRun.GVK(), Object: runObj(big)})
	require.Equal(t, fault.PayloadTooLarge, fault.KindOf(err), "an over-cap input ⇒ PayloadTooLarge")
	p := fault.ToProblem(err)
	require.Equal(t, http.StatusRequestEntityTooLarge, p.Status)
	require.Equal(t, "urn:funcd:problem:payload-too-large", p.Type)

	off := admission.NewWorkflowRunPayloadAdmission(0)
	_, err = off.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindWorkflowRun.GVK(), Object: runObj(big)})
	require.NoError(t, err, "limit ≤ 0 disables the cap")
}

// fakeGetter returns a fixed Workflow (or NotFound when nil) for the contract admission's parent lookup.
type fakeGetter struct{ wf *v1.Workflow }

func (f fakeGetter) Get(context.Context, v1.GroupVersionKind, v1.NamespaceName, v1.ObjectName) (v1.Object, error) {
	if f.wf == nil {
		return nil, fault.NotFoundf("test", "workflow not found")
	}
	return f.wf, nil
}

func wfWithContract(inputSchema string) *v1.Workflow {
	wf := &v1.Workflow{TypeMeta: v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow}}
	wf.Name, wf.Namespace = "wf", "default"
	if inputSchema != "" {
		wf.Status.Contract = &v1.WorkflowContract{Input: json.RawMessage(inputSchema)}
	}
	return wf
}

// scenario: input-rejected-at-admission + run-input-valid-admits — a WorkflowRun's input is validated
// against the parent's CACHED contract (zero registry I/O); not-Ready/absent parents are allowed (ADR-0098).
func TestWorkflowRunContractAdmission(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const schema = `{"type":"object","required":["day"],"properties":{"day":{"type":"string"}}}`
	req := func(input string) admission.Request {
		return admission.Request{Operation: admission.Create, GVK: v1.KindWorkflowRun.GVK(), Object: runObj(input)}
	}

	a := admission.NewWorkflowRunContractAdmission(fakeGetter{wfWithContract(schema)})
	require.True(t, a.Handles(v1.KindWorkflowRun.GVK(), admission.Create))
	require.False(t, a.Handles(v1.KindFunction.GVK(), admission.Create))

	// input-rejected-at-admission: missing required "day" ⇒ Invalid, zero registry I/O.
	_, err := a.Admit(ctx, req(`{}`))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a run input missing a required field is rejected at admission")

	// run-input-valid-admits.
	_, err = a.Admit(ctx, req(`{"day":"mon"}`))
	require.NoError(t, err, "a valid input against the cached contract is admitted")

	// not-yet-Ready parent (no cached contract) ⇒ allow (run-start is the backstop).
	na := admission.NewWorkflowRunContractAdmission(fakeGetter{wfWithContract("")})
	_, err = na.Admit(ctx, req(`{}`))
	require.NoError(t, err, "no cached contract ⇒ admission allows (backstop)")

	// absent parent ⇒ allow (the run reconciler owns a dangling spec.workflow).
	aa := admission.NewWorkflowRunContractAdmission(fakeGetter{nil})
	_, err = aa.Admit(ctx, req(`{}`))
	require.NoError(t, err, "absent parent ⇒ admission allows")

	// replay run ⇒ allow with empty input (ADR-0107: the input comes from the already-validated source).
	replay := runObj("")
	replay.Spec.Replay = &v1.ReplaySeed{Run: "src", From: "score"}
	_, err = a.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindWorkflowRun.GVK(), Object: replay})
	require.NoError(t, err, "a replay run has empty input by design — the contract check is skipped")
}

// Issue #125: an Update that changes a WorkflowRun's workflow, input or replay is a second run under a
// taken name, so it is a Conflict (ADR-0094 duplicate-run-name-rejected); the same spec spelled with other
// whitespace and a pause/cancel patch are admitted; Create is not handled.
func TestIssue125_RunSpecIsImmutableOnUpdate(t *testing.T) {
	t.Parallel()
	a := admission.NewWorkflowRunSpecImmutableAdmission()
	gvk := v1.KindWorkflowRun.GVK()
	require.Equal(t, "workflowrun-spec-immutable", a.Name())
	require.Equal(t, admission.Validating, a.Phase())
	require.True(t, a.Handles(gvk, admission.Update))
	require.False(t, a.Handles(gvk, admission.Create))
	require.False(t, a.Handles(v1.KindWorkflow.GVK(), admission.Update))

	update := func(old, obj *v1.WorkflowRun) error {
		_, err := a.Admit(context.Background(), admission.Request{Operation: admission.Update, GVK: gvk, Old: old, Object: obj})
		return err
	}
	old := runObj(`{"n":1,"day":"mon"}`)

	newInput := runObj(`{"n":42,"day":"mon"}`)
	newWorkflow := runObj(`{"n":1,"day":"mon"}`)
	newWorkflow.Spec.Workflow = "other"
	newReplay := runObj(`{"n":1,"day":"mon"}`)
	newReplay.Spec.Replay = &v1.ReplaySeed{Run: "src", From: "a"}
	for name, obj := range map[string]*v1.WorkflowRun{"input": newInput, "workflow": newWorkflow, "replay": newReplay} {
		err := update(old, obj)
		require.Equal(t, fault.Conflict, fault.KindOf(err), "a changed spec.%s is a Conflict", name)
		require.Contains(t, err.Error(), `WorkflowRun "run" already exists`)
	}

	paused := runObj(`{ "n": 1, "day": "mon" }`)
	paused.Spec.Paused, paused.Spec.Cancel = true, true
	require.NoError(t, update(old, paused), "a pause/cancel patch with the same input spelled with other whitespace is admitted")
	require.NoError(t, update(runObj(""), runObj("")), "a run with no input is admitted unchanged")
}
