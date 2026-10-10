// Package runner runs the platform backup (ADR-0205): one run at a time, on a cadence anchored on the newest complete
// ladder generation; a status read from the target's listing; the funcd.backup metrics; and the rpoRisk alert on the
// newest verified generation. The blob mirror (ADR-0208) and the KV export (ADR-0209) report their runs to Recorder.
package runner

import (
	"context"
	"log/slog"
	"maps"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/snapshot"
)

// streamPlatform is the platform backup's stream in the metrics; the blob mirror and the KV export are "blob" and "kv".
const streamPlatform = "platform"

// Config builds a Runner. Target nil runs no platform backup (the streams only); Hold nil is never held (ADR-0206);
// Meter nil records nothing; Clock nil is the system clock; After nil is time.After.
type Config struct {
	Target backup.Target
	Sealer *envelope.Sealer
	Hold   interface{ Held() bool }
	Times  config.BackupTimes
	Meter  metric.Meter
	Logger *slog.Logger
	Clock  clock.Clock
	After  func(time.Duration) <-chan time.Time
}

// Inputs are the stores a run cuts (ADR-0202) and the generation a restore loaded (ADR-0206), nil at a first start.
type Inputs struct {
	Events, Meta, Runs snapshot.Source
	Parent             *backup.GenRef
}

// Status is the platform backup's status (Decision 4). Its times come from the target's listing, so they survive a
// restart; LastFailure and Streams are since the start.
type Status struct {
	Enabled              bool              `json:"enabled"`
	Held                 bool              `json:"held"`
	RPORisk              bool              `json:"rpoRisk"`
	Interval             v1.Duration       `json:"interval"`
	RPO                  v1.Duration       `json:"rpo"`
	LastFailure          *Failure          `json:"lastFailure,omitempty"`
	LastSuccessTime      *v1.Timestamp     `json:"lastSuccessTime,omitempty"`
	LastVerifiedTime     *v1.Timestamp     `json:"lastVerifiedTime,omitempty"`
	EarliestRestorePoint *v1.Timestamp     `json:"earliestRestorePoint,omitempty"`
	NextRunTime          *v1.Timestamp     `json:"nextRunTime,omitempty"`
	Streams              map[string]Stream `json:"streams,omitempty"`
}

// Stream is what a sibling backup reported through Recorder since the start.
type Stream struct {
	LastSuccessTime *v1.Timestamp `json:"lastSuccessTime,omitempty"`
	LastFailure     *Failure      `json:"lastFailure,omitempty"`
}

// Failure is a failed run: when it ended and its error.
type Failure struct {
	Time  v1.Timestamp `json:"time"`
	Error string       `json:"error"`
}

// Runner runs the platform backup and keeps its status in memory.
type Runner struct {
	cfg      Config
	log      *slog.Logger
	in       Inputs
	runs     metric.Int64Counter
	duration metric.Float64Histogram

	mu      sync.Mutex
	view    view
	next    time.Time
	failure *Failure
	streams map[string]Stream
	// risk is rpoRisk at the last evaluation and warnedAt the last "backup rpo at risk" (Decision 5).
	risk     bool
	warnedAt time.Time
}

// view is what the last listing showed (Decision 4); a zero time is absent.
type view struct{ lastSuccess, lastVerified, earliest time.Time }

// New builds a Runner and registers its instruments; a Target without a Sealer, or with a time not positive, is
// fault.Invalid.
func New(cfg Config) (*Runner, error) {
	const op = "runner.New"
	t := cfg.Times
	if cfg.Target != nil {
		switch {
		case cfg.Sealer == nil:
			return nil, fault.Invalidf(op, "a backup target needs a sealer")
		case t.Interval <= 0 || t.RPO <= 0 || t.RetryInterval <= 0:
			return nil, fault.Invalidf(op, "backup interval %s, rpo %s and retry interval %s must be positive",
				t.Interval, t.RPO, t.RetryInterval)
		}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.System()
	}
	if cfg.After == nil {
		cfg.After = time.After
	}
	if cfg.Meter == nil {
		cfg.Meter = metricnoop.NewMeterProvider().Meter("funcd.backup")
	}
	r := &Runner{cfg: cfg, log: cfg.Logger, streams: map[string]Stream{}}
	var err error
	if r.runs, err = cfg.Meter.Int64Counter("funcd.backup.runs"); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "register funcd.backup.runs")
	}
	if r.duration, err = cfg.Meter.Float64Histogram("funcd.backup.duration_ms", metric.WithUnit("ms")); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "register funcd.backup.duration_ms")
	}
	if _, err = cfg.Meter.Int64ObservableGauge("funcd.backup.rpo_risk",
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			var v int64
			if r.riskNow() {
				v = 1
			}
			o.Observe(v)
			return nil
		})); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "register funcd.backup.rpo_risk")
	}
	return r, nil
}

// Bind sets the stores a run cuts; a Target with a nil Source is fault.Invalid.
func (r *Runner) Bind(in Inputs) error {
	if r.cfg.Target != nil && (in.Events == nil || in.Meta == nil || in.Runs == nil) {
		return fault.Invalidf("runner.Bind", "the platform backup needs the event store, the metastore and the run state")
	}
	r.in = in
	return nil
}

// Run runs the platform backup until ctx ends, at once without a Target (Decision 2). A failed run is logged, counted
// and kept in the status; the next attempt follows retryInterval later.
func (r *Runner) Run(ctx context.Context) {
	if r.cfg.Target == nil {
		return
	}
	if r.in.Events == nil {
		r.log.ErrorContext(ctx, "backup runner has no stores bound: no platform backup runs")
		return
	}
	due, ok := r.start(ctx)
	for ok && r.waitUntil(ctx, due) {
		now := r.cfg.Clock.Now()
		if r.held() {
			due = now.Add(r.cfg.Times.RetryInterval)
			r.setNext(due)
			continue
		}
		due, ok = r.run(ctx, now)
	}
}

// start lists the target: the first run is due an interval after the newest complete ladder generation, at once when
// none. A failed listing is a failed run.
func (r *Runner) start(ctx context.Context) (time.Time, bool) {
	now := r.cfg.Clock.Now()
	entries, err := r.cfg.Target.List(ctx)
	if ctx.Err() != nil {
		return now, false
	}
	if err != nil {
		return r.failed(ctx, now, now, err), true
	}
	r.observe(ctx, entries, now)
	due := dueTime(entries, now, r.cfg.Times.Interval)
	r.setNext(due)
	return due, true
}

// waitUntil waits for due, relisting the target at each rpo check on the way (Decision 4); false when ctx ended.
func (r *Runner) waitUntil(ctx context.Context, due time.Time) bool {
	for {
		now := r.cfg.Clock.Now()
		if !now.Before(due) {
			return ctx.Err() == nil
		}
		check := r.nextCheck()
		if !check.After(now) {
			r.relist(ctx, now)
			continue
		}
		wake := due
		if check.Before(wake) {
			wake = check
		}
		select {
		case <-ctx.Done():
			return false
		case <-r.cfg.After(wake.Sub(now)):
		}
	}
}

// run writes one generation and lists the target, returning the next due time; false when ctx ended during it.
func (r *Runner) run(ctx context.Context, start time.Time) (time.Time, bool) {
	s := r.cfg.Sealer
	_, err := r.cfg.Target.Write(ctx, r.in.Events, r.in.Meta, r.in.Runs,
		backup.WriteOptions{Seal: s.Seal(), Keys: s.Keys(), Parent: r.in.Parent})
	var entries []backup.Entry
	if err == nil {
		entries, err = r.cfg.Target.List(ctx)
	}
	end := r.cfg.Clock.Now()
	if ctx.Err() != nil {
		return end, false
	}
	interval := r.cfg.Times.Interval
	if took := end.Sub(start); took > interval {
		r.log.WarnContext(ctx, "backup run overran its interval",
			"duration_ms", took.Milliseconds(), "interval_ms", interval.Milliseconds())
	}
	if err != nil {
		return r.failed(ctx, start, end, err), true
	}
	r.count(ctx, streamPlatform, end.Sub(start), nil)
	r.mu.Lock()
	r.failure = nil
	r.mu.Unlock()
	r.observe(ctx, entries, end)
	due := start.Add(interval)
	r.setNext(due)
	return due, true
}

// failed records a failed run and returns the next attempt: retryInterval after its end, at most an interval after
// its start.
func (r *Runner) failed(ctx context.Context, start, end time.Time, err error) time.Time {
	r.log.ErrorContext(ctx, "backup run failed", "error", err)
	r.count(ctx, streamPlatform, end.Sub(start), err)
	r.mu.Lock()
	r.failure = &Failure{Time: v1.NewTimestamp(end), Error: err.Error()}
	r.mu.Unlock()
	r.evaluate(ctx, end)
	due := end.Add(r.cfg.Times.RetryInterval)
	if limit := start.Add(r.cfg.Times.Interval); limit.Before(due) {
		due = limit
	}
	r.setNext(due)
	return due
}

// relist refreshes the view at an rpo check; a failed listing leaves the last one.
func (r *Runner) relist(ctx context.Context, now time.Time) {
	entries, err := r.cfg.Target.List(ctx)
	if err != nil {
		r.evaluate(ctx, now)
		return
	}
	r.observe(ctx, entries, now)
}

// Status lists the Target and returns the status; a failed listing returns the last one with fault.Unavailable.
func (r *Runner) Status(ctx context.Context) (Status, error) {
	if r.cfg.Target == nil {
		return r.status(r.cfg.Clock.Now()), nil
	}
	entries, err := r.cfg.Target.List(ctx)
	now := r.cfg.Clock.Now()
	if err != nil {
		return r.status(now), fault.Wrapf(err, fault.Unavailable, "runner.Status", "list the backup target")
	}
	r.observe(ctx, entries, now)
	return r.status(now), nil
}

// Recorder returns the report function of a sibling backup stream ("blob", "kv"): a nil error is a success at the
// call, else a failure; either is counted.
func (r *Runner) Recorder(stream string) func(start time.Time, err error) {
	return func(start time.Time, err error) {
		now := r.cfg.Clock.Now()
		r.count(context.Background(), stream, now.Sub(start), err)
		at := v1.NewTimestamp(now)
		r.mu.Lock()
		defer r.mu.Unlock()
		s := r.streams[stream]
		if err != nil {
			s.LastFailure = &Failure{Time: at, Error: err.Error()}
		} else {
			s.LastSuccessTime, s.LastFailure = &at, nil
		}
		r.streams[stream] = s
	}
}

func (r *Runner) count(ctx context.Context, stream string, took time.Duration, err error) {
	result := "ok"
	if err != nil {
		result = "failed"
	}
	attrs := metric.WithAttributes(attribute.String("stream", stream), attribute.String("result", result))
	r.runs.Add(ctx, 1, attrs)
	r.duration.Record(ctx, float64(took)/float64(time.Millisecond), attrs)
}

// observe takes a listing's view and evaluates rpoRisk at now.
func (r *Runner) observe(ctx context.Context, entries []backup.Entry, now time.Time) {
	v := viewOf(entries)
	r.mu.Lock()
	r.view = v
	r.mu.Unlock()
	r.evaluate(ctx, now)
}

// evaluate applies the alert (Decision 5): Warn on turning on and once per rpo while on, Info on clearing.
func (r *Runner) evaluate(ctx context.Context, now time.Time) {
	rpo := r.cfg.Times.RPO
	r.mu.Lock()
	verified := r.view.lastVerified
	risk := riskAt(verified, now, rpo)
	warn := risk && (!r.risk || now.Sub(r.warnedAt) >= rpo)
	cleared := !risk && r.risk
	if warn {
		r.warnedAt = now
	}
	r.risk = risk
	r.mu.Unlock()
	attrs := []slog.Attr{slog.Int64("rpo_ms", rpo.Milliseconds())}
	if !verified.IsZero() {
		attrs = append(attrs, slog.Int64("age_ms", now.Sub(verified).Milliseconds()))
	}
	switch {
	case warn:
		r.log.LogAttrs(ctx, slog.LevelWarn, "backup rpo at risk", attrs...)
	case cleared:
		r.log.LogAttrs(ctx, slog.LevelInfo, "backup rpo no longer at risk", attrs...)
	}
}

// nextCheck is when rpoRisk next needs a listing: the next Warn while on, else when the verified generation turns
// rpo old. After evaluate(now) it is after now.
func (r *Runner) nextCheck() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.risk {
		return r.warnedAt.Add(r.cfg.Times.RPO)
	}
	return r.view.lastVerified.Add(r.cfg.Times.RPO)
}

// riskNow is rpoRisk now on the last listing, for the gauge.
func (r *Runner) riskNow() bool {
	if r.cfg.Target == nil {
		return false
	}
	now := r.cfg.Clock.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	return riskAt(r.view.lastVerified, now, r.cfg.Times.RPO)
}

func (r *Runner) held() bool { return r.cfg.Hold != nil && r.cfg.Hold.Held() }

func (r *Runner) setNext(due time.Time) {
	r.mu.Lock()
	r.next = due
	r.mu.Unlock()
}

func (r *Runner) status(now time.Time) Status {
	st := Status{
		Enabled:  r.cfg.Target != nil,
		Held:     r.held(),
		Interval: v1.Duration(r.cfg.Times.Interval),
		RPO:      v1.Duration(r.cfg.Times.RPO),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.streams) > 0 {
		st.Streams = maps.Clone(r.streams)
	}
	if !st.Enabled {
		return st
	}
	st.RPORisk = riskAt(r.view.lastVerified, now, r.cfg.Times.RPO)
	st.LastFailure = r.failure
	st.LastSuccessTime = stamp(r.view.lastSuccess)
	st.LastVerifiedTime = stamp(r.view.lastVerified)
	st.EarliestRestorePoint = stamp(r.view.earliest)
	st.NextRunTime = stamp(r.next)
	return st
}

// riskAt is rpoRisk: no verified generation, or one at least rpo old.
func riskAt(verified, now time.Time, rpo time.Duration) bool {
	return verified.IsZero() || now.Sub(verified) >= rpo
}

func stamp(t time.Time) *v1.Timestamp {
	if t.IsZero() {
		return nil
	}
	ts := v1.NewTimestamp(t)
	return &ts
}

// dueTime is the first run's due time: an interval after the newest complete ladder generation, now when none; pins
// never count (ADR-0203 Decision 6).
func dueTime(entries []backup.Entry, now time.Time, interval time.Duration) time.Time {
	last := viewOf(entries).lastSuccess
	if last.IsZero() {
		return now
	}
	return last.Add(interval)
}

// viewOf reads a listing (Decision 4): the newest complete ladder generation; the oldest complete ladder or
// pre-upgrade one; and the highest-numbered complete verified copy, at its original's At, absent when the original
// expired. A verified copy's own At is the verify time, so it is no restore point.
func viewOf(entries []backup.Entry) view {
	var v view
	originals := map[backup.GenRef]time.Time{}
	var verified *backup.Entry
	for i, e := range entries {
		if !e.Complete {
			continue
		}
		switch e.Class {
		case backup.Hourly, backup.Daily, backup.Weekly:
			if e.At.After(v.lastSuccess) {
				v.lastSuccess = e.At
			}
		case backup.PreUpgrade:
		case backup.Verified:
			if verified == nil || e.Generation > verified.Generation {
				verified = &entries[i]
			}
			continue
		default:
			continue
		}
		originals[backup.GenRef{Timeline: e.Timeline, Generation: e.Generation}] = e.At
		if v.earliest.IsZero() || e.At.Before(v.earliest) {
			v.earliest = e.At
		}
	}
	if verified != nil {
		v.lastVerified = originals[backup.GenRef{Timeline: verified.Timeline, Generation: verified.Generation}]
	}
	return v
}
