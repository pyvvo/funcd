// Package gc is the owner garbage collector (ADR-0170): it deletes every object whose controller
// OwnerReference names an owner that no longer exists, or exists under another UID. It runs beside the
// controller in background mode: a watch on each owner kind collects a deleted owner's children soon after
// the delete, and a periodic sweep over every child kind catches what a crash or a dropped watch missed.
package gc

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
)

// DefaultInterval is the sweep period when Deps.Interval is 0 (config controller.gcSweepInterval).
const DefaultInterval = 5 * time.Minute

// rewatchBackoff paces a re-watch that failed for a reason other than an expired resourceVersion.
const rewatchBackoff = time.Second

// Pair is an owner kind and a child kind a reconciler stamps with a controller OwnerReference.
type Pair struct{ Owner, Child v1.Kind }

// Pairs is every pair a reconciler stamps. Revision is last: one sweep collects a Function, then its Revisions.
func Pairs() []Pair {
	return []Pair{
		{Owner: v1.KindWorkflow, Child: v1.KindFunction}, {Owner: v1.KindWorkflow, Child: v1.KindKVStore},
		{Owner: v1.KindIdentity, Child: v1.KindSecret}, {Owner: v1.KindSite, Child: v1.KindRoute},
		{Owner: v1.KindFunction, Child: v1.KindRevision},
	}
}

// Deps configures a Collector. Store is required; Interval 0 ⇒ DefaultInterval, < 0 ⇒ fault.Invalid.
type Deps struct {
	Store    store.Store
	Interval time.Duration
	Logger   *slog.Logger
}

// Collector deletes dead-owned children. Its passes are stateless and may run concurrently.
type Collector struct {
	store    store.Store
	interval time.Duration
	log      *slog.Logger

	mu      sync.Mutex
	deleted map[ownerKey]struct{}
	sweep   bool
	wake    chan struct{}

	// startSwept, when set, runs once Run's start sweep has returned: tests use it to order owner deletes after it.
	startSwept func()
}

// ownerKey names a deleted owner recorded by a watch.
type ownerKey struct {
	kind v1.Kind
	ns   v1.NamespaceName
	name v1.ObjectName
}

// New builds a Collector.
func New(d Deps) (*Collector, error) {
	const op = "gc.New"
	if d.Store == nil {
		return nil, fault.Invalidf(op, "store is required")
	}
	if d.Interval < 0 {
		return nil, fault.Invalidf(op, "sweep interval %s must be positive", d.Interval)
	}
	interval := d.Interval
	if interval == 0 {
		interval = DefaultInterval
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Collector{
		store: d.Store, interval: interval, log: log.With("component", "gc"),
		deleted: map[ownerKey]struct{}{}, wake: make(chan struct{}, 1),
	}, nil
}

// Run watches every owner kind and collects until ctx ends. It sweeps once after the watches open, then
// collects the children of each deleted owner, runs requested sweeps and sweeps every interval. It errors
// only when a start watch cannot open.
func (c *Collector) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	defer wg.Wait()
	for _, kind := range ownerKinds() {
		w, err := c.store.Watch(ctx, kind.GVK(), store.WatchOptions{})
		if err != nil {
			cancel()
			return fault.Wrapf(err, fault.KindOf(err), "gc.Run", "watch %s", kind)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.watchOwner(ctx, kind, w)
		}()
	}
	c.logErr(ctx, "sweep", c.collect(ctx, ""))
	if c.startSwept != nil {
		c.startSwept()
	}
	tick := time.NewTicker(c.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			c.logErr(ctx, "sweep", c.collect(ctx, ""))
		case <-c.wake:
			keys, sweep := c.take()
			c.logErr(ctx, "collect deleted owners", c.collectOwners(ctx, keys))
			if sweep {
				c.logErr(ctx, "sweep", c.collect(ctx, ""))
			}
		}
	}
}

// CollectNamespace runs one pass over every child kind in ns (empty ns is every namespace). List errors are
// returned joined. Safe beside Run.
func (c *Collector) CollectNamespace(ctx context.Context, ns v1.NamespaceName) error {
	return c.collect(ctx, ns)
}

// ownerKinds is the distinct owner kinds of Pairs, in order.
func ownerKinds() []v1.Kind {
	var out []v1.Kind
	seen := map[v1.Kind]bool{}
	for _, p := range Pairs() {
		if !seen[p.Owner] {
			seen[p.Owner] = true
			out = append(out, p.Owner)
		}
	}
	return out
}

// childKinds is the distinct child kinds of Pairs, in order (Revision last).
func childKinds() []v1.Kind {
	var out []v1.Kind
	seen := map[v1.Kind]bool{}
	for _, p := range Pairs() {
		if !seen[p.Child] {
			seen[p.Child] = true
			out = append(out, p.Child)
		}
	}
	return out
}

// watchOwner records each Deleted key of one owner kind. A closed stream re-watches from its last
// resourceVersion; an expired one (Unavailable), or none seen, re-watches from now and requests a sweep.
func (c *Collector) watchOwner(ctx context.Context, kind v1.Kind, w store.Watch) {
	var last uint64
	for {
		for ev := range w.ResultChan() {
			m := ev.Object.GetObjectMeta()
			if rv, err := strconv.ParseUint(m.ResourceVersion, 10, 64); err == nil && rv > last {
				last = rv
			}
			if ev.Type == store.Deleted {
				c.record(ownerKey{kind: kind, ns: m.Namespace, name: m.Name}, false)
			}
		}
		w.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			opts := store.WatchOptions{}
			if last > 0 {
				opts.SinceResourceVersion = strconv.FormatUint(last, 10)
			}
			nw, err := c.store.Watch(ctx, kind.GVK(), opts)
			if err == nil {
				if last == 0 {
					c.record(ownerKey{}, true)
				}
				w = nw
				break
			}
			if fault.KindOf(err) == fault.Unavailable {
				last = 0
				continue
			}
			c.log.WarnContext(ctx, "re-watch failed", "kind", kind, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(rewatchBackoff):
			}
		}
	}
}

// record queues a deleted owner (or, with sweep, a sweep request) and wakes the worker.
func (c *Collector) record(k ownerKey, sweep bool) {
	c.mu.Lock()
	if sweep {
		c.sweep = true
	} else {
		c.deleted[k] = struct{}{}
	}
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// take drains the queued owners and the coalesced sweep request.
func (c *Collector) take() ([]ownerKey, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]ownerKey, 0, len(c.deleted))
	for k := range c.deleted {
		keys = append(keys, k)
	}
	c.deleted = map[ownerKey]struct{}{}
	sweep := c.sweep
	c.sweep = false
	return keys, sweep
}

func (c *Collector) logErr(ctx context.Context, what string, err error) {
	if err != nil && ctx.Err() == nil {
		c.log.WarnContext(ctx, "garbage collection incomplete", "pass", what, "error", err)
	}
}

// collect is one pass over every child kind in ns ("" ⇒ all namespaces).
func (c *Collector) collect(ctx context.Context, ns v1.NamespaceName) error {
	p := newPass(c)
	var errs []error
	for _, kind := range childKinds() {
		res, err := c.store.List(ctx, kind.GVK(), store.ListOptions{Namespace: ns})
		if err != nil {
			c.log.WarnContext(ctx, "list children failed", "kind", kind, "namespace", ns, "error", err)
			errs = append(errs, err)
			continue
		}
		errs = append(errs, p.judgeAll(ctx, res.Items, nil))
	}
	return errors.Join(errs...)
}

// collectOwners applies the collection rule to the children of each deleted owner.
func (c *Collector) collectOwners(ctx context.Context, keys []ownerKey) error {
	p := newPass(c)
	var errs []error
	for _, k := range keys {
		for _, pair := range Pairs() {
			if pair.Owner != k.kind {
				continue
			}
			res, err := c.store.List(ctx, pair.Child.GVK(), store.ListOptions{Namespace: k.ns})
			if err != nil {
				errs = append(errs, err)
				continue
			}
			errs = append(errs, p.judgeAll(ctx, res.Items, &k))
		}
	}
	return errors.Join(errs...)
}

// pass memoizes owner judgments by the full controller ref for one pass only.
type pass struct {
	c    *Collector
	live map[v1.OwnerReference]bool
}

func newPass(c *Collector) *pass { return &pass{c: c, live: map[v1.OwnerReference]bool{}} }

// judgeAll collects each dead-owned object of items; with only set, only children naming that owner.
func (p *pass) judgeAll(ctx context.Context, items []v1.Object, only *ownerKey) error {
	var errs []error
	for _, obj := range items {
		ref, ok := childRef(obj)
		if !ok {
			continue
		}
		if only != nil && (ref.Kind != only.kind || ref.Name != only.name) {
			continue
		}
		errs = append(errs, p.collectChild(ctx, obj, ref))
	}
	return errors.Join(errs...)
}

// childRef returns obj's controller ref when obj is a child: a KVStore only when kvStoreCollectable.
func childRef(obj v1.Object) (v1.OwnerReference, bool) {
	refs := obj.GetObjectMeta().OwnerReferences
	if obj.GroupVersionKind().Kind == v1.KindKVStore && !kvStoreCollectable(refs) {
		return v1.OwnerReference{}, false
	}
	return v1.ControllerOf(refs)
}

// kvStoreCollectable reports whether a KVStore's controller ref and a non-controller marker name the same
// kind, name and UID, so a ref the previous materializer wrote onto a store it did not make deletes nothing
// (ADR-0178 Decision 5).
func kvStoreCollectable(refs []v1.OwnerReference) bool {
	c, ok := v1.ControllerOf(refs)
	if !ok {
		return false
	}
	for _, r := range refs {
		if !r.Controller && r.Kind == c.Kind && r.Name == c.Name && r.UID == c.UID {
			return true
		}
	}
	return false
}

// collectChild deletes obj when its owner is dead, with its resourceVersion as the precondition. A Conflict
// re-reads it and re-judges once with a fresh owner Get; a second Conflict waits for the next sweep.
func (p *pass) collectChild(ctx context.Context, obj v1.Object, ref v1.OwnerReference) error {
	live, err := p.ownerLive(ctx, obj, ref, true)
	if err != nil || live {
		return err
	}
	m := obj.GetObjectMeta()
	gvk := obj.GroupVersionKind()
	err = p.c.store.Delete(ctx, gvk, m.Namespace, m.Name, m.ResourceVersion)
	if err == nil {
		p.c.log.InfoContext(ctx, "collected", "kind", gvk.Kind, "namespace", m.Namespace, "name", m.Name, "ownerKind", ref.Kind, "owner", ref.Name)
		return nil
	}
	switch fault.KindOf(err) {
	case fault.NotFound:
		return nil
	case fault.Conflict:
	default:
		return err
	}
	cur, err := p.c.store.Get(ctx, gvk, m.Namespace, m.Name)
	if fault.KindOf(err) == fault.NotFound {
		return nil
	}
	if err != nil {
		return err
	}
	ref, ok := childRef(cur)
	if !ok {
		return nil
	}
	if live, err = p.ownerLive(ctx, cur, ref, false); err != nil || live {
		return err
	}
	cm := cur.GetObjectMeta()
	err = p.c.store.Delete(ctx, gvk, cm.Namespace, cm.Name, cm.ResourceVersion)
	switch fault.KindOf(err) {
	case fault.NotFound:
		return nil
	case fault.Conflict:
		p.c.log.DebugContext(ctx, "child changed twice; left for the next sweep", "kind", gvk.Kind, "namespace", cm.Namespace, "name", cm.Name)
		return nil
	}
	return err
}

// ownerLive reports whether the owner a controller ref names exists with the ref's UID.
func (p *pass) ownerLive(ctx context.Context, child v1.Object, ref v1.OwnerReference, memo bool) (bool, error) {
	ns := ref.Namespace
	if ns == "" && ref.Kind.Namespaced() {
		ns = child.GetObjectMeta().Namespace
	}
	key := v1.OwnerReference{ObjectRef: v1.ObjectRef{Kind: ref.Kind, Namespace: ns, Name: ref.Name}, UID: ref.UID}
	if memo {
		if live, ok := p.live[key]; ok {
			return live, nil
		}
	}
	owner, err := p.c.store.Get(ctx, ref.Kind.GVK(), ns, ref.Name)
	var live bool
	switch {
	case err == nil:
		live = owner.GetObjectMeta().UID == ref.UID
	case fault.KindOf(err) == fault.NotFound:
		live = false
	default:
		return false, err
	}
	p.live[key] = live
	return live, nil
}
