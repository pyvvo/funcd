//go:build dev

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// shopTemplate is App shop's template: its one part is the Job migrate, built from build/migrate.mjs.
func shopTemplate(t *testing.T) string {
	t.Helper()
	return devProject(t, map[string]string{
		"app/app.yaml": "name: shop\nversion: 1.0.0\nregistry: registry.example\nimages:\n  migrate: shop-migrate:1.0.0\n",
		"app/resources/job.yaml": "jobs:\n  - name: migrate\n    runtime: nodejs22\n    handler: handle\n" +
			"    image: ${{ images.migrate }}\n    approval:\n      bypass: true\n",
		"build/migrate.mjs":           "export function handle() { return { v: 1 }; }\n",
		"build/migrate.contract.yaml": "runtime: nodejs22\ninput: {}\noutput: {}\n",
	})
}

// runJob starts a run of Job job on r's platform and waits until it ends, then returns it.
func (r *appRun) runJob(t *testing.T, job v1.ObjectName) *v1.JobRun {
	t.Helper()
	ctx := context.Background()
	created, err := r.inst.client.Create(ctx, &v1.JobRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindJobRun.GVK().APIVersion(), Kind: v1.KindJobRun},
		ObjectMeta: v1.ObjectMeta{GenerateName: string(job) + "-", Namespace: devNamespace, ResourceGroup: devResourceGroup},
		Spec:       v1.JobRunSpec{Job: job, Input: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)
	var run *v1.JobRun
	r.eventually(t, func() bool {
		obj, err := r.inst.client.Get(ctx, v1.KindJobRun, devNamespace, created.GetName())
		if err != nil {
			return false
		}
		run = obj.(*v1.JobRun)
		return run.Status.Phase.Ended()
	}, "the run of "+string(job)+" ends")
	require.Equal(t, v1.JobRunSucceeded, run.Status.Phase, "%+v", run.Status.Conditions)
	return run
}

// scenario: app-dev-builds-job — funcdctl dev runs the Job part migrate from its local build, and once the source
// changes, dev rebuilds the image and the next run takes the new revision.
func TestScenarioAppDevBuildsJob(t *testing.T) {
	requireNode(t)
	t.Parallel()
	root := shopTemplate(t)
	r := startApp(t, root, devConfig{builds: []string{"build"}})
	r.settled(t, "shop")
	first := r.runJob(t, "migrate")
	require.JSONEq(t, `{"v":1}`, string(first.Status.Result))
	require.Equal(t, v1.ObjectName("migrate-v1"), first.Status.JobRevision)

	replaceIn(t, filepath.Join(root, "build", "migrate.mjs"), "v: 1", "v: 2")
	r.eventually(t, func() bool {
		obj, err := r.inst.client.Get(context.Background(), v1.KindJob, devNamespace, "migrate")
		return err == nil && obj.(*v1.Job).Status.CurrentRevision == "migrate-v2"
	}, "dev rebuilds migrate and the Job stamps migrate-v2")
	next := r.runJob(t, "migrate")
	require.JSONEq(t, `{"v":2}`, string(next.Status.Result))
	require.Equal(t, v1.ObjectName("migrate-v2"), next.Status.JobRevision)
}

// ADR-0350 Decision 6: a Job part's build picks the shims as a Function part's does, so dev refuses to start without
// the interpreter of its runtime (devShimOptions) rather than fail each run.
func TestAppDevJobBuildPicksShims(t *testing.T) {
	t.Parallel()
	const digest = "sha256:0000000000000000000000000000000000000000000000000000000000000001"
	build := devBuild{name: "migrate", entry: "migrate.py"}
	s := &devAppSession{images: map[string]*devImage{"migrate": {part: partJob, runtime: "python314", build: build, digest: digest}}}
	app := &v1.App{Spec: v1.AppSpec{Jobs: []v1.AppJob{
		{Name: "migrate", JobSpec: v1.JobSpec{Runtime: "python314", Image: "oci-layout://x/shop-migrate:1.0.0@" + digest}},
		{Ref: "elsewhere"},
	}}}
	require.Equal(t, []devBuild{build}, s.platformFolder(app, nil).builds())
}
