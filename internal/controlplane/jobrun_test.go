package controlplane_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

const (
	jobBase    = "/apis/funcd.io/v1alpha1/namespaces/team-a/jobs"
	jobRunBase = "/apis/funcd.io/v1alpha1/namespaces/team-a/jobruns"
)

// newJobServer is the control plane with the JobRun admissions, over a store the test seeds what a reconciler writes.
func newJobServer(t *testing.T, payloadLimit int64) (http.Handler, store.Store) {
	t.Helper()
	st := store.New(memory.New())
	h, err := controlplane.NewServer(controlplane.Deps{
		Store:      st,
		Authorizer: rbac.New(),
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{
			devToken:  {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
			viewToken: {Subject: "obs", Role: auth.RoleViewer, Namespaces: []v1.NamespaceName{"team-a"}},
		}),
		Admissions: []admission.Admission{admission.NewJobRunPayloadAdmission(payloadLimit),
			admission.NewJobRunContractAdmission(st), admission.NewJobRunSpecAdmission(),
			admission.NewJobRunApprovalAdmission(rbac.New())},
	})
	require.NoError(t, err)
	return h, st
}

func jobRunBody(t *testing.T, name, job, input string, status string) []byte {
	t.Helper()
	run := map[string]json.RawMessage{}
	b := func(v interface{}) json.RawMessage {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		return raw
	}
	run["apiVersion"], run["kind"] = b("funcd.io/v1alpha1"), b("JobRun")
	meta := map[string]string{"namespace": "team-a", "resourceGroup": "rg1"}
	if name == "" {
		meta["generateName"] = job + "-"
	} else {
		meta["name"] = name
	}
	run["metadata"] = b(meta)
	spec := map[string]json.RawMessage{"job": b(job)}
	if input != "" {
		spec["input"] = json.RawMessage(input)
	}
	run["spec"] = b(spec)
	if status != "" {
		run["status"] = json.RawMessage(status)
	}
	return b(run)
}

func decodeRun(t *testing.T, body []byte) v1.JobRun {
	t.Helper()
	var run v1.JobRun
	require.NoError(t, json.Unmarshal(body, &run), "%s", body)
	return run
}

// ADR-0347 Decision 3: the API server sets status.startedBy to the caller's identity subject and status.jobUID to the
// Job's UID when the Job exists; a client sets neither, and a replace keeps both.
func TestJobRunCreateRecordsWhoStartedIt(t *testing.T) {
	t.Parallel()
	srv, _ := newJobServer(t, 1024)
	rec := do(t, srv, http.MethodPost, jobRunBase, devToken, jobRunBody(t, "early", "hello", `{"name":"Ada"}`,
		`{"startedBy":"mallory","jobUID":"forged"}`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	early := decodeRun(t, rec.Body.Bytes())
	require.Equal(t, "dev", early.Status.StartedBy, "the caller's subject, never the body's")
	require.Empty(t, early.Status.JobUID, "a run created before its Job has no jobUID")

	jobRec := do(t, srv, http.MethodPost, jobBase, devToken, mustJSON(t, map[string]interface{}{
		"apiVersion": "funcd.io/v1alpha1", "kind": "Job",
		"metadata": map[string]string{"name": "hello", "namespace": "team-a", "resourceGroup": "rg1"},
		"spec":     map[string]string{"runtime": "nodejs22", "handler": "handle", "image": "oci://example/hello:v1"},
	}))
	require.Equal(t, http.StatusOK, jobRec.Code, jobRec.Body.String())
	var job v1.Job
	require.NoError(t, json.Unmarshal(jobRec.Body.Bytes(), &job))
	require.NotEmpty(t, job.UID)

	rec = do(t, srv, http.MethodPost, jobRunBase, devToken, jobRunBody(t, "", "hello", `{"name":"Ada"}`, ""))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	run := decodeRun(t, rec.Body.Bytes())
	require.Regexp(t, `^hello-[0-9a-f]{8}$`, string(run.Name), "generateName <job>- names the run <job>-<id>")
	require.Equal(t, "dev", run.Status.StartedBy)
	require.Equal(t, job.UID, run.Status.JobUID)

	var withCancel map[string]interface{}
	require.NoError(t, json.Unmarshal(jobRunBody(t, string(run.Name), "hello", `{"name":"Ada"}`, `{"startedBy":"mallory"}`), &withCancel))
	withCancel["spec"].(map[string]interface{})["cancel"] = true
	rec = do(t, srv, http.MethodPut, jobRunBase+"/"+string(run.Name), devToken, mustJSON(t, withCancel))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	cancelled := decodeRun(t, rec.Body.Bytes())
	require.True(t, cancelled.Spec.Cancel)
	require.Equal(t, "dev", cancelled.Status.StartedBy, "a replace keeps the recorded status")
	require.Equal(t, job.UID, cancelled.Status.JobUID)

	require.Equal(t, http.StatusForbidden, do(t, srv, http.MethodPost, jobRunBase, viewToken,
		jobRunBody(t, "", "hello", "", "")).Code, "a viewer starts no run")
}

// ADR-0347 Decision 3: admission refuses an input over job.payloadLimit with 413 and one off the Job's status.contract
// with 422 naming the path; a replace may only turn spec.cancel true.
func TestJobRunAdmissionAnswers(t *testing.T) {
	t.Parallel()
	srv, st := newJobServer(t, 32)
	ctx := context.Background()
	obj, _ := v1.NewObject(v1.KindJob)
	job := obj.(*v1.Job)
	job.Name, job.Namespace, job.ResourceGroup = "hello", "team-a", "rg1"
	job.Status.Contract = &v1.WorkflowContract{Input: json.RawMessage(
		`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`)}
	_, err := st.Create(ctx, job)
	require.NoError(t, err)

	rec := do(t, srv, http.MethodPost, jobRunBase, devToken, jobRunBody(t, "", "hello", `{}`, ""))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.Contains(t, problem(t, rec.Body.Bytes()).Detail, "(body.spec.input.name)")
	require.Contains(t, problem(t, rec.Body.Bytes()).Detail, "InputSchemaMismatch")

	rec = do(t, srv, http.MethodPost, jobRunBase, devToken, jobRunBody(t, "", "hello", `{"name":"Ada Lovelace, Countess of Lovelace"}`, ""))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	require.Contains(t, problem(t, rec.Body.Bytes()).Detail, "exceeds job.payloadLimit 32")

	rec = do(t, srv, http.MethodPost, jobRunBase, devToken, jobRunBody(t, "r1", "hello", `{"name":"Ada"}`, ""))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = do(t, srv, http.MethodPut, jobRunBase+"/r1", devToken, jobRunBody(t, "r1", "hello", `{"name":"Bob"}`, ""))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, problem(t, rec.Body.Bytes()).Detail, "spec.job and spec.input are fixed")
}

// ADR-0348 Decisions 3 and 5: a create with an approval is refused with 422; an update that approves the preview of a
// run AwaitingApproval gets status.approvedAt and status.approvedBy from the API server, never from the body, one
// naming another digest 409 with the current one, and a rejection status.rejectedBy.
func TestJobRunApprovalThroughTheAPI(t *testing.T) {
	t.Parallel()
	srv, st := newJobServer(t, 1024)
	ctx := context.Background()
	d1, d2 := "sha256:"+strings.Repeat("1", 64), "sha256:"+strings.Repeat("2", 64)

	var withApproval map[string]interface{}
	require.NoError(t, json.Unmarshal(jobRunBody(t, "early", "hello", "", ""), &withApproval))
	withApproval["spec"].(map[string]interface{})["approve"] = d1
	rec := do(t, srv, http.MethodPost, jobRunBase, devToken, mustJSON(t, withApproval))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "body.spec.approve")

	for _, name := range []string{"tofu-a", "tofu-b"} {
		rec = do(t, srv, http.MethodPost, jobRunBase, devToken, jobRunBody(t, name, "hello", "", ""))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		obj, err := st.Get(ctx, v1.KindJobRun.GVK(), "team-a", v1.ObjectName(name))
		require.NoError(t, err)
		run := obj.(*v1.JobRun)
		run.Status.Phase, run.Status.Preview = v1.JobRunAwaitingApproval, &v1.JobRunPreview{Digest: d2, Document: json.RawMessage(`{}`)}
		_, err = st.Update(ctx, run)
		require.NoError(t, err)
	}
	decide := func(token, name string, edit func(spec map[string]interface{})) *httptest.ResponseRecorder {
		var body map[string]interface{}
		require.NoError(t, json.Unmarshal(jobRunBody(t, name, "hello", "", `{"approvedBy":"mallory","rejectedBy":"mallory"}`), &body))
		edit(body["spec"].(map[string]interface{}))
		return do(t, srv, http.MethodPut, jobRunBase+"/"+name, token, mustJSON(t, body))
	}

	rec = decide(devToken, "tofu-a", func(spec map[string]interface{}) { spec["approve"] = d1 })
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, problem(t, rec.Body.Bytes()).Detail, d2, "the 409 names the current digest")
	require.Equal(t, http.StatusForbidden, decide(viewToken, "tofu-a", func(spec map[string]interface{}) { spec["approve"] = d2 }).Code)

	rec = decide(devToken, "tofu-a", func(spec map[string]interface{}) { spec["approve"] = d2 })
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	approved := decodeRun(t, rec.Body.Bytes())
	require.Equal(t, d2, approved.Spec.Approve)
	require.Equal(t, "dev", approved.Status.ApprovedBy, "the caller's subject, never the body's")
	require.False(t, approved.Status.ApprovedAt.IsZero())
	require.True(t, approved.Approved())

	rec = decide(devToken, "tofu-b", func(spec map[string]interface{}) { spec["reject"], spec["rejectReason"] = true, "too wide" })
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rejected := decodeRun(t, rec.Body.Bytes())
	require.Equal(t, "dev", rejected.Status.RejectedBy)
	require.Empty(t, rejected.Status.ApprovedBy)
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
