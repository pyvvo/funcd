//go:build e2e

package funcd_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app/template"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// The scenarios of ADR-0350 on the process driver and the real Node shim: App shop declares the Job migrate as it
// declares a Function, with the Bucket state whose prefix tf the Job owns and binds.

// appJobWithin bounds an App's rollout in these scenarios: its Bucket, its Job and the revision's switch.
const appJobWithin = 20 * time.Second

// writesState is a migrate handler that writes its lock through the state binding and returns what it reads back.
const writesState = `export async function handle(ctx) {
  await ctx.blob.put('state', 'lock', new TextEncoder().encode('locked'));
  return { read: new TextDecoder().decode(await ctx.blob.get('state', 'lock')) };
}
`

// shopApp is App shop whose jobs entry migrate runs src, pushed as funcdctl push does.
func (h *shimRig) shopApp(t *testing.T, src string) *v1.App {
	t.Helper()
	ref, digest := funcd.PushJob(t, h.layout, "migrate", src, "")
	return &v1.App{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp},
		ObjectMeta: v1.ObjectMeta{Name: "shop", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.AppSpec{
			Buckets: []v1.BucketEntry{{Name: "state", BucketSpec: v1.BucketSpec{
				Prefixes: []v1.BucketPrefix{{Name: "tf", Owner: v1.JobOwner("migrate")}},
			}}},
			Jobs: []v1.AppJob{{Name: "migrate", JobSpec: v1.JobSpec{
				Runtime: "nodejs22", Handler: "handle", Image: ref, ImageDigest: digest,
				Bindings: v1.FunctionBindings{Blob: []v1.FunctionBlob{{Alias: "state", Bucket: "state", Prefix: "tf"}}},
			}}},
		},
	}
}

func (h *shimRig) getShop(t *testing.T) *v1.App {
	t.Helper()
	obj, err := h.c.Get(context.Background(), v1.KindApp, "default", "shop")
	require.NoError(t, err)
	return obj.(*v1.App)
}

// shopRolledOut waits until App shop is Ready at its generation with its latest revision current, and returns it.
func (h *shimRig) shopRolledOut(t *testing.T) *v1.App {
	t.Helper()
	var a *v1.App
	require.Eventually(t, func() bool {
		a = h.getShop(t)
		c, ok := a.Status.Conditions.Get("Ready")
		return ok && c.Status == v1.ConditionTrue && c.ObservedGeneration == a.Generation && a.Status.LatestRevision != "" &&
			a.Status.CurrentRevision == a.Status.LatestRevision
	}, appJobWithin, 20*time.Millisecond, "App shop rolls out")
	return a
}

// jobChild is the status line of App a's part Job/migrate, the zero line before the App's first pass.
func jobChild(a *v1.App) v1.AppChild {
	for _, c := range a.Status.Children {
		if c.Kind == v1.KindJob && c.Name == "migrate" {
			return c
		}
	}
	return v1.AppChild{}
}

// scenario: app-declares-job — an App whose jobs declares migrate and whose buckets declares state writes the Job
// migrate, which binds state, and the App is Ready once the Job is.
func TestScenarioAppDeclaresJob(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	a := h.shopApp(t, writesState)
	a.Spec.Jobs[0].Runtime = "python314" // no Python shim on this platform: the Job is not Ready
	h.applyObj(t, a)
	var line v1.AppChild
	require.Eventually(t, func() bool {
		line = jobChild(h.getShop(t))
		return line.State == v1.AppChildPending && line.Reason == "RuntimeUnavailable"
	}, appJobWithin, 20*time.Millisecond, "the Job part carries its Job's reason; last line %+v", &line)
	ready, _ := h.getShop(t).Status.Conditions.Get("Ready")
	require.Equal(t, v1.ConditionFalse, ready.Status, "the App waits for its Job")

	a = h.getShop(t)
	a.Spec.Jobs[0].Runtime = "nodejs22"
	h.applyObj(t, a)
	a = h.shopRolledOut(t)
	require.Equal(t, v1.AppChildReady, jobChild(a).State)
	job := h.getJob(t, "migrate")
	c, _ := job.Status.Conditions.Get("Ready")
	require.Equal(t, v1.ConditionTrue, c.Status, "the App is Ready once the Job is")
	require.Equal(t, []v1.FunctionBlob{{Alias: "state", Bucket: "state", Prefix: "tf"}}, job.Spec.Bindings.Blob)
	ctrl, ok := v1.ControllerOf(job.OwnerReferences)
	require.True(t, ok)
	require.Equal(t, v1.ObjectRef{Kind: v1.KindApp, Namespace: "default", Name: "shop"}, ctrl.ObjectRef)
	require.Equal(t, a.UID, ctrl.UID)

	run := h.waitRun(t, h.startRun(t, "migrate", `{}`), v1.JobRunSucceeded)
	require.JSONEq(t, `{"read":"locked"}`, string(run.Status.Result), "the run writes and reads state through its binding")
}

// scenario: app-job-runs-keep-their-revision — an upgrade that changes the image of a Job with a Running run rolls out
// without waiting for the run, which ends on its revision; the next run takes the new one.
func TestScenarioAppJobRunsKeepTheirRevision(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	g, calls := newGate(t), filepath.Join(t.TempDir(), "calls")
	h.applyObj(t, h.shopApp(t, waitsFor(g, calls, "{ v: 1 }")))
	h.shopRolledOut(t)
	run := h.startRun(t, "migrate", `{}`)
	require.Equal(t, v1.ObjectName("migrate-v1"), h.waitRun(t, run, v1.JobRunRunning).Status.JobRevision)

	a := h.getShop(t)
	before := a.Status.CurrentRevision
	ref, digest := funcd.PushJob(t, h.layout, "migrate", "export function handle() { return { v: 2 }; }\n", "")
	a.Spec.Jobs[0].Image, a.Spec.Jobs[0].ImageDigest = ref, digest
	h.applyObj(t, a)
	require.Eventually(t, func() bool {
		a = h.shopRolledOut(t)
		return a.Status.CurrentRevision != before && h.getJob(t, "migrate").Status.CurrentRevision == "migrate-v2"
	}, appJobWithin, 20*time.Millisecond, "the upgrade rolls out to migrate-v2")
	require.Equal(t, v1.JobRunRunning, h.getJobRun(t, run).Status.Phase, "the upgrade waits for no run and cuts none")

	g.open(t)
	ended := h.waitRun(t, run, v1.JobRunSucceeded)
	require.Equal(t, v1.ObjectName("migrate-v1"), ended.Status.JobRevision)
	require.JSONEq(t, `{"v":1}`, string(ended.Status.Result))
	next := h.waitRun(t, h.startRun(t, "migrate", `{}`), v1.JobRunSucceeded)
	require.Equal(t, v1.ObjectName("migrate-v2"), next.Status.JobRevision)
	require.JSONEq(t, `{"v":2}`, string(next.Status.Result))
}

// scenario: app-job-self-heals — a hand edit of the Job migrate of a Ready App is written back from the App's entry.
func TestScenarioAppJobSelfHeals(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.applyObj(t, h.shopApp(t, "export function handle() { return {}; }\n"))
	h.shopRolledOut(t)
	job := h.getJob(t, "migrate")
	job.Spec.Timeout = v1.Duration(time.Minute)
	h.applyObj(t, job)
	require.Eventually(t, func() bool {
		healed := h.getShop(t).Status.LastSelfHeal
		return h.getJob(t, "migrate").Spec.Timeout == 0 && healed != nil && healed.Kind == v1.KindJob && healed.Name == "migrate"
	}, appJobWithin, 20*time.Millisecond, "the App writes its entry back and records it")
}

// scenario: app-delete-removes-job — deleting the App deletes migrate; its run that has not ended ends Cancelled with
// JobDeleted and stays listed.
func TestScenarioAppDeleteRemovesJob(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	g, calls := newGate(t), filepath.Join(t.TempDir(), "calls")
	t.Cleanup(func() { g.open(t) })
	h.applyObj(t, h.shopApp(t, waitsFor(g, calls, "{ done: true }")))
	h.shopRolledOut(t)
	run := h.startRun(t, "migrate", `{}`)
	h.waitRun(t, run, v1.JobRunRunning)

	require.NoError(t, h.c.Delete(context.Background(), v1.KindApp, "default", "shop"))
	require.Eventually(t, func() bool { return h.getJob(t, "migrate") == nil }, appJobWithin, 20*time.Millisecond,
		"the owner GC deletes the App's Job")
	ended := h.waitRun(t, run, v1.JobRunCancelled)
	reason, msg := runReason(ended)
	require.Equal(t, v1.JobRunJobDeleted, reason, msg)
	require.Zero(t, runWorkers(t, h.rt, run), "the run's worker is gone")
	stays(t, func() bool {
		_, err := h.c.Get(context.Background(), v1.KindJobRun, "default", v1.ObjectName(run))
		return err == nil
	}, 300*time.Millisecond, 50*time.Millisecond, "the run stays listed until job.retention")
}

// scenario: app-template-pins-job-image — funcdctl app lock pins the digest of the image a jobs entry names by tag,
// and the rendered and deployed Job carries it.
func TestScenarioAppTemplatePinsJobImage(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	cli := funcdctl(t, e)
	reg := shortDataDir(t)
	writeStep(t, e.src, "1.0.0", `export async function handle() { return { v: 1 }; }`)
	ref := pushStepImage(t, filepath.Join(reg, "shop-migrate"), e.src, "1.0.0")
	pinned, err := artifact.ResolveDigest(e.ctx, ref)
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "resources"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.yaml"), []byte("name: shop\nversion: 1.0.0\nregistry: oci-layout://"+reg+
		"\nimages:\n  migrate: shop-migrate:1.0.0\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "resources", "job.yaml"), []byte(
		"jobs:\n  - name: shop-migrate\n    runtime: nodejs22\n    handler: handle\n    image: ${{ images.migrate }}\n"), 0o600))

	out, err := cli("app", "lock", dir)
	require.NoError(t, err, out)
	lock, err := template.ReadLock(dir)
	require.NoError(t, err)
	require.Equal(t, template.LockedImage{Requested: "shop-migrate:1.0.0", Version: "1.0.0", Digest: pinned}, lock["migrate"])
	out, err = cli("app", "render", dir)
	require.NoError(t, err, out)
	require.Contains(t, out, ref+"@"+pinned, "the rendered Job carries the locked digest")

	out, err = cli("app", "deploy", dir, "--no-wait")
	require.NoError(t, err, out)
	require.Eventually(t, func() bool {
		obj, err := e.c.Get(e.ctx, v1.KindJob, "default", "shop-migrate")
		if fault.KindOf(err) == fault.NotFound {
			return false
		}
		require.NoError(t, err)
		return obj.(*v1.Job).Spec.Image == ref+"@"+pinned
	}, appJobWithin, 20*time.Millisecond, "the deployed Job carries the locked digest")
}
