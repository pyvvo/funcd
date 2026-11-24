package local

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// A Function starts a Job's run through its spec.bindings.job and reads it back (ADR-0349): the facet resolves the
// alias and asks Cedar through JobGrants, as the add-on facet's Addon.Target makes both checks (ADR-0338), then starts
// or reads the run through JobRunStarter.

// JobRunStarter starts and reads the runs of the Job a binding names (ADR-0349 Decisions 2 and 3).
type JobRunStarter interface {
	// Start creates a run of job through ADR-0347's admission, named name or <job>-<id>, started by caller. A name a
	// run of job with the same input holds returns that run with created false; one another run holds is
	// ErrJobRunNameTaken.
	Start(ctx context.Context, caller Ref, job v1.ObjectName, name v1.ObjectName, input json.RawMessage) (run v1.ObjectName, created bool, err error)
	// Get returns run of namespace ns when it is a run of job; ErrJobRunNotFound otherwise.
	Get(ctx context.Context, ns v1.NamespaceName, job, run v1.ObjectName) (v1.JobRun, error)
}

// JobGrants checks a caller's right to act on the Job that one of its aliases binds (ADR-0349 Decisions 1 and 4).
type JobGrants interface {
	// Grant returns the Job that caller binds under alias once Cedar allows action to caller on it: ErrJobNoBinding for
	// an alias the caller does not bind, ErrJobDenied when Cedar denies the action.
	Grant(ctx context.Context, caller Ref, alias string, action auth.Action) (v1.ObjectName, error)
}

// The causes of a start and a read (ADR-0349 Decision 4), each declared on its endpoint (ADR-0330).
var (
	ErrJobNoBinding = fault.NewKnownCause(fault.Forbidden, "job.no-binding",
		"The Function binds no Job under the alias.",
		"Start or read a run under an alias of spec.bindings.job, or add the alias there.")
	ErrJobDenied = fault.NewKnownCause(fault.Forbidden, "job.denied",
		"Cedar denies the Function job::start or job::read on the bound Job, as when a namespace Policy forbids it.",
		"Ask the operator to lift the forbid.")
	ErrJobNotFound = fault.NewKnownCause(fault.NotFound, "job.job-not-found",
		"The Job that the alias binds does not exist.",
		"Create the Job, or bind a Job that exists.")
	ErrJobRunNotFound = fault.NewKnownCause(fault.NotFound, "job.run-not-found",
		"No run of the bound Job has the name: the name is another Job's run's, or job.retention removed the run.",
		"Read a run that ctx.job.start returned, before job.retention removes it.")
	ErrJobRunNameTaken = fault.NewKnownCause(fault.Conflict, "job.run-name-taken",
		"Another run of the namespace holds the name: a run of another Job, or one with another input.",
		"Start the run under another name, or with no name, so that funcd names it.")
	ErrJobInputTooLarge = fault.NewKnownCause(fault.PayloadTooLarge, "job.input-too-large",
		"The run's input is larger than job.payloadLimit.",
		"Pass large data by reference, such as the key of an object in a Bucket.")
	ErrJobInputRefused = fault.NewKnownCause(fault.Invalid, "job.input-refused",
		"The run's input does not match the input schema of the Job's contract; the message names each path.",
		"Send an input that the Job's contract accepts.")
	ErrJobStartBodyInvalid = fault.NewKnownCause(fault.Invalid, "job.start-body-invalid",
		`The body of the start is not {"input": <JSON>, "name": "<run>"}, or the name is not a DNS-1123 name.`,
		"Send the input as JSON, and a name of lowercase letters, digits and dashes, or no name.")
)

// jobStart is the body of POST /job/{alias}; jobStarted its answer.
type jobStart struct {
	Input json.RawMessage `json:"input"`
	Name  v1.ObjectName   `json:"name"`
}

type jobStarted struct {
	Run v1.ObjectName `json:"run"`
}

// JobRunState is the body of GET /job/{alias}/{run}: where the run is, why, its times and, once it Succeeded, its
// result; a time or the result it does not have yet is null.
type JobRunState struct {
	Name      v1.ObjectName   `json:"name"`
	Phase     v1.JobRunPhase  `json:"phase"`
	Reason    string          `json:"reason"`
	StartedAt *v1.Timestamp   `json:"startedAt"`
	EndedAt   *v1.Timestamp   `json:"endedAt"`
	Result    json.RawMessage `json:"result"`
}

// registerJobStart adds POST /job/{alias} and GET /job/{alias}/{run}, each declaring its causes.
func registerJobStart(mux facetMux, caller Ref, starter JobRunStarter, grants JobGrants, logger *slog.Logger) {
	start := []*fault.KnownCause{ErrBodyTooLarge, ErrBodyUnreadable, ErrJobNoBinding, ErrJobDenied, ErrJobStartBodyInvalid,
		ErrJobNotFound, ErrJobRunNameTaken, ErrJobInputTooLarge, ErrJobInputRefused}
	read := []*fault.KnownCause{ErrJobNoBinding, ErrJobDenied, ErrJobRunNotFound}
	handle(mux, logger, "POST /job/{alias}", start, func(w http.ResponseWriter, r *http.Request) error {
		const op = "workernode.local.job.start"
		alias := r.PathValue("alias")
		body, err := readBody(w, r, op, maxInvokeBytes)
		if err != nil {
			return err
		}
		job, err := grants.Grant(r.Context(), caller, alias, auth.ActionJobStart)
		if err != nil {
			logger.Warn("job start refused", "caller", caller.String(), "alias", alias, "err", err.Error())
			return err
		}
		in, err := startOf(op, body)
		if err != nil {
			return err
		}
		run, created, err := starter.Start(r.Context(), caller, job, in.Name, in.Input)
		if err != nil {
			logger.Warn("job start failed", "caller", caller.String(), "alias", alias, "job", job, "err", err.Error())
			return err
		}
		logger.Info("job run started", "caller", caller.String(), "alias", alias, "job", job, "run", run, "created", created)
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJob(w, status, jobStarted{Run: run})
		return nil
	})
	handle(mux, logger, "GET /job/{alias}/{run}", read, func(w http.ResponseWriter, r *http.Request) error {
		alias, name := r.PathValue("alias"), v1.ObjectName(r.PathValue("run"))
		job, err := grants.Grant(r.Context(), caller, alias, auth.ActionJobRead)
		if err != nil {
			logger.Warn("job read refused", "caller", caller.String(), "alias", alias, "err", err.Error())
			return err
		}
		run, err := starter.Get(r.Context(), caller.Namespace, job, name)
		if err != nil {
			return err
		}
		writeJob(w, http.StatusOK, stateOf(&run))
		return nil
	})
}

// startOf decodes a start's body: its input as given, JSON null as no input, and a name that is a DNS-1123 name or
// empty.
func startOf(op string, body []byte) (jobStart, error) {
	var in jobStart
	if err := json.Unmarshal(body, &in); err != nil {
		return jobStart{}, ErrJobStartBodyInvalid.Errorf(op, "the body is not a JSON object of input and name: %v", err)
	}
	if bytes.Equal(bytes.TrimSpace(in.Input), []byte("null")) {
		in.Input = nil
	}
	if in.Name != "" {
		if err := in.Name.Validate(); err != nil {
			return jobStart{}, ErrJobStartBodyInvalid.Errorf(op, "the run name %q is not a DNS-1123 name", in.Name)
		}
	}
	return in, nil
}

// stateOf is run as a read answers it. A run that no pass has reached yet has no phase, and is Pending; its reason is
// its Ready condition's.
func stateOf(run *v1.JobRun) JobRunState {
	st := JobRunState{Name: run.Name, Phase: run.Status.Phase}
	if st.Phase == "" {
		st.Phase = v1.JobRunPending
	}
	if c, ok := run.Status.Conditions.Get("Ready"); ok {
		st.Reason = c.Reason
	}
	if !run.Status.StartedAt.IsZero() {
		st.StartedAt = new(run.Status.StartedAt)
	}
	if !run.Status.EndedAt.IsZero() {
		st.EndedAt = new(run.Status.EndedAt)
	}
	if run.Status.Phase == v1.JobRunSucceeded && len(run.Status.Result) > 0 {
		st.Result = slices.Clone(run.Status.Result)
	}
	return st
}

func writeJob[T jobStarted | JobRunState](w http.ResponseWriter, status int, v T) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
