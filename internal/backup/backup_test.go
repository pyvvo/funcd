package backup_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

func TestMain(m *testing.M) { s3stub.Main(m) }

const (
	tl1 = "1111111111111111"
	tl2 = "2222222222222222"
	tl9 = "9999999999999999"
)

// monday is the first hour of ISO week 41 of 2026 that the ladder tests start in.
var monday = time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC) //nolint:gochecknoglobals // a test fixture

func defaultRetention() backup.Retention {
	return backup.Retention{Hourly: 48, Daily: 30, Weekly: 12, Verified: 2}
}

// source is a store of fixed records at a version.
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

func record(k string) snapshot.Record {
	return snapshot.Record{Key: []byte(k), Value: []byte("value of " + k)}
}

// write runs one generation of three small stores whose metastore is at <timeline>-7.
func write(t backup.Target, timeline string, opts backup.WriteOptions) (backup.Manifest, error) {
	return t.Write(context.Background(),
		source{records: []snapshot.Record{record("e1")}},
		source{version: timeline + "-7", records: []snapshot.Record{record("m1"), record("m2")}},
		source{records: []snapshot.Record{record("r1")}}, opts)
}

func open(t *testing.T, cfg backup.Config, c clock.Clock) backup.Target {
	t.Helper()
	if cfg.Retention == (backup.Retention{}) {
		cfg.Retention = defaultRetention()
	}
	tg, err := backup.Open(context.Background(), cfg)
	require.NoError(t, err)
	if c != nil {
		backup.SetClock(tg, c)
	}
	t.Cleanup(func() { _ = tg.Close() })
	return tg
}

func stubTarget(t *testing.T, s *s3stub.Stub, mod func(*backup.Config)) backup.Target {
	t.Helper()
	cfg := backup.Config{Target: s.URL(), CredentialsFile: s3stub.CredentialsFile(t, "AKIDBOX")}
	if mod != nil {
		mod(&cfg)
	}
	return open(t, cfg, s.Clock)
}

func fileTarget(t *testing.T, dir string, mod func(*backup.Config)) backup.Target {
	t.Helper()
	cfg := backup.Config{Target: gocloud.FileURL(dir)}
	if mod != nil {
		mod(&cfg)
	}
	return open(t, cfg, nil)
}

// reader opens the stub's bucket with a credential that reads, as the operator's restore credential does.
func reader(t *testing.T, s *s3stub.Stub) blob.Bucket {
	t.Helper()
	b, err := gocloud.OpenWith(context.Background(), s.URL(), gocloud.OpenOptions{CredentialsFile: s3stub.CredentialsFile(t, "AKIDREAD")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func lastEntry(t *testing.T, tg backup.Target) backup.Entry {
	t.Helper()
	es, err := tg.List(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, es)
	return es[len(es)-1]
}

// scenario: generation-layout — a run puts the stores' parts in cut order, an 8 MiB part at a time, then
// manifest.yaml with at in ADR-0196's form; creating the manifest again is fault.Conflict.
func TestScenarioGenerationLayout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := s3stub.New(t, clock.NewManual(monday))
	tg := stubTarget(t, s, nil)
	big := snapshot.Record{Key: []byte("e2"), Value: bytes.Repeat([]byte("x"), 9<<20)}
	events := source{records: []snapshot.Record{record("e1"), big}}
	meta := source{version: tl1 + "-7", records: []snapshot.Record{record("m1")}}
	runs := source{records: []snapshot.Record{record("r1")}}
	m, err := tg.Write(ctx, events, meta, runs, backup.WriteOptions{})
	require.NoError(t, err)

	dir := "gen/weekly/0000000001-" + tl1 + "/"
	require.Equal(t, []string{
		dir + "events/part-00000", dir + "events/part-00001", dir + "metastore/part-00000", dir + "runs/part-00000",
		dir + "manifest.yaml",
	}, s.PutKeys("gen/"))

	b := reader(t, s)
	es, err := backup.List(ctx, b)
	require.NoError(t, err)
	require.Equal(t, []backup.Entry{{Generation: 1, Timeline: tl1, Class: backup.Weekly, Complete: true, At: monday}}, es)
	got, err := backup.ReadManifest(ctx, b, es[0])
	require.NoError(t, err)
	require.Equal(t, m, got)
	require.Equal(t, backup.Format, got.Format)
	require.Equal(t, uint64(1), got.Generation)
	require.Equal(t, v1alpha1.NewTimestamp(monday), got.At)
	require.Equal(t, version.Version, got.Funcd)
	require.Equal(t, tl1, got.Timeline)
	require.Equal(t, uint64(7), got.Revision)
	require.Nil(t, got.Parent)

	stored := map[string][]snapshot.Record{"events": events.records, "metastore": meta.records, "runs": runs.records}
	require.Len(t, got.Stores, 3)
	for i, name := range []string{"events", "metastore", "runs"} {
		f := got.Stores[i]
		require.Equal(t, name, f.Name)
		var data []byte
		for p := range f.Parts {
			part, err := b.Get(ctx, fmt.Sprintf("%s%s/part-%05d", dir, name, p))
			require.NoError(t, err)
			require.LessOrEqual(t, len(part), 8<<20)
			data = append(data, part...)
		}
		sum := sha256.Sum256(data)
		require.Equal(t, int64(len(data)), f.Bytes)
		require.Equal(t, hex.EncodeToString(sum[:]), f.SHA256)
		next := backup.Records(bytes.NewReader(data))
		for _, want := range stored[name] {
			r, err := next()
			require.NoError(t, err)
			require.Equal(t, string(want.Key), string(r.Key))
			require.True(t, bytes.Equal(want.Value, r.Value))
		}
		_, err := next()
		require.ErrorIs(t, err, io.EOF)
	}

	raw, err := b.Get(ctx, dir+"manifest.yaml")
	require.NoError(t, err)
	require.Regexp(t, regexp.MustCompile(`(?m)^at: "?2026-10-05T10:00:00\.000Z"?$`), string(raw))
	err = b.Put(ctx, dir+"manifest.yaml", raw, blob.PutOptions{IfNotExist: true})
	require.Equal(t, fault.Conflict, fault.KindOf(err))
}

// scenario: failed-run-skips-number — a run failing after the metastore parts leaves n incomplete; the next run
// writes n+1 and nothing is deleted.
func TestScenarioFailedRunSkipsNumber(t *testing.T) {
	t.Parallel()
	s := s3stub.New(t, clock.NewManual(monday))
	tg := stubTarget(t, s, nil)
	s.Set(func(s *s3stub.Stub) { s.FailPut = func(key string) bool { return strings.Contains(key, "/runs/") } })
	_, err := write(tg, tl1, backup.WriteOptions{})
	require.Error(t, err)
	failed := "gen/weekly/0000000001-" + tl1 + "/"
	require.Equal(t, []string{failed + "events/part-00000", failed + "metastore/part-00000"}, s.Keys("gen/"))

	s.Set(func(s *s3stub.Stub) { s.FailPut = nil })
	m, err := write(tg, tl1, backup.WriteOptions{})
	require.NoError(t, err)
	require.Equal(t, uint64(2), m.Generation)
	es, err := tg.List(context.Background())
	require.NoError(t, err)
	require.Len(t, es, 2)
	require.Equal(t, backup.Entry{Generation: 1, Timeline: tl1, Class: backup.Weekly}, es[0])
	require.Equal(t, uint64(2), es[1].Generation)
	require.True(t, es[1].Complete)
	require.Subset(t, s.Keys("gen/"), []string{failed + "events/part-00000", failed + "metastore/part-00000"})
	requests, _ := s.Seen()
	for _, r := range requests {
		require.False(t, strings.HasPrefix(r, "DELETE"), "a run deleted %s", r)
	}
}

// scenario: probe-outcomes — a file target or an S3 target honoring If-None-Match: * is ready and runs write; a
// target ignoring or refusing it writes no generation and names backup.singleWriter; singleWriter: true warns
// and writes.
func TestScenarioProbeOutcomes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("file", func(t *testing.T) {
		var logs bytes.Buffer
		data := t.TempDir()
		tg := fileTarget(t, t.TempDir(), func(c *backup.Config) {
			c.DataDir = data
			c.Logger = slog.New(slog.NewTextHandler(&logs, nil))
		})
		require.NoError(t, tg.Ready(ctx))
		require.True(t, tg.Conditional())
		require.Contains(t, logs.String(), "not an independent copy", "a target on the data directory's device warns")
		_, err := write(tg, tl1, backup.WriteOptions{})
		require.NoError(t, err)
		spooled, err := os.ReadDir(data)
		require.NoError(t, err)
		require.Empty(t, spooled, "a run leaves no spool behind")
	})
	t.Run("s3 honoring", func(t *testing.T) {
		s := s3stub.New(t, clock.NewManual(monday))
		tg := stubTarget(t, s, nil)
		require.NoError(t, tg.Ready(ctx))
		_, err := write(tg, tl1, backup.WriteOptions{})
		require.NoError(t, err)
		require.Len(t, s.Keys("probe/"), 1)
	})
	for name, cond := range map[string]s3stub.Condition{"ignoring": s3stub.Ignored, "refusing": s3stub.Refused} {
		t.Run("s3 "+name, func(t *testing.T) {
			s := s3stub.New(t, clock.NewManual(monday))
			s.Set(func(s *s3stub.Stub) { s.Cond = cond })
			tg := stubTarget(t, s, nil)
			err := tg.Ready(ctx)
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.Contains(t, err.Error(), "backup.singleWriter")
			_, err = write(tg, tl1, backup.WriteOptions{})
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.Empty(t, s.Keys("gen/"))
		})
		t.Run("s3 "+name+" with singleWriter", func(t *testing.T) {
			s := s3stub.New(t, clock.NewManual(monday))
			s.Set(func(s *s3stub.Stub) { s.Cond = cond })
			var logs bytes.Buffer
			tg := stubTarget(t, s, func(c *backup.Config) {
				c.SingleWriter = true
				c.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			})
			require.NoError(t, tg.Ready(ctx))
			require.Contains(t, logs.String(), "level=WARN")
			require.Contains(t, logs.String(), "backup.singleWriter")
			_, err := write(tg, tl1, backup.WriteOptions{})
			require.NoError(t, err)
			require.NotEmpty(t, s.Keys("gen/"))
		})
	}
}

// TestProbeOutcomes: Conditional per probe outcome, and a probe that fails otherwise is not ready, with its cause,
// until a later Ready probes again.
func TestProbeOutcomes(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name         string
		cond         s3stub.Condition
		singleWriter bool
		kind         fault.Kind
		conditional  bool
	}{
		{"honored", s3stub.Honored, false, "", true},
		{"honored with singleWriter", s3stub.Honored, true, "", true},
		{"ignored", s3stub.Ignored, false, fault.Invalid, true},
		{"ignored with singleWriter", s3stub.Ignored, true, "", true},
		{"refused", s3stub.Refused, false, fault.Invalid, true},
		{"refused with singleWriter", s3stub.Refused, true, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := s3stub.New(t, clock.NewManual(monday))
			s.Set(func(s *s3stub.Stub) { s.Cond = c.cond })
			tg := stubTarget(t, s, func(cfg *backup.Config) { cfg.SingleWriter = c.singleWriter })
			err := tg.Ready(ctx)
			require.Equal(t, c.kind, fault.KindOf(err))
			require.Equal(t, c.conditional, tg.Conditional())
		})
	}
	t.Run("another outcome is not ready, then re-probed", func(t *testing.T) {
		s := s3stub.New(t, clock.NewManual(monday))
		s.Set(func(s *s3stub.Stub) { s.FailPut = func(key string) bool { return strings.HasPrefix(key, "probe/") } })
		tg := stubTarget(t, s, func(cfg *backup.Config) { cfg.SingleWriter = true })
		err := tg.Ready(ctx)
		require.Error(t, err)
		require.NotEqual(t, fault.Invalid, fault.KindOf(err))
		require.Contains(t, err.Error(), "probe/")
		s.Set(func(s *s3stub.Stub) { s.FailPut = nil })
		require.NoError(t, tg.Ready(ctx))
		require.True(t, tg.Conditional())
	})
}

const holdEnv = "FUNCD_BACKUP_TEST_HOLD_DIR"

// TestHelperHoldsDirectory is the child process of TestScenarioDirectorySecondWriterRefused: it readies a target on
// the directory, says so, and holds it until its stdin closes. Without the variable it does nothing.
func TestHelperHoldsDirectory(t *testing.T) {
	dir := os.Getenv(holdEnv)
	if dir == "" {
		return
	}
	tg := fileTarget(t, dir, nil)
	require.NoError(t, tg.Ready(context.Background()))
	_, _ = os.Stdout.WriteString("held\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// scenario: directory-second-writer-refused — while process A holds a directory, B writes nothing and reports
// fault.Conflict, singleWriter or not; after A exits, B's next run writes.
func TestScenarioDirectorySecondWriterRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldsDirectory$", "-test.count=1") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), holdEnv+"="+dir)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "held\n", line)

	for _, singleWriter := range []bool{false, true} {
		b := fileTarget(t, dir, func(c *backup.Config) { c.SingleWriter = singleWriter })
		_, err = write(b, tl1, backup.WriteOptions{})
		require.Equal(t, fault.Conflict, fault.KindOf(err))
		require.Contains(t, err.Error(), "backup.target")
		_, statErr := os.Stat(filepath.Join(dir, "gen"))
		require.True(t, errors.Is(statErr, os.ErrNotExist), "B wrote under gen/")
		require.NoError(t, b.Close())
	}

	b := fileTarget(t, dir, nil)
	_, err = write(b, tl1, backup.WriteOptions{})
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	require.NoError(t, stdin.Close())
	require.NoError(t, cmd.Wait())
	m, err := write(b, tl1, backup.WriteOptions{})
	require.NoError(t, err)
	require.Equal(t, uint64(1), m.Generation)
}

// scenario: second-platform-refused — another timeline's key above the run's last, or another run's parts at the
// run's n, end the run before its manifest with fault.Conflict naming backup.target; a restored copy and its live
// source refuse each other, the source putting only its refused mark.
func TestScenarioSecondPlatformRefused(t *testing.T) {
	t.Parallel()
	t.Run("another platform's key", func(t *testing.T) {
		s := s3stub.New(t, clock.NewManual(monday))
		foreign := "gen/hourly/0000000005-" + tl9 + "/manifest.yaml"
		s.Seed(foreign, []byte("x"))
		tg := stubTarget(t, s, nil)
		_, err := write(tg, tl1, backup.WriteOptions{})
		require.Equal(t, fault.Conflict, fault.KindOf(err))
		require.Contains(t, err.Error(), "backup.target")
		require.Contains(t, err.Error(), foreign)
		require.Empty(t, s.PutKeys("gen/"))
	})
	t.Run("a daily run's parts at the hourly n", func(t *testing.T) {
		s := s3stub.New(t, clock.NewManual(monday))
		tg := stubTarget(t, s, nil)
		_, err := write(tg, tl1, backup.WriteOptions{})
		require.NoError(t, err)
		hourly := "gen/hourly/0000000002-" + tl1 + "/"
		daily := "gen/daily/0000000002-" + tl1 + "/events/part-00000"
		s.Set(func(s *s3stub.Stub) {
			s.OnPut = func(s *s3stub.Stub, key string) {
				if key == hourly+"events/part-00000" {
					s.PutLocked(daily, []byte("x"))
				}
			}
		})
		_, err = write(tg, tl1, backup.WriteOptions{})
		require.Equal(t, fault.Conflict, fault.KindOf(err))
		require.Contains(t, err.Error(), "backup.target")
		require.Contains(t, err.Error(), daily)
		require.NotContains(t, s.Keys(hourly), hourly+"manifest.yaml")
		require.Contains(t, s.Keys(hourly), hourly+"runs/part-00000")
	})
	t.Run("a restored copy and its live source", func(t *testing.T) {
		s := s3stub.New(t, clock.NewManual(monday))
		for n := 38; n <= 40; n++ {
			s.Seed(fmt.Sprintf("gen/hourly/%010d-%s/manifest.yaml", n, tl1), []byte("x"))
		}
		t2 := fmt.Sprintf("gen/hourly/%010d-%s/manifest.yaml", 43, tl2)
		s.Seed(t2, []byte("x"))
		live := stubTarget(t, s, nil)
		_, err := write(live, tl1, backup.WriteOptions{})
		require.Equal(t, fault.Conflict, fault.KindOf(err))
		require.Contains(t, err.Error(), t2)
		mark := "gen/hourly/0000000044-" + tl1 + "/refused"
		require.Equal(t, []string{mark}, s.PutKeys("gen/"))

		copied := stubTarget(t, s, nil)
		_, err = write(copied, tl2, backup.WriteOptions{Parent: &backup.GenRef{Timeline: tl1, Generation: 40}})
		require.Equal(t, fault.Conflict, fault.KindOf(err))
		require.Contains(t, err.Error(), mark)
		require.Equal(t, []string{mark, "gen/hourly/0000000045-" + tl2 + "/refused"}, s.PutKeys("gen/"))
	})
}

// scenario: ladder-class — an ISO week's first run is weekly, a later day's first run daily and that day's second
// run hourly; with daily 0 the second is hourly. Pins never count.
func TestScenarioLadderClass(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, c *clock.Manual, tg backup.Target, at time.Time) backup.Class {
		t.Helper()
		c.Advance(at.Sub(c.Now()))
		_, err := write(tg, tl1, backup.WriteOptions{})
		require.NoError(t, err)
		return lastEntry(t, tg).Class
	}
	t.Run("defaults", func(t *testing.T) {
		c := clock.NewManual(monday)
		tg := stubTarget(t, s3stub.New(t, c), nil)
		require.Equal(t, backup.Weekly, run(t, c, tg, monday))
		require.Equal(t, backup.Daily, run(t, c, tg, monday.Add(23*time.Hour)))
		require.Equal(t, backup.Hourly, run(t, c, tg, monday.Add(24*time.Hour)))
		require.Equal(t, backup.Weekly, run(t, c, tg, monday.Add(7*24*time.Hour)))
	})
	t.Run("daily 0", func(t *testing.T) {
		c := clock.NewManual(monday)
		r := defaultRetention()
		r.Daily = 0
		tg := stubTarget(t, s3stub.New(t, c), func(cfg *backup.Config) { cfg.Retention = r })
		require.Equal(t, backup.Weekly, run(t, c, tg, monday))
		require.Equal(t, backup.Hourly, run(t, c, tg, monday.Add(23*time.Hour)))
	})
	t.Run("pins never count", func(t *testing.T) {
		c := clock.NewManual(monday)
		tg := stubTarget(t, s3stub.New(t, c), nil)
		_, err := write(tg, tl1, backup.WriteOptions{Pin: backup.PreUpgrade})
		require.NoError(t, err)
		require.Equal(t, backup.PreUpgrade, lastEntry(t, tg).Class)
		require.Equal(t, backup.Weekly, run(t, c, tg, monday.Add(time.Hour)))
		_, err = write(tg, tl1, backup.WriteOptions{Pin: backup.Verified})
		require.Equal(t, fault.Invalid, fault.KindOf(err))
		pins := []backup.Entry{
			{Generation: 1, Class: backup.PreUpgrade, Complete: true, At: monday},
			{Generation: 2, Class: backup.Verified, Complete: true, At: monday},
			{Generation: 3, Class: backup.Weekly, At: monday},
		}
		require.Equal(t, backup.Weekly, backup.ClassFor(pins, monday.Add(time.Hour), defaultRetention()))
	})
}

// scenario: put-and-list-suffice — against the box policy (no Get, Attributes, Delete, nor puts under
// gen/verified/) the probe and a weekly, a daily and an hourly run succeed.
func TestScenarioPutAndListSuffice(t *testing.T) {
	t.Parallel()
	c := clock.NewManual(monday)
	s := s3stub.New(t, c)
	s.Set(func(s *s3stub.Stub) { s.Box = true })
	tg := stubTarget(t, s, nil)
	require.NoError(t, tg.Ready(context.Background()))
	for i, at := range []time.Time{monday, monday.Add(23 * time.Hour), monday.Add(24 * time.Hour)} {
		c.Advance(at.Sub(c.Now()))
		_, err := write(tg, tl1, backup.WriteOptions{})
		require.NoError(t, err)
		require.Equal(t, []backup.Class{backup.Weekly, backup.Daily, backup.Hourly}[i], lastEntry(t, tg).Class)
	}
	requests, _ := s.Seen()
	for _, r := range requests {
		require.True(t, strings.HasPrefix(r, "PUT ") || r == "GET ", "the box sent %q", r)
	}
	require.Empty(t, s.Keys("gen/verified/"))
}

// scenario: manifest-records-lineage — timeline T2, restored from T1's generation 40, records its parent in
// generation 43, and T1's 41 and 42 are abandoned.
func TestScenarioManifestRecordsLineage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := s3stub.New(t, clock.NewManual(monday))
	for n := uint64(40); n <= 42; n++ {
		data, err := yaml.Marshal(backup.Manifest{Format: backup.Format, Generation: n, At: v1alpha1.NewTimestamp(monday),
			Funcd: "v0.1.0", Timeline: tl1, Revision: n * 10})
		require.NoError(t, err)
		s.Seed(fmt.Sprintf("gen/hourly/%010d-%s/manifest.yaml", n, tl1), data)
	}
	tg := stubTarget(t, s, nil)
	parent := &backup.GenRef{Timeline: tl1, Generation: 40}
	m, err := write(tg, tl2, backup.WriteOptions{Parent: parent})
	require.NoError(t, err)
	require.Equal(t, uint64(43), m.Generation)

	b := reader(t, s)
	es, err := backup.List(ctx, b)
	require.NoError(t, err)
	var ms []backup.Manifest
	for _, e := range es {
		got, err := backup.ReadManifest(ctx, b, e)
		require.NoError(t, err)
		ms = append(ms, got)
	}
	got := ms[len(ms)-1]
	require.Equal(t, backup.Format, got.Format)
	require.Equal(t, tl2, got.Timeline)
	require.Equal(t, uint64(7), got.Revision)
	require.Equal(t, version.Version, got.Funcd)
	require.Equal(t, parent, got.Parent)
	require.Equal(t, map[backup.GenRef]bool{{Timeline: tl1, Generation: 41}: true, {Timeline: tl1, Generation: 42}: true},
		backup.Abandoned(ms))
}

// scenario: target-checked — a mem://, gs:// or azblob:// target, or a credentials file beside a file:// target,
// is refused with fault.Invalid naming the key.
func TestScenarioTargetChecked(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cases := []struct {
		name string
		cfg  backup.Config
		key  string
	}{
		{"mem", backup.Config{Target: "mem://"}, "backup.target"},
		{"gs", backup.Config{Target: "gs://b"}, "backup.target"},
		{"azblob", backup.Config{Target: "azblob://b"}, "backup.target"},
		{"credentials file with a directory", backup.Config{Target: gocloud.FileURL(dir), CredentialsFile: "creds"}, "backup.credentialsFile"},
		{"empty", backup.Config{}, "backup.target"},
		{"relative directory", backup.Config{Target: "file://./x"}, "backup.target"},
		{"directory with a prefix", backup.Config{Target: gocloud.FileURL(dir) + "?prefix=p/"}, "backup.target"},
		{"hourly below 1", backup.Config{Target: gocloud.FileURL(dir), Retention: backup.Retention{Daily: 30}}, "backup.retention.hourly"},
		{"weekly below 0", backup.Config{Target: gocloud.FileURL(dir), Retention: backup.Retention{Hourly: 1, Weekly: -1}}, "backup.retention.weekly"},
		{"kv prefix", backup.Config{Target: "gs://b", KeyPrefix: "kvstore.backup."}, "kvstore.backup.target"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.cfg.Retention == (backup.Retention{}) {
				c.cfg.Retention = defaultRetention()
			}
			_, err := backup.Open(context.Background(), c.cfg)
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.Contains(t, err.Error(), c.key)
		})
	}
}

// scenario: credentials-file-signs — with a credentials file, every request is signed with its key, not the
// environment's.
func TestScenarioCredentialsFileSigns(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret-env")
	s := s3stub.New(t, clock.NewManual(monday))
	tg := stubTarget(t, s, func(c *backup.Config) { c.CredentialsFile = s3stub.CredentialsFile(t, "AKIDFILE") })
	_, err := write(tg, tl1, backup.WriteOptions{})
	require.NoError(t, err)
	_, auth := s.Seen()
	require.NotEmpty(t, auth)
	for _, a := range auth {
		require.Contains(t, a, "Credential=AKIDFILE/")
	}
}

// TestLifecycleRules: one expiry per prefix in days, none for an unused class or Verified 0.
func TestLifecycleRules(t *testing.T) {
	require.Equal(t, map[string]int{
		"gen/hourly/": 2, "gen/daily/": 30, "gen/weekly/": 84, "gen/verified/": 2, "probe/": 1,
	}, backup.LifecycleRules(defaultRetention()))
	require.Equal(t, map[string]int{"gen/hourly/": 1, "probe/": 1},
		backup.LifecycleRules(backup.Retention{Hourly: 1}))
	require.Equal(t, map[string]int{"gen/hourly/": 3, "gen/weekly/": 7, "probe/": 1},
		backup.LifecycleRules(backup.Retention{Hourly: 49, Weekly: 1}))
}

// TestRecordFraming: records round-trip through uvarint(len key) ‖ key ‖ uvarint(len value) ‖ value; the end is
// io.EOF and a torn record fault.Invalid.
func TestRecordFraming(t *testing.T) {
	data := []byte{}
	frame := func(k, v string) []byte {
		var b []byte
		b = append(b, byte(len(k)))
		b = append(b, k...)
		b = append(b, byte(len(v)))
		return append(b, v...)
	}
	data = append(data, frame("k1", "v1")...)
	data = append(data, frame("", "")...)
	data = append(data, frame("k3", strings.Repeat("v", 100))...)
	next := backup.Records(bytes.NewReader(data))
	for _, want := range [][2]string{{"k1", "v1"}, {"", ""}, {"k3", strings.Repeat("v", 100)}} {
		r, err := next()
		require.NoError(t, err)
		require.Equal(t, want, [2]string{string(r.Key), string(r.Value)})
	}
	_, err := next()
	require.ErrorIs(t, err, io.EOF)

	whole := frame("key", "value")
	for cut := 1; cut < len(whole); cut++ {
		_, err := backup.Records(bytes.NewReader(whole[:cut]))()
		require.Equalf(t, fault.Invalid, fault.KindOf(err), "torn at %d: %v", cut, err)
	}
	_, err = backup.Records(bytes.NewReader([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f, 'k'}))()
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

// TargetURL is the URL Open opens: a directory's with dir_file_mode 0700 (448 in fileblob's decimal), an s3:// one as
// given, a bad one fault.Invalid naming the key.
func TestTargetURL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	u, err := backup.TargetURL(backup.Config{Target: gocloud.FileURL(dir), Retention: defaultRetention()})
	require.NoError(t, err)
	require.Equal(t, gocloud.FileURL(dir)+"?dir_file_mode=448", u)
	s3 := "s3://b?region=us-east-1"
	u, err = backup.TargetURL(backup.Config{Target: s3, Retention: defaultRetention()})
	require.NoError(t, err)
	require.Equal(t, s3, u)
	_, err = backup.TargetURL(backup.Config{Target: "mem://", KeyPrefix: "kvstore.backup.", Retention: defaultRetention()})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), "kvstore.backup.target")
}
