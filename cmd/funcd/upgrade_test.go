//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/safemode"
	"github.com/pyvvo/funcd/internal/store"
	badgerstore "github.com/pyvvo/funcd/internal/store/badger"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// upgradeKey is the secrets key of the upgrade scenarios' nodes; backup.encryption.none needs one.
func upgradeKey() []byte { return bytes.Repeat([]byte{9}, 32) }

// backupYAML turns the platform backup on to a directory target, unsealed.
func backupYAML(target string) string {
	return fmt.Sprintf("backup:\n  target: %q\n  encryption:\n    none: true\n", gocloud.FileURL(target))
}

// funcdBinary builds cmd/funcd stamped ver, as scripts/build.sh does (ADR-0207 test plan: two releases, so a
// rollback really restores with the older one).
func funcdBinary(t *testing.T, ver string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs funcd twice; skipped under -short")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	bin := filepath.Join(dir, "funcd-"+ver)
	build := exec.Command("go", "build", "-ldflags", "-X github.com/pyvvo/funcd/internal/platform/version.Version="+ver,
		"-o", bin, "./cmd/funcd")
	build.Dir = repoRoot(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := build.CombinedOutput()
	require.NoErrorf(t, err, "build: %s", out)
	return bin
}

// installed copies bin to <dir>/funcd, the path the unit would run.
func installed(t *testing.T, bin string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	data, err := os.ReadFile(bin) //nolint:gosec // the test's own build
	require.NoError(t, err)
	self := filepath.Join(dir, "funcd")
	require.NoError(t, os.WriteFile(self, data, 0o755)) //nolint:gosec // a binary is executable
	return self
}

// fakeFuncd is a script answering `version` as funcd ver does, for the in-process upgrades.
func fakeFuncd(t *testing.T, ver string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	p := filepath.Join(dir, "funcd")
	require.NoError(t, os.WriteFile(p, fmt.Appendf(nil, "#!/bin/sh\necho 'funcd %s (commit abc1234, built today, go, test)'\n", ver), 0o755)) //nolint:gosec // a test binary
	return p
}

func runFuncd(t *testing.T, bin string, args ...string) string {
	t.Helper()
	out, err := exec.Command(bin, args...).CombinedOutput() //nolint:gosec // the test's own build
	require.NoErrorf(t, err, "%s %v: %s", bin, args, out)
	return string(out)
}

// child is a funcd process a test can kill: a built binary, or this test binary re-executed (runChildDaemon).
type child struct {
	cmd  *exec.Cmd
	logs *lockedBuffer
	done chan struct{}
}

func startChild(t *testing.T, cmd *exec.Cmd) *child {
	t.Helper()
	ch := &child{cmd: cmd, logs: &lockedBuffer{}, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = ch.logs, ch.logs
	require.NoError(t, cmd.Start())
	go func() { _ = cmd.Wait(); close(ch.done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-ch.done
		if t.Failed() {
			t.Logf("%s output:\n%s", cmd.Path, ch.logs.String())
		}
	})
	return ch
}

// serveWith runs `bin --config <n's config>`.
func (n *node) serveWith(t *testing.T, bin string) *child {
	t.Helper()
	return startChild(t, exec.Command(bin, "--config", n.cfgPath)) //nolint:gosec // the test's own build
}

// serving waits until the child's API answers and returns a client.
func (ch *child) serving(t *testing.T, n *node) *sdk.Client {
	t.Helper()
	c, err := sdk.New("http://"+n.control, sdk.WithToken(restoreToken))
	require.NoError(t, err)
	exited := func() bool {
		select {
		case <-ch.done:
			return true
		default:
			return false
		}
	}
	require.Eventually(t, func() bool {
		_, err := c.HoldStatus(context.Background())
		return err == nil || exited()
	}, 30*time.Second, 50*time.Millisecond, "funcd serves")
	require.False(t, exited(), "funcd exited:\n%s", ch.logs.String())
	return c
}

func (ch *child) kill(t *testing.T) {
	t.Helper()
	require.NoError(t, ch.cmd.Process.Kill())
	<-ch.done
}

// exit waits for the child to end and returns its status.
func (ch *child) exit(t *testing.T) int {
	t.Helper()
	select {
	case <-ch.done:
	case <-time.After(30 * time.Second):
		t.Fatalf("funcd did not exit:\n%s", ch.logs.String())
	}
	return ch.cmd.ProcessState.ExitCode()
}

// stop stops the child as systemctl stop does and expects a clean exit.
func (ch *child) stop(t *testing.T) {
	t.Helper()
	require.NoError(t, ch.cmd.Process.Signal(syscall.SIGTERM))
	require.Equal(t, 0, ch.exit(t), ch.logs.String())
}

func readSafeMode(t *testing.T, dataDir string) safemode.State {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, safemode.StateFile))
	require.NoError(t, err)
	var s safemode.State
	require.NoError(t, json.Unmarshal(data, &s))
	return s
}

func configMap(name string) *v1.ConfigMap {
	return &v1.ConfigMap{TypeMeta: tmeta(v1.KindConfigMap), ObjectMeta: om(name), Spec: v1.ConfigMapSpec{Data: map[string]string{"k": name}}}
}

// seedTeam creates the team namespace and resource group the objects of om() live in.
func seedTeam(t *testing.T, c *sdk.Client) {
	t.Helper()
	for _, o := range []v1.Object{&v1.Namespace{TypeMeta: tmeta(v1.KindNamespace), ObjectMeta: v1.ObjectMeta{Name: "team"}},
		&v1.ResourceGroup{TypeMeta: tmeta(v1.KindResourceGroup), ObjectMeta: om("rg")}} {
		_, err := c.Apply(context.Background(), o)
		require.NoError(t, err)
	}
}

// beginStarts records n unclean starts, as n funcd starts killed before stableAfter leave them.
func beginStarts(t *testing.T, dataDir string, n int, afterCrashes int) {
	t.Helper()
	for range n {
		_, _, _, err := safemode.Begin(dataDir, version.Version, safemode.Config{AfterCrashes: afterCrashes})
		require.NoError(t, err)
	}
}

// ownerFor picks the owner the repo unit's funcd user stands for: a second uid as root on Linux, else another of
// this user's groups (uid -1 keeps it). It skips when there is none.
func ownerFor(t *testing.T, uid int) (int, int) {
	t.Helper()
	if os.Geteuid() == 0 && runtime.GOOS == "linux" {
		return uid, uid
	}
	groups, err := os.Getgroups()
	require.NoError(t, err)
	for _, g := range groups {
		if g != os.Getgid() {
			return -1, g
		}
	}
	t.Skip("needs root on Linux, or a user with a second group")
	return 0, 0
}

func chownTree(t *testing.T, root string, uid, gid int) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	}))
}

func requireOwnedTree(t *testing.T, root string, want [2]uint32) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		require.NoError(t, err)
		require.Equal(t, want, ownerOf(t, p), "owner of %s", p)
		return nil
	}))
}

// scenario: upgrade-keeps-data-owner — root's upgrade leaves every entry of the data directory, safemode.json
// included, with the data's owner; the pin's new entries under a target whose root another owner has take that owner,
// an older entry keeps its own; the daemon serves after it. As root on Linux the data is uid 1000's and the target
// 1001's; otherwise both get another of this user's groups.
func TestScenarioUpgradeKeepsDataOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	target := t.TempDir()
	n := newNode(t, upgradeKey(), backupYAML(target))
	c, stop := n.start(t)
	seedTeam(t, c)
	_, err := c.Apply(ctx, configMap("a"))
	require.NoError(t, err)
	var older []string
	require.Eventually(t, func() bool {
		older, _ = filepath.Glob(filepath.Join(target, "gen", "*", "*", "manifest.yaml"))
		return len(older) > 0
	}, 10*time.Second, 20*time.Millisecond, "the daemon's first backup run")
	stop()

	duid, dgid := ownerFor(t, 1000)
	chownTree(t, n.dataDir, duid, dgid)
	tuid, tgid := ownerFor(t, 1001)
	for _, d := range []string{target, filepath.Join(target, "gen")} {
		require.NoError(t, os.Lchown(d, tuid, tgid))
	}
	data, tgt, olderOwner := ownerOf(t, n.dataDir), ownerOf(t, target), ownerOf(t, older[0])

	var out bytes.Buffer
	require.NoError(t, runUpgrade(ctx, &out, upgradeFlags{config: n.cfgPath}, fakeFuncd(t, "v0.9.0"), fakeFuncd(t, "dev")), out.String())
	requireOwnedTree(t, n.dataDir, data)
	require.FileExists(t, filepath.Join(n.dataDir, safemode.StateFile))
	pin := readSafeMode(t, n.dataDir).Upgrade.Generation
	require.NotNil(t, pin)
	requireOwnedTree(t, filepath.Join(target, "gen", string(backup.PreUpgrade)), tgt)
	require.Equal(t, olderOwner, ownerOf(t, older[0]), "an older entry keeps its owner")
	require.Equal(t, olderOwner, ownerOf(t, filepath.Dir(older[0])), "an older entry keeps its owner")

	c, _ = n.start(t)
	_, err = c.Get(ctx, v1.KindConfigMap, "team", "a")
	require.NoError(t, err)
}

// scenario: rollback-to-pre-upgrade — v0.8.0 serves A, upgrades to v0.9.0, which serves B; Decision 2's runbook
// (stores moved aside, `<self>.previous restore run <pin>`, .previous installed) starts v0.8.0 held with A and
// without B.
func TestScenarioRollbackToPreUpgrade(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	v08, v09 := funcdBinary(t, "v0.8.0"), funcdBinary(t, "v0.9.0")
	self := installed(t, v08)
	n := newNode(t, upgradeKey(), backupYAML(t.TempDir()))

	a := n.serveWith(t, self)
	c := a.serving(t, n)
	seedTeam(t, c)
	_, err := c.Apply(ctx, configMap("a"))
	require.NoError(t, err)
	a.stop(t)

	out := runFuncd(t, self, "upgrade", v09, "--unit", "", "--config", n.cfgPath)
	pin := readSafeMode(t, n.dataDir).Upgrade.Generation
	require.NotNil(t, pin, out)
	point := fmt.Sprintf("%s/%d", pin.Timeline, pin.Generation)
	require.Contains(t, out, self+".previous restore run "+point)
	require.Contains(t, runFuncd(t, self, "version"), "v0.9.0")

	b := n.serveWith(t, self)
	c = b.serving(t, n)
	_, err = c.Apply(ctx, configMap("b"))
	require.NoError(t, err)
	b.stop(t)

	for _, d := range []string{"store", "workflow", "deadletter"} {
		require.NoError(t, os.Rename(filepath.Join(n.dataDir, d), filepath.Join(n.dataDir, d+".moved")))
	}
	// The operator's escrow set (ADR-0204): the node's master secret, which the generation names.
	escrowDir := t.TempDir()
	master, err := os.ReadFile(s3gateway.MasterPath("", n.dataDir))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(escrowDir, "master"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(escrowDir, "master", "node.key"), master, 0o600))
	runFuncd(t, self+".previous", "restore", "run", point, "--config", n.cfgPath, "--escrow", escrowDir)
	require.NoError(t, os.Rename(self+".previous", self))
	r := n.serveWith(t, self)
	c = r.serving(t, n)
	ev, err := c.HoldStatus(ctx)
	require.NoError(t, err)
	require.True(t, ev.Held)
	require.Equal(t, "restore", ev.Marker.Reason)
	_, err = c.Get(ctx, v1.KindConfigMap, "team", "a")
	require.NoError(t, err, "the pin holds what v0.8.0 wrote")
	_, err = c.Get(ctx, v1.KindConfigMap, "team", "b")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "nothing v0.9.0 wrote after the pin: %v", err)
	require.Contains(t, runFuncd(t, self, "version"), "v0.8.0")
}

// scenario: manual-swap-warns — v0.9.0 started on data v0.8.0 last ran, with no upgrade to it recorded, logs one
// Warn naming both versions and the missing pre-upgrade generation, and serves.
func TestScenarioManualSwapWarns(t *testing.T) {
	t.Parallel()
	v08, v09 := funcdBinary(t, "v0.8.0"), funcdBinary(t, "v0.9.0")
	n := newNode(t, nil, "")
	a := n.serveWith(t, v08)
	a.serving(t, n)
	a.stop(t)
	require.Equal(t, "v0.8.0", readSafeMode(t, n.dataDir).Version)

	b := n.serveWith(t, v09)
	b.serving(t, n)
	logs := b.logs.String()
	require.Equal(t, 1, strings.Count(logs, "without `funcd upgrade`"), logs)
	require.Contains(t, logs, "no pre-upgrade generation")
	require.Contains(t, logs, `"previous":"v0.8.0"`)
	require.Contains(t, logs, `"version":"v0.9.0"`)
}

// scenario: safe-mode-holds — two unclean starts and a third killed: the next start is held with reason safe-mode,
// logs an Error naming 3 and the last error, and neither a 100 ms timer nor the platform backup acts.
func TestScenarioSafeModeHolds(t *testing.T) {
	if runChildDaemon(t) {
		return
	}
	t.Parallel()
	ctx := context.Background()
	n := newNode(t, nil, restorePacing)
	s, _, _, err := safemode.Begin(n.dataDir, version.Version, safemode.Config{AfterCrashes: 3})
	require.NoError(t, err)
	require.NoError(t, s.Failed(errors.New("assemble platform: boom")))
	beginStarts(t, n.dataDir, 1, 3)
	again := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$") //nolint:gosec // this test binary
	again.Env = append(os.Environ(), childDaemonEnv+"="+n.cfgPath)
	killed := startChild(t, again)
	killed.serving(t, n)
	killed.kill(t)
	require.Equal(t, 3, readSafeMode(t, n.dataDir).Unclean)

	eng, err := badgerstore.Open(filepath.Join(n.dataDir, "store"))
	require.NoError(t, err)
	st := store.New(eng)
	for _, o := range []v1.Object{
		&v1.Namespace{TypeMeta: tmeta(v1.KindNamespace), ObjectMeta: v1.ObjectMeta{Name: "team"}},
		&v1.ResourceGroup{TypeMeta: tmeta(v1.KindResourceGroup), ObjectMeta: om("rg")},
		&v1.EventSource{TypeMeta: tmeta(v1.KindEventSource), ObjectMeta: om("tick"), Spec: v1.EventSourceSpec{
			Timer: &v1.TimerSource{Events: []v1.TimerEvent{{Name: "t", Interval: v1.Duration(100 * time.Millisecond)}}}}},
		&v1.Sensor{TypeMeta: tmeta(v1.KindSensor), ObjectMeta: om("s"), Spec: v1.SensorSpec{
			On: []v1.Dependency{{Name: "tick", Source: "tick", Event: "t"}},
			Do: []v1.Action{{Name: "a", On: "tick", Workflow: "wf"}}}},
	} {
		_, err := st.Create(ctx, o)
		require.NoError(t, err)
	}
	require.NoError(t, st.Close())
	target := t.TempDir()
	withBackup := filepath.Join(t.TempDir(), "funcdconfig.yaml")
	base, err := os.ReadFile(n.cfgPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(withBackup, append(base, backupYAML(target)+
		fmt.Sprintf("secrets:\n  encryptionKeyFile: %q\n", writeKey(t))...), 0o600))
	n.cfgPath = withBackup

	c, _ := n.start(t)
	ev, err := c.HoldStatus(ctx)
	require.NoError(t, err)
	require.True(t, ev.Held)
	require.Equal(t, safemode.MarkerReason, ev.Marker.Reason)
	require.Contains(t, n.logs.String(), `"unclean":3`)
	require.Contains(t, n.logs.String(), "assemble platform: boom")
	acted := func() (int, error) {
		invs, err1 := c.List(ctx, v1.KindInvocation, "team")
		runs, err2 := c.List(ctx, v1.KindWorkflowRun, "team")
		dls, err3 := c.DeadLetters(ctx, "team")
		return len(invs) + len(runs) + len(dls), errors.Join(err1, err2, err3)
	}
	require.Never(t, func() bool {
		k, err := acted()
		return err == nil && k > 0
	}, 500*time.Millisecond, 50*time.Millisecond, "a held timer fired")
	count, err := acted()
	require.NoError(t, err)
	require.Zero(t, count, "a held timer fired")
	bs, err := c.PlatformBackup(ctx)
	require.NoError(t, err)
	require.True(t, bs.Held)
	require.NoDirExists(t, filepath.Join(target, "gen"), "a held platform backup wrote a generation")
}

func writeKey(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secrets.key")
	require.NoError(t, os.WriteFile(p, upgradeKey(), 0o600))
	return p
}

// scenario: stable-run-resets-count — after two unclean starts, a third that runs stableAfter, or one stopped by a
// signal before it, sets the count to 0, so two more unclean starts stay normal.
func TestScenarioStableRunResetsCount(t *testing.T) {
	t.Parallel()
	n := newNode(t, nil, "recovery:\n  safeMode:\n    stableAfter: 200ms\n")
	beginStarts(t, n.dataDir, 2, 3)
	_, stop := n.start(t)
	require.Eventually(t, func() bool { return readSafeMode(t, n.dataDir).Unclean == 0 }, 5*time.Second, 20*time.Millisecond,
		"the start ran stableAfter")
	stop()

	beginStarts(t, n.dataDir, 2, 3)
	cfg, err := os.ReadFile(n.cfgPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(n.cfgPath, bytes.Replace(cfg, []byte("stableAfter: 200ms"), []byte("stableAfter: 1h"), 1), 0o600))
	_, stop = n.start(t)
	require.Equal(t, 3, readSafeMode(t, n.dataDir).Unclean, "the third start counts until it ends")
	stop()
	require.Equal(t, 0, readSafeMode(t, n.dataDir).Unclean, "a signal stop is clean")
	for range 2 {
		_, _, mode, err := safemode.Begin(n.dataDir, version.Version, safemode.Config{AfterCrashes: 3})
		require.NoError(t, err)
		require.Equal(t, safemode.Normal, mode)
	}
}

// scenario: safe-mode-stops-loop — after an upgrade from v0.8.0, v0.9.0 killed once (normal) and once (held, with
// afterCrashes 1) exits 70 at its next start, opening no store and naming the count and `<self>.previous restore run`
// of the pin; after a --no-snapshot upgrade it names "no way back" and `restore run verified`.
func TestScenarioSafeModeStopsLoop(t *testing.T) {
	t.Parallel()
	v08, v09 := funcdBinary(t, "v0.8.0"), funcdBinary(t, "v0.9.0")
	self := installed(t, v08)
	n := newNode(t, upgradeKey(), backupYAML(t.TempDir())+"recovery:\n  safeMode:\n    afterCrashes: 1\n")
	a := n.serveWith(t, self)
	a.serving(t, n)
	a.stop(t)
	runFuncd(t, self, "upgrade", v09, "--unit", "", "--config", n.cfgPath)
	pin := readSafeMode(t, n.dataDir).Upgrade.Generation
	require.NotNil(t, pin)

	for range 2 {
		ch := n.serveWith(t, self)
		ch.serving(t, n)
		ch.kill(t)
	}
	meta := filepath.Join(n.dataDir, "store")
	require.NoError(t, os.Rename(meta, meta+".aside"))
	stopped := n.serveWith(t, self)
	require.Equal(t, safemode.ExitStopped, stopped.exit(t), stopped.logs.String())
	require.NoDirExists(t, meta, "a stopped start opens no store")
	logs := stopped.logs.String()
	require.Contains(t, logs, "2 unclean starts")
	require.Contains(t, logs, fmt.Sprintf("%s.previous restore run %s/%d", self, pin.Timeline, pin.Generation))
	h, err := hold.Open(n.dataDir)
	require.NoError(t, err)
	m, held := h.Marker()
	require.True(t, held)
	require.Equal(t, safemode.MarkerReason, m.Reason)

	require.NoError(t, safemode.RecordUpgrade(n.dataDir, safemode.Upgrade{From: "v0.8.0", To: "v0.9.0"}))
	beginStarts(t, n.dataDir, 2, 1)
	again := n.serveWith(t, self)
	require.Equal(t, safemode.ExitStopped, again.exit(t), again.logs.String())
	require.Contains(t, again.logs.String(), "no way back")
	require.Contains(t, again.logs.String(), "restore run verified")
}

// scenario: leave-safe-mode — held by safe mode, `hold release` and a run of stableAfter leave no marker and a count
// of 0; after a stopped start (exit status 70), `funcd safe-mode reset` makes the next start held.
func TestScenarioLeaveSafeMode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	n := newNode(t, nil, "recovery:\n  safeMode:\n    stableAfter: 300ms\n")
	marker := filepath.Join(n.dataDir, hold.MarkerFile)
	beginStarts(t, n.dataDir, 3, 3)
	c, stop := n.start(t)
	ev, err := c.HoldStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, safemode.MarkerReason, ev.Marker.Reason)
	require.NoError(t, c.ReleaseHold(ctx, nil))
	require.Eventually(t, func() bool {
		_, err := os.Stat(marker)
		return errors.Is(err, fs.ErrNotExist) && readSafeMode(t, n.dataDir).Unclean == 0
	}, 5*time.Second, 20*time.Millisecond)
	stop()

	beginStarts(t, n.dataDir, 6, 3)
	err = serve(ctx, n.cfgPath, nil, &n.logs)
	require.Equal(t, safemode.ExitStopped, exitCode(err), "%v", err)
	require.FileExists(t, marker)
	var out bytes.Buffer
	cmd := newRootCmd(&out)
	cmd.SetArgs([]string{"safe-mode", "reset", "--config", n.cfgPath})
	require.NoError(t, cmd.ExecuteContext(ctx))
	require.Contains(t, out.String(), "stays held")
	require.Equal(t, 0, readSafeMode(t, n.dataDir).Unclean)
	c, _ = n.start(t)
	ev, err = c.HoldStatus(ctx)
	require.NoError(t, err)
	require.True(t, ev.Held)
	require.Equal(t, safemode.MarkerReason, ev.Marker.Reason)
}

// The platform backup's parent is the generation a restore loaded (ADR-0205 Inputs.Parent from restore.Parent, the
// obligation ADR-0205 left to the later of ADR-0206 and ADR-0207): after the release, the first generation names it.
func TestBackupParentWired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newGSource(t, upgradeKey())
	m := s.cut(t)
	target := t.TempDir()
	n := newNode(t, upgradeKey(), backupYAML(target)+"  retryInterval: 100ms\n")
	_, err := n.restore("run", "latest", "--from", s.target)
	require.NoError(t, err)
	c, _ := n.start(t)
	require.NoError(t, c.ReleaseHold(ctx, nil))
	var manifests []string
	require.Eventually(t, func() bool {
		manifests, _ = filepath.Glob(filepath.Join(target, "gen", "*", "*", "manifest.yaml"))
		return len(manifests) > 0
	}, 10*time.Second, 20*time.Millisecond, "a backup run after the release")
	data, err := os.ReadFile(manifests[0])
	require.NoError(t, err)
	var got backup.Manifest
	require.NoError(t, yaml.Unmarshal(data, &got))
	require.Equal(t, &backup.GenRef{Timeline: m.Timeline, Generation: m.Generation}, got.Parent)
}
