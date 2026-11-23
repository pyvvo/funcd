package admission

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/internal/contractcheck"
)

// --- jobrun-payload (ADR-0347 Decision 3): Create on JobRun ------------------------------------------------------

type jobRunPayload struct{ limit int64 }

// NewJobRunPayloadAdmission returns the Validating admission that refuses a JobRun whose spec.input is over limit
// bytes, job.payloadLimit, with 413 (ADR-0148). The input is fixed once the run is created, so a create alone carries
// one to check, and a cancel of an older run never meets a lowered limit.
func NewJobRunPayloadAdmission(limit int64) Admission { return jobRunPayload{limit: limit} }

func (jobRunPayload) Name() string { return "jobrun-payload" }
func (jobRunPayload) Phase() Phase { return Validating }

func (jobRunPayload) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindJobRun.GVK() && op == Create
}

func (a jobRunPayload) Admit(_ context.Context, req Request) (v1.Object, error) {
	run, ok := req.Object.(*v1.JobRun)
	if !ok {
		return req.Object, nil
	}
	if n := int64(len(run.Spec.Input)); n > a.limit {
		return nil, fault.PayloadTooLargef("admission.jobrun-payload", "run input %d bytes exceeds job.payloadLimit %d: "+
			"pass large data by reference, such as the key of an object in a Bucket", n, a.limit)
	}
	return req.Object, nil
}

// --- jobrun-contract (ADR-0347 Decision 3): Create on JobRun -----------------------------------------------------

type jobRunContract struct{ r objectGetter }

// NewJobRunContractAdmission returns the Validating admission that checks a new JobRun's spec.input against the input
// schema in its Job's status.contract, with the rules and bounds of a call's contract check (ADR-0303), and refuses
// one that breaks it with 422, naming each value under body.spec.input. A Job that is missing, unreadable or has no
// contract leaves the input unchecked: the run waits for its Job, and its start checks the input against the revision
// it pins (ADR-0347 Decision 4).
func NewJobRunContractAdmission(r objectGetter) Admission { return jobRunContract{r: r} }

func (jobRunContract) Name() string { return "jobrun-contract" }
func (jobRunContract) Phase() Phase { return Validating }

func (jobRunContract) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindJobRun.GVK() && op == Create
}

func (a jobRunContract) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.jobrun-contract"
	run, ok := req.Object.(*v1.JobRun)
	if !ok {
		return req.Object, nil
	}
	obj, err := a.r.Get(ctx, v1.KindJob.GVK(), run.Namespace, run.Spec.Job)
	if err != nil {
		return req.Object, nil
	}
	job, ok := obj.(*v1.Job)
	if !ok || job.Status.Contract == nil || len(job.Status.Contract.Input) == 0 {
		return req.Object, nil
	}
	v, err := contract.Compile(job.Status.Contract.Input, contract.WithCallBounds(contractcheck.CallBudget, contractcheck.MaxDigits))
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "compile the input schema of Job %q", run.Spec.Job)
	}
	input := run.Spec.Input
	if len(input) == 0 {
		input = json.RawMessage("null") // the call's event carries null data for a run without input
	}
	details, err := v.Validate(input)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "spec.input")
	}
	if len(details) == 0 {
		return req.Object, nil
	}
	errs := make([]error, len(details))
	for i, d := range details {
		errs[i] = &huma.ErrorDetail{Message: d.Message, Location: "body.spec.input" + strings.ReplaceAll(d.Path, "/", ".")}
	}
	return nil, huma.Error422UnprocessableEntity("run input does not match the input schema of Job "+
		string(run.Spec.Job)+" (InputSchemaMismatch)", errs...)
}

// --- jobrun-spec (ADR-0347 Decision 3): Update on JobRun ---------------------------------------------------------

type jobRunSpec struct{}

// NewJobRunSpecAdmission returns the Validating admission that refuses an Update of a JobRun's spec other than turning
// spec.cancel true: spec.job and spec.input are fixed once the run is created, and a cancel is one-way.
func NewJobRunSpecAdmission() Admission { return jobRunSpec{} }

func (jobRunSpec) Name() string { return "jobrun-spec" }
func (jobRunSpec) Phase() Phase { return Validating }

func (jobRunSpec) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindJobRun.GVK() && op == Update
}

func (jobRunSpec) Admit(_ context.Context, req Request) (v1.Object, error) {
	const op = "admission.jobrun-spec"
	oldR, ok := req.Old.(*v1.JobRun)
	if !ok {
		return req.Object, nil
	}
	newR, ok := req.Object.(*v1.JobRun)
	if !ok {
		return req.Object, nil
	}
	was, is := oldR.Spec, newR.Spec
	if was.Cancel && !is.Cancel {
		return nil, fault.Invalidf(op, "JobRun %q: spec.cancel may only turn true, and a cancelled run stays cancelled", newR.Name)
	}
	sameInput := sameJSON(is.Input, was.Input)
	was.Input, is.Input, was.Cancel, is.Cancel = nil, nil, false, false
	if !sameInput || !reflect.DeepEqual(was, is) {
		return nil, fault.Invalidf(op, "JobRun %q: a run's spec.job and spec.input are fixed once it is created: "+
			"start a new run under another name", newR.Name)
	}
	return req.Object, nil
}
