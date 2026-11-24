package funcd

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// starterOver is a starter over a store that holds the Job tofu, with the JobRun admissions of a person's create.
func starterOver(t *testing.T) (*jobRunStarter, store.Store) {
	t.Helper()
	st := store.New(memory.New())
	_, err := st.Create(context.Background(), &v1.Job{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJob.GVK().APIVersion(), Kind: v1.KindJob},
		ObjectMeta: v1.ObjectMeta{Name: "tofu", Namespace: "team", ResourceGroup: "rg1"},
		Spec:       v1.JobSpec{Runtime: "nodejs22", Handler: "handle", Image: "oci://x/tofu:1"}})
	require.NoError(t, err)
	s := &jobRunStarter{store: st, admit: admission.NewPipeline(admission.NewValidateAdmission(),
		admission.NewJobRunPayloadAdmission(64), admission.NewJobRunContractAdmission(storeReader{st}))}
	return s, st
}

// ADR-0349 Decision 2: a start creates a run of the bound Job started by the caller's Cedar principal, with its Job's
// UID and resource group; a named start sent again with an equal input answers that run and creates none, and a name
// another run holds is refused.
func TestJobRunStarterStarts(t *testing.T) {
	t.Parallel()
	s, st := starterOver(t)
	ctx, caller := context.Background(), local.FunctionRef("team", "planner")

	name, created, err := s.Start(ctx, caller, "tofu", "", json.RawMessage(`{"op": "plan"}`))
	require.NoError(t, err)
	require.True(t, created)
	require.Regexp(t, `^tofu-[a-f0-9]+$`, string(name))
	obj, err := st.Get(ctx, v1.KindJobRun.GVK(), "team", name)
	require.NoError(t, err)
	run := obj.(*v1.JobRun)
	job, err := st.Get(ctx, v1.KindJob.GVK(), "team", "tofu")
	require.NoError(t, err)
	require.Equal(t, `Function::"team/planner"`, run.Status.StartedBy)
	require.Equal(t, job.GetObjectMeta().UID, run.Status.JobUID)
	require.Equal(t, v1.ResourceGroupName("rg1"), run.ResourceGroup)
	require.JSONEq(t, `{"op": "plan"}`, string(run.Spec.Input))

	// oracle: docs/design/job/start.fizz#AtMostOneRun
	for _, again := range []string{`{"op":"plan","n":[1,{"a":true}]}`, `{"n": [1, {"a": true}], "op": "plan"}`} {
		got, created, err := s.Start(ctx, caller, "tofu", "tofu-gen-7", json.RawMessage(again))
		require.NoError(t, err, again)
		require.Equal(t, v1.ObjectName("tofu-gen-7"), got)
		require.Equal(t, again == `{"op":"plan","n":[1,{"a":true}]}`, created, "%s: the second start is the first's", again)
	}
	l, err := st.List(ctx, v1.KindJobRun.GVK(), store.ListOptions{Namespace: "team"})
	require.NoError(t, err)
	require.Len(t, l.Items, 2, "a start sent again creates no run")

	_, _, err = s.Start(ctx, caller, "tofu", "tofu-gen-7", json.RawMessage(`{"op": "apply"}`))
	require.ErrorIs(t, err, local.ErrJobRunNameTaken, "another input")
	_, _, err = s.Start(ctx, caller, "missing", "", nil)
	require.ErrorIs(t, err, local.ErrJobNotFound)
	_, _, err = s.Start(ctx, caller, "tofu", "", json.RawMessage(`"0123456789012345678901234567890123456789012345678901234567890123"`))
	require.ErrorIs(t, err, local.ErrJobInputTooLarge, "an input over job.payloadLimit")

	got, err := s.Get(ctx, "team", "tofu", "tofu-gen-7")
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("tofu-gen-7"), got.Name)
	_, err = s.Get(ctx, "team", "other", "tofu-gen-7")
	require.ErrorIs(t, err, local.ErrJobRunNotFound, "a run of another Job")
	_, err = s.Get(ctx, "team", "tofu", "tofu-9")
	require.ErrorIs(t, err, local.ErrJobRunNotFound, "a run that is gone")

	require.NoError(t, st.Delete(ctx, v1.KindJob.GVK(), "team", "tofu", ""))
	got, err = s.Get(ctx, "team", "tofu", "tofu-gen-7")
	require.NoError(t, err, "a run of a deleted Job stays readable until job.retention removes it")
	require.Equal(t, v1.ObjectName("tofu-gen-7"), got.Name)
	_, _, err = s.Start(ctx, caller, "tofu", "", nil)
	require.ErrorIs(t, err, local.ErrJobNotFound, "a start on a deleted Job")
}

// ADR-0349 Decision 2: an input is equal to another as a JSON value: objects in any key order, arrays item by item,
// and no input as null.
func TestSameJSONValue(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{`{"a": 1, "b": [1, 2]}`, `{"b":[1,2],"a":1}`, true},
		{`{"a": 1}`, `{"a": 1, "b": null}`, false},
		{`[1, 2]`, `[2, 1]`, false},
		{`"x"`, ` "x" `, true},
		{`1`, `1.0`, false},
		{``, `null`, true},
		{``, `{}`, false},
		{`{"a": {"b": [true]}}`, `{"a": {"b": [false]}}`, false},
	} {
		require.Equal(t, c.same, sameJSONValue(json.RawMessage(c.a), json.RawMessage(c.b)), "%s vs %s", c.a, c.b)
	}
}
