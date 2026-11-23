package admission_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
)

func jobRun(input string) *v1.JobRun {
	r := &v1.JobRun{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJobRun.GVK().APIVersion(), Kind: v1.KindJobRun}}
	r.Name, r.Namespace, r.ResourceGroup = "hello-1a2b3c4d", "default", "rg1"
	r.Spec.Job = "hello"
	if input != "" {
		r.Spec.Input = json.RawMessage(input)
	}
	return r
}

// jobGetter returns job for the run's Job, or NotFound when it is nil.
type jobGetter struct{ job *v1.Job }

func (g jobGetter) Get(_ context.Context, gvk v1.GroupVersionKind, _ v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if g.job == nil || gvk != v1.KindJob.GVK() || name != g.job.Name {
		return nil, fault.NotFoundf("test", "Job %q not found", name)
	}
	return g.job, nil
}

func jobWithContract(input string) *v1.Job {
	j := &v1.Job{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJob.GVK().APIVersion(), Kind: v1.KindJob}}
	j.Name, j.Namespace = "hello", "default"
	if input != "" {
		j.Status.Contract = &v1.WorkflowContract{Input: json.RawMessage(input)}
	}
	return j
}

// ADR-0347 Decision 3 and ADR-0148: an input over job.payloadLimit is refused with 413 at creation.
func TestJobRunPayloadAdmission(t *testing.T) {
	t.Parallel()
	a := admission.NewJobRunPayloadAdmission(16)
	require.True(t, a.Handles(v1.KindJobRun.GVK(), admission.Create))
	require.False(t, a.Handles(v1.KindJobRun.GVK(), admission.Update), "the input is fixed: a cancel of an older run passes")
	require.False(t, a.Handles(v1.KindWorkflowRun.GVK(), admission.Create))
	ctx := context.Background()
	create := func(input string) error {
		_, err := a.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindJobRun.GVK(), Object: jobRun(input)})
		return err
	}
	require.NoError(t, create(`{"name":"Ada"}`), "16 bytes is at the limit")
	require.NoError(t, create(""))
	err := create(`{"name":"Adaline"}`)
	require.Equal(t, fault.PayloadTooLarge, fault.KindOf(err))
	require.Equal(t, http.StatusRequestEntityTooLarge, fault.ToProblem(err).Status)
	require.ErrorContains(t, err, "run input 18 bytes exceeds job.payloadLimit 16")
}

// ADR-0347 Decision 3: an input off the Job's status.contract is refused with 422 naming each path; a missing Job or
// one without a contract leaves it unchecked.
func TestJobRunContractAdmission(t *testing.T) {
	t.Parallel()
	const schema = `{"type":"object","required":["name"],"properties":{"name":{"type":"string"},"count":{"type":"integer"}}}`
	ctx := context.Background()
	admit := func(g jobGetter, input string) error {
		a := admission.NewJobRunContractAdmission(g)
		_, err := a.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindJobRun.GVK(), Object: jobRun(input)})
		return err
	}
	unprocessable := func(t *testing.T, err error, locations ...string) {
		t.Helper()
		var se huma.StatusError
		require.True(t, errors.As(err, &se), "%v is a huma status error", err)
		require.Equal(t, http.StatusUnprocessableEntity, se.GetStatus())
		require.ErrorContains(t, err, "run input does not match the input schema of Job hello (InputSchemaMismatch)")
		model, ok := se.(*huma.ErrorModel)
		require.True(t, ok)
		var got []string
		for _, d := range model.Errors {
			got = append(got, d.Location)
		}
		require.Equal(t, locations, got)
	}

	withContract := jobGetter{job: jobWithContract(schema)}
	unprocessable(t, admit(withContract, `{}`), "body.spec.input.name")
	unprocessable(t, admit(withContract, `{"name":7,"count":"x"}`), "body.spec.input.count", "body.spec.input.name")
	unprocessable(t, admit(withContract, ""), "body.spec.input")
	require.NoError(t, admit(withContract, `{"name":"Ada"}`))

	require.NoError(t, admit(jobGetter{}, `{}`), "a missing Job leaves the input to the run's start")
	require.NoError(t, admit(jobGetter{job: jobWithContract("")}, `{}`), "an image with no contract leaves the input unchecked")

	a := admission.NewJobRunContractAdmission(withContract)
	require.True(t, a.Handles(v1.KindJobRun.GVK(), admission.Create))
	require.False(t, a.Handles(v1.KindJobRun.GVK(), admission.Update))
}

// ADR-0347 Decision 3: spec.job and spec.input are fixed at creation, spec.cancel may only turn true, and any other
// change of the spec is refused as invalid.
func TestJobRunSpecAdmission(t *testing.T) {
	t.Parallel()
	a := admission.NewJobRunSpecAdmission()
	require.True(t, a.Handles(v1.KindJobRun.GVK(), admission.Update))
	require.False(t, a.Handles(v1.KindJobRun.GVK(), admission.Create))
	ctx := context.Background()
	update := func(old, next *v1.JobRun) error {
		_, err := a.Admit(ctx, admission.Request{Operation: admission.Update, GVK: v1.KindJobRun.GVK(), Old: old, Object: next})
		return err
	}
	old := jobRun(`{"name": "Ada"}`)
	cancel := jobRun(`{"name":"Ada"}`)
	cancel.Spec.Cancel = true
	require.NoError(t, update(old, cancel), "a cancel turns true, the input compared as JSON")
	require.NoError(t, update(cancel, cancel), "a cancelled run stays cancelled")
	tagged := jobRun(`{"name":"Ada"}`)
	tagged.Tags = v1.Tags{"team": "a"}
	require.NoError(t, update(old, tagged), "metadata is not the spec")

	otherJob := jobRun(`{"name":"Ada"}`)
	otherJob.Spec.Job = "other"
	otherInput := jobRun(`{"name":"Bob"}`)
	noInput := jobRun("")
	for name, c := range map[string]struct {
		old, next *v1.JobRun
		want      string
	}{
		"another job":   {old, otherJob, "spec.job and spec.input are fixed"},
		"another input": {old, otherInput, "spec.job and spec.input are fixed"},
		"input dropped": {old, noInput, "spec.job and spec.input are fixed"},
		"cancel undone": {cancel, old, "spec.cancel may only turn true"},
	} {
		err := update(c.old, c.next)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
		require.Equal(t, http.StatusBadRequest, fault.ToProblem(err).Status, name)
		require.True(t, strings.Contains(err.Error(), c.want), "%s: %v", name, err)
	}
}
