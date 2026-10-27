package hold

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// The per-namespace hold (ADR-0222 Decision 8): <dataDir>/.hold-ns/<ns>.<reason> holds a Marker, and a release
// writes its time to <dataDir>/.hold-ns-released/<ns> before it deletes the marker.
const NamespaceDir, NamespaceReleasedDir = ".hold-ns", ".hold-ns-released"

// The reasons a namespace is held for: an in-place Restore loading it, or a platform restore that returned its
// BackupSchedules without their data. HeldIn reads only these.
const ReasonRestore, ReasonData = "restore", "data"

// NamespaceGate is what a per-namespace runner asks: HeldIn is true while the platform or ns is held; ReleasedAtIn is
// the later of the platform's release and ns's.
type NamespaceGate interface {
	HeldIn(ns v1.NamespaceName) bool
	ReleasedAtIn(ns v1.NamespaceName) time.Time
}

// NeverNamespace is the namespace gate of a platform without a hold.
//
//nolint:gochecknoglobals // ADR-0222 contract: the default of every per-namespace runner
var NeverNamespace NamespaceGate = never{}

func (never) HeldIn(v1.NamespaceName) bool            { return false }
func (never) ReleasedAtIn(v1.NamespaceName) time.Time { return time.Time{} }

// CallGate is what the data plane asks before it serves a call into a namespace (ADR-0290).
type CallGate interface {
	// NamespaceMarker is the marker holding ns: its restore marker if any, else its data marker. False when ns holds
	// neither; the platform's own hold never counts.
	NamespaceMarker(ns v1.NamespaceName) (Marker, bool)
}

// NeverCall is the call gate of a platform without a hold.
//
//nolint:gochecknoglobals // ADR-0290 contract: the data plane's default
var NeverCall CallGate = never{}

func (never) NamespaceMarker(v1.NamespaceName) (Marker, bool) { return Marker{}, false }

// HeldIn reports whether the platform is held or ns holds a restore or data marker.
func (h *Hold) HeldIn(ns v1.NamespaceName) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.m != nil || len(h.nsHeld[ns]) > 0
}

// NamespaceMarker is ns's restore marker if it holds one, else its data marker; the platform's hold never counts.
func (h *Hold) NamespaceMarker(ns v1.NamespaceName) (Marker, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if m, ok := h.nsHeld[ns][ReasonRestore]; ok {
		return m, true
	}
	m, ok := h.nsHeld[ns][ReasonData]
	return m, ok
}

// ReleasedAtIn is the later of ReleasedAt and ns's last release, zero when neither happened.
func (h *Hold) ReleasedAtIn(ns v1.NamespaceName) time.Time {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if at := h.nsReleased[ns]; at.After(h.released) {
		return at
	}
	return h.released
}

// HoldNamespace writes ns's marker for m.Reason and holds ns live. It never replaces a marker: ns already held for
// m.Reason ⇒ fault.Conflict naming the Restore of the marker that stands.
func (h *Hold) HoldNamespace(ns v1.NamespaceName, m Marker) error {
	const op = "hold.HoldNamespace"
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur, ok := h.nsHeld[ns][m.Reason]; ok {
		return fault.Conflictf(op, "namespace %q is already held for %q (Restore %q)", ns, m.Reason, cur.Restore)
	}
	if err := writeNamespaceMarker(op, h.dir, ns, m); err != nil {
		return err
	}
	if h.nsHeld[ns] == nil {
		h.nsHeld[ns] = map[string]Marker{}
	}
	h.nsHeld[ns][m.Reason] = m
	return nil
}

// NamespaceMarkers is a copy of each held namespace's markers by reason.
func (h *Hold) NamespaceMarkers() map[v1.NamespaceName]map[string]Marker {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[v1.NamespaceName]map[string]Marker, len(h.nsHeld))
	for ns, ms := range h.nsHeld {
		out[ns] = maps.Clone(ms)
	}
	return out
}

// ReleaseNamespace lifts ns's reason, as Release lifts the platform: it writes now to .hold-ns-released/<ns>,
// deletes the marker, syncs the directory, then lifts the gate. No such marker ⇒ fault.Conflict.
func (h *Hold) ReleaseNamespace(ns v1.NamespaceName, reason string) error {
	return h.releaseNamespace("hold.ReleaseNamespace", ns, reason, nil)
}

// ReleaseNamespaceMarker lifts ns's m.Reason as ReleaseNamespace does, only while that marker is still m: one another
// Restore wrote since ⇒ fault.Conflict.
func (h *Hold) ReleaseNamespaceMarker(ns v1.NamespaceName, m Marker) error {
	return h.releaseNamespace("hold.ReleaseNamespaceMarker", ns, m.Reason, &m)
}

func (h *Hold) releaseNamespace(op string, ns v1.NamespaceName, reason string, want *Marker) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur, ok := h.nsHeld[ns][reason]
	if !ok {
		return fault.Conflictf(op, "namespace %q is not held for %q", ns, reason)
	}
	if want != nil && (cur.Restore != want.Restore || !time.Time(cur.Since).Equal(time.Time(want.Since))) {
		return fault.Conflictf(op, "the %s marker of %q changed (Restore %q): read it again", reason, ns, cur.Restore)
	}
	at := v1.NewTimestamp(time.Now())
	data, err := json.Marshal(at)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "encode the release time")
	}
	released := filepath.Join(h.dir, NamespaceReleasedDir)
	if err := os.MkdirAll(released, 0o700); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "create %s", NamespaceReleasedDir)
	}
	if err := WriteFile(op, released, string(ns), data); err != nil {
		return err
	}
	markers := filepath.Join(h.dir, NamespaceDir)
	if err := os.Remove(filepath.Join(markers, markerName(ns, reason))); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fault.Wrapf(err, fault.Internal, op, "remove the %s marker of %q", reason, ns)
	}
	if err := syncDir(op, markers); err != nil {
		return err
	}
	delete(h.nsHeld[ns], reason)
	if len(h.nsHeld[ns]) == 0 {
		delete(h.nsHeld, ns)
	}
	h.nsReleased[ns] = time.Time(at)
	return nil
}

// MarkNamespaces writes m's marker for each of nss, offline: restore run holds the namespaces whose BackupSchedules
// return without their data.
func MarkNamespaces(dataDir string, nss []v1.NamespaceName, m Marker) error {
	for _, ns := range nss {
		if err := writeNamespaceMarker("hold.MarkNamespaces", dataDir, ns, m); err != nil {
			return err
		}
	}
	return nil
}

func writeNamespaceMarker(op, dataDir string, ns v1.NamespaceName, m Marker) error {
	if err := ns.Validate(); err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "namespace")
	}
	if m.Reason != ReasonRestore && m.Reason != ReasonData {
		return fault.Invalidf(op, "reason %q: want %q or %q", m.Reason, ReasonRestore, ReasonData)
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "encode the marker")
	}
	dir := filepath.Join(dataDir, NamespaceDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "create %s", NamespaceDir)
	}
	return WriteFile(op, dir, markerName(ns, m.Reason), data)
}

func markerName(ns v1.NamespaceName, reason string) string { return string(ns) + "." + reason }

// openNamespaces reads the namespace markers of the known reasons and the release times into h.
func (h *Hold) openNamespaces(op string) error {
	h.nsHeld, h.nsReleased = map[v1.NamespaceName]map[string]Marker{}, map[v1.NamespaceName]time.Time{}
	entries, err := os.ReadDir(filepath.Join(h.dir, NamespaceDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fault.Wrapf(err, fault.Internal, op, "read %s", NamespaceDir)
	}
	for _, e := range entries {
		ns, reason, ok := strings.Cut(e.Name(), ".")
		if !ok || (reason != ReasonRestore && reason != ReasonData) || v1.NamespaceName(ns).Validate() != nil {
			continue
		}
		data, err := readFile(op, filepath.Join(h.dir, NamespaceDir), e.Name())
		if err != nil {
			return err
		}
		var m Marker
		if err := json.Unmarshal(data, &m); err != nil {
			return fault.Wrapf(err, fault.Internal, op, "decode %s/%s", NamespaceDir, e.Name())
		}
		if h.nsHeld[v1.NamespaceName(ns)] == nil {
			h.nsHeld[v1.NamespaceName(ns)] = map[string]Marker{}
		}
		h.nsHeld[v1.NamespaceName(ns)][reason] = m
	}
	if entries, err = os.ReadDir(filepath.Join(h.dir, NamespaceReleasedDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fault.Wrapf(err, fault.Internal, op, "read %s", NamespaceReleasedDir)
	}
	for _, e := range entries {
		ns := v1.NamespaceName(e.Name())
		if ns.Validate() != nil {
			continue
		}
		data, err := readFile(op, filepath.Join(h.dir, NamespaceReleasedDir), e.Name())
		if err != nil {
			return err
		}
		var at v1.Timestamp
		if err := json.Unmarshal(data, &at); err != nil {
			return fault.Wrapf(err, fault.Internal, op, "decode %s/%s", NamespaceReleasedDir, e.Name())
		}
		h.nsReleased[ns] = time.Time(at)
	}
	return nil
}
