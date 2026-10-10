// Package upgrade is `funcd upgrade` (ADR-0207 Decision 1): the installed binary checks the new one, stops the unit,
// writes a pre-upgrade generation of the three stores that it can restore itself, swaps the binary keeping
// <self>.previous, records the upgrade for safe mode and starts the unit again.
package upgrade

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/eventing/eventstore"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/restore"
	"github.com/pyvvo/funcd/internal/safemode"
	"github.com/pyvvo/funcd/internal/store"
	badgerstore "github.com/pyvvo/funcd/internal/store/badger"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	runbadger "github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

// noSnapshotHint ends every error of steps 2 and 4: the operator's explicit way to upgrade without a way back.
const noSnapshotHint = "or pass --no-snapshot to upgrade without a way back"

// Options configure Run. Self is the installed binary (os.Executable, symlinks resolved); Unit "" runs no systemctl;
// Systemctl runs `systemctl <args>` and returns its standard output.
type Options struct {
	Config     config.Config
	NewBinary  string
	Self       string
	Unit       string
	NoSnapshot bool
	Systemctl  func(args ...string) ([]byte, error)
	Out        io.Writer
	// version is the installed binary's version, "" ⇒ version.Version; tests set it.
	version string
}

// Result is what Run did: the versions, the pre-upgrade generation (nil with --no-snapshot) and the complete
// pre-upgrade generations past backup.retention.preUpgrade, oldest first.
type Result struct {
	From, To      string
	Pin           *backup.GenRef
	PastRetention []backup.Entry
}

func (o Options) printf(format string, args ...any) {
	if o.Out != nil {
		_, _ = fmt.Fprintf(o.Out, format, args...)
	}
}

// Run is Decision 1's steps in order; a failure stops at once. A failure after the stop starts the unit again on the
// old binary; one after the swap is reported and the swap stands.
func Run(ctx context.Context, o Options) (Result, error) {
	const op = "upgrade.Run"
	res := Result{From: cmp.Or(o.version, version.Version)}
	cfg := o.Config
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	log := slog.New(slog.NewTextHandler(out, nil))

	to, err := newVersion(ctx, o.NewBinary)
	if err != nil {
		return res, err
	}
	res.To = to
	switch c, ok := version.Compare(to, res.From); {
	case !ok:
		o.printf("warning: funcd %s and the installed funcd %s cannot be ordered (no release version): upgrading\n", to, res.From)
	case c < 0:
		return res, fault.Invalidf(op, "%s is funcd %s, older than the installed funcd %s: funcd upgrade does not "+
			"downgrade; roll back with the runbook of examples/restore-runbook.md", o.NewBinary, to, res.From)
	}
	if o.Unit != "" {
		if err := checkUnit(o); err != nil {
			return res, err
		}
	}
	if err := checkHold(o); err != nil {
		return res, err
	}

	var s *snapshotRun
	if o.NoSnapshot {
		o.printf("warning: --no-snapshot: no pre-upgrade generation is written, so this upgrade has no way back to funcd %s\n", res.From)
	} else {
		if s, err = prepare(ctx, cfg, log); err != nil {
			return res, err
		}
		defer s.closeTarget()
	}

	stopped := false
	if o.Unit != "" {
		if _, err := o.Systemctl("stop", o.Unit); err != nil {
			return res, fault.Wrapf(err, fault.Unavailable, op, "systemctl stop %s", o.Unit)
		}
		stopped = true
	}
	restart := func(err error) error {
		if !stopped {
			return err
		}
		if _, serr := o.Systemctl("start", o.Unit); serr != nil {
			return errors.Join(err, fault.Wrapf(serr, fault.Unavailable, op, "systemctl start %s on the old binary", o.Unit))
		}
		return err
	}

	if s != nil {
		m, past, err := s.write(ctx)
		if err != nil {
			return res, restart(fault.Wrapf(err, fault.KindOf(err), op, "write the pre-upgrade generation, %s", noSnapshotHint))
		}
		res.Pin, res.PastRetention = &backup.GenRef{Timeline: m.Timeline, Generation: m.Generation}, past
	}
	if err := swap(o.NewBinary, o.Self); err != nil {
		return res, restart(err)
	}

	err = safemode.RecordUpgrade(cfg.Storage.DataDir, safemode.Upgrade{From: res.From, To: to, Generation: res.Pin,
		At: v1.NewTimestamp(time.Now())})
	if stopped {
		if _, serr := o.Systemctl("start", o.Unit); serr != nil {
			err = errors.Join(err, fault.Wrapf(serr, fault.Unavailable, op, "systemctl start %s", o.Unit))
		}
	}
	o.printf("upgraded %s from funcd %s to funcd %s; the old binary is %s.previous\n", o.Self, res.From, to, o.Self)
	if p := res.Pin; p != nil {
		o.printf("pre-upgrade generation %s/%d: roll back with `%s.previous restore run %s/%d` (examples/restore-runbook.md)\n",
			p.Timeline, p.Generation, o.Self, p.Timeline, p.Generation)
	}
	if len(res.PastRetention) > 0 {
		o.printf("%d complete pre-upgrade generations are past backup.retention.preUpgrade %d; funcd deletes none: "+
			"prune each with a credential that may delete under the prefix (examples/backup-lifecycle.md)\n",
			len(res.PastRetention), cfg.Backup.Retention.PreUpgrade)
		for _, e := range res.PastRetention {
			o.printf("  %s\n", pruneCommand(cfg.Backup.Target, e))
		}
	}
	return res, err
}

// newVersion runs `<new> version` and returns the second field of version.Info.String's line.
func newVersion(ctx context.Context, bin string) (string, error) {
	out, err := exec.CommandContext(ctx, bin, "version").Output() //nolint:gosec // the operator's new binary
	if err != nil {
		return "", fault.Wrapf(err, fault.Invalid, "upgrade.Run", "run `%s version`", bin)
	}
	return parseVersion(bin, out)
}

func parseVersion(bin string, out []byte) (string, error) {
	f := strings.Fields(string(out))
	if len(f) < 2 || f[0] != "funcd" {
		return "", fault.Invalidf("upgrade.Run", "`%s version` printed %q: not a funcd binary", bin, bytes.TrimSpace(out))
	}
	return f[1], nil
}

// checkUnit refuses a unit whose ExecStart path is not Self: the upgrade would swap a binary the unit does not run.
func checkUnit(o Options) error {
	const op = "upgrade.Run"
	out, err := o.Systemctl("show", "-p", "ExecStart", o.Unit)
	if err != nil {
		return fault.Wrapf(err, fault.Unavailable, op, "systemctl show %s", o.Unit)
	}
	path := execStartPath(out)
	if path == "" {
		return fault.Invalidf(op, "unit %s has no ExecStart: is --unit the funcd service?", o.Unit)
	}
	if p, err := filepath.EvalSymlinks(path); err == nil {
		path = p
	}
	if path != o.Self {
		return fault.Invalidf(op, "unit %s runs %s, not this funcd %s: run the upgrade with the unit's binary", o.Unit, path, o.Self)
	}
	return nil
}

// execStartPath is the path= of `systemctl show -p ExecStart` ("ExecStart={ path=/usr/local/bin/funcd ; argv[]=… }").
func execStartPath(out []byte) string {
	_, rest, ok := strings.Cut(string(out), "path=")
	if !ok {
		return ""
	}
	path, _, _ := strings.Cut(rest, " ")
	return strings.TrimSpace(path)
}

// checkHold refuses an upgrade of a held platform, whose data the operator has not accepted yet; with --no-snapshot
// it warns once and goes on.
func checkHold(o Options) error {
	dataDir := o.Config.Storage.DataDir
	h, err := hold.Open(dataDir)
	if err != nil {
		return err
	}
	m, held := h.Marker()
	if !held {
		return nil
	}
	marker := filepath.Join(dataDir, hold.MarkerFile)
	if !o.NoSnapshot {
		return fault.Conflictf("upgrade.Run", "the platform is held (%s, reason %s): release it with `funcdctl hold "+
			"release` once its data is right, then upgrade; %s", marker, m.Reason, noSnapshotHint)
	}
	o.printf("warning: the platform is held (%s, reason %s): upgrading it without a pre-upgrade generation\n", marker, m.Reason)
	return nil
}

// swap installs newBin at self keeping the old one at self.previous: a copy synced beside self, the old binary
// hard-linked and renamed onto .previous, the copy renamed onto self, the directory synced. A failure removes the
// temporary names and leaves self as it was.
func swap(newBin, self string) (err error) {
	const op = "upgrade.swap"
	tmpNew, tmpPrev := self+".new", self+".previous.tmp"
	defer func() {
		if err != nil {
			_ = os.Remove(tmpNew)
			_ = os.Remove(tmpPrev)
			err = fault.Wrapf(err, fault.Internal, op, "install %s at %s; %s is unchanged", newBin, self, self)
		}
	}()
	if err := copyFile(newBin, tmpNew); err != nil {
		return err
	}
	if err := os.Remove(tmpPrev); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Link(self, tmpPrev); err != nil {
		return err
	}
	if err := os.Rename(tmpPrev, self+".previous"); err != nil {
		return err
	}
	if err := os.Rename(tmpNew, self); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(self))
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // the operator's new binary
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755) //nolint:gosec // a binary is executable
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	err = errors.Join(err, out.Sync(), out.Close())
	if err != nil {
		return err
	}
	return os.Chmod(dst, 0o755) //nolint:gosec // a binary is executable
}

// pruneCommand is the operator's command deleting one pre-upgrade generation (Decision 2): rm on a directory target,
// the AWS CLI on s3://.
func pruneCommand(target string, e backup.Entry) string {
	dir := backup.GenDir(e.Class, e.Generation, e.Timeline)
	u, err := url.Parse(target)
	if err != nil {
		return dir
	}
	if u.Scheme == "file" {
		return "rm -r " + filepath.Join(filepath.FromSlash(u.Path), filepath.FromSlash(dir))
	}
	return "aws s3 rm --recursive s3://" + u.Host + "/" + u.Query().Get("prefix") + dir
}

// Snapshot is steps 2 and 4 alone: it writes a pre-upgrade generation of cfg's stores. An empty metastore directory
// is fault.Invalid; a store or the file:// lock held by a running daemon is fault.Conflict naming its directory; every
// return after step 4 began re-owns what root wrote (Decision 1, Owner).
func Snapshot(ctx context.Context, cfg config.Config) (backup.Manifest, error) {
	s, err := prepare(ctx, cfg, slog.Default())
	if err != nil {
		return backup.Manifest{}, err
	}
	defer s.closeTarget()
	m, _, err := s.write(ctx)
	return m, err
}

// snapshotRun is a pre-upgrade generation being written: the checked config, its sealer and its open target.
type snapshotRun struct {
	cfg    config.Config
	sealer *envelope.Sealer
	tg     backup.Target
	// dir is a file:// target's directory, "" on s3://.
	dir string
}

// prepare is step 2: the checks that take no lock. On s3:// the probe runs here; a directory's lock waits for step 4.
// A master secret it creates under <storage.dataDir> takes that directory's owner (Decision 1, Owner).
func prepare(ctx context.Context, cfg config.Config, log *slog.Logger) (*snapshotRun, error) {
	const op = "upgrade.Snapshot"
	if cfg.Storage.Mode != "file" || cfg.Backup.Target == "" {
		return nil, fault.Invalidf(op, "a pre-upgrade generation needs the platform backup on (storage.mode file, "+
			"backup.target set; storage.mode is %q, backup.target %q), %s", cfg.Storage.Mode, cfg.Backup.Target, noSnapshotHint)
	}
	for _, f := range cfg.CheckBackup() {
		if f.Error {
			return nil, fault.Invalidf(op, "%s; %s", f.Message, noSnapshotHint)
		}
	}
	dir := cfg.Storage.MetastoreDir
	if entries, err := os.ReadDir(dir); err != nil || len(entries) == 0 {
		return nil, fault.Invalidf(op, "storage.metastoreDir %s is absent or empty: is --config the daemon's? (its unit "+
			"sets FUNCD_DATA_DIR, this shell may not), %s", dir, noSnapshotHint)
	}
	data := cfg.Storage.DataDir
	before, err := listDirs(data, masterDirs())
	if err != nil {
		return nil, err
	}
	master, err := envelope.LoadMaster(cfg, log)
	if oerr := errors.Join(ownNew(data, data, masterDirs(), before)...); oerr != nil {
		return nil, errors.Join(err, fault.Wrapf(oerr, fault.Internal, op, "re-own the master secret"))
	}
	if err != nil {
		return nil, err
	}
	sealer, err := envelope.FromConfig(cfg, master, log)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "backup.encryption, %s", noSnapshotHint)
	}
	r := cfg.Backup.Retention
	tg, err := backup.Open(ctx, backup.Config{
		Target:          cfg.Backup.Target,
		CredentialsFile: cfg.Backup.CredentialsFile,
		DataDir:         cfg.Storage.DataDir,
		SingleWriter:    cfg.Backup.SingleWriter,
		Retention:       backup.Retention{Hourly: r.Hourly, Daily: r.Daily, Weekly: r.Weekly, Verified: r.Verified},
		Logger:          log,
	})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "%s", noSnapshotHint)
	}
	s := &snapshotRun{cfg: cfg, sealer: sealer, tg: tg}
	u, err := url.Parse(cfg.Backup.Target)
	if err != nil {
		_ = tg.Close()
		return nil, fault.Wrapf(err, fault.Invalid, op, "backup.target")
	}
	if u.Scheme == "file" {
		s.dir = filepath.Clean(filepath.FromSlash(u.Path))
		return s, nil
	}
	if err := tg.Ready(ctx); err != nil {
		_ = tg.Close()
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "the probe of backup.target %s, %s", cfg.Backup.Target, noSnapshotHint)
	}
	return s, nil
}

func (s *snapshotRun) closeTarget() {
	if s.tg != nil {
		_ = s.tg.Close()
		s.tg = nil
	}
}

// stores are the three store directories a generation holds, as restore run loads them.
func (s *snapshotRun) stores() []struct{ key, dir string } {
	c := s.cfg
	return []struct{ key, dir string }{
		{"storage.metastoreDir", c.Storage.MetastoreDir},
		{"workflow.dataDir", c.Workflow.DataDir},
		{"eventing.deadletter.dataDir", c.Eventing.Deadletter.DataDir},
	}
}

// write is step 4: the directory's lock, the stores' locks, the opens and Target.Write with the pre-upgrade pin;
// every return closes what it opened, then re-owns (Decision 1, Owner). It returns the manifest and the complete
// pre-upgrade generations past backup.retention.preUpgrade.
func (s *snapshotRun) write(ctx context.Context) (m backup.Manifest, past []backup.Entry, err error) {
	const op = "upgrade.Snapshot"
	before, err := listDirs(s.dir, trackedDirs())
	if err != nil {
		return m, nil, err
	}
	var closers []io.Closer
	defer func() {
		var errs []error
		for i := len(closers) - 1; i >= 0; i-- {
			errs = append(errs, closers[i].Close())
		}
		s.closeTarget()
		errs = append(errs, s.own(before))
		if cerr := errors.Join(errs...); cerr != nil {
			err = errors.Join(err, fault.Wrapf(cerr, fault.Internal, op, "close and re-own the stores"))
		}
	}()
	if err := s.tg.Ready(ctx); err != nil {
		return m, nil, err
	}
	for _, d := range s.stores() {
		if err := probeLock(op, d.key, d.dir); err != nil {
			return m, nil, err
		}
	}
	eng, err := badgerstore.Open(s.cfg.Storage.MetastoreDir)
	if err != nil {
		return m, nil, err
	}
	st := store.New(eng)
	closers = append(closers, st)
	var runs runstate.Store
	if runs, err = runbadger.New(runbadger.Config{Dir: s.cfg.Workflow.DataDir}); err != nil {
		return m, nil, err
	}
	closers = append(closers, runs)
	events, err := eventstore.Open(eventstore.Config{Dir: s.cfg.Eventing.Deadletter.DataDir})
	if err != nil {
		return m, nil, err
	}
	closers = append(closers, events)
	tl, err := restore.Timeline(ctx, st)
	if err != nil {
		return m, nil, err
	}
	parent, err := restore.Parent(s.cfg.Storage.DataDir, tl)
	if err != nil {
		return m, nil, err
	}
	m, err = s.tg.Write(ctx, events, st, runs,
		backup.WriteOptions{Seal: s.sealer.Seal(), Keys: s.sealer.Keys(), Parent: parent, Pin: backup.PreUpgrade})
	if err != nil {
		return m, nil, err
	}
	entries, err := s.tg.List(ctx)
	if err != nil {
		return m, nil, err
	}
	return m, pastRetention(entries, s.cfg.Backup.Retention.PreUpgrade), nil
}

// pastRetention is the complete pre-upgrade generations older than the newest keep, oldest first.
func pastRetention(entries []backup.Entry, keep int) []backup.Entry {
	var pins []backup.Entry
	for _, e := range entries {
		if e.Class == backup.PreUpgrade && e.Complete {
			pins = append(pins, e)
		}
	}
	if len(pins) <= keep {
		return nil
	}
	return pins[:len(pins)-keep]
}

// probeLock refuses a store directory a running funcd holds: Badger flocks the directory itself, so a second exclusive
// lock that would block is fault.Conflict naming it. The probe's lock is released before the store opens.
func probeLock(op, key, dir string) error {
	f, err := os.Open(dir) //nolint:gosec // a configured store directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "open %s %s", key, dir)
	}
	defer func() { _ = f.Close() }()
	fd := int(f.Fd()) //nolint:gosec // a file descriptor fits an int
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return fault.Conflictf(op, "%s %s is open in a running funcd: stop it first, or pass --unit to stop its unit", key, dir)
		}
		return fault.Wrapf(err, fault.Internal, op, "lock %s %s", key, dir)
	}
	return unix.Flock(fd, unix.LOCK_UN)
}

// trackedDirs are the target directories, relative to its root, whose new entries a run writes (Decision 1, Owner).
func trackedDirs() []string { return []string{".", "gen", "gen/pre-upgrade", "probe"} }

// masterDirs are the data directory's directories, relative to it, where loading the master may create an entry
// (ADR-0204 Decision 7).
func masterDirs() []string { return []string{".", "s3gateway"} }

// dirListing is, per listed directory that exists, the names it holds.
type dirListing map[string]map[string]bool

// listDirs lists dirs under root before the run writes there; nil when root is "" (s3://, or no data directory).
func listDirs(root string, dirs []string) (dirListing, error) {
	if root == "" {
		return nil, nil
	}
	l := dirListing{}
	for _, d := range dirs {
		if fi, err := os.Lstat(filepath.Join(root, d)); err != nil || !fi.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, d))
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, "upgrade.Snapshot", "list %s", filepath.Join(root, d))
		}
		l[d] = map[string]bool{}
		for _, e := range entries {
			l[d][e.Name()] = true
		}
	}
	return l, nil
}

// own gives what root wrote its owner: the store directories, and a new lock, take <storage.dataDir>'s; under the
// target only this run's entries (new in a tracked directory that existed) take the nearest pre-existing directory's.
func (s *snapshotRun) own(before dirListing) error {
	dataDir := s.cfg.Storage.DataDir
	var roots []string
	for _, d := range s.stores() {
		roots = append(roots, d.dir)
	}
	errs := []error{hold.Own(dataDir, roots...)}
	if s.dir == "" {
		return errors.Join(errs...)
	}
	errs = append(errs, ownNew(s.dir, "", trackedDirs(), before)...)
	// After ownNew, so a new lock ends with <storage.dataDir>'s owner, not the target root's.
	if _, err := os.Lstat(filepath.Join(s.dir, "lock")); err == nil && !before["."]["lock"] {
		errs = append(errs, hold.Own(dataDir, filepath.Join(s.dir, "lock")))
	}
	return errors.Join(errs...)
}

// ownNew gives each entry of dirs under root that before does not name ref's owner, or, with ref "", its directory's.
// A directory absent from before is skipped: its entries are under a new entry of a listed one.
func ownNew(root, ref string, dirs []string, before dirListing) []error {
	var errs []error
	for _, d := range dirs {
		names, existed := before[d]
		if !existed {
			continue
		}
		parent := filepath.Join(root, d)
		entries, err := os.ReadDir(parent)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, e := range entries {
			if !names[e.Name()] {
				errs = append(errs, hold.Own(cmp.Or(ref, parent), filepath.Join(parent, e.Name())))
			}
		}
	}
	return errs
}
