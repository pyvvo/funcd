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
	Keys     func(multinode.NamespaceKey)
	Version  string
	Platform string
}

// Agent keeps a node's local store a copy of the namespaces placed on it (FEAT-0002 F243), writes their status and the
// children the node owns back (F244), and answers the node calls (F250).
type Agent struct {
	cfg Config
	id  identity
	log *slog.Logger

	mu     sync.Mutex
	cursor uint64
	mine   map[v1.NamespaceName]bool
	pushed map[string]string
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
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	a := &Agent{cfg: cfg, id: id, log: cfg.Logger.With("component", "node-agent", "node", id.name),
		mine: map[v1.NamespaceName]bool{}, pushed: map[string]string{}}
	if b, err := os.ReadFile(filepath.Join(cfg.Dir, cursorFile)); err == nil { //nolint:gosec // the node's own directory
		a.cursor, _ = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	}
	return a, nil
}

// Name is the node's name, the one it joined under.
func (a *Agent) Name() v1.ObjectName { return a.id.name }

// SetLogs gives the agent the node's local log reads, once the node's platform built them.
func (a *Agent) SetLogs(logs multinode.LogReader, runLogs multinode.RunLogReader) {
	a.cfg.Logs, a.cfg.RunLogs = logs, runLogs
}

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
	backoff, failing, resync := retryMin, false, a.cursor == 0
	fail := func(what string, err error) {
		if !failing {
			a.log.WarnContext(ctx, what, "error", err.Error())
		}
		failing = true
		sleep(ctx, backoff)
		backoff = min(backoff*2, retryMax)
	}
	for ctx.Err() == nil {
		if resync {
			if err := a.syncAll(ctx); err != nil {
				fail("node agent: snapshot", err)
				continue
			}
			resync = false
		}
		var feed multinode.Feed
		path := multinode.PathFeed + "?since=" + strconv.FormatUint(a.cursor, 10) + "&wait=" + pollWait.String()
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
		if feed.Rev > a.cursor {
			a.saveCursor(feed.Rev)
		}
	}
}

func (a *Agent) saveCursor(rev uint64) {
	a.cursor = rev
	tmp := filepath.Join(a.cfg.Dir, cursorFile+".tmp")
	if err := os.WriteFile(tmp, []byte(strconv.FormatUint(rev, 10)+"\n"), 0o600); err == nil {
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
	for _, c := range snap.Objects {
		seen[key(c.Kind, c.Namespace, c.Name)] = true
		if err := a.upsert(ctx, c); err != nil {
			a.log.WarnContext(ctx, "node agent: apply from the snapshot", "kind", c.Kind, "namespace", c.Namespace, "name", c.Name, "error", err)
		}
	}
	for ns := range placed {
		a.prune(ctx, ns, seen)
		a.fetchKey(ctx, ns)
	}
	a.saveCursor(snap.Rev)
	a.log.InfoContext(ctx, "node agent: synced from a snapshot", "rev", snap.Rev, "objects", len(snap.Objects), "namespaces", len(placed))
	return nil
}

// syncNamespace adds one newly placed namespace from its snapshot.
func (a *Agent) syncNamespace(ctx context.Context, ns v1.NamespaceName) {
	var snap multinode.Snapshot
	path := multinode.PathSnapshot + "?namespace=" + url.QueryEscape(string(ns))
	if _, err := a.do(ctx, http.MethodGet, path, nil, &snap); err != nil {
		a.log.WarnContext(ctx, "node agent: snapshot of a newly placed namespace", "namespace", ns, "error", err)
		return
	}
	for _, c := range snap.Objects {
		if err := a.upsert(ctx, c); err != nil {
			a.log.WarnContext(ctx, "node agent: apply from the snapshot", "kind", c.Kind, "name", c.Name, "error", err)
		}
	}
	a.fetchKey(ctx, ns)
}

// fetchKey hands namespace ns's key to the node's platform (decision 8).
func (a *Agent) fetchKey(ctx context.Context, ns v1.NamespaceName) {
	if a.cfg.Keys == nil {
		return
	}
	var k multinode.NamespaceKey
	if _, err := a.do(ctx, http.MethodGet, multinode.PathKeys+"?namespace="+url.QueryEscape(string(ns)), nil, &k); err != nil {
		a.log.WarnContext(ctx, "node agent: fetch the namespace's key", "namespace", ns, "error", err)
		return
	}
	a.cfg.Keys(k)
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
		err = a.cfg.Store.Delete(ctx, c.Kind.GVK(), c.Namespace, c.Name, "")
		if fault.KindOf(err) == fault.NotFound {
			err = nil
		}
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
	a.log.InfoContext(ctx, "node agent: namespace left this node", "namespace", ns)
}

// prune deletes the local objects of ns the control plane does not hold (not in keep). The node's own children stay:
// its reconcilers own them.
func (a *Agent) prune(ctx context.Context, ns v1.NamespaceName, keep map[string]bool) {
	for _, k := range v1.AllKinds() {
		if !k.Namespaced() {
			continue
		}
		res, err := a.cfg.Store.List(ctx, k.GVK(), store.ListOptions{Namespace: ns})
		if err != nil {
			continue
		}
		for _, obj := range res.Items {
			m := obj.GetObjectMeta()
			if len(m.OwnerReferences) > 0 || keep[key(k, ns, m.Name)] {
				continue
			}
			if err := a.cfg.Store.Delete(ctx, k.GVK(), ns, m.Name, ""); err != nil && fault.KindOf(err) != fault.NotFound {
				a.log.WarnContext(ctx, "node agent: prune", "kind", k, "namespace", ns, "name", m.Name, "error", err)
			}
		}
	}
}

func key(k v1.Kind, ns v1.NamespaceName, name v1.ObjectName) string {
	return string(k) + "/" + string(ns) + "/" + string(name)
}

// upsert writes a control-plane object into the local store, without the fields the local store mints, and keeps the
// status the node's reconcilers wrote.
func (a *Agent) upsert(ctx context.Context, c store.Change) error {
	obj, ok := v1.NewObject(c.Kind)
	if !ok {
		return fault.Invalidf("agent.upsert", "unknown kind %q", c.Kind)
	}
	if err := json.Unmarshal(c.Object, obj); err != nil {
		return fault.Wrapf(err, fault.Invalid, "agent.upsert", "decode the %s", c.Kind)
	}
	m := obj.GetObjectMeta()
	m.UID, m.ResourceVersion, m.Generation = "", "", 0
	m.CreationTime, m.DeletionTime = v1.Timestamp{}, nil
	for range 5 {
		cur, err := a.cfg.Store.Get(ctx, obj.GroupVersionKind(), m.Namespace, m.Name)
		if fault.KindOf(err) == fault.NotFound {
			if _, err = a.cfg.Store.Create(ctx, obj); fault.KindOf(err) == fault.Conflict {
				continue
			}
			return err
		}
		if err != nil {
			return err
		}
		next, err := keepStatus(obj, cur)
		if err != nil {
			return err
		}
		next.GetObjectMeta().ResourceVersion = cur.GetObjectMeta().ResourceVersion
		if _, err = a.cfg.Store.Update(ctx, next); fault.KindOf(err) != fault.Conflict {
			return err
		}
	}
	return fault.Conflictf("agent.upsert", "%s %s kept changing", c.Kind, c.Name)
}

// keepStatus is obj with cur's status.
func keepStatus(obj, cur v1.Object) (v1.Object, error) {
	const op = "agent.keepStatus"
	curRaw, err := json.Marshal(cur)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "marshal")
	}
	var curMap map[string]json.RawMessage
	if err := json.Unmarshal(curRaw, &curMap); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "unmarshal")
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "marshal")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "unmarshal")
	}
	if st, ok := curMap["status"]; ok {
		m["status"] = st
	} else {
		delete(m, "status")
	}
	if raw, err = json.Marshal(m); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "marshal")
	}
	next, _ := v1.NewObject(obj.GroupVersionKind().Kind)
	if err := json.Unmarshal(raw, next); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "unmarshal")
	}
	return next, nil
}
