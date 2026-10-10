package restore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/escrow"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/eventing/eventstore"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/secrets/aesgcm"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
	badgerstore "github.com/pyvvo/funcd/internal/store/badger"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	runbadger "github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

// Report is restore.json (Decision 2 step 5): the generation loaded, the timeline the restored metastore minted,
// the funcd that restored it and when, the objects per kind, and the runs Decision 7 holds as <ns>/<name>.
type Report struct {
	From              backup.GenRef   `json:"from"`
	Timeline          string          `json:"timeline"`
	Funcd             string          `json:"funcd"`
	At                v1.Timestamp    `json:"at"`
	Counts            map[v1.Kind]int `json:"counts"`
	PausedByRestore   []string        `json:"pausedByRestore,omitempty"`
	AlreadyPaused     []string        `json:"alreadyPaused,omitempty"`
	RecordsWithoutRun []string        `json:"recordsWithoutRun,omitempty"`
}

// Options configure Run and Inspect. Source is the target opened with the operator's restore credential
// (gocloud.OpenWith); Identities are envelope.ReadIdentities'; SecretsKey is --secrets-key, which only Inspect
// reads (Run uses secrets.encryptionKeyFile); Out takes the warnings and the credentials a new master changes.
type Options struct {
	Config          config.Config
	Source          blob.Bucket
	Identities      []age.Identity
	EscrowDir       string
	NewMasterSecret bool
	SecretsKey      []byte
	Out             io.Writer
}

func (o Options) printf(format string, args ...any) {
	if o.Out != nil {
		_, _ = fmt.Fprintf(o.Out, format, args...)
	}
}

// storeDir is one directory a restore loads into, and whether it existed (empty) before.
type storeDir struct {
	key, path string
	existed   bool
}

// restoring is one Run's state: what it opened and created, which an error undoes.
type restoring struct {
	o         Options
	dataDir   string
	dirs      []storeDir
	hadMarker bool
	eng       store.Engine
	st        store.Store
	runs      runstate.Store
	events    *eventstore.Store
	master    *savedFile
}

// savedFile is a file a restore replaced, with what it held before.
type savedFile struct {
	path    string
	old     []byte
	existed bool
}

// Run restores the generation p names into o.Config's empty directories and leaves the platform held (Decision 2):
// every check, then hold.Begin and the marker, before any part is read; each store's sha256 before its Load; the
// canary on the new timeline; the runs paused, the master written; restore.json, the owner, then hold.End. An error
// removes what it created; a kill leaves restore.inprogress, which refuses the next start.
func Run(ctx context.Context, p Point, o Options) (_ Report, err error) {
	const op = "restore.Run"
	cfg := o.Config
	if cfg.Storage.Mode == "memory" {
		return Report{}, fault.Invalidf(op, "storage.mode is memory: a restore loads storage.mode file directories")
	}
	dirs := []storeDir{
		{key: "storage.metastoreDir", path: cfg.Storage.MetastoreDir},
		{key: "workflow.dataDir", path: cfg.Workflow.DataDir},
		{key: "eventing.deadletter.dataDir", path: cfg.Eventing.Deadletter.DataDir},
	}
	for i := range dirs {
		if dirs[i].existed, err = emptyDir(op, dirs[i]); err != nil {
			return Report{}, err
		}
	}
	gens, err := List(ctx, o.Source)
	if err != nil {
		return Report{}, err
	}
	g, err := Resolve(gens, p)
	if err != nil {
		return Report{}, err
	}
	m := g.Manifest
	if err := readable(op, g); err != nil {
		return Report{}, err
	}
	unseal, err := envelope.Opener(o.Identities)(m.Recipients)
	if err != nil {
		return Report{}, err
	}
	key, err := readKey(cfg.Secrets.EncryptionKeyFile)
	if err != nil {
		return Report{}, err
	}
	if err := escrow.CheckSecretsKey(m, key, o.EscrowDir); err != nil {
		return Report{}, err
	}
	plan, err := escrow.PlanMaster(m, cfg.S3Gateway.MasterSecretFile, cfg.Storage.DataDir, o.EscrowDir, o.NewMasterSecret)
	if err != nil {
		return Report{}, err
	}
	var opts []store.Option
	if key != nil {
		enc, err := aesgcm.NewAESEncryptor(key)
		if err != nil {
			return Report{}, fault.Wrapf(err, fault.Invalid, op, "secrets.encryptionKeyFile")
		}
		opts = append(opts, store.WithEncryptor([]v1.Kind{v1.KindSecret}, enc))
	}
	if !ordered(m.Funcd, version.Version) {
		o.printf("warning: generation %s/%d was written by funcd %q and this is funcd %q; one is no release version, "+
			"so their order is unknown: restoring\n", m.Timeline, m.Generation, m.Funcd, version.Version)
	}

	r := &restoring{o: o, dataDir: cfg.Storage.DataDir, dirs: dirs}
	if err := os.MkdirAll(r.dataDir, 0o700); err != nil {
		return Report{}, fault.Wrapf(err, fault.Internal, op, "create storage.dataDir %s", r.dataDir)
	}
	_, statErr := os.Stat(filepath.Join(r.dataDir, hold.MarkerFile))
	r.hadMarker = statErr == nil
	if err := hold.Begin(r.dataDir, "run"); err != nil {
		return Report{}, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, r.undo())
		}
	}()
	if err = hold.Write(r.dataDir, hold.Marker{Reason: "restore", Since: v1.NewTimestamp(time.Now())}); err != nil {
		return Report{}, err
	}
	if err = r.open(); err != nil {
		return Report{}, err
	}
	loaders := map[string]snapshot.Loader{"events": r.events, "metastore": r.eng, "runs": r.runs}
	if err = load(ctx, o.Source, g, unseal, r.dataDir, loaders); err != nil {
		return Report{}, err
	}
	r.st = store.New(r.eng, opts...)
	rep := Report{From: g.Ref(), Funcd: version.Version, At: v1.NewTimestamp(time.Now())}
	if rep.Timeline, err = timeline(ctx, r.st); err != nil {
		return Report{}, err
	}
	if err = r.canary(ctx, key != nil); err != nil {
		return Report{}, err
	}
	if err = r.holdRuns(ctx, &rep); err != nil {
		return Report{}, err
	}
	if err = r.installMaster(ctx, plan, m); err != nil {
		return Report{}, err
	}
	if rep.Counts, err = Counts(ctx, r.st); err != nil {
		return Report{}, err
	}
	data, err := json.Marshal(rep)
	if err != nil {
		return Report{}, fault.Wrapf(err, fault.Internal, op, "encode %s", hold.ReportFile)
	}
	if err = hold.WriteReport(r.dataDir, data); err != nil {
		return Report{}, err
	}
	if err = r.close(); err != nil {
		return Report{}, err
	}
	roots := []string{filepath.Join(r.dataDir, hold.MarkerFile), filepath.Join(r.dataDir, hold.ReportFile)}
	for _, d := range r.dirs {
		roots = append(roots, d.path)
	}
	if r.master != nil {
		roots = append(roots, r.master.path)
	}
	if err = hold.Own(r.dataDir, roots...); err != nil {
		return Report{}, err
	}
	if err = hold.End(r.dataDir); err != nil {
		return Report{}, err
	}
	return rep, nil
}

// readable refuses a generation this funcd cannot read: an incomplete one, a format above backup.Format, a newer
// minor (Decision 4).
func readable(op string, g Generation) error {
	m := g.Manifest
	switch {
	case g.State == StateIncomplete:
		return fault.Invalidf(op, "generation %s/%d is incomplete: it has no manifest", m.Timeline, m.Generation)
	case m.Format > backup.Format:
		return fault.Invalidf(op, "generation %s/%d has format %d; this funcd reads format %d", m.Timeline,
			m.Generation, m.Format, backup.Format)
	}
	return CheckVersion(m.Funcd, version.Version)
}

// emptyDir reports whether d exists; one that holds an entry is fault.Conflict naming it.
func emptyDir(op string, d storeDir) (bool, error) {
	entries, err := os.ReadDir(d.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fault.Wrapf(err, fault.Internal, op, "read %s %s", d.key, d.path)
	case len(entries) > 0:
		return true, fault.Conflictf(op, "%s %s is not empty: a restore loads into empty directories", d.key, d.path)
	}
	return true, nil
}

func readKey(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	key, err := os.ReadFile(path) //nolint:gosec // the operator's secrets.encryptionKeyFile
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, "restore.Run", "read secrets.encryptionKeyFile %s", path)
	}
	return key, nil
}

func (r *restoring) open() error {
	cfg := r.o.Config
	var err error
	if r.eng, err = badgerstore.Open(cfg.Storage.MetastoreDir); err != nil {
		return err
	}
	if r.runs, err = runbadger.New(runbadger.Config{Dir: cfg.Workflow.DataDir}); err != nil {
		return err
	}
	r.events, err = eventstore.Open(eventstore.Config{Dir: cfg.Eventing.Deadletter.DataDir})
	return err
}

// close closes what open opened, once.
func (r *restoring) close() error {
	var errs []error
	switch {
	case r.st != nil:
		errs = append(errs, r.st.Close())
	case r.eng != nil:
		errs = append(errs, r.eng.Close())
	}
	if r.runs != nil {
		errs = append(errs, r.runs.Close())
	}
	if r.events != nil {
		errs = append(errs, r.events.Close())
	}
	r.st, r.eng, r.runs, r.events = nil, nil, nil, nil
	return errors.Join(errs...)
}

// undo closes the stores and removes what the restore created: the stores' content, restore.json, its marker and
// the master it wrote, then restore.inprogress.
func (r *restoring) undo() error {
	errs := []error{r.close()}
	for _, d := range r.dirs {
		errs = append(errs, emptyOut(d))
	}
	errs = append(errs, removeFile(filepath.Join(r.dataDir, hold.ReportFile)))
	if !r.hadMarker {
		errs = append(errs, removeFile(filepath.Join(r.dataDir, hold.MarkerFile)))
	}
	if m := r.master; m != nil {
		if m.existed {
			errs = append(errs, os.WriteFile(m.path, m.old, 0o600))
		} else {
			errs = append(errs, removeFile(m.path))
		}
	}
	errs = append(errs, hold.End(r.dataDir))
	if err := errors.Join(errs...); err != nil {
		return fault.Wrapf(err, fault.Internal, "restore.Run", "remove what the failed restore created")
	}
	return nil
}

// emptyOut empties a directory that existed and removes one the restore created.
func emptyOut(d storeDir) error {
	if !d.existed {
		return os.RemoveAll(d.path)
	}
	entries, err := os.ReadDir(d.path)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		errs = append(errs, os.RemoveAll(filepath.Join(d.path, e.Name())))
	}
	return errors.Join(errs...)
}

func removeFile(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// timeline is the timeline store.New minted over the loaded engine (ADR-0202 Decision 3).
func timeline(ctx context.Context, st store.Store) (string, error) {
	l, err := st.List(ctx, v1.KindNamespace.GVK(), store.ListOptions{})
	if err != nil {
		return "", err
	}
	v, err := store.ParseVersion(l.ResourceVersion)
	if err != nil {
		return "", err
	}
	return v.Timeline, nil
}

// canary reads every restored Secret with the configured key, naming the first that does not open; a generation
// without a Secret checks nothing, which it warns.
func (r *restoring) canary(ctx context.Context, keyed bool) error {
	const op = "restore.Run"
	refs, err := secretRefs(ctx, r.eng)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		r.o.printf("warning: the generation holds no Secret, so nothing checks secrets.encryptionKeyFile against it\n")
		return nil
	}
	for _, ref := range refs {
		_, err := r.st.Get(ctx, v1.KindSecret.GVK(), ref.Namespace, ref.Name)
		switch {
		case err == nil:
		case keyed:
			return fault.Wrapf(err, fault.Invalid, op, "Secret %s/%s does not open with secrets.encryptionKeyFile: "+
				"it was written under another key", ref.Namespace, ref.Name)
		default:
			return fault.Wrapf(err, fault.Invalid, op, "Secret %s/%s is encrypted and secrets.encryptionKeyFile is "+
				"not set", ref.Namespace, ref.Name)
		}
	}
	return nil
}

// secretRefs lists the Secrets an engine holds by key, in key order, without decoding them (Decision 5).
func secretRefs(ctx context.Context, eng store.Engine) ([]v1.ObjectRef, error) {
	var out []v1.ObjectRef
	err := eng.View(ctx, func(tx store.Txn) error {
		return tx.Scan(v1.KindSecret.GVK().String(), func(key string, _ []byte) error {
			ns, name, _ := strings.Cut(key, "/")
			out = append(out, v1.ObjectRef{Kind: v1.KindSecret, Namespace: v1.NamespaceName(ns), Name: v1.ObjectName(name)})
			return nil
		})
	})
	sortRefs(out)
	return out, err
}

// holdRuns pauses each non-terminal WorkflowRun not already paused (Decision 7) and lists the run records no
// WorkflowRun names, which stay inert.
func (r *restoring) holdRuns(ctx context.Context, rep *Report) error {
	const op = "restore.Run"
	l, err := r.st.List(ctx, v1.KindWorkflowRun.GVK(), store.ListOptions{})
	if err != nil {
		return err
	}
	for _, o := range l.Items {
		run, ok := o.(*v1.WorkflowRun)
		if !ok || terminal(run.Status.Phase) {
			continue
		}
		ref := string(run.Namespace) + "/" + string(run.Name)
		if run.Spec.Paused {
			rep.AlreadyPaused = append(rep.AlreadyPaused, ref)
			continue
		}
		run.Spec.Paused = true
		if _, err := r.st.Update(ctx, run); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "pause WorkflowRun %s", ref)
		}
		rep.PausedByRestore = append(rep.PausedByRestore, ref)
	}
	recs, err := r.runs.List(ctx, runstate.ListOptions{})
	if err != nil {
		return err
	}
	for _, rec := range recs {
		_, err := r.st.Get(ctx, v1.KindWorkflowRun.GVK(), rec.Namespace, rec.Name)
		switch {
		case fault.KindOf(err) == fault.NotFound:
			rep.RecordsWithoutRun = append(rep.RecordsWithoutRun, string(rec.Namespace)+"/"+string(rec.Name))
		case err != nil:
			return err
		}
	}
	return nil
}

func terminal(p v1.RunPhase) bool {
	return p == v1.RunSucceeded || p == v1.RunFailed || p == v1.RunCancelled
}

// installMaster writes the escrowed master PlanMaster found, and prints the credentials a new master changes
// (ADR-0204 Decision 5): "may change" when the generation recorded no master.
func (r *restoring) installMaster(ctx context.Context, plan escrow.MasterPlan, m backup.Manifest) error {
	const op = "restore.Run"
	if plan.Install != nil {
		old, err := os.ReadFile(plan.Path) //nolint:gosec // the node master secret's place
		r.master = &savedFile{path: plan.Path, old: old, existed: err == nil}
		if err := os.MkdirAll(filepath.Dir(plan.Path), 0o700); err != nil {
			return fault.Wrapf(err, fault.Internal, op, "create the directory of %s", plan.Path)
		}
		if err := os.WriteFile(plan.Path, plan.Install, 0o600); err != nil {
			return fault.Wrapf(err, fault.Internal, op, "write the master secret to %s", plan.Path)
		}
	}
	changed, err := escrow.ListChanged(ctx, plan, r.st)
	if err != nil || !plan.List {
		return err
	}
	verb := "change"
	if m.MasterSecret == "" {
		verb = "may change"
	}
	r.o.printf("the node master secret is not the generation's: these credentials derived from it %s (%d)\n", verb, len(changed))
	for _, d := range changed {
		r.o.printf("  %s %s/%s: %s\n", d.Kind, d.Namespace, d.Name, d.Credential)
	}
	return nil
}

// Counts counts the objects per kind of st; a kind without one is absent.
func Counts(ctx context.Context, st store.Store) (map[v1.Kind]int, error) {
	out := map[v1.Kind]int{}
	for _, k := range v1.AllKinds() {
		l, err := st.List(ctx, k.GVK(), store.ListOptions{})
		if err != nil {
			return nil, err
		}
		if len(l.Items) > 0 {
			out[k] = len(l.Items)
		}
	}
	return out, nil
}

// Parent is the generation restore.json says was loaded, while timeline is the one that restore minted: ADR-0205
// writes it as each generation's parent. No report, or another timeline, is nil.
func Parent(dataDir, timeline string) (*backup.GenRef, error) {
	h, err := hold.Open(dataDir)
	if err != nil {
		return nil, err
	}
	data, err := h.Report()
	if err != nil || data == nil {
		return nil, err
	}
	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "restore.Parent", "decode %s", hold.ReportFile)
	}
	if rep.Timeline != timeline {
		return nil, nil
	}
	return &rep.From, nil
}

// load verifies each store file of g against the manifest and loads it into the loader its name selects (Decision 2
// step 2): every part present, the bytes and sha256 the manifest names before any record is read. Parts spool to
// spoolDir ("" ⇒ the temp directory).
func load(ctx context.Context, src blob.Bucket, g Generation, unseal backup.Unseal, spoolDir string, loaders map[string]snapshot.Loader) error {
	const op = "restore.load"
	m := g.Manifest
	for _, s := range m.Stores {
		l, ok := loaders[s.Name]
		if !ok {
			return fault.Invalidf(op, "generation %s/%d holds store %q, which this funcd does not know", m.Timeline,
				m.Generation, s.Name)
		}
		f, err := fetch(ctx, src, g, s, spoolDir)
		if err != nil {
			return err
		}
		err = loadFile(ctx, f, unseal, l)
		err = errors.Join(err, f.Close(), os.Remove(f.Name()))
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "generation %s/%d: load store %s", m.Timeline, m.Generation, s.Name)
		}
	}
	return nil
}

func loadFile(ctx context.Context, f *os.File, unseal backup.Unseal, l snapshot.Loader) error {
	r, err := unseal(f)
	if err != nil {
		return err
	}
	return l.Load(ctx, backup.Records(r))
}

// fetch spools a store file's parts, checking their count, size and sha256 against the manifest.
func fetch(ctx context.Context, src blob.Bucket, g Generation, s backup.StoreFile, dir string) (_ *os.File, err error) {
	const op = "restore.load"
	m := g.Manifest
	f, err := os.CreateTemp(dir, "restore-"+s.Name+"-*")
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "spool store %s", s.Name)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	h := sha256.New()
	var n int64
	for i := range s.Parts {
		key := fmt.Sprintf("gen/%s/%010d-%s/%s/part-%05d", g.Class, m.Generation, m.Timeline, s.Name, i)
		data, err := src.Get(ctx, key)
		if fault.KindOf(err) == fault.NotFound {
			return nil, fault.Invalidf(op, "generation %s/%d: store %s lacks part %d (%s)", m.Timeline, m.Generation, s.Name, i, key)
		}
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "generation %s/%d: read %s", m.Timeline, m.Generation, key)
		}
		if _, err := f.Write(data); err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "spool store %s", s.Name)
		}
		h.Write(data)
		n += int64(len(data))
	}
	if sum := hex.EncodeToString(h.Sum(nil)); n != s.Bytes || sum != s.SHA256 {
		return nil, fault.Invalidf(op, "generation %s/%d: store %s has %d bytes with sha256 %s; the manifest names %d "+
			"bytes with sha256 %s", m.Timeline, m.Generation, s.Name, n, sum, s.Bytes, s.SHA256)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "spool store %s", s.Name)
	}
	return f, nil
}

// sortRefs orders refs by namespace, then name.
func sortRefs(refs []v1.ObjectRef) {
	slices.SortFunc(refs, func(a, b v1.ObjectRef) int {
		return strings.Compare(string(a.Namespace)+"/"+string(a.Name), string(b.Namespace)+"/"+string(b.Name))
	})
}
