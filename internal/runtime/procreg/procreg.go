// Package procreg is the saved worker registry (ADR-0167): the process driver and the dev catalog engine record every
// process they start, so the next open reaps what a crashed run left behind. An entry is reaped only when the live
// pid's start time and boot match it (ADR-0187), so a reused pid is never signalled.
package procreg

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/pyvvo/funcd/api/fault"
)

// pollInterval is how often Reap checks whether the signalled process groups are gone.
const pollInterval = 20 * time.Millisecond

// Entry is one process a registry owner started.
type Entry struct {
	ID        string   `json:"id"`
	PID       int      `json:"pid"`
	PGID      int      `json:"pgid"`
	StartTime uint64   `json:"startTime"`        // OS-native: clock ticks since boot (Linux), µs since epoch (macOS)
	BootID    string   `json:"bootID,omitempty"` // Linux boot_id at save; "" on macOS and in entries of earlier releases
	Token     string   `json:"token"`            // still in argv; checked only for a Linux entry without BootID
	Files     []string `json:"files"`            // driver-owned temp files deleted at reap
}

// Registry is the saved set of entries in <dir>/<name>.json, owned by one process through the lock on
// <dir>/<name>.lock. It has no mutex of its own: the caller serializes Put, Delete and Close.
type Registry struct {
	path    string
	lock    *os.File
	entries map[string]Entry
}

// Open takes the registry at <dir>/<name>.json, creating dir, and loads its entries. It fails with fault.Conflict
// while another owner holds the lock.
func Open(dir, name string) (*Registry, error) {
	const op = "procreg.Open"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "create state dir")
	}
	lock, err := os.OpenFile(filepath.Join(dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // dir is the owner's state dir
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "open lock file")
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil { //nolint:gosec // a file descriptor fits an int
		_ = lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fault.Conflictf(op, "registry %s is held by another process", filepath.Join(dir, name+".json"))
		}
		return nil, fault.Wrapf(err, fault.Internal, op, "lock registry")
	}
	r := &Registry{path: filepath.Join(dir, name+".json"), lock: lock, entries: map[string]Entry{}}
	b, err := os.ReadFile(r.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return r, nil
	case err != nil:
		_ = lock.Close()
		return nil, fault.Wrapf(err, fault.Internal, op, "read registry")
	}
	var saved []Entry
	if err := json.Unmarshal(b, &saved); err != nil {
		_ = lock.Close()
		return nil, fault.Wrapf(err, fault.Internal, op, "decode registry %s", r.path)
	}
	for _, e := range saved {
		r.entries[e.ID] = e
	}
	return r, nil
}

// Put records e, replacing the entry with its ID.
func (r *Registry) Put(e Entry) error {
	r.entries[e.ID] = e
	return r.write()
}

// Delete forgets the entry with id.
func (r *Registry) Delete(id string) error {
	if _, ok := r.entries[id]; !ok {
		return nil
	}
	delete(r.entries, id)
	return r.write()
}

// Reap ends every saved process that is still ours: SIGTERM to each owned process group, up to grace for all of them
// together, then SIGKILL to the ones still owned. A group whose leader exits, or is a zombie, gets SIGKILL at once, as
// a worker's exit does (ADR-0011 C4): its other members outlive the leader, and its pgid is not reused while one
// lives. It deletes every entry's files, owned or not, and empties the registry. killed counts the process groups it
// signalled.
func (r *Registry) Reap(ctx context.Context, grace time.Duration) (killed int, err error) {
	var owned []Entry
	for _, e := range r.entries {
		if e.PGID > 1 && Owned(e) {
			owned = append(owned, e)
			_ = unix.Kill(-e.PGID, unix.SIGTERM)
		}
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for alive := owned; len(alive) > 0; {
		select {
		case <-ctx.Done():
		case <-deadline.C:
		case <-tick.C:
			alive = slices.DeleteFunc(alive, func(e Entry) bool {
				if Alive(e) {
					return false
				}
				_ = unix.Kill(-e.PGID, unix.SIGKILL)
				return true
			})
			continue
		}
		for _, e := range alive {
			if Owned(e) {
				_ = unix.Kill(-e.PGID, unix.SIGKILL)
			}
		}
		break
	}
	for _, e := range r.entries {
		for _, f := range e.Files {
			_ = os.RemoveAll(f)
		}
	}
	clear(r.entries)
	return len(owned), r.write()
}

// Close empties the registry and releases the lock; a second Close does nothing.
func (r *Registry) Close() error {
	if r.lock == nil {
		return nil
	}
	clear(r.entries)
	err := r.write()
	_ = r.lock.Close()
	r.lock = nil
	return err
}

// write replaces the registry file atomically: a temp file, fsync, rename, fsync of the dir.
func (r *Registry) write() error {
	const op = "procreg.write"
	saved := make([]Entry, 0, len(r.entries))
	for _, e := range r.entries {
		saved = append(saved, e)
	}
	slices.SortFunc(saved, func(a, b Entry) int { return strings.Compare(a.ID, b.ID) })
	b, err := json.Marshal(saved)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "encode registry")
	}
	tmp := r.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // path is the owner's state dir
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "create %s", tmp)
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, r.path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fault.Wrapf(err, fault.Internal, op, "write %s", r.path)
	}
	dir, err := os.Open(filepath.Dir(r.path))
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "open state dir")
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "sync state dir")
	}
	return nil
}

// StartTime reads the OS start time of pid, which an owner records right after it starts a process.
func StartTime(pid int) (uint64, error) {
	return startTime(pid)
}

// BootID reads the boot ID an owner records next to the start time: Linux boot_id, read once; "" on macOS.
func BootID() (string, error) {
	return bootID()
}

// Owned reports whether e's pid still names the process its owner started, from facts the process cannot rewrite:
// the start time and the boot match (ADR-0187). Any read error means "not ours".
func Owned(e Entry) bool {
	st, err := startTime(e.PID)
	if err != nil || st != e.StartTime {
		return false
	}
	ok, err := bootMatches(e)
	return err == nil && ok
}

// Alive reports whether e's process is ours and still runs: a zombie counts as gone.
func Alive(e Entry) bool {
	return Owned(e) && !zombie(e.PID)
}
