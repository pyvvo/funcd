// Package hold is the platform-wide hold (ADR-0206 Decision 6): <storage.dataDir>/.hold exists iff the platform is
// held, every side-effect runner asks the Gate before it acts, and only the release deletes the marker. A restore in
// progress leaves restore.inprogress, which refuses a start until the operator empties what it wrote (Decision 2).
package hold

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// The files the hold keeps in the data directory; BusyFile marks a restore in progress (Decision 2).
const MarkerFile, BusyFile, ReleasedFile = ".hold", "restore.inprogress", ".hold-released"

// ReportFile is restore.json, the report restore run writes beside the marker (Decision 2 step 5).
const ReportFile = "restore.json"

// Marker is the content of MarkerFile; Reason is "restore" or "safe-mode" (ADR-0207).
type Marker struct {
	Reason string       `json:"reason"`
	Since  v1.Timestamp `json:"since"`
}

// Gate is what a runner asks: Held, and the time of the last release (zero when none).
type Gate interface {
	Held() bool
	ReleasedAt() time.Time
}

// Never is the gate of a platform without a hold: never held, never released.
//
//nolint:gochecknoglobals // ADR-0206 contract: funcd.WithHold's default
var Never Gate = never{}

type never struct{}

func (never) Held() bool            { return false }
func (never) ReleasedAt() time.Time { return time.Time{} }

// Hold is the opened hold of one data directory, a Gate whose release is live.
type Hold struct {
	dir      string
	mu       sync.RWMutex
	m        *Marker
	released time.Time
}

// busy is the content of BusyFile: the restore command that runs, and since when.
type busy struct {
	Command string       `json:"command"`
	Since   v1.Timestamp `json:"since"`
}

// Evidence is what the hold shows the operator (Decisions 1, 8): GET …/hold returns it, `funcdctl hold status` prints
// it. Report is restore.json as written; runs and dead letters are <ns>/<name> and <ns>/<id>; Pending is, per
// <ns>/<source>, the blob keys per event a release replays; Orphans and BucketOrphans are data no object names,
// which the reclaims would drop; FunctionsNotReady are <ns>/<name>.
type Evidence struct {
	Held              bool                      `json:"held"`
	Marker            *Marker                   `json:"marker,omitempty"`
	ReleasedAt        *v1.Timestamp             `json:"releasedAt,omitempty"`
	Report            json.RawMessage           `json:"report,omitempty"`
	Counts            map[v1.Kind]int           `json:"counts,omitempty"`
	PausedRuns        []string                  `json:"pausedRuns,omitempty"`
	DeadLetters       []string                  `json:"deadLetters,omitempty"`
	Pending           map[string]map[string]int `json:"pending,omitempty"`
	Orphans           []string                  `json:"orphans,omitempty"`
	BucketOrphans     []string                  `json:"bucketOrphans,omitempty"`
	FunctionsNotReady []string                  `json:"functionsNotReady,omitempty"`
}

// Write writes the marker: the platform is held from the next start until a release.
func Write(dataDir string, m Marker) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "hold.Write", "encode the marker")
	}
	return writeFile("hold.Write", dataDir, MarkerFile, data)
}

// Begin writes BusyFile naming command (run, kv, blob); a BusyFile naming another command is fault.Conflict. One
// naming the same command is a rerun after a kill, whose directories the command itself found empty.
func Begin(dataDir, command string) error {
	const op = "hold.Begin"
	b, err := readBusy(op, dataDir)
	if err != nil {
		return err
	}
	if b != nil && b.Command != command {
		return fault.Conflictf(op, "%s: restore %s, begun %s, did not finish: empty what it restored and rerun it",
			filepath.Join(dataDir, BusyFile), b.Command, b.Since)
	}
	data, err := json.Marshal(busy{Command: command, Since: v1.NewTimestamp(time.Now())})
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "encode %s", BusyFile)
	}
	return writeFile(op, dataDir, BusyFile, data)
}

// End removes BusyFile: the restore finished.
func End(dataDir string) error {
	const op = "hold.End"
	if err := os.Remove(filepath.Join(dataDir, BusyFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fault.Wrapf(err, fault.Internal, op, "remove %s", BusyFile)
	}
	return syncDir(op, dataDir)
}

// Open reads the hold of dataDir: BusyFile ⇒ fault.Conflict naming the interrupted restore; no marker ⇒ not held;
// a file that does not read or decode ⇒ fault.Internal.
func Open(dataDir string) (*Hold, error) {
	const op = "hold.Open"
	b, err := readBusy(op, dataDir)
	if err != nil {
		return nil, err
	}
	if b != nil {
		return nil, fault.Conflictf(op, "restore %s, begun %s, was interrupted (%s): a partial load must not start; "+
			"empty the directories it restores into, then run it again", b.Command, b.Since, filepath.Join(dataDir, BusyFile))
	}
	h := &Hold{dir: dataDir}
	data, err := readFile(op, dataDir, MarkerFile)
	if err != nil {
		return nil, err
	}
	if data != nil {
		h.m = &Marker{}
		if err := json.Unmarshal(data, h.m); err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "decode %s", MarkerFile)
		}
	}
	if data, err = readFile(op, dataDir, ReleasedFile); err != nil {
		return nil, err
	}
	if data != nil {
		var at v1.Timestamp
		if err := json.Unmarshal(data, &at); err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "decode %s", ReleasedFile)
		}
		h.released = time.Time(at)
	}
	return h, nil
}

// Held reports whether the marker exists.
func (h *Hold) Held() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.m != nil
}

// ReleasedAt is the time the last release wrote to ReleasedFile, zero when none did.
func (h *Hold) ReleasedAt() time.Time {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.released
}

// Marker returns the marker while held.
func (h *Hold) Marker() (Marker, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.m == nil {
		return Marker{}, false
	}
	return *h.m, true
}

// Report reads ReportFile, nil when absent.
func (h *Hold) Report() (json.RawMessage, error) { return readFile("hold.Report", h.dir, ReportFile) }

// Release lifts the hold (Decision 8): it writes now to ReleasedFile, deletes the marker, syncs the directory, then
// lifts the gate. Not held ⇒ fault.Conflict.
func (h *Hold) Release(now time.Time) error {
	const op = "hold.Release"
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m == nil {
		return fault.Conflictf(op, "the platform is not held")
	}
	at := v1.NewTimestamp(now)
	data, err := json.Marshal(at)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "encode the release time")
	}
	if err := writeFile(op, h.dir, ReleasedFile, data); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(h.dir, MarkerFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fault.Wrapf(err, fault.Internal, op, "remove %s", MarkerFile)
	}
	if err := syncDir(op, h.dir); err != nil {
		return err
	}
	h.m, h.released = nil, time.Time(at)
	return nil
}

// Own gives each root that exists, and everything below it, ref's owner and group where they differ (os.Lchown),
// so a restore run as root leaves what funcd, running as the data directory's owner, can open (ADR-0026 §4).
func Own(ref string, roots ...string) error {
	const op = "hold.Own"
	info, err := os.Stat(ref)
	if err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "stat %s", ref)
	}
	want, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fault.Internalf(op, "%s has no owner on this platform", ref)
	}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
			if err != nil {
				if p == root && errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			fi, err := os.Lstat(p)
			if err != nil {
				return err
			}
			if got, ok := fi.Sys().(*syscall.Stat_t); ok && (got.Uid != want.Uid || got.Gid != want.Gid) {
				return os.Lchown(p, int(want.Uid), int(want.Gid))
			}
			return nil
		})
		if err != nil {
			return fault.Wrapf(err, fault.Internal, op, "give %s the owner of %s", root, ref)
		}
	}
	return nil
}

func readBusy(op, dataDir string) (*busy, error) {
	data, err := readFile(op, dataDir, BusyFile)
	if err != nil || data == nil {
		return nil, err
	}
	var b busy
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "decode %s", BusyFile)
	}
	return &b, nil
}

// readFile reads dir/name, nil when absent.
func readFile(op, dir, name string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // the data directory's own file
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "read %s", name)
	}
	return data, nil
}

// writeFile replaces dir/name with data, mode 0600: a temporary file synced and renamed, then the directory synced,
// so a crash leaves the old file or the new one.
func writeFile(op, dir, name string, data []byte) error {
	f, err := os.CreateTemp(dir, name+".tmp-*")
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "write %s", name)
	}
	tmp := f.Name()
	_, err = f.Write(data)
	err = errors.Join(err, f.Sync(), f.Close())
	if err == nil {
		err = os.Rename(tmp, filepath.Join(dir, name))
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fault.Wrapf(err, fault.Internal, op, "write %s", name)
	}
	return syncDir(op, dir)
}

func syncDir(op, dir string) error {
	d, err := os.Open(dir) //nolint:gosec // the data directory
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "open %s", dir)
	}
	err = errors.Join(d.Sync(), d.Close())
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "sync %s", dir)
	}
	return nil
}

// WriteReport writes ReportFile, mode 0600, synced as Write is.
func WriteReport(dataDir string, report json.RawMessage) error {
	return writeFile("hold.WriteReport", dataDir, ReportFile, report)
}
