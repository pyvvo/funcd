package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/store"
)

const (
	retryMin    = 100 * time.Millisecond
	retryMax    = 5 * time.Second
	pollWait    = 25 * time.Second
	resyncEvery = 30 * time.Second
	// selfEvery paces the node's report of itself, which lists its backup target.
	selfEvery = 5 * time.Minute
)

// Config configures a node's agent.
type Config struct {
	Dir       string      // the node's cluster directory, which holds its identity
	Store     store.Store // the node's local store
	Logger    *slog.Logger
	Connector nodeapi.Connector      // the node side of the node API's driver
	Logs      multinode.LogReader    // the node's local log reads, which answer the log calls
	RunLogs   multinode.RunLogReader // the node's local run-log reads
	Commands  multinode.Commands     // the node's local parts that answer the other node commands
	Version   string
	Platform  string
	Now       func() time.Time // nil ⇒ time.Now
}

// Agent keeps a node's local store a copy of the namespaces placed on it (FEAT-0002 F243), writes their status and the
// children the node owns back (F244), and answers the node calls (F250).
type Agent struct {
	cfg Config
	id  atomic.Pointer[identity]
	log *slog.Logger

	mu       sync.Mutex
	cursor   uint64
	timeline string      // the control plane metastore's timeline the cursor belongs to; "" ⇒ the store holds no copy yet
	local    string      // the timeline of the node's own store, which the saved cursor names too
	restored bool        // the cursor is of before the control plane's restore, and the store holds what the node ran since
	resync   atomic.Bool // a namespace's snapshot failed: the next poll starts from a whole snapshot
	mine     map[v1.NamespaceName]bool
	keys     map[v1.NamespaceName]nodeapi.NamespaceKey
	pushed   map[string]string
	// reported is what the control plane holds of the node's report of itself, as last sent
	reported string
	// unsent are the deletes the control plane did not take yet
	unsent map[string]nodeapi.ObjectRef
	// resynced asks the report for every status and child again, after a snapshot
	resynced chan struct{}
}

// New loads the node's identity from cfg.Dir.
func New(cfg Config) (*Agent, error) {
	if cfg.Store == nil || cfg.Connector == nil {
		return nil, fault.Invalidf("agent.New", "the node's store and the node side of the node API's driver are required")
	}
	base, pair, err := loadIdentity(cfg.Dir)
	if err != nil {
		return nil, err
	}
	id, err := base.with(cfg.Connector, pair, cfg.Version, cfg.Platform)
	if err != nil {
		return nil, err
	}
	keys, err := loadKeys(cfg.Dir)
	if err != nil {
		return nil, err
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	a := &Agent{cfg: cfg, log: cfg.Logger.With("component", "node-agent", "node", id.name),
		mine: map[v1.NamespaceName]bool{}, keys: keys, pushed: map[string]string{}, unsent: map[string]nodeapi.ObjectRef{},
		resynced: make(chan struct{}, 1)}
	a.id.Store(&id)
	if cl, ok := cfg.Store.(store.ChangeLog); ok {
		a.local = cl.Timeline()
	}
	if b, err := os.ReadFile(filepath.Join(cfg.Dir, cursorFile)); err == nil { //nolint:gosec // the node's own directory
		switch f := strings.Fields(string(b)); {
		case len(f) != 3 || f[2] != a.local:
		case f[0] == restoredMark:
			a.restored = true
		default:
			a.timeline = f[0]
			a.cursor, _ = strconv.ParseUint(f[1], 10, 64)
		}
	}
	return a, nil
}

// Name is the node's name, the one it joined under.
func (a *Agent) Name() v1.ObjectName { return a.id.Load().name }

// SetLogs gives the agent the node's local log reads, once the node's platform built them.
func (a *Agent) SetLogs(logs multinode.LogReader, runLogs multinode.RunLogReader) {
	a.cfg.Logs, a.cfg.RunLogs = logs, runLogs
}

// SetCommands gives the agent the node's local parts that answer the other node commands (F250), once the node's
// platform built them.
func (a *Agent) SetCommands(c multinode.Commands) { a.cfg.Commands = c }

// Run follows, reports and answers until ctx ends, or until the control plane turns out to be restored from a backup
// (ErrRestored).
func (a *Agent) Run(ctx context.Context) error {
	if err := a.Prepare(ctx); err != nil {
		return err
	}
	a.log.InfoContext(ctx, "node agent starting", "server", a.id.Load().server, "since", a.cursor)
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	var wg sync.WaitGroup
	var err error
	for _, loop := range []func(context.Context){
		func(ctx context.Context) { err = a.follow(ctx); stop() }, a.report, a.serveCalls, a.renew} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			loop(ctx)
		}()
	}
	wg.Wait()
	return err
}

// loadMine reads back which namespaces the node runs, from the Namespace objects of its local store.
func (a *Agent) loadMine(ctx context.Context) error {
	res, err := a.cfg.Store.List(ctx, v1.KindNamespace.GVK(), store.ListOptions{})
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, obj := range res.Items {
		if n, ok := obj.(*v1.Namespace); ok && n.Spec.Node == a.id.Load().name {
			a.mine[v1.NamespaceName(n.Name)] = true
		}
	}
	return nil
}

func (a *Agent) isMine(ns v1.NamespaceName) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mine[ns]
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// ErrRestored is a control plane that was restored from a backup since the node last followed it: the node's platform
// starts again, and takes the restored state before it runs anything, so no running reconciler drops the data of what the
// backup does not know (ADR-0206, ADR-0288).
var ErrRestored = fault.Conflictf("agent.follow", "the control plane was restored from a backup: the node starts again on its state")

// restoredMark is the timeline a cursor carries from ErrRestored to the node's next start.
const restoredMark = "restored"

// client is the node API client of the node's current certificate.
func (a *Agent) client() nodeapi.Client { return a.id.Load().client }

// follow keeps the local copy with the feed, from the saved revision; one the change log no longer holds starts again
// from a snapshot.
func (a *Agent) follow(ctx context.Context) error {
	backoff, failing, resync := retryMin, false, false
	fail := func(what string, err error) {
		if !failing {
			a.log.WarnContext(ctx, what, "error", err.Error())
		}
		failing = true
		sleep(ctx, backoff)
		backoff = min(backoff*2, retryMax)
	}
	for ctx.Err() == nil {
		if resync || a.resync.Swap(false) {
			if err := a.syncAll(ctx); errors.Is(err, ErrRestored) {
				a.saveCursor(restoredMark, 0)
				return err
			} else if err != nil {
				fail("node agent: snapshot", err)
				continue
			}
			resync = false
		}
		feed, err := a.client().Feed(ctx, a.cursor, a.timeline, pollWait)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if fault.KindOf(err) == fault.PreconditionFailed { // a revision the change log no longer holds: a snapshot
				resync = true
				continue
			}
			fail("node agent: feed", err)
			continue
		}
		if failing {
			a.log.InfoContext(ctx, "node agent: feed resumed", "since", a.cursor)
		}
		backoff, failing = retryMin, false
		applied := true
		for _, c := range feed.Changes {
			if err := a.apply(ctx, c); err != nil {
				applied = false
			}
		}
		a.fetchMissingKeys(ctx)
		if ctx.Err() != nil {
			return nil // a change cut short is applied again after the restart
		}
		if !applied { // a snapshot brings what the node failed to write, and saves the revision then (ADR-0365 Decision 6)
			resync = true
			continue
		}
		if feed.Rev > a.cursor {
			a.saveCursor(a.timeline, feed.Rev)
		}
	}
	return nil
}

func (a *Agent) saveCursor(timeline string, rev uint64) {
	a.cursor, a.timeline = rev, timeline
	tmp := filepath.Join(a.cfg.Dir, cursorFile+".tmp")
	if err := os.WriteFile(tmp, []byte(timeline+" "+strconv.FormatUint(rev, 10)+" "+a.local+"\n"), 0o600); err == nil {
		_ = os.Rename(tmp, filepath.Join(a.cfg.Dir, cursorFile))
	}
}

// syncAll replaces the local copy with a snapshot of every namespace placed on the node. The runs and records the node
// made go to the control plane first, so the snapshot holds them, unless the store is new or restored to an older point.
func (a *Agent) syncAll(ctx context.Context) error {
	if a.timeline != "" || a.restored {
		a.pushMade(ctx)
	}
	snap, err := a.client().Snapshot(ctx, "")
	if err != nil {
		return err
	}
	if a.timeline != "" && snap.Timeline != a.timeline {
		return ErrRestored
	}
	placed := map[v1.NamespaceName]bool{}
	seen := map[string]bool{}
	for _, c := range snap.Objects {
		if c.Kind == v1.KindNamespace {
			placed[v1.NamespaceName(c.Name)] = true
		}
	}
	a.mu.Lock()
	gone := map[v1.NamespaceName]bool{}
	for ns := range a.mine {
		if !placed[ns] {
			gone[ns] = true
		}
	}
	a.mine = placed
	a.mu.Unlock()
	for ns := range gone {
		a.removeNamespace(ctx, ns)
	}
	a.mu.Lock()
	var stale []v1.NamespaceName
	for ns := range a.keys {
		if !placed[ns] {
			stale = append(stale, ns)
		}
	}
	a.mu.Unlock()
	for _, ns := range stale {
		a.dropKey(ctx, ns)
	}
	// A namespace's master comes before its objects, whose credentials derive from it (decision 8).
	for ns := range placed {
		if err := a.fetchKey(ctx, ns); err != nil {
			a.log.WarnContext(ctx, "node agent: fetch a namespace's master", "namespace", ns, "error", err)
		}
	}
	for _, c := range snap.Objects {
		seen[key(c.Kind, c.Namespace, c.Name)] = true
		if err := a.upsert(ctx, c); err != nil {
			a.log.WarnContext(ctx, "node agent: apply from the snapshot", "kind", c.Kind, "namespace", c.Namespace, "name", c.Name, "error", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err // a snapshot cut short saves no revision, so the next start takes it again
	}
	for ns := range placed {
		a.prune(ctx, ns, seen)
	}
	a.pruneClusterKinds(ctx, seen)
	a.reportAgain()
	a.saveCursor(snap.Timeline, snap.Rev)
	a.log.InfoContext(ctx, "node agent: synced from a snapshot", "rev", snap.Rev, "objects", len(snap.Objects), "namespaces", len(placed))
	return nil
}

// syncNamespace adds one newly placed namespace from its snapshot.
func (a *Agent) syncNamespace(ctx context.Context, ns v1.NamespaceName) {
	snap, err := a.client().Snapshot(ctx, ns)
	if err != nil {
		a.log.WarnContext(ctx, "node agent: snapshot of a newly placed namespace", "namespace", ns, "error", err)
		a.resync.Store(true)
		return
	}
	if err := a.fetchKey(ctx, ns); err != nil {
		a.log.WarnContext(ctx, "node agent: fetch a namespace's master", "namespace", ns, "error", err)
	}
	for _, c := range snap.Objects {
		if err := a.upsert(ctx, c); err != nil {
			a.log.WarnContext(ctx, "node agent: apply from the snapshot", "kind", c.Kind, "name", c.Name, "error", err)
		}
	}
}

// apply applies one feed record to the local copy; an error leaves the copy short of it.
func (a *Agent) apply(ctx context.Context, c store.Change) error {
	if c.Kind == v1.KindNamespace {
		return a.applyNamespace(ctx, c)
	}
	if c.Namespace != "" && !a.isMine(c.Namespace) {
		return nil
	}
	var err error
	if c.Type == store.Deleted {
		err = a.delete(ctx, c)
	} else {
		err = a.upsert(ctx, c)
	}
	if err != nil {
		a.log.WarnContext(ctx, "node agent: apply a change", "rev", c.Rev, "kind", c.Kind, "namespace", c.Namespace, "name", c.Name, "error", err)
	}
	return err
}

// applyNamespace follows a Namespace record: a namespace placed on the node starts from its snapshot, and one that left
// the node goes.
func (a *Agent) applyNamespace(ctx context.Context, c store.Change) error {
	ns := v1.NamespaceName(c.Name)
	placed := false
	if c.Type != store.Deleted {
		var n v1.Namespace
		if err := json.Unmarshal(c.Object, &n); err == nil {
			placed = n.Spec.Node == a.id.Load().name
		}
	}
	was := a.isMine(ns)
	switch {
	case placed:
		if err := a.upsert(ctx, c); err != nil {
			a.log.WarnContext(ctx, "node agent: apply a Namespace", "namespace", ns, "error", err)
			return err
		}
		if !was {
			a.mu.Lock()
			a.mine[ns] = true
			a.mu.Unlock()
			a.log.InfoContext(ctx, "node agent: namespace placed on this node", "namespace", ns)
			a.syncNamespace(ctx, ns)
		}
	case was:
		a.removeNamespace(ctx, ns)
	}
	return nil
}

// removeNamespace drops every local object of a namespace that left the node, then its Namespace.
func (a *Agent) removeNamespace(ctx context.Context, ns v1.NamespaceName) {
	a.mu.Lock()
	delete(a.mine, ns)
	a.mu.Unlock()
	a.prune(ctx, ns, map[string]bool{})
	if err := a.cfg.Store.Delete(ctx, v1.KindNamespace.GVK(), "", v1.ObjectName(ns), ""); err != nil && fault.KindOf(err) != fault.NotFound {
		a.log.WarnContext(ctx, "node agent: remove a Namespace", "namespace", ns, "error", err)
	}
	a.dropKey(ctx, ns)
	a.dropTokens(ctx)
	a.log.InfoContext(ctx, "node agent: namespace left this node", "namespace", ns)
}

// dropTokens removes the API tokens that span none of the node's namespaces any more (decision 7).
func (a *Agent) dropTokens(ctx context.Context) {
	res, err := a.cfg.Store.List(ctx, v1.KindAPIToken.GVK(), store.ListOptions{})
	if err != nil {
		a.log.WarnContext(ctx, "node agent: list the API tokens", "error", err)
		return
	}
	for _, obj := range res.Items {
		t, ok := obj.(*v1.APIToken)
		if !ok || len(t.Spec.Namespaces) == 0 || slices.ContainsFunc(t.Spec.Namespaces, a.isMine) {
			continue
		}
		if err := a.remove(ctx, t); err != nil {
			a.log.WarnContext(ctx, "node agent: remove an API token", "name", t.Name, "error", err)
		}
	}
}
