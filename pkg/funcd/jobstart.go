package funcd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	huma "github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/internal/contractcheck"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/services"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// jobGrants is the Job facet's binding and Cedar check (ADR-0349 Decisions 1 and 4): the caller's spec.bindings.job
// names the Job under the alias, and the PDP answers the action on it as the caller's principal.
type jobGrants struct {
	store store.Store
	authz auth.Authorizer
}

var _ local.JobGrants = jobGrants{}

// Grant returns the Job that caller binds under alias once Cedar allows action to it: ErrJobNoBinding with no such
// alias, ErrJobDenied on a deny, as a forbid Policy's.
func (g jobGrants) Grant(ctx context.Context, caller local.Ref, alias string, action auth.Action) (v1.ObjectName, error) {
	const op = "funcd.jobGrants.Grant"
	job, err := g.bound(ctx, caller, alias)
	if err != nil {
		return "", err
	}
	dec, err := g.authz.Authorize(ctx, auth.Request{
		Identity: auth.Identity{Subject: caller.String(), Principal: services.Principal(caller.Namespace, caller.Workload())},
		Action:   action, Resource: &auth.EntityRef{Type: v1.KindJob, Namespace: caller.Namespace, Name: job}})
	if err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), op, "authorize %s", action)
	}
	if !dec.Allowed {
		return "", local.ErrJobDenied.Errorf(op, "%s may not %s on Job %q: %s", caller, action, job, dec.Reason)
	}
	return job, nil
}

// bound is the Job that caller's spec.bindings.job names under alias, read from its current spec. A Job's worker binds
// none, since a Job's spec refuses the list, and neither does a caller that is not stored.
func (g jobGrants) bound(ctx context.Context, caller local.Ref, alias string) (v1.ObjectName, error) {
	const op = "funcd.jobGrants.bound"
	bindings, err := services.CallerBindings(ctx, g.store, caller.Namespace, caller.Workload())
	switch fault.KindOf(err) {
	case fault.NotFound, fault.Forbidden:
		return "", local.ErrJobNoBinding.Errorf(op, "%s binds no Job under the alias %q", caller, alias)
	}
	if err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), op, "read the bindings of %s", caller)
	}
	for _, b := range bindings.Job {
		if b.Alias == alias {
			return b.Job, nil
		}
	}
	return "", local.ErrJobNoBinding.Errorf(op, "%s binds no Job under the alias %q", caller, alias)
}

// jobRunStarter creates and reads the runs that a Function starts (ADR-0349 Decisions 2 and 3). A create passes the
// control plane's admission pipeline, as a person's run does (ADR-0347 Decision 3); its startedBy is the caller's
// Cedar principal.
type jobRunStarter struct {
	store store.Store
	// admit is the control plane's admission pipeline, set once the platform's admissions are built, before any
	// worker runs.
	admit *admission.Pipeline
}

var _ local.JobRunStarter = (*jobRunStarter)(nil)

// Start creates a run of job named name, or <job>-<id> when name is empty. A name that a run of job with an equal input
// holds answers that run, created false, so a start sent again starts nothing; a name another run holds is
// ErrJobRunNameTaken.
func (s *jobRunStarter) Start(ctx context.Context, caller local.Ref, job, name v1.ObjectName, input json.RawMessage) (v1.ObjectName, bool, error) {
	const op = "funcd.jobRunStarter.Start"
	if s == nil || s.admit == nil {
		return "", false, fault.Internalf(op, "the admission pipeline is not built yet")
	}
	j, err := s.job(ctx, caller.Namespace, job)
	if err != nil {
		return "", false, err
	}
	run := &v1.JobRun{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJobRun.GVK().APIVersion(), Kind: v1.KindJobRun},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: caller.Namespace, ResourceGroup: j.ResourceGroup},
		Spec:       v1.JobRunSpec{Job: job, Input: input}}
	if name == "" {
		run.GenerateName = string(job) + "-"
		run.Name = v1.GenerateObjectName(run.GenerateName)
	}
	principal := cedarPrincipal(caller)
	admitted, err := s.admit.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindJobRun.GVK(), Object: run,
		Identity: auth.Identity{Subject: principal, Principal: services.Principal(caller.Namespace, caller.Workload())}})
	if err != nil {
		return "", false, admissionRefusal(op, j, input, err)
	}
	created, ok := admitted.(*v1.JobRun)
	if !ok {
		return "", false, fault.Internalf(op, "the admitted object is no JobRun")
	}
	created.Status = v1.JobRunStatus{StartedBy: principal, JobUID: j.UID}
	out, err := s.store.Create(ctx, created)
	if err == nil {
		return out.GetName(), true, nil
	}
	if fault.KindOf(err) != fault.Conflict || name == "" {
		return "", false, fault.Wrapf(err, fault.KindOf(err), op, "create run %q of Job %q", run.Name, job)
	}
	return s.held(ctx, caller.Namespace, job, name, input)
}

// held answers a start whose name a run already holds: that run when it is job's with an equal input, else
// ErrJobRunNameTaken, which says no more than that the namespace holds the name.
func (s *jobRunStarter) held(ctx context.Context, ns v1.NamespaceName, job, name v1.ObjectName, input json.RawMessage) (v1.ObjectName, bool, error) {
	const op = "funcd.jobRunStarter.Start"
	obj, err := s.store.Get(ctx, v1.KindJobRun.GVK(), ns, name)
	if err != nil && fault.KindOf(err) != fault.NotFound {
		return "", false, fault.Wrapf(err, fault.KindOf(err), op, "read run %q", name)
	}
	if run, ok := obj.(*v1.JobRun); ok && err == nil && run.Spec.Job == job && sameJSONValue(run.Spec.Input, input) {
		return name, false, nil
	}
	return "", false, local.ErrJobRunNameTaken.Errorf(op, "the namespace %q holds a run named %q", ns, name)
}

// Get returns run of namespace ns when it is a run of job: ErrJobRunNotFound for another Job's run, and for a run that
// is gone, as job.retention removes one.
func (s *jobRunStarter) Get(ctx context.Context, ns v1.NamespaceName, job, run v1.ObjectName) (v1.JobRun, error) {
	const op = "funcd.jobRunStarter.Get"
	obj, err := s.store.Get(ctx, v1.KindJobRun.GVK(), ns, run)
	if fault.KindOf(err) == fault.NotFound {
		return v1.JobRun{}, local.ErrJobRunNotFound.Errorf(op, "no run %q of Job %q", run, job)
	}
	if err != nil {
		return v1.JobRun{}, fault.Wrapf(err, fault.KindOf(err), op, "read run %q", run)
	}
	r, ok := obj.(*v1.JobRun)
	if !ok || r.Spec.Job != job {
		return v1.JobRun{}, local.ErrJobRunNotFound.Errorf(op, "no run %q of Job %q", run, job)
	}
	return *r, nil
}

// job reads Job name of ns: ErrJobNotFound when it is gone, so a binding of a missing Job starts nothing.
func (s *jobRunStarter) job(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*v1.Job, error) {
	const op = "funcd.jobRunStarter.Start"
	obj, err := s.store.Get(ctx, v1.KindJob.GVK(), ns, name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, local.ErrJobNotFound.Errorf(op, "no Job %q in namespace %q", name, ns)
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "read Job %q", name)
	}
	j, ok := obj.(*v1.Job)
	if !ok {
		return nil, fault.Internalf(op, "object %q is no Job", name)
	}
	return j, nil
}

// cedarPrincipal is caller as Cedar names it, Function::"<ns>/<name>": the startedBy of a run it starts (ADR-0349
// Decision 2).
func cedarPrincipal(caller local.Ref) string {
	return fmt.Sprintf("%s::%q", caller.Kind, string(caller.Namespace)+"/"+string(caller.Name))
}

// admissionRefusal maps the admission's refusal of a run to the start's answer (ADR-0349 Decision 4): an input off the
// Job's contract is the 422 that a Function's call answers for it, a ContractViolation that the facet relays as it is,
// as invoke relays a target's (the decider, 2026-11-24); an input over job.payloadLimit is ErrJobInputTooLarge, and any
// other refusal of the body ErrJobStartBodyInvalid.
func admissionRefusal(op string, job *v1.Job, input json.RawMessage, err error) error {
	var refused huma.StatusError
	switch {
	case errors.As(err, &refused) && refused.GetStatus() == http.StatusUnprocessableEntity:
		return contractRefusal(op, job, input, refused)
	case fault.KindOf(err) == fault.PayloadTooLarge:
		return local.ErrJobInputTooLarge.Errorf(op, "%v", err)
	case fault.KindOf(err) == fault.Invalid:
		return local.ErrJobStartBodyInvalid.Errorf(op, "%v", err)
	}
	return fault.Wrapf(err, fault.KindOf(err), op, "admit the run")
}

// contractRefusal is the 422 of an input off job's contract: the admission's message, and each value that breaks the
// contract from the check the admission ran (admission.JobRunInputDetails).
func contractRefusal(op string, job *v1.Job, input json.RawMessage, refused huma.StatusError) error {
	details, err := admission.JobRunInputDetails(job, input)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "check the run input")
	}
	violation := contractcheck.ContractViolation{Error: admission.JobRunInputMismatch(job.Name), Details: details}
	if len(details) == 0 { // the Job changed since its admission: the admission's text names what broke
		violation = contractcheck.ContractViolation{Error: refused.Error(), Details: []contract.Detail{}}
	}
	body, err := json.Marshal(violation)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "encode the refusal")
	}
	return &local.UpstreamError{Status: http.StatusUnprocessableEntity, Body: body}
}

// sameJSONValue reports whether a and b hold the same JSON value: an object equal key by key in any order, an array
// item by item, a string by its decoded text, whatever its escapes, any other value as the same compact text. An
// empty value is null, as a run with no input calls its handler with null data.
func sameJSONValue(a, b json.RawMessage) bool {
	a, b = nullIfEmpty(a), nullIfEmpty(b)
	switch {
	case a[0] == '{' && b[0] == '{':
		var ma, mb map[string]json.RawMessage
		if json.Unmarshal(a, &ma) != nil || json.Unmarshal(b, &mb) != nil || len(ma) != len(mb) {
			return false
		}
		for k, va := range ma {
			if vb, ok := mb[k]; !ok || !sameJSONValue(va, vb) {
				return false
			}
		}
		return true
	case a[0] == '[' && b[0] == '[':
		var la, lb []json.RawMessage
		if json.Unmarshal(a, &la) != nil || json.Unmarshal(b, &lb) != nil || len(la) != len(lb) {
			return false
		}
		for i := range la {
			if !sameJSONValue(la[i], lb[i]) {
				return false
			}
		}
		return true
	}
	var sa, sb string
	if json.Unmarshal(a, &sa) == nil && json.Unmarshal(b, &sb) == nil {
		return sa == sb // the store writes &, < and > in a string as \u0026, \u003c and \u003e
	}
	var ca, cb bytes.Buffer
	return json.Compact(&ca, a) == nil && json.Compact(&cb, b) == nil && bytes.Equal(ca.Bytes(), cb.Bytes())
}

func nullIfEmpty(v json.RawMessage) json.RawMessage {
	if v = bytes.TrimSpace(v); len(v) == 0 {
		return json.RawMessage("null")
	}
	return v
}
