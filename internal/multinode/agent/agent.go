package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/store"
)

const (
	retryMin    = 100 * time.Millisecond
	retryMax    = 5 * time.Second
	pollWait    = 25 * time.Second
	resyncEvery = 30 * time.Second
)

// Config configures a node's agent.
type Config struct {
	Dir      string      // the node's cluster directory, which holds its identity
	Store    store.Store // the node's local store
	Logger   *slog.Logger
	Logs     multinode.LogReader    // the node's local log reads, which answer the log calls
	RunLogs  multinode.RunLogReader // the node's local run-log reads
	Commands multinode.Commands     // the node's local parts that answer the other node commands
	Version  string
	Platform string
}

// Agent keeps a node's local store a copy of the namespaces placed on it (FEAT-0002 F243), writes their status and the
// children the node owns back (F244), and answers the node calls (F250).
type Agent struct {
	cfg Config
	id  identity
	log *slog.Logger

	mu       sync.Mutex
	cursor   uint64
	timeline string      // the control plane metastore's timeline the cursor belongs to
	resync   atomic.Bool // a namespace's snapshot failed: the next poll starts from a whole snapshot
	mine     map[v1.NamespaceName]bool
	keys     map[v1.NamespaceName]multinode.NamespaceKey
	pushed   map[string]string
	// backupSent is the backup status the control plane holds, as last sent
	backupSent string
	// unsent are the deletes the control plane did not take yet
	unsent map[string]url.Values
	// resynced asks the report for every status and child again, after a snapshot
	resynced chan struct{}
}

// New loads the node's identity from cfg.Dir.
func New(cfg Config) (*Agent, error) {
	if cfg.Store == nil {
		return nil, fault.Invalidf("agent.New", "the node's store is required")
	}
	id, err := loadIdentity(cfg.Dir)
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
	a := &Agent{cfg: cfg, id: id, log: cfg.Logger.With("component", "node-agent", "node", id.name),
		mine: map[v1.NamespaceName]bool{}, keys: keys, pushed: map[string]string{}, unsent: map[string]url.Values{},
		resynced: make(chan struct{}, 1)}
	if b, err := os.ReadFile(filepath.Join(cfg.Dir, cursorFile)); err == nil { //nolint:gosec // the node's own directory
		if f := strings.Fields(string(b)); len(f) == 2 {
			a.timeline = f[0]
			a.cursor, _ = strconv.ParseUint(f[1], 10, 64)
		}
	}
	return a, nil
}

// Name is the node's name, the one it joined under.
func (a *Agent) Name() v1.ObjectName { return a.id.name }

// SetLogs gives the agent the node's local log reads, once the node's platform built them.
func (a *Agent) SetLogs(logs multinode.LogReader, runLogs multinode.RunLogReader) {
	a.cfg.Logs, a.cfg.RunLogs = logs, runLogs
}

// SetCommands gives the agent the node's local parts that answer the other node commands (F250), once the node's
// platform built them.
func (a *Agent) SetCommands(c multinode.Commands) { a.cfg.Commands = c }

// Run follows, reports and answers until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.loadMine(ctx); err != nil {
		return err
	}
	a.log.InfoContext(ctx, "node agent starting", "server", a.id.server, "since", a.cursor)
	var wg sync.WaitGroup
	for _, loop := range []func(context.Context){a.follow, a.report, a.serveCalls} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			loop(ctx)
		}()
	}
	wg.Wait()
	return nil
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
		if n, ok := obj.(*v1.Namespace); ok && n.Spec.Node == a.id.name {
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

// errGone is a feed revision older than the change log: the node starts again from a snapshot.
var errGone = fault.PreconditionFailedf("agent.feed", "the change log no longer holds the node's revision")

//nolint:forbidigo // a JSON round trip of the node API: body and out are its typed requests and answers
func (a *Agent) do(ctx context.Context, method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.id.server+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set(multinode.HeaderAPIVersion, strconv.Itoa(multinode.APIVersion))
	req.Header.Set(multinode.HeaderVersion, a.cfg.Version)
	req.Header.Set(multinode.HeaderPlatform, a.cfg.Platform)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.id.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	switch {
	case resp.StatusCode == http.StatusGone:
		return resp.StatusCode, errGone
	case resp.StatusCode >= 300:
		return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	case out != nil && len(data) > 0:
		return resp.StatusCode, json.Unmarshal(data, out)
	}
	return resp.StatusCode, nil
}

// follow keeps the local copy: a snapshot first, then the feed from its revision; a revision the change log no longer
// holds starts again from a snapshot.
func (a *Agent) follow(ctx context.Context) {
	backoff, failing, resync := retryMin, false, a.cursor == 0 || a.timeline == ""
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
			if err := a.syncAll(ctx); err != nil {
				fail("node agent: snapshot", err)
				continue
			}
			resync = false
		}
		var feed multinode.Feed
		path := multinode.PathFeed + "?since=" + strconv.FormatUint(a.cursor, 10) + "&timeline=" + url.QueryEscape(a.timeline) +
			"&wait=" + pollWait.String()
		if _, err := a.do(ctx, http.MethodGet, path, nil, &feed); err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, errGone) {
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
		for _, c := range feed.Changes {
			a.apply(ctx, c)
		}
		a.fetchMissingKeys(ctx)
		if ctx.Err() != nil {
			return // a change cut short is applied again after the restart
		}
		if feed.Rev > a.cursor {
			a.saveCursor(a.timeline, feed.Rev)
		}
	}
}

func (a *Agent) saveCursor(timeline string, rev uint64) {
	a.cursor, a.timeline = rev, timeline
	tmp := filepath.Join(a.cfg.Dir, cursorFile+".tmp")
	if err := os.WriteFile(tmp, []byte(timeline+" "+strconv.FormatUint(rev, 10)+"\n"), 0o600); err == nil {
		_ = os.Rename(tmp, filepath.Join(a.cfg.Dir, cursorFile))
	}
}

// syncAll replaces the local copy with a snapshot of every namespace placed on the node.
func (a *Agent) syncAll(ctx context.Context) error {
	var snap multinode.Snapshot
	if _, err := a.do(ctx, http.MethodGet, multinode.PathSnapshot, nil, &snap); err != nil {
		return err
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
	var snap multinode.Snapshot
	path := multinode.PathSnapshot + "?namespace=" + url.QueryEscape(string(ns))
	if _, err := a.do(ctx, http.MethodGet, path, nil, &snap); err != nil {
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

// apply applies one feed record to the local copy.
func (a *Agent) apply(ctx context.Context, c store.Change) {
	if c.Kind == v1.KindNamespace {
		a.applyNamespace(ctx, c)
		return
	}
	if c.Namespace != "" && !a.isMine(c.Namespace) {
		return
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
}

// applyNamespace follows a Namespace record: a namespace placed on the node starts from its snapshot, and one that left
// the node goes.
func (a *Agent) applyNamespace(ctx context.Context, c store.Change) {
	ns := v1.NamespaceName(c.Name)
	placed := false
	if c.Type != store.Deleted {
		var n v1.Namespace
		if err := json.Unmarshal(c.Object, &n); err == nil {
			placed = n.Spec.Node == a.id.name
		}
	}
	was := a.isMine(ns)
	switch {
	case placed:
		if err := a.upsert(ctx, c); err != nil {
			a.log.WarnContext(ctx, "node agent: apply a Namespace", "namespace", ns, "error", err)
			return
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
	a.log.InfoContext(ctx, "node agent: namespace left this node", "namespace", ns)
}
