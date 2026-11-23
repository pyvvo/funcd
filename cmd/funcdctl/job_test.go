package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
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
