package main

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/backup/plan"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const commentedConfig = `# funcd daemon config.

storage:
  # Where the stores live.
  dataDir: /var/lib/funcd

# The platform backup (ADR-0203, ADR-0205).
backup:
  target: s3://funcd-backup?region=eu-west-3   # the box credential lists and puts
  encryption:
    recipients:
      - /etc/funcd/recipients/owner.txt
      - /etc/funcd/recipients/recovery.txt

  # One run an hour.
  interval: 1h

# Logs.
log:
  level: info
`

const kvConfig = `kvstore:
  engine: badger
  backup:
    enabled: true
backup:
  target: s3://funcd-backup?region=eu-west-3
  encryption:
    recipients:
      - /etc/funcd/recipients/owner.txt
      - /etc/funcd/recipients/recovery.txt
secrets:
  encryptionKeyFile: /etc/funcd/secrets.key
`

const ordersNightly = `# Nightly copy of the orders stores (ADR-0222).
apiVersion: funcd.io/v1alpha1
kind: BackupSchedule
metadata:
  name: orders-nightly
  namespace: team-a
  resourceGroup: rg1
spec:
  schedule: 0 3 * * *   # 03:00 in timeZone
  timeZone: Europe/Paris

  ttl: 720h
  scope:
    kvStores:
      - orders-state
  target:
    url: s3://orders-backup?region=eu-west-3
    credentialsSecret: backup-writer
  encryption:
    recipients:
      - %s
      - %s
`

const (
	verifyJob15m = "# run funcdctl backup verify every 15m, off the funcd host: (backup.objectives.rpo − backup.interval) " +
		"/ 2 (ADR-0205)\n"
	recipientsWarn = "warning: backup.encryption.recipients: backup.encryption.recipients without " +
		"secrets.encryptionKeyFile: the backup envelope alone protects Secret values\n"
	lakeNoKey = "# lake: no key to set: a Lake saves its catalog before it answers a commit (ADR-0279).\n"
)

// lakeWayBack is the end of the lake example for --rpo rpo (ADR-0286 Decision 3).
func lakeWayBack(rpo string) string {
	return "# If the blob store is lost, a Lake comes back from the blob mirror: funcdctl backup plan blob --rpo " + rpo +
		"\n# An App's own backups of its Lakes are a BackupSchedule's (scope.lakes): funcdctl backup plan schedule -f " +
		"<manifest> --rpo " + rpo + "\n"
}

// planEnv is a scenario's world: no $FUNCD_CONFIG and a fresh working directory, so config.Locate finds only what the
// test writes there as ./funcdconfig.yaml.
func planEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("FUNCD_CONFIG", "")
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

// runPlan drives `funcdctl backup plan` through newRootCmdWith with no SDK client and no server.
func runPlan(args ...string) (stdout, stderr string, err error) {
	var out, errOut bytes.Buffer
	root := newRootCmdWith(&out, nil)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"backup", "plan"}, args...))
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func writePlanFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(body), mode))
	require.NoError(t, os.Chmod(path, mode))
}

func requireFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, want, string(got))
}

func ageRecipients(t *testing.T) [2]string {
	t.Helper()
	var r [2]string
	for i := range r {
		id, err := age.GenerateX25519Identity()
		require.NoError(t, err)
		r[i] = id.Recipient().String()
	}
	return r
}

// scenario: plan-shows-verify-job (ADR-0286), with platform-from-objectives (ADR-0223) — no config file and no
// server: the platform keys in block style, then the verify job as a comment; only the daemon's no-target warning on
// stderr, exit 0.
func TestScenarioPlanShowsVerifyJob(t *testing.T) {
	planEnv(t)
	stdout, stderr, err := runPlan("platform", "--rpo", "1h", "--keep", "720h")
	require.NoError(t, err)
	require.Equal(t, "backup:\n  interval: 30m\n  objectives:\n    rpo: 1h\n  retention:\n    hourly: 48\n    daily: 30\n"+
		"    weekly: 0\n"+verifyJob15m, stdout)
	cfg, err := config.Parse("", []byte(stdout), config.Flags{})
	require.NoError(t, err)
	daemon := cfg.CheckBackup()
	require.Len(t, daemon, 1)
	require.Equal(t, "backup.retention.weekly, backup.interval, backup.objectives.rpo set without backup.target: ignored, "+
		"no platform backup runs", daemon[0].Message)
	require.Equal(t, "warning: backup.retention.weekly: "+daemon[0].Message+"\n", stderr)
}

// scenario: workload-keys-from-objectives (ADR-0223) — a config with the KV backup on: the KV interval and ladder, the
// hourly floor at the default rebaseline, the blob interval and retention plus one interval; exit 0 each.
func TestScenarioWorkloadKeysFromObjectives(t *testing.T) {
	dir := planEnv(t)
	writePlanFile(t, filepath.Join(dir, "funcdconfig.yaml"), kvConfig, 0o600)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"kv", "--rpo", "1m", "--keep", "168h"},
			"kvstore:\n  backup:\n    interval: 30s\n    retention:\n      hourly: 48\n      daily: 7\n      weekly: 0\n"},
		{[]string{"kv", "--keep", "12h"}, "kvstore:\n  backup:\n    retention:\n      hourly: 25\n      daily: 0\n      weekly: 0\n"},
		{[]string{"blob", "--rpo", "2h", "--keep", "720h"}, "blob:\n  backup:\n    interval: 1h\n    retention: 721h\n"},
	} {
		stdout, stderr, err := runPlan(tc.args...)
		require.NoError(t, err, tc.args)
		require.Equal(t, tc.want, stdout, tc.args)
		require.Empty(t, stderr, tc.args)
	}
	requireFile(t, filepath.Join(dir, "funcdconfig.yaml"), kvConfig)
}

// scenario: schedule-from-objectives (ADR-0223) — the manifest with only spec.schedule and spec.ttl changed, which
// `funcdctl apply -f -` accepts; --rpo 90s exits 1 naming --rpo with stdout empty.
func TestScenarioScheduleFromObjectives(t *testing.T) {
	dir := planEnv(t)
	r := ageRecipients(t)
	manifest := fmt.Sprintf(ordersNightly, r[0], r[1])
	file := filepath.Join(dir, "orders-nightly.yaml")
	writePlanFile(t, file, manifest, 0o600)

	stdout, stderr, err := runPlan("schedule", "-f", file, "--rpo", "2h", "--keep", "336h")
	require.NoError(t, err)
	want := strings.Replace(strings.Replace(manifest, "schedule: 0 3 * * *", "schedule: 0 * * * *", 1), "ttl: 720h", "ttl: 336h", 1)
	require.Equal(t, want, stdout)
	require.Empty(t, stderr)
	requireFile(t, file, manifest)

	h, err := controlplane.NewServer(controlplane.Deps{
		Store: store.New(memory.New()), Authorizer: rbac.New(),
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{
			devToken: {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
		}),
		Admissions: []admission.Admission{admission.NewBackupScheduleAdmission("s3://store?region=eu-west-3")},
	})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	dev, err := sdk.New(srv.URL, sdk.WithToken(devToken))
	require.NoError(t, err)
	stdin, err := os.Open(writeManifest(t, stdout))
	require.NoError(t, err)
	saved := os.Stdin
	os.Stdin = stdin
	t.Cleanup(func() { os.Stdin = saved; _ = stdin.Close() })
	var applied bytes.Buffer
	require.NoError(t, execCLI(&applied, dev, "apply", "-f", "-"))
	require.Equal(t, "applied BackupSchedule/orders-nightly\n", applied.String())
	got, err := dev.Get(t.Context(), v1.KindBackupSchedule, "team-a", "orders-nightly")
	require.NoError(t, err)
	require.Equal(t, "0 * * * *", got.(*v1.BackupSchedule).Spec.Schedule)
	require.Equal(t, v1.Duration(336*time.Hour), got.(*v1.BackupSchedule).Spec.TTL)

	stdout, _, err = runPlan("schedule", "-f", file, "--rpo", "90s")
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, errorLine(err), "--rpo")
	require.Equal(t, 1, exitCode(err))
	require.Empty(t, stdout)
}

// scenario: plan-lake-sets-nothing (ADR-0286) — no lake.saveInterval: comments only, with --rpo in the blob mirror's
// and the BackupSchedule's plans, no floor; a config file is read and left unchanged.
func TestScenarioPlanLakeSetsNothing(t *testing.T) {
	dir := planEnv(t)
	for _, rpo := range []string{"10s", "800ms"} {
		stdout, stderr, err := runPlan("lake", "--rpo", rpo)
		require.NoError(t, err, rpo)
		require.Equal(t, lakeNoKey+lakeWayBack(rpo), stdout, rpo)
		require.Empty(t, stderr, rpo)
	}

	file := filepath.Join(dir, "funcdconfig.yaml")
	writePlanFile(t, file, commentedConfig, 0o640)
	stdout, stderr, err := runPlan("lake", "--rpo", "10s")
	require.NoError(t, err)
	require.Equal(t, lakeNoKey+lakeWayBack("10s"), stdout)
	require.Equal(t, recipientsWarn, stderr)
	requireFile(t, file, commentedConfig)
}

// scenario: plan-lake-names-the-opt-in (ADR-0286) — lake.saveInterval set in the file: the first comment names it and
// its loss bound (ADR-0274), the rest as without it.
func TestScenarioPlanLakeNamesTheOptIn(t *testing.T) {
	dir := planEnv(t)
	file := filepath.Join(dir, "funcdconfig.yaml")
	body := commentedConfig + "lake:\n  saveInterval: 1s\n"
	writePlanFile(t, file, body, 0o600)
	stdout, _, err := runPlan("lake", "--rpo", "10s")
	require.NoError(t, err)
	require.Equal(t, "# lake: lake.saveInterval 1s answers a commit before its save: a crash can lose up to 1s and the two "+
		"saves around\n# it (ADR-0274). Without the key a Lake saves its catalog before it answers a commit (ADR-0279).\n"+
		lakeWayBack("10s"), stdout)
	requireFile(t, file, body)
}

// scenario: plan-edits-nothing (ADR-0286) — a commented config: the keys and the verify job on stdout, the file
// unchanged byte for byte and nothing beside it; --write is an unknown flag, exit 1, nothing printed.
func TestScenarioPlanEditsNothing(t *testing.T) {
	dir := planEnv(t)
	file := filepath.Join(dir, "funcdconfig.yaml")
	writePlanFile(t, file, commentedConfig, 0o640)
	stdout, stderr, err := runPlan("platform", "--rpo", "30m")
	require.NoError(t, err)
	require.Equal(t, "backup:\n  interval: 15m\n  objectives:\n    rpo: 30m\n# run funcdctl backup verify every 7m30s, off the "+
		"funcd host: (backup.objectives.rpo − backup.interval) / 2 (ADR-0205)\n", stdout)
	require.Equal(t, recipientsWarn, stderr)
	requireFile(t, file, commentedConfig)

	stdout, _, err = runPlan("platform", "--rpo", "30m", "--write")
	require.ErrorContains(t, err, "unknown flag: --write")
	require.Equal(t, 1, exitCode(err))
	require.Empty(t, stdout)
	requireFile(t, file, commentedConfig)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no file beside it")
}

// scenario: refused-as-the-daemon-refuses (ADR-0223) — derived values the start refuses, and a file hand-edited into a
// refusal, exit 1 with the error config.Load returns for the same bytes; the file is unchanged.
func TestScenarioRefusedAsTheDaemonRefuses(t *testing.T) {
	dir := planEnv(t)
	file := filepath.Join(dir, "funcdconfig.yaml")
	startError := func(body string) string {
		path := filepath.Join(t.TempDir(), "funcdconfig.yaml")
		writePlanFile(t, path, body, 0o600)
		_, err := config.Load(path, config.Flags{})
		require.Error(t, err)
		return "funcdctl: " + err.Error()
	}

	writePlanFile(t, file, commentedConfig, 0o600)
	stdout, _, err := runPlan("platform", "--rpo", "4h", "--keep", "1h")
	require.Error(t, err)
	merged := strings.Replace(commentedConfig, "  interval: 1h\n", "  interval: 2h\n  objectives:\n    rpo: 4h\n"+
		"  retention:\n    hourly: 1\n    daily: 0\n    weekly: 0\n", 1)
	require.Equal(t, startError(merged), errorLine(err))
	require.Contains(t, err.Error(), "backup.retention.hourly keeps a generation 1h, below backup.interval 2h")
	require.Equal(t, 1, exitCode(err))
	require.Empty(t, stdout)
	requireFile(t, file, commentedConfig)

	edited := strings.Replace(commentedConfig, "  interval: 1h\n", "  interval: 1h\n  objectives:\n    rpo: 30m\n", 1)
	writePlanFile(t, file, edited, 0o600)
	stdout, _, err = runPlan("platform", "--keep", "720h")
	require.Error(t, err)
	require.Equal(t, startError(edited), errorLine(err))
	require.Contains(t, err.Error(), "backup.objectives.rpo 30m is below backup.interval 1h")
	require.Equal(t, 1, exitCode(err))
	require.Empty(t, stdout)
	requireFile(t, file, edited)
}

// scenario: rto-impossible (ADR-0223) — settings accepted, a 34m8s download above --rto 15m: the knobs on stderr,
// nothing printed or written, exit 3.
func TestScenarioRTOImpossible(t *testing.T) {
	dir := planEnv(t)
	file := filepath.Join(dir, "funcdconfig.yaml")
	writePlanFile(t, file, kvConfig, 0o600)
	stdout, stderr, err := runPlan("kv", "--rpo", "1m", "--rto", "15m", "--size", "200Gi", "--speed", "100Mi")
	require.Error(t, err)
	require.Equal(t, "funcdctl: RTO 15m is impossible: the download alone takes 34m8s. Knobs: a faster or closer target "+
		"(--speed), a smaller scope (--size).", errorLine(err))
	require.Equal(t, 3, exitCode(err))
	require.Empty(t, stdout)
	require.Empty(t, stderr)
	requireFile(t, file, kvConfig)
}

// scenario: rto-lower-bound-only (ADR-0223) — a 41s download below --rto 4h: stderr says only a restore drill proves
// the target, exit 0.
func TestScenarioRTOLowerBoundOnly(t *testing.T) {
	planEnv(t)
	stdout, stderr, err := runPlan("platform", "--rpo", "1h", "--rto", "4h", "--size", "2Gi", "--speed", "50Mi")
	require.NoError(t, err)
	require.Equal(t, "backup:\n  interval: 30m\n  objectives:\n    rpo: 1h\n"+verifyJob15m, stdout)
	require.Equal(t, "warning: backup.interval: backup.interval, backup.objectives.rpo set without backup.target: ignored, "+
		"no platform backup runs\n"+
		"RTO 4h: the download takes 41s; only a restore drill (ADR-0206) proves the target.\n", stderr)
}

// Derive reads the located file's values, and schedule runs apply's pre-flight before it prints or writes anything.
func TestPlanReadsTheFileAndPreflights(t *testing.T) {
	dir := planEnv(t)
	writePlanFile(t, filepath.Join(dir, "funcdconfig.yaml"), kvConfig+"blob:\n  backup:\n    interval: 2h\n", 0o600)
	stdout, _, err := runPlan("blob", "--keep", "720h")
	require.NoError(t, err)
	require.Equal(t, "blob:\n  backup:\n    retention: 722h\n", stdout, "retention adds the file's blob.backup.interval")

	r := ageRecipients(t)
	manifest := fmt.Sprintf(ordersNightly, r[0], r[0])
	file := filepath.Join(dir, "orders-nightly.yaml")
	writePlanFile(t, file, manifest, 0o600)
	stdout, _, err = runPlan("schedule", "-f", file, "--rpo", "2h")
	require.Error(t, err)
	require.Contains(t, errorLine(err), `manifest is invalid (BackupSchedule "orders-nightly")`)
	require.Equal(t, 1, exitCode(err))
	require.Empty(t, stdout)
	requireFile(t, file, manifest)
}

// TestFlagRules (ADR-0223 Decision 1): each refusal names its flag, exits 1 and prints nothing.
func TestFlagRules(t *testing.T) {
	dir := planEnv(t)
	manifest := filepath.Join(dir, "m.yaml")
	writePlanFile(t, manifest, fmt.Sprintf(ordersNightly, "age1a", "age1b"), 0o600)
	for _, tc := range []struct {
		args []string
		kind fault.Kind
		want string
	}{
		{[]string{"platform"}, fault.Invalid, "give --rpo, --keep or both"},
		{[]string{"platform", "--rpo", "1h", "--rto", "1h"}, fault.Invalid, "--rto, --size and --speed go together"},
		{[]string{"platform", "--rpo", "1h", "--size", "1Gi", "--speed", "1Mi"}, fault.Invalid, "--rto, --size and --speed go together"},
		{[]string{"platform", "--rpo", "1h", "--rto", "1h", "--size", "1Gi"}, fault.Invalid, "--rto, --size and --speed go together"},
		{[]string{"platform", "--rpo", "0s"}, fault.Invalid, "--rpo 0s: want a duration above zero"},
		{[]string{"platform", "--keep", "0s"}, fault.Invalid, "--keep 0s: want a duration above zero"},
		{[]string{"platform", "--rpo", "1h", "--rto", "0s", "--size", "1", "--speed", "1"}, fault.Invalid, "--rto 0s: want a duration above zero"},
		{[]string{"platform", "--rpo", "1h", "--rto", "1h", "--size", "0", "--speed", "1"}, fault.Invalid, `--size "0"`},
		{[]string{"platform", "--rpo", "1h", "--rto", "1h", "--size", "1", "--speed", "0"}, fault.Invalid, `--speed "0"`},
		{[]string{"platform", "--rpo", "1d"}, fault.Invalid, "--rpo"},
		{[]string{"platform", "--keep", "30d"}, fault.Invalid, "--keep"},
		{[]string{"platform", "--rpo", "1h", "--rto", "1h", "--size", "1GB", "--speed", "1"}, fault.Invalid, `--size "1GB"`},
		{[]string{"platform", "--rpo", "1h", "--rto", "1h", "--size", "16777216Ti", "--speed", "1"}, fault.Invalid, `--size "16777216Ti"`},
		{[]string{"schedule", "--rpo", "1h"}, fault.Invalid, "schedule needs -f"},
		{[]string{"schedule", "-f", manifest, "--config", manifest, "--rpo", "1h"}, fault.Invalid, "--config applies to platform, kv, blob and lake"},
		{[]string{"platform", "-f", manifest, "--rpo", "1h"}, fault.Invalid, "-f applies to schedule"},
		{[]string{"lake"}, fault.Invalid, "lake needs --rpo"},
		{[]string{"lake", "--rpo", "10s", "--keep", "720h"}, fault.Invalid, "--keep does not apply to lake"},
		{[]string{"lake", "-f", manifest, "--rpo", "10s"}, fault.Invalid, "-f applies to schedule; lake reads --config"},
		{[]string{"rqlite", "--rpo", "1h"}, fault.Invalid, `component "rqlite"`},
		{[]string{"platform", "--config", "missing.yaml", "--rpo", "1h"}, fault.NotFound, `"missing.yaml" does not exist`},
	} {
		stdout, stderr, err := runPlan(tc.args...)
		require.Equal(t, tc.kind, fault.KindOf(err), "%v: %v", tc.args, err)
		require.ErrorContains(t, err, tc.want, tc.args)
		require.Equal(t, 1, exitCode(err), tc.args)
		require.Empty(t, stdout, tc.args)
		require.Empty(t, stderr, tc.args)
	}
	requireFile(t, manifest, fmt.Sprintf(ordersNightly, "age1a", "age1b"))
}

// TestApplyKeepsBytes (ADR-0223 Decision 4): only the derived keys' bytes change — comments, blank lines, a missing
// parent, quoted crons, the right document of a stream — and Decision 4's forms are refused.
func TestApplyKeepsBytes(t *testing.T) {
	t.Parallel()
	apply := func(t *testing.T, kind v1.Kind, in string, s ...plan.Setting) string {
		t.Helper()
		out, err := plan.Apply([]byte(in), kind, s)
		require.NoError(t, err)
		return string(out)
	}

	t.Run("comments and blank lines", func(t *testing.T) {
		t.Parallel()
		out := apply(t, "", commentedConfig, plan.Setting{Key: "backup.interval", Value: "15m"},
			plan.Setting{Key: "backup.objectives.rpo", Value: "30m"}, plan.Setting{Key: "log.level", Value: "info"})
		require.Equal(t, strings.Replace(commentedConfig, "  interval: 1h\n", "  interval: 15m\n  objectives:\n    rpo: 30m\n", 1), out,
			"a key already at its value is left as it is")
	})

	t.Run("a missing parent", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			in, want string
			s        []plan.Setting
		}{
			{"storage:\n  dataDir: /srv/funcd\n\n# trailing\n", "storage:\n  dataDir: /srv/funcd\n\n# trailing\nbackup:\n" +
				"  interval: 30m\n  objectives:\n    rpo: 1h\n",
				[]plan.Setting{{Key: "backup.interval", Value: "30m"}, {Key: "backup.objectives.rpo", Value: "1h"}}},
			{"storage:\n    dataDir: /srv/funcd\n", "storage:\n    dataDir: /srv/funcd\nbackup:\n    objectives:\n        rpo: 1h\n",
				[]plan.Setting{{Key: "backup.objectives.rpo", Value: "1h"}}},
			{"storage:\n  dataDir: /srv/funcd", "storage:\n  dataDir: /srv/funcd\nbackup:\n  interval: 1h\n",
				[]plan.Setting{{Key: "backup.interval", Value: "1h"}}},
			{"backup:\n  objectives:\n    # rpo: 2h\nlog:\n  level: info\n",
				"backup:\n  objectives:\n    rpo: 1h\n    # rpo: 2h\nlog:\n  level: info\n",
				[]plan.Setting{{Key: "backup.objectives.rpo", Value: "1h"}}},
			{"backup:\n  target: s3://b\n  encryption:\n    recipients:\n    - /a\n    - /b\n\n# Logs.\nlog:\n  level: info\n",
				"backup:\n  target: s3://b\n  encryption:\n    recipients:\n    - /a\n    - /b\n  retention:\n    hourly: 48\n" +
					"    daily: 30\n\n# Logs.\nlog:\n  level: info\n",
				[]plan.Setting{{Key: "backup.retention.hourly", Value: "48"}, {Key: "backup.retention.daily", Value: "30"}}},
			{"storage:\n  dataDir: /srv/funcd\n...\n", "storage:\n  dataDir: /srv/funcd\nbackup:\n  interval: 30m\n...\n",
				[]plan.Setting{{Key: "backup.interval", Value: "30m"}}},
			{"backup:\n    target: s3://b\n", "backup:\n    target: s3://b\n    objectives:\n        rpo: 1h\n",
				[]plan.Setting{{Key: "backup.objectives.rpo", Value: "1h"}}},
			{"", "backup:\n  interval: 30m\n", []plan.Setting{{Key: "backup.interval", Value: "30m"}}},
			{"# only a comment\n", "# only a comment\nbackup:\n  interval: 30m\n", []plan.Setting{{Key: "backup.interval", Value: "30m"}}},
		} {
			out := apply(t, "", tc.in, tc.s...)
			require.Equal(t, tc.want, out, tc.in)
		}
	})

	t.Run("quoted crons and the document of the kind", func(t *testing.T) {
		t.Parallel()
		const doc = "kind: BackupSchedule\nspec:\n  schedule: %s  # comment\n  scope:\n    app: orders\n"
		for _, tc := range []struct{ in, value, want string }{
			{`"*/30 * * * *"`, "0 * * * *", "0 * * * *"},
			{"0 3 * * *", "*/15 * * * *", `"*/15 * * * *"`},
			{"'0 3 * * *'", "0 */2 * * *", "0 */2 * * *"},
			{`"0 3 * * *"`, "0 3 * * *", `"0 3 * * *"`},
		} {
			out := apply(t, v1.KindBackupSchedule, fmt.Sprintf(doc, tc.in), plan.Setting{Key: "spec.schedule", Value: tc.value})
			require.Equal(t, fmt.Sprintf(doc, tc.want), out)
		}
		const stream = "kind: Namespace\nmetadata:\n  name: team-a\n---\n" + "kind: BackupSchedule\nspec:\n  schedule: 0 3 * * *\n" +
			"  scope:\n    app: orders\n---\nkind: KVStore\nspec:\n  schedule: keep\n"
		out := apply(t, v1.KindBackupSchedule, stream, plan.Setting{Key: "spec.schedule", Value: "0 * * * *"},
			plan.Setting{Key: "spec.ttl", Value: "48h"})
		require.Equal(t, strings.Replace(stream, "  schedule: 0 3 * * *\n  scope:\n    app: orders\n",
			"  schedule: 0 * * * *\n  scope:\n    app: orders\n  ttl: 48h\n", 1), out)
	})

	t.Run("refused forms", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			kind     v1.Kind
			in, key  string
			contains string
		}{
			{"", "base: &d\n  interval: 1h\nbackup: *d\n", "backup.interval", "alias"},
			{"", "backup: &b\n  interval: 1h\n", "backup.interval", "anchor"},
			{"", "backup:\n  interval: &i 1h\n", "backup.interval", "anchor"},
			{"", "base: &d\n  retryInterval: 5m\nbackup:\n  <<: *d\n  interval: 1h\n", "backup.interval", "alias"},
			{"", "backup:\n  interval: |\n    1h\n", "backup.interval", "block scalar"},
			{"", "backup:\n  interval: >-\n    1h\n", "backup.interval", "block scalar"},
			{"", "backup: {interval: 1h}\n", "backup.interval", "flow collection"},
			{"", "backup:\n  objectives: {rpo: 2h}\n", "backup.objectives.rpo", "flow collection"},
			{"", "backup:\n  interval: !!str 1h\n", "backup.interval", "tag"},
			{"", "backup:\n  interval: \"1h\n    \"\n", "backup.interval", "several lines"},
			{"", "backup:\n  interval:\n", "backup.interval", "no value"},
			{"", "backup: on\n", "backup.interval", "backup is not a mapping"},
			{"", "backup:\n  interval: 1h\n---\nlog:\n  level: info\n", "backup.interval", "2 documents"},
			{v1.KindBackupSchedule, "kind: BackupSchedule\nspec: {}\n---\nkind: BackupSchedule\nspec: {}\n", "spec.ttl", "2 BackupSchedule"},
		} {
			_, err := plan.Apply([]byte(tc.in), tc.kind, []plan.Setting{{Key: tc.key, Value: "30m"}})
			require.Equal(t, fault.Invalid, fault.KindOf(err), tc.in)
			require.ErrorContains(t, err, "cannot edit "+tc.key, tc.in)
			require.ErrorContains(t, err, tc.contains, tc.in)
		}
	})

}
