package upgrade_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/safemode"
	"github.com/pyvvo/funcd/internal/store"
	badgerstore "github.com/pyvvo/funcd/internal/store/badger"
	"github.com/pyvvo/funcd/internal/upgrade"
	runbadger "github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

// fixture is an installed funcd: a binary at self printing v0.8.0, a seeded metastore and a file:// target.
type fixture struct {
	dataDir, targetDir, self string
	cfg                      config.Config
	out                      bytes.Buffer
}

func newFixture(t *testing.T, installed string) *fixture {
	t.Helper()
	f := &fixture{dataDir: t.TempDir(), targetDir: t.TempDir()}
	cfgDir := t.TempDir()
	key := filepath.Join(cfgDir, "secrets.key")
	require.NoError(t, os.WriteFile(key, bytes.Repeat([]byte{1}, 32), 0o600))
	path := filepath.Join(cfgDir, "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(path, fmt.Appendf(nil, "storage:\n  mode: file\n  dataDir: %q\nsecrets:\n  "+
		"encryptionKeyFile: %q\nbackup:\n  target: %q\n  encryption:\n    none: true\n", f.dataDir, key,
		gocloud.FileURL(f.targetDir)), 0o600))
	var err error
	f.cfg, err = config.Load(path, config.Flags{})
	require.NoError(t, err)
	eng, err := badgerstore.Open(f.cfg.Storage.MetastoreDir)
	require.NoError(t, err)
	st := store.New(eng)
	_, err = st.Create(context.Background(), &v1.Namespace{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindNamespace.GVK().APIVersion(), Kind: v1.KindNamespace},
		ObjectMeta: v1.ObjectMeta{Name: "team"}})
	require.NoError(t, err)
	require.NoError(t, st.Close())
	f.self = fakeBinary(t, "funcd", installed)
	return f
}

// fakeBinary is a script that answers `version` as funcd ver does.
func fakeBinary(t *testing.T, name, ver string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, fmt.Appendf(nil, "#!/bin/sh\necho 'funcd %s (commit abc1234, built today, go1.26, test)'\n", ver), 0o755)) //nolint:gosec // a test binary
	return p
}

func versionOf(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command(bin, "version").Output() //nolint:gosec // a test binary
	require.NoError(t, err)
	return strings.Fields(string(out))[1]
}

func (f *fixture) options(newBin string) upgrade.Options {
	return upgrade.WithVersion(upgrade.Options{Config: f.cfg, NewBinary: newBin, Self: f.self, Out: &f.out}, "v0.8.0")
}

func (f *fixture) manifest(t *testing.T, ref backup.GenRef) backup.Manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.targetDir, backup.GenDir(backup.PreUpgrade, ref.Generation, ref.Timeline), "manifest.yaml"))
	require.NoError(t, err)
	var m backup.Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	return m
}

func (f *fixture) requireNotSwapped(t *testing.T, installed string) {
	t.Helper()
	require.Equal(t, installed, versionOf(t, f.self))
	for _, suffix := range []string{".new", ".previous.tmp", ".previous"} {
		require.NoFileExists(t, f.self+suffix)
	}
}

// systemd is a fake systemctl: show answers ExecStart, stop and start run their hooks, every call is recorded.
type systemd struct {
	mu              sync.Mutex
	calls           []string
	execStart       string
	onStop, onStart func() error
}

func (s *systemd) run(args ...string) ([]byte, error) {
	s.mu.Lock()
	s.calls = append(s.calls, args[0])
	s.mu.Unlock()
	switch args[0] {
	case "show":
		return fmt.Appendf(nil, "ExecStart={ path=%s ; argv[]=%s ; ignore_errors=no ; start_time=[n/a] }\n", s.execStart, s.execStart), nil
	case "stop":
		if s.onStop != nil {
			return nil, s.onStop()
		}
	case "start":
		if s.onStart != nil {
			return nil, s.onStart()
		}
	}
	return nil, nil
}

func (s *systemd) called() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// scenario: upgrade-pins-old-state — with --unit "" and the daemon stopped, or with --unit and the daemon holding the
// target's lock and the metastore, the pre-upgrade manifest names funcd v0.8.0, the installed path prints v0.9.0 and
// .previous v0.8.0; with --unit the stop comes before the lock and the start after the swap. It sets the process-wide
// version.Version, which the manifest records, so it stays serial.
func TestScenarioUpgradePinsOldState(t *testing.T) {
	prev := version.Version
	t.Cleanup(func() { version.Version = prev })
	version.Version = "v0.8.0"
	ctx := context.Background()

	t.Run("unit empty, daemon stopped", func(t *testing.T) {
		f := newFixture(t, "v0.8.0")
		res, err := upgrade.Run(ctx, f.options(fakeBinary(t, "funcd-0.9.0", "v0.9.0")))
		require.NoError(t, err, f.out.String())
		require.NotNil(t, res.Pin)
		require.Equal(t, "v0.8.0", f.manifest(t, *res.Pin).Funcd)
		require.Equal(t, "v0.9.0", versionOf(t, f.self))
		require.Equal(t, "v0.8.0", versionOf(t, f.self+".previous"))
		require.Contains(t, f.out.String(), fmt.Sprintf(".previous restore run %s/%d", res.Pin.Timeline, res.Pin.Generation))
	})

	t.Run("unit set, daemon running", func(t *testing.T) {
		f := newFixture(t, "v0.8.0")
		tg, err := backup.Open(ctx, backup.Config{Target: f.cfg.Backup.Target, DataDir: f.dataDir, Retention: backup.Retention{Hourly: 1}})
		require.NoError(t, err)
		require.NoError(t, tg.Ready(ctx), "the daemon holds the target's lock")
		eng, err := badgerstore.Open(f.cfg.Storage.MetastoreDir)
		require.NoError(t, err)
		sd := &systemd{execStart: f.self}
		sd.onStop = func() error { return errors.Join(eng.Close(), tg.Close()) }
		sd.onStart = func() error {
			if v := versionOf(t, f.self); v != "v0.9.0" {
				return fmt.Errorf("started %s before the swap", v)
			}
			return nil
		}
		o := f.options(fakeBinary(t, "funcd-0.9.0", "v0.9.0"))
		o.Unit, o.Systemctl = "funcd.service", sd.run
		res, err := upgrade.Run(ctx, o)
		require.NoError(t, err, f.out.String())
		require.Equal(t, []string{"show", "stop", "start"}, sd.called())
		require.Equal(t, "v0.8.0", f.manifest(t, *res.Pin).Funcd)
		require.Equal(t, "v0.8.0", versionOf(t, f.self+".previous"))
	})
}

// scenario: snapshot-unavailable-stops-upgrade — no backup.target, a failing s3:// probe or an empty metastore
// directory stop the upgrade before the stop, naming the cause and --no-snapshot; a live Badger with --unit "" is
// fault.Conflict; --no-snapshot swaps and warns once.
func TestScenarioSnapshotUnavailableStopsUpgrade(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>AccessDenied</Code><Message>denied</Message></Error>`))
	}))
	t.Cleanup(forbidden.Close)
	creds := filepath.Join(t.TempDir(), "aws")
	require.NoError(t, os.WriteFile(creds, []byte("[default]\naws_access_key_id = AKIDTEST\naws_secret_access_key = secret\n"), 0o600))

	for _, c := range []struct {
		name  string
		set   func(t *testing.T, f *fixture)
		kind  fault.Kind
		names func(f *fixture) []string
	}{
		{"no backup.target", func(_ *testing.T, f *fixture) { f.cfg.Backup.Target = "" }, fault.Invalid,
			func(*fixture) []string { return []string{"backup.target"} }},
		{"a failing s3 probe", func(_ *testing.T, f *fixture) {
			f.cfg.Backup.Target = "s3://funcd-backup?region=us-east-1&use_path_style=true&endpoint=" + forbidden.URL
			f.cfg.Backup.CredentialsFile = creds
		}, "", func(*fixture) []string { return []string{"probe"} }},
		{"an empty metastore directory", func(t *testing.T, f *fixture) {
			require.NoError(t, os.RemoveAll(f.cfg.Storage.MetastoreDir))
			require.NoError(t, os.MkdirAll(f.cfg.Storage.MetastoreDir, 0o700))
		}, fault.Invalid, func(f *fixture) []string { return []string{f.cfg.Storage.MetastoreDir, "--config"} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, "v0.8.0")
			c.set(t, f)
			sd := &systemd{execStart: f.self}
			o := f.options(fakeBinary(t, "funcd-0.9.0", "v0.9.0"))
			o.Unit, o.Systemctl = "funcd.service", sd.run
			_, err := upgrade.Run(ctx, o)
			require.Error(t, err)
			if c.kind != "" {
				require.Equal(t, c.kind, fault.KindOf(err), "%v", err)
			}
			for _, n := range append(c.names(f), "--no-snapshot") {
				require.ErrorContains(t, err, n)
			}
			require.Equal(t, []string{"show"}, sd.called(), "nothing is stopped")
			f.requireNotSwapped(t, "v0.8.0")
		})
	}

	t.Run("a live Badger with --unit empty", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, "v0.8.0")
		eng, err := badgerstore.Open(f.cfg.Storage.MetastoreDir)
		require.NoError(t, err)
		t.Cleanup(func() { _ = eng.Close() })
		_, err = upgrade.Run(ctx, f.options(fakeBinary(t, "funcd-0.9.0", "v0.9.0")))
		require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
		require.ErrorContains(t, err, f.cfg.Storage.MetastoreDir)
		f.requireNotSwapped(t, "v0.8.0")
	})

	t.Run("--no-snapshot", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, "v0.8.0")
		f.cfg.Backup.Target = ""
		o := f.options(fakeBinary(t, "funcd-0.9.0", "v0.9.0"))
		o.NoSnapshot = true
		res, err := upgrade.Run(ctx, o)
		require.NoError(t, err)
		require.Nil(t, res.Pin)
		require.Equal(t, 1, strings.Count(f.out.String(), "warning:"), f.out.String())
		require.Equal(t, "v0.9.0", versionOf(t, f.self))
		require.Equal(t, "v0.8.0", versionOf(t, f.self+".previous"))
		st := readState(t, f.dataDir)
		require.Equal(t, "v0.9.0", st.Upgrade.To)
		require.Nil(t, st.Upgrade.Generation)
	})
}

func readState(t *testing.T, dataDir string) safemode.State {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, safemode.StateFile))
	require.NoError(t, err)
	var s safemode.State
	require.NoError(t, json.Unmarshal(data, &s))
	return s
}

// ownTo gives dirs another owner than this process's, as the repo unit's funcd user owns the data: a second uid as
// root on Linux, else another of this user's groups. It returns the owner every entry must end with.
func ownTo(t *testing.T, dirs ...string) [2]uint32 {
	t.Helper()
	uid, gid := -1, -1
	if os.Geteuid() == 0 && runtime.GOOS == "linux" {
		uid, gid = 1000, 1000
	} else {
		groups, err := os.Getgroups()
		require.NoError(t, err)
		for _, g := range groups {
			if g != os.Getgid() {
				gid = g
				break
			}
		}
		if gid < 0 {
			t.Skip("needs root on Linux, or a user with a second group")
		}
	}
	for _, d := range dirs {
		require.NoError(t, os.Lchown(d, uid, gid))
	}
	return ownerOf(t, dirs[0])
}

func ownerOf(t *testing.T, p string) [2]uint32 {
	t.Helper()
	info, err := os.Lstat(p)
	require.NoError(t, err)
	st, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	return [2]uint32{st.Uid, st.Gid}
}

// ownedBy reports the first entry under root whose owner is not want.
func ownedBy(root string, want [2]uint32) error {
	return filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if st := info.Sys().(*syscall.Stat_t); st.Uid != want[0] || st.Gid != want[1] {
			return fmt.Errorf("%s is owned by %d:%d, want %d:%d", p, st.Uid, st.Gid, want[0], want[1])
		}
		return nil
	})
}

// scenario: write-failure-keeps-old-binary — a target refusing a part after the unit stopped fails the write naming
// the store; no binary is swapped, every entry under the data and file:// root has the data's owner when systemctl
// start runs, and the old binary starts.
func TestScenarioWriteFailureKeepsOldBinary(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "v0.8.0")
	want := ownTo(t, f.dataDir, f.targetDir)
	sd := &systemd{execStart: f.self}
	sd.onStop = func() error {
		gen := filepath.Join(f.targetDir, "gen")
		if err := os.Mkdir(gen, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(gen, "pre-upgrade"), []byte("not a directory"), 0o600); err != nil {
			return err
		}
		return hold.Own(f.targetDir, gen)
	}
	var owners error
	sd.onStart = func() error {
		owners = errors.Join(ownedBy(f.dataDir, want), ownedBy(f.targetDir, want))
		if v := versionOf(t, f.self); v != "v0.8.0" {
			owners = errors.Join(owners, fmt.Errorf("started %s", v))
		}
		return nil
	}
	o := f.options(fakeBinary(t, "funcd-0.9.0", "v0.9.0"))
	o.Unit, o.Systemctl = "funcd.service", sd.run
	_, err := upgrade.Run(context.Background(), o)
	require.Error(t, err)
	require.Regexp(t, regexp.MustCompile(`gen/pre-upgrade/\d+-[0-9a-f]+/(events|metastore|runs)/`), err.Error())
	require.ErrorContains(t, err, "--no-snapshot")
	require.Equal(t, []string{"show", "stop", "start"}, sd.called())
	require.NoError(t, owners)
	f.requireNotSwapped(t, "v0.8.0")
}

// scenario: downgrade-refused — v0.8.0 over an installed v0.9.0 is fault.Invalid naming both, before any systemctl
// call, object or file.
func TestScenarioDowngradeRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "v0.9.0")
	sd := &systemd{execStart: f.self}
	o := upgrade.WithVersion(f.options(fakeBinary(t, "funcd-0.8.0", "v0.8.0")), "v0.9.0")
	o.Unit, o.Systemctl = "funcd.service", sd.run
	_, err := upgrade.Run(context.Background(), o)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "v0.8.0")
	require.ErrorContains(t, err, "v0.9.0")
	require.Empty(t, sd.called())
	entries, err := os.ReadDir(f.targetDir)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.NoFileExists(t, filepath.Join(f.dataDir, safemode.StateFile))
	f.requireNotSwapped(t, "v0.9.0")
}

// scenario: pre-upgrade-retention-reported — with four complete pins and preUpgrade 3, the fifth reports the two
// oldest past backup.retention.preUpgrade with their prune lines, and deletes nothing.
func TestScenarioPreUpgradeRetentionReported(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t, "v0.8.0")
	var pins []backup.Manifest
	for range 4 {
		m, err := upgrade.Snapshot(ctx, f.cfg)
		require.NoError(t, err)
		pins = append(pins, m)
	}
	res, err := upgrade.Run(ctx, f.options(fakeBinary(t, "funcd-0.9.0", "v0.9.0")))
	require.NoError(t, err, f.out.String())
	require.Len(t, res.PastRetention, 2)
	for i, e := range res.PastRetention {
		require.Equal(t, pins[i].Generation, e.Generation)
		require.Contains(t, f.out.String(), "rm -r "+filepath.Join(f.targetDir, backup.GenDir(backup.PreUpgrade, e.Generation, e.Timeline)))
	}
	require.Contains(t, f.out.String(), "past backup.retention.preUpgrade 3")
	for _, m := range pins {
		f.manifest(t, backup.GenRef{Timeline: m.Timeline, Generation: m.Generation})
	}
}

// Q13 (DR, 2026-10-10): a held platform refuses the upgrade naming the marker and its reason; --no-snapshot goes on
// with one warning naming it.
func TestUpgradeWhileHeld(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "v0.8.0")
	require.NoError(t, hold.Write(f.dataDir, hold.Marker{Reason: "restore"}))
	_, err := upgrade.Run(context.Background(), f.options(fakeBinary(t, "funcd-0.9.0", "v0.9.0")))
	require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, filepath.Join(f.dataDir, hold.MarkerFile))
	require.ErrorContains(t, err, "reason restore")
	f.requireNotSwapped(t, "v0.8.0")

	o := f.options(fakeBinary(t, "funcd-0.9.0", "v0.9.0"))
	o.NoSnapshot = true
	_, err = upgrade.Run(context.Background(), o)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(f.out.String(), "the platform is held"), f.out.String())
	require.Equal(t, "v0.9.0", versionOf(t, f.self))
}

func TestParseVersionOutput(t *testing.T) {
	t.Parallel()
	v, err := upgrade.ParseVersion("new", []byte("funcd v0.9.0-3-gabc1234 (commit abc1234, built 2026-10-10, go1.26, linux/amd64)\n"))
	require.NoError(t, err)
	require.Equal(t, "v0.9.0-3-gabc1234", v)
	for _, out := range []string{"", "funcd\n", "Usage: other v1.0.0\n"} {
		_, err := upgrade.ParseVersion("new", []byte(out))
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%q", out)
	}
}

// A unit whose ExecStart is not this binary is fault.Invalid naming both, before anything stops.
func TestExecStartMismatch(t *testing.T) {
	t.Parallel()
	require.Equal(t, "/usr/local/bin/funcd", upgrade.ExecStartPath([]byte("ExecStart={ path=/usr/local/bin/funcd ; argv[]=/usr/local/bin/funcd ; }\n")))
	require.Empty(t, upgrade.ExecStartPath([]byte("ExecStart=\n")))

	f := newFixture(t, "v0.8.0")
	other := fakeBinary(t, "funcd", "v0.8.0")
	for _, execStart := range []string{other, ""} {
		sd := &systemd{execStart: execStart}
		o := f.options(fakeBinary(t, "funcd-0.9.0", "v0.9.0"))
		o.Unit, o.Systemctl = "funcd.service", sd.run
		_, err := upgrade.Run(context.Background(), o)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
		require.ErrorContains(t, err, "funcd.service")
		if execStart != "" {
			require.ErrorContains(t, err, other)
			require.ErrorContains(t, err, f.self)
		}
		require.Equal(t, []string{"show"}, sd.called())
	}
	f.requireNotSwapped(t, "v0.8.0")
}

// A swap that fails leaves self as it was and removes the temporary names.
func TestSwapLeavesBinaryOnFailure(t *testing.T) {
	t.Parallel()
	self, newBin := fakeBinary(t, "funcd", "v0.8.0"), fakeBinary(t, "funcd-new", "v0.9.0")
	require.Error(t, upgrade.Swap(filepath.Join(t.TempDir(), "absent"), self))
	require.NoError(t, os.MkdirAll(filepath.Join(self+".previous", "keep"), 0o700))
	require.Error(t, upgrade.Swap(newBin, self))
	require.Equal(t, "v0.8.0", versionOf(t, self))
	require.NoFileExists(t, self+".new")
	require.NoFileExists(t, self+".previous.tmp")

	require.NoError(t, os.RemoveAll(self+".previous"))
	require.NoError(t, upgrade.Swap(newBin, self))
	require.Equal(t, "v0.9.0", versionOf(t, self))
	require.Equal(t, "v0.8.0", versionOf(t, self+".previous"))
	info, err := os.Stat(self)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}

// A store or the file:// lock a running daemon holds is fault.Conflict naming its directory (Q5).
func TestStoreHeldConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t, "v0.8.0")
	runs, err := runbadger.New(runbadger.Config{Dir: f.cfg.Workflow.DataDir})
	require.NoError(t, err)
	_, err = upgrade.Snapshot(ctx, f.cfg)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "workflow.dataDir "+f.cfg.Workflow.DataDir)
	require.NoError(t, runs.Close())

	tg, err := backup.Open(ctx, backup.Config{Target: f.cfg.Backup.Target, DataDir: f.dataDir, Retention: backup.Retention{Hourly: 1}})
	require.NoError(t, err)
	require.NoError(t, tg.Ready(ctx))
	_, err = upgrade.Snapshot(ctx, f.cfg)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, f.targetDir)
	require.NoError(t, tg.Close())

	m, err := upgrade.Snapshot(ctx, f.cfg)
	require.NoError(t, err)
	require.Equal(t, backup.Format, m.Format)
}
