package function

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// defaultReadinessTimeout is runtime.readinessTimeout's default (ADR-0259 Decision 5).
const defaultReadinessTimeout = 5 * time.Second

// reasonNotAnswering is Ready's reason while a serving Function has no ready worker and every worker it judges runs
// and is late past runtime.readinessTimeout (ADR-0259 Decision 3).
const reasonNotAnswering = "NotAnswering"

// readAnswer is a worker's answer to a readiness read: a solo replica's verdict and dependency report, or a pool
// host's /health/members entries, listed when the answer was a 200 with a member list.
type readAnswer struct {
	ready   bool
	report  *local.DependencyReport
	members []memberHealth
	listed  bool
}

// readRecord is what this process knows of one worker's readiness reads (ADR-0259 Decision 2): the CreatedAt it was
// seen with, its last answer (nil until it answers) and, while its reads go unanswered, the time of the first
// unanswered read after that answer.
type readRecord struct {
	created    time.Time
	last       *readAnswer
	unanswered time.Time
}

// readWindow is where a read stands in its worker's window (ADR-0259): until is the window's end when the read counts
// by the worker's last answer, silent the window when the worker is unanswered past it; both are zero when the read
// answered or the worker never answered.
type readWindow struct {
	until  time.Time
	silent time.Duration
}

// settleRead records a readiness read of worker in at now, got being its answer or nil when it got none, and returns
// the answer the read counts by, nil for none, and where it stands in in's window (ADR-0259 Decision 2): a read with no
// answer counts by in's last answer until readinessTimeout after the first unanswered read that followed it.
func (r *Reconciler) settleRead(in runtime.Instance, now time.Time, got *readAnswer) (*readAnswer, readWindow) {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	rec := r.reads[in.ID]
	if !rec.created.Equal(in.CreatedAt) {
		rec = readRecord{created: in.CreatedAt}
	}
	var w readWindow
	switch {
	case got != nil:
		rec.last, rec.unanswered = got, time.Time{}
	case rec.last != nil:
		if rec.unanswered.IsZero() {
			rec.unanswered = now
		}
		if end := rec.unanswered.Add(r.readinessTimeout); now.Before(end) {
			got, w.until = rec.last, end
		} else {
			w.silent = r.readinessTimeout
		}
	}
	r.reads[in.ID] = rec
	return got, w
}

// lateRead reports whether a probe's error is its deadline, probeTimeout, passing before an answer came (ADR-0259
// Decision 1); a refused or reset connection is not late.
func lateRead(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// readReadiness reads solo replica in's /health/readiness through its record: ready on a 200, a dependency report on
// a 503 that names a kind, not ready on any other answer or failed connection, and, when the read runs past
// probeTimeout, in's last answer while its window lasts (ADR-0259 Decisions 1 and 2).
func (r *Reconciler) readReadiness(ctx context.Context, in runtime.Instance) (bool, *local.DependencyReport, readWindow) {
	var got *readAnswer
	if ok, rep, late := r.probeReadiness(ctx, in.IP, in.Port); !late {
		got = &readAnswer{ready: ok, report: rep}
	}
	a, w := r.settleRead(in, r.clock.Now(), got)
	if a == nil {
		return false, nil, w
	}
	return a.ready, a.report, w
}

// forgetRead drops worker id's read record, so a worker created under id starts with none (ADR-0259 Decision 2).
func (r *Reconciler) forgetRead(id runtime.InstanceID) {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	delete(r.reads, id)
}

// notAnswering is Ready's message for a worker unanswered past window: a pool host's when pooled, else a replica's
// (ADR-0259 Decision 3).
func notAnswering(pooled bool, window time.Duration) string {
	if pooled {
		return "the pool host has not answered " + membersPath + " for " + window.String()
	}
	return "a replica has not answered " + readinessPath + " for " + window.String()
}

// pollBy is how soon a pass that would come back after d comes back when a kept read's window ends at until (ADR-0259
// Decision 4): by until, at least a millisecond away; d when until is zero.
func (r *Reconciler) pollBy(until time.Time, d time.Duration) time.Duration {
	if until.IsZero() {
		return d
	}
	return earliest(d, max(until.Sub(r.clock.Now()), time.Millisecond))
}
