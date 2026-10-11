package plan

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/cron"
	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/config"
)

func parsed(t *testing.T, yaml string) config.Config {
	t.Helper()
	c, err := config.Parse("funcdconfig.yaml", []byte(yaml), config.Flags{})
	require.NoError(t, err)
	return c
}

// TestDeriveTable (ADR-0223 Decision 2): each component's row, cur by config.Parse; the KV hourly floor beside
// rebaseline unset, 12h, 24h, 48h, and 48h from FUNCD_KVSTORE_BACKUP_REBASELINE; blob's added interval.
func TestDeriveTable(t *testing.T) {
	defaults := parsed(t, "")
	rebaseline := func(d string) config.Config { return parsed(t, "kvstore:\n  backup:\n    rebaseline: "+d+"\n") }
	ladder := func(prefix, h, d, w string) []Setting {
		return []Setting{{prefix + "hourly", h}, {prefix + "daily", d}, {prefix + "weekly", w}}
	}
	for _, tc := range []struct {
		name string
		c    Component
		o    Objectives
		cur  config.Config
		want []Setting
	}{
		{"platform", Platform, Objectives{RPO: time.Hour, Keep: 720 * time.Hour}, defaults, append([]Setting{
			{"backup.interval", "30m"}, {"backup.objectives.rpo", "1h"}}, ladder("backup.retention.", "48", "30", "0")...)},
		{"platform rpo only", Platform, Objectives{RPO: 30 * time.Minute}, defaults,
			[]Setting{{"backup.interval", "15m"}, {"backup.objectives.rpo", "30m"}}},
		{"platform keep only", Platform, Objectives{Keep: 2000 * time.Hour}, defaults, ladder("backup.retention.", "48", "30", "12")},
		{"platform truncated to the ms", Platform, Objectives{RPO: 1001 * time.Millisecond}, defaults,
			[]Setting{{"backup.interval", "500ms"}, {"backup.objectives.rpo", "1s1ms"}}},
		{"kv", KV, Objectives{RPO: time.Minute, Keep: 168 * time.Hour}, defaults,
			append([]Setting{{"kvstore.backup.interval", "30s"}}, ladder("kvstore.backup.retention.", "48", "7", "0")...)},
		{"kv floor, rebaseline unset", KV, Objectives{Keep: 12 * time.Hour}, defaults, ladder("kvstore.backup.retention.", "25", "0", "0")},
		{"kv floor, rebaseline 12h", KV, Objectives{Keep: 12 * time.Hour}, rebaseline("12h"), ladder("kvstore.backup.retention.", "12", "0", "0")},
		{"kv floor, rebaseline 24h", KV, Objectives{Keep: 12 * time.Hour}, rebaseline("24h"), ladder("kvstore.backup.retention.", "25", "0", "0")},
		{"kv floor, rebaseline 48h", KV, Objectives{Keep: 12 * time.Hour}, rebaseline("48h"), ladder("kvstore.backup.retention.", "49", "0", "0")},
		{"kv floor below the ladder", KV, Objectives{Keep: 48 * time.Hour}, rebaseline("24h"), ladder("kvstore.backup.retention.", "48", "0", "0")},
		{"blob", Blob, Objectives{RPO: 2 * time.Hour, Keep: 720 * time.Hour}, defaults,
			[]Setting{{"blob.backup.interval", "1h"}, {"blob.backup.retention", "721h"}}},
		{"blob keep, cur's interval", Blob, Objectives{Keep: 720 * time.Hour},
			parsed(t, "blob:\n  backup:\n    interval: 2h\n"), []Setting{{"blob.backup.retention", "722h"}}},
		{"blob keep, default interval", Blob, Objectives{Keep: 24 * time.Hour}, config.Config{},
			[]Setting{{"blob.backup.retention", "25h"}}},
		{"blob rpo only", Blob, Objectives{RPO: 30 * time.Minute}, defaults, []Setting{{"blob.backup.interval", "15m"}}},
		{"schedule", Schedule, Objectives{RPO: 2 * time.Hour, Keep: 336 * time.Hour}, config.Config{},
			[]Setting{{"spec.schedule", "0 * * * *"}, {"spec.ttl", "336h"}}},
		{"no objective sets no key", Platform, Objectives{RTO: time.Hour, Size: 1, Speed: 1}, defaults, nil},
	} {
		got, err := Derive(tc.c, tc.o, tc.cur)
		require.NoError(t, err, tc.name)
		require.Equal(t, tc.want, got, tc.name)
	}

	t.Setenv("FUNCD_KVSTORE_BACKUP_REBASELINE", "48h")
	got, err := Derive(KV, Objectives{Keep: 12 * time.Hour}, parsed(t, ""))
	require.NoError(t, err)
	require.Equal(t, ladder("kvstore.backup.retention.", "49", "0", "0"), got, "the env overlay's rebaseline")

	_, err = Derive("rqlite", Objectives{RPO: time.Hour}, defaults)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	_, err = Derive(Blob, Objectives{RPO: 2 * time.Hour, Keep: time.Duration(v1.MaxDuration)}, defaults)
	require.ErrorContains(t, err, "--keep")
	_, err = Derive(Schedule, Objectives{RPO: 90 * time.Second}, config.Config{})
	require.ErrorContains(t, err, "--rpo")
}

// TestLadder (ADR-0223 Decision 2): the class bounds at 48h and 720h.
func TestLadder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		keep                  time.Duration
		hourly, daily, weekly int
	}{
		{24 * time.Hour, 24, 0, 0},
		{25 * time.Hour, 25, 0, 0},
		{90 * time.Minute, 2, 0, 0},
		{48 * time.Hour, 48, 0, 0},
		{49 * time.Hour, 48, 3, 0},
		{720 * time.Hour, 48, 30, 0},
		{721 * time.Hour, 48, 30, 5},
		{2016 * time.Hour, 48, 30, 12},
	} {
		h, d, w := Ladder(tc.keep)
		require.Equal(t, [3]int{tc.hourly, tc.daily, tc.weekly}, [3]int{h, d, w}, "keep %s", tc.keep)
	}
}

// TestCron (ADR-0223 Decision 2): each period at rpo = 2P and just above, the 1 min floor, the 24 h cap; every
// output passes CheckCron and fires P apart.
func TestCron(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		period time.Duration
		want   string
	}{
		{time.Minute, "* * * * *"}, {2 * time.Minute, "*/2 * * * *"}, {3 * time.Minute, "*/3 * * * *"},
		{4 * time.Minute, "*/4 * * * *"}, {5 * time.Minute, "*/5 * * * *"}, {6 * time.Minute, "*/6 * * * *"},
		{10 * time.Minute, "*/10 * * * *"}, {12 * time.Minute, "*/12 * * * *"}, {15 * time.Minute, "*/15 * * * *"},
		{20 * time.Minute, "*/20 * * * *"}, {30 * time.Minute, "*/30 * * * *"}, {time.Hour, "0 * * * *"},
		{2 * time.Hour, "0 */2 * * *"}, {3 * time.Hour, "0 */3 * * *"}, {4 * time.Hour, "0 */4 * * *"},
		{6 * time.Hour, "0 */6 * * *"}, {8 * time.Hour, "0 */8 * * *"}, {12 * time.Hour, "0 */12 * * *"},
		{24 * time.Hour, "0 0 * * *"},
	} {
		for _, rpo := range []time.Duration{2 * tc.period, 2*tc.period + time.Minute} {
			got, err := Cron(rpo)
			require.NoError(t, err, "rpo %s", rpo)
			require.Equal(t, tc.want, got, "rpo %s", rpo)
			require.NoError(t, v1.CheckCron("test", "spec.schedule", got, "spec.timeZone", ""))
			s, err := cron.Parse(got, time.UTC)
			require.NoError(t, err)
			first := s.Next(time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
			require.Equal(t, tc.period, s.Next(first).Sub(first), "%s fires every %s", got, tc.period)
		}
	}
	for rpo, want := range map[time.Duration]string{
		7 * time.Minute * 2: "*/6 * * * *", 50 * time.Minute * 2: "*/30 * * * *", 5 * time.Hour * 2: "0 */4 * * *",
		1000 * time.Hour: "0 0 * * *", time.Duration(v1.MaxDuration): "0 0 * * *",
	} {
		got, err := Cron(rpo)
		require.NoError(t, err)
		require.Equal(t, want, got, "rpo %s", rpo)
	}
	for _, rpo := range []time.Duration{119 * time.Second, 90 * time.Second, time.Millisecond} {
		_, err := Cron(rpo)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "rpo %s", rpo)
		require.ErrorContains(t, err, "--rpo")
	}
}

// TestCheckMatchesParse (ADR-0223 Decision 3): for each error row of CheckBackup (ADR-0203, 0204, 0205, 0209) and of
// CheckBlob (ADR-0208), plan's error is config.Parse's and config.Load's, unchanged.
func TestCheckMatchesParse(t *testing.T) {
	t.Parallel()
	const sealed = "backup:\n  target: s3://b?region=x\n  encryption:\n    recipients:\n      - /r/a\n      - /r/b\n"
	const kvOn = "kvstore:\n  backup:\n    enabled: true\n"
	for _, tc := range []struct{ key, yaml string }{
		{"backup.interval", sealed + "  interval: 1d\n"},
		{"backup.objectives.rpo", sealed + "  objectives:\n    rpo: 0s\n"},
		{"backup.retryInterval", "backup:\n  retryInterval: 5x\n"},
		{"backup.target", "backup:\n  target: \"s3://b/%zz\"\n"},
		{"backup.credentialsFile", "backup:\n  target: file:///srv/backup\n  credentialsFile: /c\n  encryption:\n    none: true\n"},
		{"backup.target", "backup:\n  target: http://x\n  encryption:\n    none: true\n"},
		{"backup.retention.hourly", sealed + "  retention:\n    hourly: 0\n"},
		{"backup.retention.daily", sealed + "  retention:\n    daily: -1\n"},
		{"backup.retention.weekly", sealed + "  retention:\n    weekly: -1\n"},
		{"backup.retention.verified", sealed + "  retention:\n    verified: 0\n"},
		{"backup.retention.preUpgrade", sealed + "  retention:\n    preUpgrade: 0\n"},
		{"backup.encryption.none", "backup:\n  target: s3://b\n  encryption:\n    none: true\n    recipients:\n      - /r/a\n"},
		{"backup.encryption.none", "backup:\n  target: s3://b\n  encryption:\n    none: true\n"},
		{"backup.encryption.recipients", "backup:\n  target: s3://b\n"},
		{"backup.retention.hourly", sealed + "  interval: 4h\n  objectives:\n    rpo: 8h\n  retention:\n    hourly: 1\n    daily: 0\n    weekly: 0\n"},
		{"backup.retention.daily", sealed + "  interval: 49h\n  objectives:\n    rpo: 100h\n  retention:\n    hourly: 1\n    daily: 2\n    weekly: 0\n"},
		{"backup.objectives.rpo", sealed + "  interval: 1h\n  objectives:\n    rpo: 30m\n"},
		{"kvstore.backup.credentialsFile", kvOn + "    credentialsFile: /c\n" + sealed},
		{"kvstore.backup.singleWriter", kvOn + "    singleWriter: true\n" + sealed},
		{"kvstore.backup.target", kvOn + "    target: s3://b?region=x\n" + sealed},
		{"kvstore.backup.target", kvOn},
		{"kvstore.backup.encryption.recipients", kvOn + "    target: s3://kv\n"},
		{"kvstore.backup.interval", kvOn + "    interval: 30\n" + sealed},
		{"kvstore.backup.rebaseline", kvOn + "    rebaseline: 1d\n" + sealed},
		{"kvstore.backup.rebaselineRetry", kvOn + "    rebaselineRetry: 0s\n" + sealed},
		{"kvstore.backup.chunkBytes", kvOn + "    chunkBytes: 9000000\n" + sealed},
		{"kvstore.backup.retention.hourly", kvOn + "    retention:\n      hourly: 0\n" + sealed},
		{"kvstore.backup.retention.daily", kvOn + "    retention:\n      daily: -1\n" + sealed},
		{"kvstore.backup.retention.weekly", kvOn + "    retention:\n      weekly: -1\n" + sealed},
		{"kvstore.backup.rebaseline", kvOn + "    rebaseline: 48h\n" + sealed},
		{"blob.target", "blob:\n  target: http://x\n"},
		{"blob.dir", "blob:\n  target: s3://b\n  dir: /srv/blob\n"},
		{"blob.credentialsFile", "blob:\n  credentialsFile: /c\n"},
		{"blob.dir", "blob:\n  dir: /var/lib\n"},
		{"blob.backup.interval", "blob:\n  backup:\n    interval: 1d\n"},
		{"blob.backup.rebaseline", "blob:\n  backup:\n    rebaseline: 1d\n"},
		{"blob.backup.retention", "blob:\n  backup:\n    retention: 1d\n"},
		{"blob.versionRetention", "blob:\n  versionRetention: 1d\n"},
		{"blob.backup.rebaseline", "blob:\n  backup:\n    interval: 2h\n    rebaseline: 1h\n"},
		{"blob.backup.retention", "blob:\n  backup:\n    interval: 2h\n    retention: 1h\n"},
	} {
		path := filepath.Join(t.TempDir(), "funcdconfig.yaml")
		require.NoError(t, os.WriteFile(path, []byte(tc.yaml), 0o600))
		_, perr := config.Parse(path, []byte(tc.yaml), config.Flags{})
		require.Error(t, perr, tc.key)
		_, lerr := config.Load(path, config.Flags{})
		warns, cerr := CheckConfig(path, []byte(tc.yaml))
		require.Nil(t, warns)
		require.Error(t, cerr, tc.key)
		require.Equal(t, perr.Error(), cerr.Error(), tc.key)
		require.Equal(t, lerr.Error(), cerr.Error(), tc.key)
		require.Contains(t, cerr.Error(), tc.key)
	}
	warns, err := CheckConfig("", []byte(sealed+"  interval: 90m\n"))
	require.NoError(t, err)
	require.Equal(t, []config.Finding{
		{Key: "backup.encryption.recipients", Message: "backup.encryption.recipients without secrets.encryptionKeyFile: the backup envelope alone protects Secret values"},
		{Key: "backup.interval", Message: "backup.interval 1h30m is above half of backup.objectives.rpo 2h: one failed run lets the newest generation pass the rpo"},
	}, warns, "the warnings alone, in CheckBackup's order")
}

// TestRTOBound (ADR-0223 Decision 5): ⌈size ÷ speed⌉ seconds, capped at MaxDuration; CheckRTO's outcomes.
func TestRTOBound(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		size, speed int64
		want        time.Duration
	}{
		{200 << 30, 100 << 20, 2048 * time.Second},
		{2 << 30, 50 << 20, 41 * time.Second},
		{1, 1, time.Second},
		{1, 1 << 40, time.Second},
		{9223372036, 1, 9223372036 * time.Second},
		{9223372037, 1, time.Duration(v1.MaxDuration)},
		{math.MaxInt64, 1, time.Duration(v1.MaxDuration)},
	} {
		require.Equal(t, tc.want, RTOBound(tc.size, tc.speed), "%d / %d", tc.size, tc.speed)
	}
	require.Equal(t, "34m8s", v1.Duration(RTOBound(200<<30, 100<<20)).String())

	bound, err := CheckRTO(Objectives{RPO: time.Hour})
	require.NoError(t, err)
	require.Zero(t, bound)
	bound, err = CheckRTO(Objectives{RTO: 41 * time.Second, Size: 2 << 30, Speed: 50 << 20})
	require.NoError(t, err, "an RTO at the bound holds")
	require.Equal(t, 41*time.Second, bound)
	_, err = CheckRTO(Objectives{RTO: 15 * time.Minute, Size: 200 << 30, Speed: 100 << 20})
	var rto *RTOError
	require.True(t, errors.As(err, &rto))
	require.Equal(t, 3, rto.ExitCode())
	require.Equal(t, "RTO 15m is impossible: the download alone takes 34m8s. Knobs: a faster or closer target (--speed), "+
		"a smaller scope (--size).", err.Error())
}
