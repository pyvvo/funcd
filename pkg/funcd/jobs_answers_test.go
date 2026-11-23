//go:build !e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	ociartifact "github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/volume"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// job is Job name of default, whose artifact is a file that no runtime reads in these tests.
func job(name v1.ObjectName) *v1.Job {
	j := &v1.Job{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJob.GVK().APIVersion(), Kind: v1.KindJob},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}}
	j.Spec = v1.JobSpec{Runtime: "nodejs22", Handler: "handle", Image: "file:///nonexistent/" + string(name) + ".mjs"}
	return j
}

// jobWithVolumes is j with a local volume of each size: state, then extra-1, extra-2 and so on.
func jobWithVolumes(j *v1.Job, sizes ...int64) *v1.Job {
	j.Spec.Volumes = nil
	for i, size := range sizes {
		name := v1.ObjectName("state")
		if i > 0 {
			name = v1.ObjectName(fmt.Sprintf("extra-%d", i))
		}
		j.Spec.Volumes = append(j.Spec.Volumes, v1.VolumeEntry{Name: name, Type: v1.VolumeLocal, SizeBytes: size})
	}
	return j
}

// jobRun is JobRun name of default, a run of Job j with input when it is not empty.
func jobRun(name, j v1.ObjectName, input string) *v1.JobRun {
	run := &v1.JobRun{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJobRun.GVK().APIVersion(), Kind: v1.KindJobRun},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}}
	run.Spec.Job = j
	if input != "" {
		run.Spec.Input = json.RawMessage(input)
	}
	return run
}

// typedJobImage pushes, to a layout of the test's, an artifact whose contract's input requires the string name, and
// returns its reference and its digest.
func typedJobImage(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "typed.mjs")
	require.NoError(t, os.WriteFile(src, []byte("export function handle() { return {}; }\n"), 0o600))
	ref := "oci-layout://" + filepath.Join(dir, "layout") + ":typed"
	contract := []byte(`{"input":{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}},` +
		`"output":{"type":"object"}}`)
	digest, err := ociartifact.Push(context.Background(), ref, src, contract, "nodejs22", "")
	require.NoError(t, err)
	return ref, digest
}

// The answers of the Job feature's operations, one row per case of each: Job and JobRun (ADR-0347).
func TestJobAnswersMatrix(t *testing.T) {
	t.Parallel()
	e := newAnswerEnv(t)
	typed := job("typed")
	typed.Spec.Image, typed.Spec.ImageDigest = typedJobImage(t) // the file driver resolves no tag, so the digest is set
	for _, obj := range []v1.Object{function("target"), job("hello"), typed, jobRun("run", "hello", "")} {
		create(t, e.c, e.api, obj)
	}
	require.Eventually(t, func() bool {
		var got v1.Job
		return json.Unmarshal(readBody(e.c, e.api, itemPath(typed)), &got) == nil && got.Status.Contract != nil
	}, 20*time.Second, 20*time.Millisecond, "Job typed reads its image's contract")

	root, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	vol := newAnswerEnv(t, funcd.WithFunctionVolumes(volume.Options{Root: root, CapacityBytes: 64 << 20}, true))
	tight := namespace("tight")
	tight.Spec.VolumeBudget = "1Mi"
	create(t, vol.c, vol.api, tight)

	broken := job("hello")
	broken.Spec.Timeout = v1.MaxJobTimeout + v1.Duration(time.Hour)
	run := kindFixture{kind: "JobRun", kinds: "JobRuns", path: "jobruns", name: "run", json: jsonOf(t, jobRun("run", "hello", "")),
		detail: `resourceGroup is required for kind "JobRun"`}
	run.broken = strings.Replace(run.json, `,"resourceGroup":"rg1"`, "", 1)
	const jobs = "/apis/funcd.io/v1alpha1/namespaces/default/jobs"
	const runs = "/apis/funcd.io/v1alpha1/namespaces/default/jobruns"
	jobLinking := func(name, target v1.ObjectName) string {
		j := job(name)
		j.Spec.Bindings.Links = []v1.FunctionLink{{Alias: "l", Target: target}}
		return jsonOf(t, j)
	}
	unbound := func(name v1.ObjectName) string {
		j := job(name)
		j.Spec.Bindings.KV = []v1.FunctionKV{{Alias: "b", Store: "ghost", Table: "t"}}
		return jsonOf(t, j)
	}
	applied := func(j *v1.Job) func(*testing.T, *answerEnv) {
		return func(t *testing.T, e *answerEnv) { create(t, e.c, e.api, j) }
	}
	inTight := func(j *v1.Job) *v1.Job {
		j.Namespace = "tight"
		return j
	}
	big := `"` + strings.Repeat("x", 300<<10) + `"`
	row := func(op, when string, on *answerEnv, a ask) answerRow {
		return answerRow{op: op, when: when, on: on, ask: a}
	}
	rows := slices.Concat(
		kindFixture{kind: "Job", kinds: "Jobs", path: "jobs", name: "hello", json: jsonOf(t, job("hello")),
			broken: jsonOf(t, broken), detail: "spec.timeout 169h is out of bounds"}.sharedRows(e),
		run.sharedRows(e),
		[]answerRow{
			row("createJob", "names a Function that does not exist", e, ask{what: "a link to a missing Function",
				method: http.MethodPost, path: jobs, token: answersDev, body: jobLinking("lonely", "ghost"),
				detail: `spec.bindings.links[l].target "ghost" does not exist in namespace "default"`}),
			row("createJob", "does not declare", e, ask{what: "a binding to an undeclared store", method: http.MethodPost,
				path: jobs, token: answersDev, body: unbound("unbound"), detail: `KVStore "ghost" is not declared in spec.kv`}),
			row("createJob", "volumes are off", e, ask{what: "a volume while volumes are off", method: http.MethodPost,
				path: jobs, token: answersDev, body: jsonOf(t, jobWithVolumes(job("vol"), 1<<20)), detail: "VolumesOff"}),
			{op: "createJob", when: "differs from the size it was applied with", on: vol,
				setup: func(t *testing.T, e *answerEnv) {
					create(t, e.c, e.api, jobWithVolumes(job("resized"), 1<<20))
					ask{what: "the delete of resized", method: http.MethodDelete, path: jobs + "/resized", token: answersAdmin,
						status: http.StatusNoContent}.run(t, e.c, e.api)
				},
				ask: ask{what: "another size for a booked volume", method: http.MethodPost, path: jobs, token: answersDev,
					body: jsonOf(t, jobWithVolumes(job("resized"), 2<<20)), detail: "a volume is not resized"}},
			row("createJob", "does not fit in the capacity", vol, ask{what: "a volume over the capacity",
				method: http.MethodPost, path: jobs, token: answersDev, body: jsonOf(t, jobWithVolumes(job("huge"), 128<<20)),
				detail: "volumes.capacityBytes"}),
			row("createJob", "spec.volumeBudget", vol, ask{what: "a volume over the budget", method: http.MethodPost,
				path: "/apis/funcd.io/v1alpha1/namespaces/tight/jobs", token: answersAdmin,
				body: jsonOf(t, inTight(jobWithVolumes(job("spender"), 2<<20))), detail: "volumeBudgetBytes"}),

			row("replaceJob", "names a Function that does not exist", e, ask{what: "a link to a missing Function",
				method: http.MethodPut, path: jobs + "/hello", token: answersDev, body: jobLinking("hello", "ghost"),
				detail: `target "ghost" does not exist in namespace "default"`}),
			row("replaceJob", "does not declare", e, ask{what: "a binding to an undeclared store", method: http.MethodPut,
				path: jobs + "/hello", token: answersDev, body: unbound("hello"), detail: `KVStore "ghost" is not declared in spec.kv`}),
			row("replaceJob", "volumes are off", e, ask{what: "a volume while volumes are off", method: http.MethodPut,
				path: jobs + "/hello", token: answersDev, body: jsonOf(t, jobWithVolumes(job("hello"), 1<<20)), detail: "VolumesOff"}),
			{op: "replaceJob", when: "differs from the size it was applied with", on: vol,
				setup: applied(jobWithVolumes(job("grower"), 1<<20)),
				ask: ask{what: "another size for its volume", method: http.MethodPut, path: jobs + "/grower", token: answersDev,
					body: jsonOf(t, jobWithVolumes(job("grower"), 2<<20)), detail: "a volume is not resized"}},
			{op: "replaceJob", when: "does not fit in the capacity", on: vol,
				setup: applied(jobWithVolumes(job("widener"), 1<<20)),
				ask: ask{what: "a volume over the capacity", method: http.MethodPut, path: jobs + "/widener", token: answersDev,
					body: jsonOf(t, jobWithVolumes(job("widener"), 1<<20, 128<<20)), detail: "volumes.capacityBytes"}},
			{op: "replaceJob", when: "spec.volumeBudget", on: vol,
				setup: applied(inTight(jobWithVolumes(job("saver"), 512<<10))),
				ask: ask{what: "a volume over the budget", method: http.MethodPut,
					path: "/apis/funcd.io/v1alpha1/namespaces/tight/jobs/saver", token: answersAdmin,
					body: jsonOf(t, inTight(jobWithVolumes(job("saver"), 512<<10, 1<<20))), detail: "volumeBudgetBytes"}},

			row("createJobRun", "larger than job.payloadLimit", e, ask{what: "an input over the payload limit",
				method: http.MethodPost, path: runs, token: answersDev, body: jsonOf(t, jobRun("huge", "hello", big)),
				detail: "exceeds job.payloadLimit 262144"}),
			row("createJobRun", "breaks the input schema", e, ask{what: "an input off the contract", method: http.MethodPost,
				path: runs, token: answersDev, body: jsonOf(t, jobRun("mismatch", "typed", `{}`)),
				detail: "(body.spec.input.name)"}),
			row("replaceJobRun", "fixed once the run is created", e, ask{what: "another input", method: http.MethodPut,
				path: runs + "/run", token: answersDev, body: jsonOf(t, jobRun("run", "hello", `{"x":1}`)),
				detail: "a run's spec.job and spec.input are fixed once it is created"}),
		},
	)
	runMatrix(t, rows)
}

// ADR-0347 Contracts: WithJobs holds the job.* keys to the bounds the daemon config holds them to.
func TestWithJobsBounds(t *testing.T) {
	t.Parallel()
	for _, opt := range []funcd.Option{funcd.WithJobs(0, 1, time.Hour), funcd.WithJobs(1, 0, time.Hour), funcd.WithJobs(1, 1, 0)} {
		_, err := funcd.New(funcd.InMemory(), opt)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
		require.ErrorContains(t, err, "funcd.WithJobs")
	}
}
