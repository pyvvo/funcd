package runner_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/runner"
	"github.com/pyvvo/funcd/internal/backup/verify"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/snapshot"
)

const tl = "1111111111111111"

// t0 is when each test's clock starts.
var t0 = time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC) //nolint:gochecknoglobals // a test fixture

type source struct {
	version string
	records []snapshot.Record
}

func (s source) Snapshot(_ context.Context, emit func(snapshot.Record) error) (string, error) {
	for _, r := range s.records {
		if err := emit(r); err != nil {
			return "", err
		}
	}
	return s.version, nil
}

func inputs() runner.Inputs {
	rec := func(k string) snapshot.Record { return snapshot.Record{Key: []byte(k), Value: []byte("v")} }
	return runner.Inputs{
		Events: source{records: []snapshot.Record{rec("e1")}},
		Meta:   source{version: tl + "-7", records: []snapshot.Record{rec("m1")}},
		Runs:   source{records: []snapshot.Record{rec("r1")}},
	}
}

// stamped is a file:// target whose generations carry the test clock's time as their ModTime, as if each run had
// started then; took[i] is how far the clock moves during the i-th Write.
type stamped struct {
	backup.Target
	dir   string
	clock *clock.Manual
	took  []time.Duration

	mu             sync.Mutex
	writes, active int
	maxActive      int
}

func (s *stamped) Write(ctx context.Context, events, meta, runs snapshot.Source, opts backup.WriteOptions) (backup.Manifest, error) {
	s.mu.Lock()
	s.active++
	s.maxActive = max(s.maxActive, s.active)
	var took time.Duration
	if s.writes < len(s.took) {
		took = s.took[s.writes]
	}
	s.writes++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	at := s.clock.Now()
	m, err := s.Target.Write(ctx, events, meta, runs, opts)
	if err == nil {
		err = stampGeneration(s.dir, m.Generation, at)
	}
	s.clock.Advance(took)
	return m, err
}

func (s *stamped) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

// stampGeneration sets the ModTime of every file of generation n, in any class, to at.
func stampGeneration(dir string, n uint64, at time.Time) error {
	name := fmt.Sprintf("%010d-%s", n, tl)
	return filepath.WalkDir(filepath.Join(dir, "gen"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.Contains(p, string(filepath.Separator)+name+string(filepath.Separator)) {
			return err
		}
		return os.Chtimes(p, at, at)
	})
}

// syncBuffer is a log sink the runner's goroutine writes while the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) count(msg string) int { return strings.Count(s.String(), "msg=\""+msg+"\"") }

type harness struct {
	clock  *clock.Manual
	waits  chan time.Duration
	fire   chan time.Time
	reader *sdkmetric.ManualReader
	logs   *syncBuffer
	target *stamped
	dir    string
	cfg    runner.Config
	r      *runner.Runner
}

func times() config.BackupTimes {
	return config.BackupTimes{Interval: time.Hour, RPO: 2 * time.Hour, RetryInterval: 5 * time.Minute}
}

func noSeal(t *testing.T) *envelope.Sealer {
	t.Helper()
	s, err := envelope.New(envelope.Config{None: true, NoSecrets: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	return s
}

// newHarness opens an hourly-only file:// target at the clock t0 and a runner over it.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		clock: clock.NewManual(t0),
		waits: make(chan time.Duration, 64),
		fire:  make(chan time.Time),
		logs:  &syncBuffer{},
		dir:   t.TempDir(),
	}
	tg, err := backup.Open(context.Background(), backup.Config{
		Target: gocloud.FileURL(h.dir), DataDir: t.TempDir(), Retention: backup.Retention{Hourly: 48},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = tg.Close() })
	h.target = &stamped{Target: tg, dir: h.dir, clock: h.clock}
	h.reader = sdkmetric.NewManualReader()
	h.cfg = runner.Config{
		Target: h.target, Sealer: noSeal(t), Times: times(),
		Meter:  sdkmetric.NewMeterProvider(sdkmetric.WithReader(h.reader)).Meter("funcd.backup"),
		Logger: slog.New(slog.NewTextHandler(h.logs, nil)), Clock: h.clock,
		After: func(d time.Duration) <-chan time.Time {
			h.waits <- d
			return h.fire
		},
	}
	return h
}

// build makes the runner from h.cfg, bound to the test stores.
func (h *harness) build(t *testing.T) *runner.Runner {
	t.Helper()
	r, err := runner.New(h.cfg)
	require.NoError(t, err)
	require.NoError(t, r.Bind(inputs()))
	h.r = r
	return r
}

// run builds the runner and runs it until the test ends.
func (h *harness) run(t *testing.T) *runner.Runner {
	t.Helper()
	r := h.build(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return r
}

// wait returns the next wait the runner asks for.
func (h *harness) wait(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-h.waits:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("the runner asked for no wait")
		return 0
	}
}

// elapse moves the clock by d and ends the runner's wait.
func (h *harness) elapse(t *testing.T, d time.Duration) {
	t.Helper()
	h.clock.Advance(d)
	select {
	case h.fire <- time.Time{}:
	case <-time.After(10 * time.Second):
		t.Fatal("the runner is not waiting")
	}
}

// write puts one generation at the clock's time, outside the runner.
func (h *harness) write(t *testing.T, pin backup.Class) backup.Manifest {
	t.Helper()
	in := inputs()
	m, err := h.target.Write(context.Background(), in.Events, in.Meta, in.Runs, backup.WriteOptions{Pin: pin})
	require.NoError(t, err)
	return m
}

// verifyGen pins generation n under gen/verified/ as the operator's verify does.
func (h *harness) verifyGen(t *testing.T, n uint64) {
	t.Helper()
	b, err := gocloud.OpenWith(context.Background(), gocloud.FileURL(h.dir), gocloud.OpenOptions{})
	require.NoError(t, err)
	defer func() { _ = b.Close() }()
	res, err := verify.Verify(context.Background(), verify.Options{Bucket: b, Generation: n})
	require.NoError(t, err)
	require.True(t, res.Pinned)
}

func (h *harness) status(t *testing.T) runner.Status {
	t.Helper()
	st, err := h.r.Status(context.Background())
	require.NoError(t, err)
	return st
}

// runs reads funcd.backup.runs as "stream/result" → count.
func runs(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, m := range collect(t, reader) {
		if m.Name != "funcd.backup.runs" {
			continue
		}
		for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
			s, _ := dp.Attributes.Value("stream")
			r, _ := dp.Attributes.Value("result")
			out[s.AsString()+"/"+r.AsString()] = dp.Value
		}
	}
	return out
}

func rpoRisk(t *testing.T, reader *sdkmetric.ManualReader) int64 {
	t.Helper()
	for _, m := range collect(t, reader) {
		if m.Name == "funcd.backup.rpo_risk" {
			return m.Data.(metricdata.Gauge[int64]).DataPoints[0].Value
		}
	}
	t.Fatal("no funcd.backup.rpo_risk")
	return 0
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) []metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	var out []metricdata.Metrics
	for _, sm := range rm.ScopeMetrics {
		out = append(out, sm.Metrics...)
	}
	return out
}

func complete(t *testing.T, tg backup.Target, class backup.Class) []backup.Entry {
	t.Helper()
	es, err := tg.List(context.Background())
	require.NoError(t, err)
	var out []backup.Entry
	for _, e := range es {
		if e.Complete && e.Class == class {
			out = append(out, e)
		}
	}
	return out
}

// scenario: runs-on-interval — a file:// target and interval 1h on a test clock: the first run starts at once, the
// second an hour later, and by the second hour two complete generations exist; lastSuccessTime is the newest one's.
func TestScenarioRunsOnInterval(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.run(t)
	require.Equal(t, time.Hour, h.wait(t))
	h.elapse(t, time.Hour)
	require.Equal(t, time.Hour, h.wait(t), "the third run is due at the second hour")

	gens := complete(t, h.target, backup.Hourly)
	require.Len(t, gens, 2)
	st := h.status(t)
	require.True(t, st.Enabled)
	require.True(t, gens[1].At.Equal(time.Time(*st.LastSuccessTime)))
	require.True(t, t0.Add(time.Hour).Equal(gens[1].At))
	require.Equal(t, t0.Add(2*time.Hour), time.Time(*st.NextRunTime))
	require.Nil(t, st.LastFailure)
	require.Equal(t, map[string]int64{"platform/ok": 2}, runs(t, h.reader))
}

// scenario: restart-keeps-cadence — the newest complete hourly generation 20 min old and a pre-upgrade pin 5 min old:
// after a restart the first run starts 40 min after the start.
func TestScenarioRestartKeepsCadence(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.clock.Advance(-20 * time.Minute)
	h.write(t, "")
	h.clock.Advance(15 * time.Minute)
	h.write(t, backup.PreUpgrade)
	h.clock.Advance(5 * time.Minute)

	h.run(t)
	require.Equal(t, 40*time.Minute, h.wait(t))
	require.Equal(t, t0.Add(40*time.Minute), time.Time(*h.status(t).NextRunTime))
	require.Equal(t, 2, h.target.count(), "no run at the start")
}

// failing is a target refusing every call, as one unreachable at the start.
type failing struct {
	backup.Target
	err error
}

func (f failing) List(context.Context) ([]backup.Entry, error) { return nil, f.err }

func (f failing) Write(context.Context, snapshot.Source, snapshot.Source, snapshot.Source, backup.WriteOptions) (backup.Manifest, error) {
	return backup.Manifest{}, f.err
}

// scenario: failed-run-retries — a target refusing puts, or unreachable at the start: the runner keeps going,
// lastFailure holds the error, funcd.backup.runs{result=failed} is 1, and the next attempt is 5 min later.
func TestScenarioFailedRunRetries(t *testing.T) {
	t.Parallel()
	t.Run("a target refusing puts", func(t *testing.T) {
		h := newHarness(t)
		require.NoError(t, os.WriteFile(filepath.Join(h.dir, "gen"), nil, 0o600), "gen/ cannot be created")
		h.run(t)
		require.Equal(t, 5*time.Minute, h.wait(t))
		st := h.status(t)
		require.NotNil(t, st.LastFailure)
		require.NotEmpty(t, st.LastFailure.Error)
		require.Equal(t, t0, time.Time(st.LastFailure.Time))
		require.Equal(t, map[string]int64{"platform/failed": 1}, runs(t, h.reader))
		require.Equal(t, 1, h.logs.count("backup run failed"))
	})
	t.Run("a target unreachable at the start", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.Target = failing{err: fault.Unavailablef("test", "target unreachable")}
		r := h.run(t)
		require.Equal(t, 5*time.Minute, h.wait(t))
		st, err := r.Status(context.Background())
		require.Equal(t, fault.Unavailable, fault.KindOf(err))
		require.Contains(t, st.LastFailure.Error, "target unreachable")
		require.Equal(t, t0.Add(5*time.Minute), time.Time(*st.NextRunTime))
		require.Equal(t, map[string]int64{"platform/failed": 1}, runs(t, h.reader))

		h.elapse(t, 5*time.Minute)
		require.Equal(t, 5*time.Minute, h.wait(t), "each failed attempt retries 5 min later")
		require.Equal(t, map[string]int64{"platform/failed": 2}, runs(t, h.reader))
	})
}

// scenario: overrun-no-overlap — a 70 min run with interval 1h: the next starts at once when it ends, no two runs
// overlap, and one warning carries duration_ms and interval_ms.
func TestScenarioOverrunNoOverlap(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.cfg.Times.RPO = 24 * time.Hour
	h.target.took = []time.Duration{70 * time.Minute, time.Minute}
	h.run(t)
	require.Equal(t, 59*time.Minute, h.wait(t), "the second run started at once and lasted a minute")
	require.Equal(t, 2, h.target.count())
	require.Equal(t, 1, h.target.maxActive)
	gens := complete(t, h.target, backup.Hourly)
	require.Len(t, gens, 2)
	require.True(t, t0.Add(70*time.Minute).Equal(gens[1].At), "the second run started when the first ended")
	logs := h.logs.String()
	require.Equal(t, 1, h.logs.count("backup run overran its interval"))
	require.Contains(t, logs, "duration_ms=4200000")
	require.Contains(t, logs, "interval_ms=3600000")
}

// scenario: rpo-risk-follows-verified — only generation 7 verified and objectives.rpo 2h: when 7 is 2 h old, rpoRisk is
// true, funcd.backup.rpo_risk is 1 and one warning is logged; once 9 is verified, a listing clears it.
func TestScenarioRPORiskFollowsVerified(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for range 7 {
		h.write(t, "")
		h.clock.Advance(time.Hour)
	}
	seven := t0.Add(6 * time.Hour)
	h.verifyGen(t, 7)
	h.build(t)

	h.clock.Advance(seven.Add(2*time.Hour - time.Millisecond).Sub(h.clock.Now()))
	st := h.status(t)
	require.False(t, st.RPORisk)
	require.Equal(t, seven, time.Time(*st.LastVerifiedTime))
	require.Equal(t, int64(0), rpoRisk(t, h.reader))

	h.clock.Advance(time.Millisecond)
	require.True(t, h.status(t).RPORisk)
	require.Equal(t, int64(1), rpoRisk(t, h.reader))
	require.True(t, h.status(t).RPORisk)
	require.Equal(t, 1, h.logs.count("backup rpo at risk"))
	require.Contains(t, h.logs.String(), "rpo_ms=7200000 age_ms=7200000")

	h.write(t, "")
	h.write(t, "")
	h.verifyGen(t, 9)
	st = h.status(t)
	require.False(t, st.RPORisk)
	require.Equal(t, h.clock.Now(), time.Time(*st.LastVerifiedTime))
	require.Equal(t, int64(0), rpoRisk(t, h.reader))
	require.Equal(t, 1, h.logs.count("backup rpo no longer at risk"))
}

// The first due time: a pin newer than the newest ladder generation does not count; a failed first listing is a failed
// run; while held, a due time writes nothing, counts nothing and rechecks after retryInterval.
func TestDueTime(t *testing.T) {
	t.Parallel()
	t.Run("a newer pin is ignored", func(t *testing.T) {
		h := newHarness(t)
		h.clock.Advance(-30 * time.Minute)
		h.write(t, "")
		h.clock.Advance(29 * time.Minute)
		h.write(t, backup.PreUpgrade)
		h.clock.Advance(time.Minute)
		h.run(t)
		require.Equal(t, 30*time.Minute, h.wait(t))
	})
	t.Run("a failed first listing", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.Target = failing{err: fault.Unavailablef("test", "listing refused")}
		h.cfg.Times.RetryInterval = 2 * time.Hour
		h.run(t)
		require.Equal(t, time.Hour, h.wait(t), "a failed run retries within one interval of its start")
		require.Equal(t, 1, h.logs.count("backup run failed"))
	})
	t.Run("held", func(t *testing.T) {
		h := newHarness(t)
		hold := &switchHold{}
		hold.set(true)
		h.cfg.Hold = hold
		h.run(t)
		require.Equal(t, 5*time.Minute, h.wait(t))
		h.elapse(t, 5*time.Minute)
		require.Equal(t, 5*time.Minute, h.wait(t))
		st := h.status(t)
		require.True(t, st.Held)
		require.Nil(t, st.LastFailure)
		require.Equal(t, 0, h.target.count())
		require.Empty(t, runs(t, h.reader))

		hold.set(false)
		h.elapse(t, 5*time.Minute)
		require.Equal(t, time.Hour, h.wait(t))
		require.Equal(t, 1, h.target.count(), "the next due time after the release runs")
		require.False(t, h.status(t).Held)
	})
}

type switchHold struct {
	mu   sync.Mutex
	held bool
}

func (s *switchHold) set(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held = v
}

func (s *switchHold) Held() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held
}

// listed is a target whose listing is fixed.
type listed struct {
	backup.Target
	entries []backup.Entry
}

func (l listed) List(context.Context) ([]backup.Entry, error) { return l.entries, nil }

// The status times come from the listing: the newest complete ladder generation; the oldest complete ladder or
// pre-upgrade one; and the highest-numbered complete verified copy at its original's time, absent once the original
// expired. A verified copy is no restore point and an incomplete generation counts for nothing.
func TestStatusFromEntries(t *testing.T) {
	t.Parallel()
	hour := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Hour) }
	entry := func(n uint64, class backup.Class, complete bool, at time.Time) backup.Entry {
		return backup.Entry{Generation: n, Timeline: tl, Class: class, Complete: complete, At: at}
	}
	entries := []backup.Entry{
		entry(1, backup.PreUpgrade, true, hour(1)),
		entry(2, backup.Daily, true, hour(2)),
		entry(3, backup.Hourly, true, hour(3)),
		entry(3, backup.Verified, true, hour(0)),
		entry(4, backup.Hourly, true, hour(4)),
		entry(4, backup.Verified, false, hour(9)),
		entry(5, backup.Hourly, false, time.Time{}),
	}
	statusOf := func(t *testing.T, es []backup.Entry) runner.Status {
		t.Helper()
		r, err := runner.New(runner.Config{Target: listed{entries: es}, Sealer: noSeal(t), Times: times(),
			Clock: clock.NewManual(hour(4).Add(30 * time.Minute)), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		require.NoError(t, err)
		st, err := r.Status(context.Background())
		require.NoError(t, err)
		return st
	}

	st := statusOf(t, entries)
	require.Equal(t, hour(4), time.Time(*st.LastSuccessTime))
	require.Equal(t, hour(1), time.Time(*st.EarliestRestorePoint), "a pre-upgrade pin counts; a verified copy does not")
	require.Equal(t, hour(3), time.Time(*st.LastVerifiedTime))
	require.False(t, st.RPORisk, "3 is 90 min old")

	st = statusOf(t, slices.Concat(entries[2:], []backup.Entry{entry(6, backup.Verified, true, hour(4))}))
	require.Nil(t, st.LastVerifiedTime, "6's original expired")
	require.True(t, st.RPORisk)
	require.Equal(t, hour(3), time.Time(*st.EarliestRestorePoint))
}

// The blob mirror and the KV export report through Recorder, kept per stream since the start and counted; a success
// clears the stream's failure; without a target the status is enabled: false.
func TestRecorderStreams(t *testing.T) {
	t.Parallel()
	c := clock.NewManual(t0)
	reader := sdkmetric.NewManualReader()
	r, err := runner.New(runner.Config{Times: times(), Clock: c,
		Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("funcd.backup")})
	require.NoError(t, err)
	st, err := r.Status(context.Background())
	require.NoError(t, err)
	require.False(t, st.Enabled)
	require.Nil(t, st.Streams)

	c.Advance(time.Minute)
	r.Recorder("kv")(t0, fault.Forbiddenf("test", "put refused"))
	r.Recorder("blob")(t0, nil)
	st, err = r.Status(context.Background())
	require.NoError(t, err)
	require.False(t, st.Enabled)
	require.Contains(t, st.Streams["kv"].LastFailure.Error, "put refused")
	require.Equal(t, t0.Add(time.Minute), time.Time(st.Streams["kv"].LastFailure.Time))
	require.Nil(t, st.Streams["kv"].LastSuccessTime)
	require.Equal(t, t0.Add(time.Minute), time.Time(*st.Streams["blob"].LastSuccessTime))
	require.Equal(t, map[string]int64{"kv/failed": 1, "blob/ok": 1}, runs(t, reader))

	r.Recorder("kv")(t0, nil)
	st, err = r.Status(context.Background())
	require.NoError(t, err)
	require.Nil(t, st.Streams["kv"].LastFailure)
	require.NotNil(t, st.Streams["kv"].LastSuccessTime)
	require.Equal(t, int64(0), rpoRisk(t, reader), "no rpoRisk without a platform backup")
}

// While rpoRisk is on the runner relists at each rpo and warns once per rpo, not at each listing; a verified generation
// within the rpo clears it with one Info line.
func TestRPOWarnOncePerRPO(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.cfg.Times.Interval = 10 * time.Hour
	h.write(t, "")
	h.verifyGen(t, 1)
	h.clock.Advance(30 * time.Minute)
	h.run(t)

	require.Equal(t, 90*time.Minute, h.wait(t), "the runner relists when 1 turns rpo old")
	h.elapse(t, 90*time.Minute)
	require.Equal(t, 2*time.Hour, h.wait(t))
	require.Equal(t, 1, h.logs.count("backup rpo at risk"))
	_, err := h.r.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, h.logs.count("backup rpo at risk"), "a status read within the rpo does not warn again")

	h.clock.Advance(time.Hour)
	h.write(t, "")
	h.verifyGen(t, 2)
	h.elapse(t, time.Hour)
	require.Equal(t, time.Hour, h.wait(t), "the runner relists when 2 turns rpo old")
	require.Equal(t, 1, h.logs.count("backup rpo at risk"))
	require.Equal(t, 1, h.logs.count("backup rpo no longer at risk"))
	require.Equal(t, int64(0), rpoRisk(t, h.reader))

	h.elapse(t, time.Hour)
	require.Equal(t, 2*time.Hour, h.wait(t))
	require.Equal(t, 2, h.logs.count("backup rpo at risk"))
	h.elapse(t, 2*time.Hour)
	require.Equal(t, 2*time.Hour, h.wait(t))
	require.Equal(t, 3, h.logs.count("backup rpo at risk"), "on turning on, then once per rpo")
	require.Equal(t, int64(1), rpoRisk(t, h.reader))
	require.Equal(t, 2, h.target.count(), "no run was due")
}
