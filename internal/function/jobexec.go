package function

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/invoke"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	"github.com/pyvvo/funcd/internal/platform/observability"
	"github.com/pyvvo/funcd/internal/runtime"
)

// runPoll is how often an executor reads the runtime while it waits for its run's name to be free and for its worker
// to listen and become ready.
const runPoll = 50 * time.Millisecond

// execute is a run's executor (ADR-0347 Decisions 5 and 6). Once no worker of the run's name remains, it creates one
// worker of the pinned revision, as a solo Function worker of that runtime is built, waits for it to listen and
// become ready within runtime.bootTimeout, and calls it once through the invoke chain. The Job's timeout runs from the
// worker's start. The worker is stopped and removed at every end, at once when ctx is cancelled; the answer of a
// cancelled executor is never written.
func (j *JobRunReconciler) execute(ctx context.Context, run *v1.JobRun, job *v1.Job, rev *v1.Revision) runAnswer {
	r := j.r
	w := runWorkload(job, rev)
	id := runtime.NewInstanceID(run.Namespace, run.Name, rev.Name, 0)
	if err := j.nameFree(ctx, run); err != nil {
		return startFailed(err)
	}
	env, err := r.bindings(ctx, w)
	switch {
	case err != nil:
		return startFailed(err)
	case env.gate != nil:
		return startFailed(errors.New(env.gate.message))
	}
	path, err := r.materialize(ctx, w.fn())
	if err != nil {
		return startFailed(err)
	}
	wo := runWorker(run, rev)
	defer r.removeSocket(callerRef(w, wo)) // after the worker: its socket goes with the run (ADR-0351 Decision 2)
	spec, err := r.workerSpec(ctx, w, rev.Name, wo, path, env.worker)
	if err != nil {
		return startFailed(err)
	}
	spec.Revision = rev.Name
	if _, err := r.runtime.Create(ctx, spec); err != nil {
		if rerr := r.releaseEmptyDirs(context.WithoutCancel(ctx), id); rerr != nil {
			r.logger.WarnContext(ctx, "could not release a run's emptyDirs", "run", run.Name, "error", rerr)
		}
		return startFailed(err)
	}
	defer j.removeWorker(ctx, id)
	if ctx.Err() != nil {
		return runAnswer{phase: v1.JobRunCancelled, reason: v1.JobRunCancelledReason} // its worker goes at once
	}
	if err := r.runtime.Start(ctx, id); err != nil {
		return startFailed(err)
	}
	callCtx, stop := ctx, context.CancelFunc(func() {})
	if t := time.Duration(job.Spec.Timeout); t > 0 {
		callCtx, stop = context.WithTimeout(ctx, t)
	}
	defer stop()
	in, err := j.ready(callCtx, id)
	if a, over := timedOut(ctx, callCtx, job); over {
		return a
	}
	if err != nil {
		return startFailed(err)
	}
	a := j.call(callCtx, run, job, rev, in)
	if t, over := timedOut(ctx, callCtx, job); over {
		return t
	}
	return a
}

// timedOut is the end of a run whose call context passed the Job's timeout while the executor's own did not.
func timedOut(ctx, callCtx context.Context, job *v1.Job) (runAnswer, bool) {
	if ctx.Err() != nil || !errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		return runAnswer{}, false
	}
	return runAnswer{phase: v1.JobRunFailed, reason: v1.JobRunTimedOut,
		message: fmt.Sprintf("the run passed its Job's timeout of %s, so its worker was stopped", time.Duration(job.Spec.Timeout))}, true
}

func startFailed(err error) runAnswer {
	return runAnswer{phase: v1.JobRunFailed, reason: v1.JobRunStartFailed, message: observability.StatusError(err.Error())}
}

// runWorkload is job as revision rev runs it: the revision's runtime, handler, image and digest over the Job's shared
// fields, as a Function's revision template is (ADR-0347 Decision 5).
func runWorkload(job *v1.Job, rev *v1.Revision) workload {
	w := jobWorkload(job)
	w.spec.Runtime, w.spec.Handler, w.spec.Image, w.spec.ImageDigest = rev.Spec.Runtime, rev.Spec.Handler, rev.Spec.Image, rev.Spec.ImageDigest
	return w
}

// runWorker is a run's one worker: named after the run, of kind JobRun, with the run's env and no FUNCD_FUNCTION
// (ADR-0347 Decision 5).
func runWorker(run *v1.JobRun, rev *v1.Revision) workerOf {
	return workerOf{name: run.Name, kind: v1.KindJobRun, env: map[string]string{
		"FUNCD_JOB":            string(run.Spec.Job),
		"FUNCD_JOB_RUN":        string(run.Name),
		"FUNCD_JOB_REVISION":   string(rev.Name),
		"FUNCD_JOB_STARTED_BY": run.Status.StartedBy,
	}}
}

// nameFree waits until no worker of the run's name remains, for at most runtime.bootTimeout, the bound its own
// worker's start has: the worker of a run deleted under that name may still be stopping (ADR-0347 Decision 6), and a
// worker under the same name would meet it.
func (j *JobRunReconciler) nameFree(ctx context.Context, run *v1.JobRun) error {
	const op = "function.runNameFree"
	by := time.Now().Add(j.r.bootTimeout)
	tick := time.NewTicker(runPoll)
	defer tick.Stop()
	for {
		all, err := j.r.runtime.List(ctx, run.Namespace)
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "list the workers of %q", run.Namespace)
		}
		if !slices.ContainsFunc(all, func(in runtime.Instance) bool { return in.OwnerKind == v1.KindJobRun && in.Name == run.Name }) {
			return nil
		}
		if !time.Now().Before(by) {
			return fault.Unavailablef(op, "a worker of an earlier run named %q remains after runtime.bootTimeout %s", run.Name, j.r.bootTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// ready waits until worker id listens and answers its readiness read with 200 (ADR-0161), for at most
// runtime.bootTimeout from its start; a worker that ends first never becomes ready.
func (j *JobRunReconciler) ready(ctx context.Context, id runtime.InstanceID) (runtime.Instance, error) {
	const op = "function.runReady"
	r := j.r
	by := time.Now().Add(r.bootTimeout)
	tick := time.NewTicker(runPoll)
	defer tick.Stop()
	for {
		in, err := r.runtime.Status(ctx, id)
		switch {
		case err != nil:
			return in, fault.Wrapf(err, fault.KindOf(err), op, "read the worker")
		case in.State.Terminal():
			return in, fault.Unavailablef(op, "the worker ended before it was ready (%s)", exitText(in.Exit))
		case in.Port > 0:
			if ok, _, _ := r.probeReadiness(ctx, in.IP, in.Port); ok {
				return in, nil
			}
		}
		if !time.Now().Before(by) {
			return in, fault.Unavailablef(op, "the worker did not listen and become ready within runtime.bootTimeout %s", r.bootTimeout)
		}
		select {
		case <-ctx.Done():
			return in, ctx.Err()
		case <-tick.C:
		}
	}
}

func exitText(ex runtime.Exit) string {
	switch ex.Cause {
	case runtime.ExitByCode:
		return fmt.Sprintf("exit code %d", ex.Code)
	case runtime.ExitBySignal:
		return fmt.Sprintf("signal %d", ex.Signal)
	}
	return cmp.Or(string(ex.Cause), "no exit status")
}

// call makes the run's one call to worker in through the invoke chain (ADR-0347 Decision 5): caller JobRun, Target
// the Job at its pinned revision, so only the contract plugins apply to it, with the run's traceparent. The client
// sets X-Funcd-Timeout-Ms from ctx's deadline, the Job's timeout, and no other.
func (j *JobRunReconciler) call(ctx context.Context, run *v1.JobRun, job *v1.Job, rev *v1.Revision, in runtime.Instance) runAnswer {
	body, err := runEvent(run)
	if err != nil {
		return handlerFailed("encode the run's event: " + err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, instanceURL(run.Namespace, run.Name, in), bytes.NewReader(body))
	if err != nil {
		return handlerFailed("build the call: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("traceparent", "00-"+run.Status.TraceID+"-"+observability.NewSpanID()+"-01")
	call := &invoke.Call{Caller: invoke.JobRun, Request: req,
		Target: activator.FunctionRef{Namespace: run.Namespace, Name: job.Name, Revision: rev.Name, UID: job.UID}}
	if rf := j.cfg.Chain.Admit(ctx, call); rf != nil {
		return j.refused(rf)
	}
	if rf := j.cfg.Chain.Before(ctx, call); rf != nil {
		return j.refused(rf)
	}
	resp, err := j.cfg.Client.Do(call.Request)
	if err != nil {
		return handlerFailed("the call failed: " + err.Error())
	}
	defer httpx.CloseBody(resp.Body)
	if rf := j.cfg.Chain.After(ctx, call, invoke.NewAnswer(resp)); rf != nil {
		return j.refused(rf)
	}
	return j.answerOf(resp.StatusCode, resp.Body)
}

// runEvent is the body of a run's call (ADR-0347 Contracts): a CloudEvent whose id is the run's UID and whose data is
// its input, null without one.
func runEvent(run *v1.JobRun) ([]byte, error) {
	data := run.Spec.Input
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	return json.Marshal(struct {
		SpecVersion string          `json:"specversion"`
		Type        string          `json:"type"`
		Source      string          `json:"source"`
		ID          string          `json:"id"`
		Data        json.RawMessage `json:"data"`
	}{"1.0", "funcd.job.run", "funcd://" + string(run.Namespace) + "/job/" + string(run.Spec.Job), string(run.UID), data})
}

// refused maps a refusal of the chain as ADR-0302 Decision 3 says: one with its own status and body as the worker's
// answer, a problem as a failed call.
func (j *JobRunReconciler) refused(rf *invoke.Refusal) runAnswer {
	if rf.Status != 0 {
		return j.answerOf(rf.Status, bytes.NewReader(rf.Body))
	}
	return handlerFailed("the call was refused: " + rf.Error())
}

// errorBodyMax is how much of an error answer the run's message reads, before its first line is cut.
const errorBodyMax = 4 << 10

// answerOf maps the worker's answer to the run's end (ADR-0347 Decision 5): a 2xx Succeeded with the body as the result,
// or PayloadLimitExceeded over job.payloadLimit; a 422 InputSchemaMismatch; any other answer HandlerFailed with its
// cause's first line, a 2xx whose body is not JSON included, since a result is JSON.
func (j *JobRunReconciler) answerOf(status int, body io.Reader) runAnswer {
	switch {
	case status >= 200 && status < 300:
		got, err := io.ReadAll(io.LimitReader(body, j.cfg.PayloadLimit+1))
		switch {
		case err != nil:
			return handlerFailed("read the answer: " + err.Error())
		case int64(len(got)) > j.cfg.PayloadLimit:
			return runAnswer{phase: v1.JobRunFailed, reason: v1.JobRunPayloadLimitExceeded,
				message: fmt.Sprintf("the result exceeds job.payloadLimit %d bytes", j.cfg.PayloadLimit)}
		case len(bytes.TrimSpace(got)) == 0:
			return runAnswer{phase: v1.JobRunSucceeded}
		case !json.Valid(got):
			return handlerFailed(fmt.Sprintf("the handler answered %d, and the answer is not JSON", status))
		}
		return runAnswer{phase: v1.JobRunSucceeded, result: got}
	case status == http.StatusUnprocessableEntity:
		return runAnswer{phase: v1.JobRunFailed, reason: v1.JobRunInputSchemaMismatch,
			message: observability.StatusError("the input does not match the pinned revision's contract: " + causeOf(body))}
	}
	return handlerFailed(fmt.Sprintf("the handler answered %d: %s", status, causeOf(body)))
}

// causeOf is what an error answer says: the error field of a JSON object, as the shims answer a handler that throws,
// else the body.
func causeOf(body io.Reader) string {
	got, _ := io.ReadAll(io.LimitReader(body, errorBodyMax))
	var shaped struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(got, &shaped) == nil && shaped.Error != "" {
		return shaped.Error
	}
	return strings.TrimSpace(string(got))
}

func handlerFailed(cause string) runAnswer {
	return runAnswer{phase: v1.JobRunFailed, reason: v1.JobRunHandlerFailed, message: observability.StatusError(cause)}
}

// removeWorker stops worker id with its driver's grace, removes it and releases its emptyDirs, on a context of its own,
// so an executor that ends on its run's cancel still clears its worker (ADR-0347 Decision 5).
func (j *JobRunReconciler) removeWorker(ctx context.Context, id runtime.InstanceID) {
	ctx = context.WithoutCancel(ctx)
	r := j.r
	if err := r.stopWorker(ctx, id); err != nil && fault.KindOf(err) != fault.NotFound {
		r.logger.WarnContext(ctx, "could not stop a run's worker", "instance", id, "error", err)
	}
	if err := r.runtime.Remove(ctx, id); err != nil && fault.KindOf(err) != fault.NotFound {
		r.logger.WarnContext(ctx, "could not remove a run's worker", "instance", id, "error", err)
	}
	if err := r.releaseEmptyDirs(ctx, id); err != nil {
		r.logger.WarnContext(ctx, "could not release a run's emptyDirs", "instance", id, "error", err)
	}
}
