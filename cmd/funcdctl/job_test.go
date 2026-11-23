package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/funclog/logread"
)

const jobManifest = `apiVersion: funcd.io/v1alpha1
kind: Job
metadata:
  name: hello
  namespace: team-a
  resourceGroup: rg1
spec:
  runtime: nodejs22
  handler: handle
  image: oci-layout://layout/hello:1.0.0
  timeout: 2h
  concurrency: 2
---
apiVersion: funcd.io/v1alpha1
kind: JobRun
metadata:
  name: hello-first
  namespace: team-a
  resourceGroup: rg1
spec:
  job: hello
  input:
    name: Ada
`

// ADR-0347: funcdctl applies a Job and a JobRun through the real control plane and reads them back; the server records
// the caller as the run's startedBy and the Job's UID as its jobUID.
func TestCLIApplyJobAndJobRun(t *testing.T) {
	t.Parallel()
	c, _, _ := newTestServerVia(t, nil, func(d *controlplane.Deps) {
		d.Admissions = []admission.Admission{admission.NewJobRunPayloadAdmission(262144),
			admission.NewJobRunContractAdmission(d.Store), admission.NewJobRunSpecAdmission()}
	})
	path := filepath.Join(t.TempDir(), "hello.yaml")
	require.NoError(t, os.WriteFile(path, []byte(jobManifest), 0o600))

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "apply", "-f", path))
	require.Contains(t, out.String(), "applied Job/hello")
	require.Contains(t, out.String(), "applied JobRun/hello-first")

	out.Reset()
	require.NoError(t, execCLI(&out, c, "get", "jobs", "-n", "team-a"))
	require.Contains(t, out.String(), "hello")
	out.Reset()
	require.NoError(t, execCLI(&out, c, "get", "jobrun", "hello-first", "-n", "team-a"))
	require.Contains(t, out.String(), "hello-first")

	ctx := context.Background()
	job, err := c.Get(ctx, v1.KindJob, "team-a", "hello")
	require.NoError(t, err)
	require.Equal(t, v1.Duration(2*time.Hour), job.(*v1.Job).Spec.Timeout)
	require.Equal(t, 2, job.(*v1.Job).Spec.Concurrency)
	obj, err := c.Get(ctx, v1.KindJobRun, "team-a", "hello-first")
	require.NoError(t, err)
	run := obj.(*v1.JobRun)
	require.Equal(t, "dev", run.Status.StartedBy)
	require.Equal(t, job.GetObjectMeta().UID, run.Status.JobUID)
	require.JSONEq(t, `{"name":"Ada"}`, string(run.Spec.Input))

	out.Reset()
	require.NoError(t, execCLI(&out, c, "apply", "-f", path), "a second apply changes nothing it may not")
}

// queriedLogs answers each read with one line that says how it was read: by the run's kind and name, or by its trace.
type queriedLogs struct {
	mu      sync.Mutex
	queries []logread.Query
}

func (q *queriedLogs) Read(_ context.Context, query logread.Query) ([]logread.Line, error) {
	q.mu.Lock()
	q.queries = append(q.queries, query)
	q.mu.Unlock()
	at, body := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC), "own "+query.Kind+"/"+query.Name
	if query.TraceID != "" {
		at, body = at.Add(time.Second), "call "+query.TraceID
	}
	return []logread.Line{{Time: v1.NewTimestamp(at), Severity: "INFO", Body: body, Namespace: query.Namespace}}, nil
}

// ADR-0347 Decision 10: funcdctl job run prints the run's name, generated as <job>-<id> or given; runs lists a Job's runs
// newest first, of one phase with --phase; describe shows the run; logs reads the run's own lines and, by its trace,
// those of the calls it made; cancel sets spec.cancel.
func TestCLIJobVerbs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	logs := &queriedLogs{}
	c, _, st := newTestServerVia(t, nil, func(d *controlplane.Deps) { d.Logs = logs })
	job := &v1.Job{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJob.GVK().APIVersion(), Kind: v1.KindJob},
		ObjectMeta: v1.ObjectMeta{Name: "hello", Namespace: "team-a", ResourceGroup: "rg1"},
		Spec:       v1.JobSpec{Runtime: "nodejs22", Handler: "handle", Image: "oci-layout://layout/hello:1.0.0"}}
	_, err := c.Apply(ctx, job)
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "job", "run", "hello", "-n", "team-a", "--resource-group", "rg1", "--input", `{"name":"Ada"}`))
	first := strings.TrimSpace(out.String())
	require.True(t, strings.HasPrefix(first, "hello-"), first)
	out.Reset()
	time.Sleep(2 * time.Millisecond) // creation times have milliseconds
	require.NoError(t, execCLI(&out, c, "job", "run", "hello", "hello-second", "-n", "team-a", "--resource-group", "rg1"))
	require.Equal(t, "hello-second\n", out.String())

	obj, err := st.Get(ctx, v1.KindJobRun.GVK(), "team-a", v1.ObjectName(first))
	require.NoError(t, err)
	run := obj.(*v1.JobRun)
	require.JSONEq(t, `{"name":"Ada"}`, string(run.Spec.Input))
	run.Status.JobRevision, run.Status.ImageDigest, run.Status.TraceID = "hello-v1", "sha256:abc", "0123456789abcdef0123456789abcdef"
	run.Status.StartedAt = v1.NewTimestamp(time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC))
	run.Status.Result = json.RawMessage(`{"greeting":"hi Ada"}`)
	run.Status.End(v1.JobRunSucceeded, "", "", v1.NewTimestamp(time.Date(2026, 11, 23, 12, 0, 3, 0, time.UTC)))
	_, err = st.Update(ctx, run)
	require.NoError(t, err)

	out.Reset()
	require.NoError(t, execCLI(&out, c, "job", "runs", "hello", "-n", "team-a"))
	listed := out.String()
	require.Less(t, strings.Index(listed, "hello-second"), strings.Index(listed, first), "newest first:\n%s", listed)
	out.Reset()
	require.NoError(t, execCLI(&out, c, "job", "runs", "hello", "-n", "team-a", "--phase", "succeeded"))
	require.Contains(t, out.String(), first)
	require.NotContains(t, out.String(), "hello-second")

	out.Reset()
	require.NoError(t, execCLI(&out, c, "job", "describe", first, "-n", "team-a"))
	for _, want := range []string{"job: hello", "phase: Succeeded", "revision: hello-v1   digest: sha256:abc", "started by: dev",
		"duration: 3s", `result: {"greeting":"hi Ada"}`, "trace: 0123456789abcdef0123456789abcdef", "full logs: funcdctl job logs " + first} {
		require.Contains(t, out.String(), want)
	}

	out.Reset()
	require.NoError(t, execCLI(&out, c, "job", "logs", first, "-n", "team-a"))
	own, call := strings.Index(out.String(), "own JobRun/"+first), strings.Index(out.String(), "call 0123456789abcdef0123456789abcdef")
	require.True(t, own >= 0 && call > own, "the run's own line, then its call's, oldest first:\n%s", out.String())

	out.Reset()
	require.NoError(t, execCLI(&out, c, "job", "cancel", "hello-second", "-n", "team-a"))
	require.Equal(t, "cancel requested for hello-second\n", out.String())
	obj, err = c.Get(ctx, v1.KindJobRun, "team-a", "hello-second")
	require.NoError(t, err)
	require.True(t, obj.(*v1.JobRun).Spec.Cancel)
}
