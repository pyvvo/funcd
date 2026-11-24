package app_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// withMigrate adds the jobs entry todo-migrate, a Job that binds the App's todo-files Bucket (ADR-0350 Decision 1).
func withMigrate(a *v1.App) {
	a.Spec.Jobs = []v1.AppJob{{Name: "todo-migrate", JobSpec: v1.JobSpec{
		Runtime:  "nodejs22",
		Handler:  "handle",
		Image:    "oci-layout://todo-migrate:1",
		Bindings: v1.FunctionBindings{Blob: []v1.FunctionBlob{{Alias: "files", Bucket: "todo-files", Prefix: "attachments"}}},
	}}}
}

func (h *harness) job() *v1.Job { return h.get(v1.KindJob, "todo-migrate").(*v1.Job) }

// ADR-0350 Decisions 1 to 4 on the reconciler: a Job part is written right after the Function parts with the App's
// controller reference, is Ready once its Job is Ready at its generation, counts none of its runs, is updated by an
// upgrade that waits for no run, is written back after a hand edit, and is pruned when its entry is dropped, while
// its runs stay.
func TestAppJobPart(t *testing.T) {
	h := newHarness(t, nil)
	a := h.create(todoApp(withMigrate)).(*v1.App)
	h.reconcile()

	ctrl := v1.OwnerReference{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Namespace: ns, Name: "todo"}, UID: a.UID,
		Controller: true, BlockOwnerDeletion: true}
	require.Equal(t, []v1.OwnerReference{ctrl}, refsOf(h.job()))
	require.Equal(t, v1.ResourceGroupName("todo-rg"), h.job().ResourceGroup)
	var kinds []v1.Kind
	for _, c := range h.app().Status.Children {
		kinds = append(kinds, c.Kind)
	}
	require.Equal(t, []v1.Kind{v1.KindKVStore, v1.KindKVStore, v1.KindBucket, v1.KindBucket, v1.KindFunction, v1.KindJob,
		v1.KindWorkflow, v1.KindRoute}, kinds, "the jobs section comes right after functions")
	require.Equal(t, v1.AppChild{Kind: v1.KindJob, Name: "todo-migrate", State: v1.AppChildPending, Reason: "Progressing"},
		h.child(v1.KindJob, "todo-migrate"), "a Job part waits for its Job's Ready")

	h.markAllReady()
	h.reconcile()
	require.Equal(t, v1.ConditionTrue, h.ready().Status, h.ready().Message)
	require.Equal(t, v1.AppChildReady, h.child(v1.KindJob, "todo-migrate").State)

	run := h.create(&v1.JobRun{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJobRun.GVK().APIVersion(), Kind: v1.KindJobRun},
		ObjectMeta: v1.ObjectMeta{Name: "todo-migrate-1", Namespace: ns, ResourceGroup: "todo-rg"},
		Spec:       v1.JobRunSpec{Job: "todo-migrate"},
		Status:     v1.JobRunStatus{Phase: v1.JobRunRunning, JobRevision: "todo-migrate-v1", JobUID: h.job().UID}})
	h.reconcile()
	require.Equal(t, v1.ConditionTrue, h.ready().Status, "a run is no part of the App")
	require.Len(t, h.app().Status.Children, 8)

	h.edit(func(a *v1.App) { a.Spec.Jobs[0].Image = "oci-layout://todo-migrate:2" })
	h.reconcile()
	require.Equal(t, "oci-layout://todo-migrate:2", h.job().Spec.Image, "an upgrade updates the Job")
	require.Equal(t, v1.AppChildPending, h.child(v1.KindJob, "todo-migrate").State)
	h.markReady(v1.KindJob, "todo-migrate")
	h.reconcile()
	got := h.app()
	require.Equal(t, v1.ConditionTrue, h.ready().Status, h.ready().Message)
	require.Equal(t, got.Status.LatestRevision, got.Status.CurrentRevision, "the upgrade waits for no run")
	require.Equal(t, run.GetObjectMeta().ResourceVersion, h.get(v1.KindJobRun, "todo-migrate-1").GetObjectMeta().ResourceVersion,
		"the App writes no run")

	job := h.job()
	job.Spec.Image = "oci-layout://elsewhere:1"
	h.update(job)
	h.reconcile()
	require.Equal(t, "oci-layout://todo-migrate:2", h.job().Spec.Image, "the App writes its entry back")
	require.Equal(t, v1.KindJob, h.app().Status.LastSelfHeal.Kind)
	require.Equal(t, v1.ObjectName("todo-migrate"), h.app().Status.LastSelfHeal.Name)

	h.markReady(v1.KindJob, "todo-migrate")
	h.edit(func(a *v1.App) { a.Spec.Jobs = nil })
	for range 3 {
		h.reconcile()
	}
	require.Nil(t, h.get(v1.KindJob, "todo-migrate"), "a dropped Job part is pruned")
	require.NotNil(t, h.get(v1.KindJobRun, "todo-migrate-1"), "its runs stay, for the run reconciler to end")
}
