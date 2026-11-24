//go:build !e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/workernode/local"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// jobEnvs are the platforms that the Job facet's matrix calls: main, whose default namespace holds the Function reader,
// whose worker is no Job's, and start, jobStartEnv's.
type jobEnvs struct {
	main, start *causeEnv
}

// jobCauseRows is the Job facet's matrix over envs: GET /job/run refuses a caller that is no Job's worker (ADR-0348
// Decision 6), and a Function's start and read of a bound Job's runs answer each cause they declare (ADR-0349
// Decision 4).
func jobCauseRows(envs jobEnvs) []causeRow {
	const start, read = "POST /job/{alias}", "GET /job/{alias}/{run}"
	post := func(path, body string) causeRequest { return causeRequest{method: http.MethodPost, path: path, body: body} }
	get := func(path string) causeRequest { return causeRequest{method: http.MethodGet, path: path} }
	row := func(endpoint string, cause *fault.KnownCause, caller v1.ObjectName, req causeRequest, before ...causeRequest) causeRow {
		return causeRow{endpoint: endpoint, cause: cause, env: envs.start, caller: caller, before: before, request: req}
	}
	return []causeRow{
		{endpoint: "GET /job/run", cause: local.ErrNotAJobWorker, env: envs.main, caller: "reader",
			request: causeRequest{method: http.MethodGet, path: "/job/run"}},
		row(start, local.ErrBodyTooLarge, "planner", post("/job/stack", `{"input": "`+strings.Repeat("x", 1<<20)+`"}`)),
		row(start, local.ErrBodyUnreadable, "planner", causeRequest{
			raw: "POST /job/stack HTTP/1.1\r\nHost: local\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n"}),
		row(start, local.ErrJobNoBinding, "planner", post("/job/other", `{}`)),
		row(start, local.ErrJobDenied, "denied", post("/job/stack", `{}`)),
		row(start, local.ErrJobStartBodyInvalid, "planner", post("/job/stack", `{"input":`)),
		row(start, local.ErrJobNotFound, "planner", post("/job/gone", `{}`)),
		row(start, local.ErrJobRunNameTaken, "planner", post("/job/stack", `{"input": {"n": 2}, "name": "taken"}`),
			post("/job/stack", `{"input": {"n": 1}, "name": "taken"}`)),
		row(start, local.ErrJobInputTooLarge, "planner", post("/job/stack", `{"input": "`+strings.Repeat("x", 262144)+`"}`)),
		row(start, local.ErrJobInputRefused, "planner", post("/job/typed", `{"input": {}}`)),
		row(read, local.ErrJobNoBinding, "planner", get("/job/other/tofu-1")),
		row(read, local.ErrJobDenied, "denied", get("/job/stack/tofu-1")),
		row(read, local.ErrJobRunNotFound, "planner", get("/job/stack/tofu-9")),
	}
}

// TestJobCauseMatrix provokes each cause that a Job endpoint declares with one request.
func TestJobCauseMatrix(t *testing.T) {
	t.Parallel()
	p := answersPlatform(t)
	create(t, answersClient(), "http://"+p.Addr(), function("reader"))
	runCauseMatrix(t, jobCauseRows(jobEnvs{main: &causeEnv{p: p}, start: jobStartEnv(t)}))
}

// jobMatrixContract is the input schema that the Job typed's status holds in the start env: an object with a name.
const jobMatrixContract = `{"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}}}`

// contractJobs is a store whose Job typed reads back with jobMatrixContract in its status, Ready at its generation, as
// a Job whose image carries that contract is: the run admission checks a start's input against it.
type contractJobs struct{ store.Store }

func (s contractJobs) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	obj, err := s.Store.Get(ctx, gvk, ns, name)
	if job, ok := obj.(*v1.Job); ok && err == nil && name == "typed" {
		job.Status.Contract = &v1.WorkflowContract{Input: json.RawMessage(jobMatrixContract)}
		job.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionTrue, ObservedGeneration: job.Generation})
	}
	return obj, err
}

// jobStartEnv runs a platform whose default namespace holds the start matrix's objects: the Jobs tofu and typed, which
// have no handler, so neither is Ready and no run of them starts on this platform, which runs no worker; planner binds
// tofu as stack, typed as typed and the missing Job gone as gone; denied binds tofu as stack, and a Policy forbids it
// job::start and job::read.
func jobStartEnv(t *testing.T) *causeEnv {
	t.Helper()
	p := answersPlatform(t, funcd.WithStore(contractJobs{store.New(memory.New())}))
	c, base := answersClient(), "http://"+p.Addr()
	for _, name := range []v1.ObjectName{"tofu", "typed"} {
		create(t, c, base, &v1.Job{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJob.GVK().APIVersion(), Kind: v1.KindJob},
			ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
			Spec:       v1.JobSpec{Runtime: "nodejs22", Image: "file:///nonexistent/" + string(name) + ".mjs"}})
	}
	planner := function("planner")
	planner.Spec.Bindings.Job = []v1.FunctionJob{{Alias: "stack", Job: "tofu"}, {Alias: "typed", Job: "typed"},
		{Alias: "gone", Job: "missing"}}
	denied := function("denied")
	denied.Spec.Bindings.Job = []v1.FunctionJob{{Alias: "stack", Job: "tofu"}}
	create(t, c, base, planner)
	create(t, c, base, denied)
	create(t, c, base, &v1.Policy{TypeMeta: v1.TypeMeta{APIVersion: v1.KindPolicy.GVK().APIVersion(), Kind: v1.KindPolicy},
		ObjectMeta: v1.ObjectMeta{Name: "no-tofu", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.PolicySpec{Cedar: `forbid(principal == Function::"default/denied", action == Action::"job::start", resource);` + "\n" +
			`forbid(principal == Function::"default/denied", action == Action::"job::read", resource);`}})
	return &causeEnv{p: p}
}
