// Package fakeaddon is a test stand-in for the add-on runtime and an engine that keeps its data in one file, which it
// copies into snapshots and restores from at its start, as rauthy's hiqlite does (ADR-0333). The Runtime is
// provider.Runtime, provider.Volumes and workload.AddonFiles over a storage root in t.TempDir; each object's engine
// serves its items over HTTP: PUT /items/<key> writes one, which the engine acknowledges once it is in its data file,
// GET /items/<key> reads one, and GET /ping is its probe. A Converge of a spec other than the running engine's replaces
// that engine, and a new engine answers its probe from the next Converge, as the real runtime's do (#1195).
package fakeaddon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/provider"
)

// RestoreEnv is the environment key whose value, file:<path in the engine>, names the copy a start loads before the
// engine answers its probe.
const RestoreEnv = "FAKE_RESTORE"

// dataFile is the engine's data, in its storage directory.
const dataFile = "data.json"

// snapshotAge is how long ago Snapshot dates a snapshot, past a Backup's quiet (ADR-0333 Decision 2).
const snapshotAge = time.Minute

// Runtime runs one fake engine per object. Its zero value is not usable: build it with New.
type Runtime struct {
	provider.EngineFiles
	t    *testing.T
	root string

	mu      sync.Mutex
	engines map[provider.ProviderRef]*Engine
	snaps   int
	stall   bool // an engine started with RestoreEnv runs and never answers its probe
}

// Engine is one object's engine: its spec, whether it runs and answers, hiqlite's crash marker, and the env of each
// start.
type Engine struct {
	rt  *Runtime
	ref provider.ProviderRef
	srv *httptest.Server

	spec    provider.ProviderSpec
	running bool // started from spec
	ready   bool // answers its probe
	marker  bool // a kill with no grace left hiqlite's crash marker, which a restore start cannot pass
	starts  []map[string]string
}

// New is a Runtime over a new storage root.
func New(t *testing.T) *Runtime {
	root := t.TempDir()
	return &Runtime{EngineFiles: provider.NewEngineFiles(root, v1.KindAddonObject), t: t, root: root,
		engines: map[provider.ProviderRef]*Engine{}}
}

// Dir is ref's storage directory on the host.
func (r *Runtime) Dir(ref provider.ProviderRef) string {
	return filepath.Join(r.root, "addonobject", string(ref.Namespace), string(ref.Name))
}

func (r *Runtime) engine(ref provider.ProviderRef) *Engine {
	e := r.engines[ref]
	if e == nil {
		e = &Engine{rt: r, ref: ref}
		e.srv = httptest.NewServer(http.HandlerFunc(e.serve))
		r.t.Cleanup(e.srv.Close)
		r.engines[ref] = e
	}
	return e
}

// Converge starts the engine of spec, replacing one of another spec, or retries a start that failed; it is Ready once
// a started engine answers, on the next Converge.
func (r *Runtime) Converge(_ context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if spec.Storage != nil {
		if err := os.MkdirAll(r.Dir(spec.Ref), 0o700); err != nil {
			return provider.ProviderStatus{}, fault.Wrapf(err, fault.Internal, "fakeaddon.Converge", "storage")
		}
	}
	e := r.engine(spec.Ref)
	if e.running && reflect.DeepEqual(e.spec, spec) {
		if !e.ready && !e.finish() {
			return provider.ProviderStatus{Running: e.count(), Reason: "EngineNotReady", Address: e.Address()}, nil
		}
		return provider.ProviderStatus{Running: 1, Ready: true, Address: e.Address()}, nil
	}
	e.spec, e.ready = spec, false
	e.running = e.start()
	return provider.ProviderStatus{Running: e.count(), Reason: "EngineNotReady", Address: e.Address()}, nil
}

func (e *Engine) count() int {
	if e.running {
		return 1
	}
	return 0
}

// start starts the engine from its spec, as hiqlite starts: with RestoreEnv it fails over the crash marker; without,
// it removes the marker.
func (e *Engine) start() bool {
	e.starts = append(e.starts, maps.Clone(e.spec.Env))
	if _, restoring := e.spec.Env[RestoreEnv]; !restoring {
		e.marker = false
		return true
	}
	return !e.marker
}

// finish ends a start, after which the engine answers: a start with RestoreEnv first replaces the data with the copy,
// and fails with no copy; while Stall is on it never ends.
func (e *Engine) finish() bool {
	from, restoring := strings.CutPrefix(e.spec.Env[RestoreEnv], "file:")
	switch {
	case !restoring:
	case e.rt.stall:
		return false
	default:
		copied, err := os.ReadFile(e.host(from))
		if err != nil || os.WriteFile(filepath.Join(e.rt.Dir(e.ref), dataFile), copied, 0o600) != nil {
			e.running = false
			return false
		}
	}
	e.ready = true
	return true
}

// host is p, a path in the engine under its storage path, on the host.
func (e *Engine) host(p string) string {
	if e.spec.Storage == nil {
		return ""
	}
	rel, ok := strings.CutPrefix(p, e.spec.Storage.Path+"/")
	if !ok {
		return ""
	}
	return filepath.Join(e.rt.Dir(e.ref), filepath.FromSlash(rel))
}

// Teardown stops the engine with SIGTERM and its grace, so no crash marker stays.
func (r *Runtime) Teardown(_ context.Context, ref provider.ProviderRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.engines[ref]; e != nil {
		e.running, e.ready, e.spec = false, false, provider.ProviderSpec{}
	}
	return nil
}

// Reclaim removes ref's storage; Conflict while its engine runs.
func (r *Runtime) Reclaim(_ context.Context, ref provider.ProviderRef) error {
	if r.busy(ref) {
		return fault.Conflictf("fakeaddon.Reclaim", "the engine of %s/%s runs", ref.Namespace, ref.Name)
	}
	return os.RemoveAll(r.Dir(ref))
}

func (r *Runtime) busy(ref provider.ProviderRef) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.engines[ref]
	return e != nil && e.running
}

// List names the objects that have storage, sorted.
func (r *Runtime) List(context.Context) ([]provider.ProviderRef, error) {
	var out []provider.ProviderRef
	nss, err := os.ReadDir(filepath.Join(r.root, "addonobject"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, ns := range nss {
		names, err := os.ReadDir(filepath.Join(r.root, "addonobject", ns.Name()))
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			out = append(out, provider.ProviderRef{Namespace: v1.NamespaceName(ns.Name()), Name: v1.ObjectName(n.Name())})
		}
	}
	return out, nil
}

// Quarantine keeps a deleted object's storage directory, as the real runtime's does (ADR-0288).
func (r *Runtime) Quarantine() quarantine.Store {
	return provider.NewDirQuarantine(filepath.Join(r.root, provider.QuarantineDir, "addonobject"), v1.KindAddonObject, r.Dir,
		func(_ context.Context, ref provider.ProviderRef) (bool, error) { return r.busy(ref), nil })
}

// Stall makes each engine started with RestoreEnv never answer its probe while on, as a restore that takes longer than
// any wait.
func (r *Runtime) Stall(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stall = on
}

// Engine is ref's engine, once a Converge made it.
func (r *Runtime) Engine(ref provider.ProviderRef) *Engine {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.engines[ref]
}

// Address is the engine's own address.
func (e *Engine) Address() string { return strings.TrimPrefix(e.srv.URL, "http://") }

// State is whether the engine runs and answers, and the env of its last start.
func (e *Engine) State() (running, ready bool, env map[string]string) {
	e.rt.mu.Lock()
	defer e.rt.mu.Unlock()
	if len(e.starts) > 0 {
		env = e.starts[len(e.starts)-1]
	}
	return e.running, e.ready, env
}

// Starts is the env of each start, in order.
func (e *Engine) Starts() []map[string]string {
	e.rt.mu.Lock()
	defer e.rt.mu.Unlock()
	return slices.Clone(e.starts)
}

// Crash ends the engine as its own crash does, leaving no marker; the next Converge starts one from the current spec.
func (e *Engine) Crash() {
	e.rt.mu.Lock()
	defer e.rt.mu.Unlock()
	e.running, e.ready = false, false
}

// Kill ends the engine with no grace, as funcd's boot sweep does after funcd's crash: hiqlite's marker stays.
func (e *Engine) Kill() {
	e.rt.mu.Lock()
	defer e.rt.mu.Unlock()
	e.running, e.ready, e.marker = false, false, true
}

// Snapshot writes the engine's data as a new file of its snapshot folder, dated past the quiet, and returns its name.
func (e *Engine) Snapshot(folder string) string {
	e.rt.mu.Lock()
	defer e.rt.mu.Unlock()
	e.rt.snaps++
	name := fmt.Sprintf("backup_%d.json", e.rt.snaps)
	dir := e.host(folder)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.rt.t.Errorf("snapshot folder: %v", err)
		return ""
	}
	data, _ := os.ReadFile(filepath.Join(e.rt.Dir(e.ref), dataFile))
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		e.rt.t.Errorf("snapshot: %v", err)
		return ""
	}
	at := time.Now().Add(-snapshotAge)
	_ = os.Chtimes(p, at, at)
	return name
}

// Items is the engine's data as its file holds it.
func (e *Engine) Items() map[string]string {
	e.rt.mu.Lock()
	defer e.rt.mu.Unlock()
	return e.items()
}

func (e *Engine) items() map[string]string {
	out := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(e.rt.Dir(e.ref), dataFile)); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func (e *Engine) serve(w http.ResponseWriter, req *http.Request) {
	e.rt.mu.Lock()
	defer e.rt.mu.Unlock()
	if !e.running {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	key, item := strings.CutPrefix(req.URL.Path, "/items/")
	switch {
	case req.URL.Path == "/ping":
		w.WriteHeader(http.StatusOK)
	case item && req.Method == http.MethodPut:
		body, err := io.ReadAll(req.Body)
		items := e.items()
		items[key] = string(body)
		data, _ := json.Marshal(items)
		if err != nil || os.WriteFile(filepath.Join(e.rt.Dir(e.ref), dataFile), data, 0o600) != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case item && req.Method == http.MethodGet:
		v, ok := e.items()[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, v)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}
