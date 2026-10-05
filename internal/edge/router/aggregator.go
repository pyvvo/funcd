package router

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// Programmer is the replace-all sink the Aggregator drives — the edge Router's Program. Router
// satisfies it; the aggregator depends only on this narrow method.
type Programmer interface {
	Program(ctx context.Context, entries []Entry) error
}

// Owner names the object an Entry belongs to (ADR-0176): claims are arbitrated and verdicts reported per owner.
type Owner struct {
	Kind      v1.Kind
	Namespace v1.NamespaceName
	Name      v1.ObjectName
}

// Verdict is the aggregator's ruling on one Entry: Reason "" means the entry is programmed (ADR-0176).
type Verdict struct {
	Owner   Owner
	Reason  string
	Message string
}

// ModeFunc reads a namespace's exposure mode; a NotFound error means implicit.
type ModeFunc func(ctx context.Context, ns v1.NamespaceName) (v1.ExposureMode, error)

// NotifyFunc asks the owner's reconciler to run again. It must not block: the aggregator calls it after
// releasing its lock, once per owner whose verdict another source's Set changed.
type NotifyFunc func(Owner)

// EntrySetter contributes ONE named source's edge entries to a shared edge table (ADR-0138). Set replaces this
// source's slice (nil/empty clears it); the implementation arbitrates the claims of every source and programs the
// winners exactly once (ADR-0176). A validation or ModeFunc error returns (nil, err) with nothing committed. A
// Program error returns (nil, err) with the source map committed, so the next Set re-programs it.
type EntrySetter interface {
	Set(ctx context.Context, source string, entries []Entry) ([]Verdict, error)
}

// The verdict reasons the aggregator gives (ADR-0110 Decision 3, ADR-0176).
const (
	reasonReservedPath  = "ReservedPath"
	reasonHostRequired  = "HostRequired"
	reasonRouteConflict = "RouteConflict"
	reasonEdgeStarting  = "EdgeStarting"
)

// routeSource is the Route reconciler's source key: until it has Set once, no entry of a later kind rank is
// programmed (ADR-0176 Decision 6).
const routeSource = "routes"

// functionPath is the by-name invoke form the data plane serves after the edge table (dataplane.pathPrefix).
const functionPath = "/function"

// kindRank orders the sources' kinds on an exact (namespace, name) tie (ADR-0176 Decision 2); a kind without a
// rank is refused.
func kindRank(k v1.Kind) (int, bool) {
	switch k {
	case v1.KindRoute:
		return 0, true
	case v1.KindCatalogService:
		return 1, true
	default:
		return 0, false
	}
}

// Aggregator is the sole writer of a Router's replace-all edge table and the one arbiter of its claims
// (ADR-0138, ADR-0176): it stores each source's entries, rules on every entry, and programs the winners. Safe for
// concurrent use.
type Aggregator struct {
	p      Programmer
	modes  ModeFunc
	notify NotifyFunc
	log    *slog.Logger

	mu        sync.Mutex
	sources   map[string][]Entry
	routesSet bool
	verdicts  map[Owner]Verdict // as last programmed
}

// NewAggregator builds an Aggregator over the edge Router's replace-all Program. A nil modes treats every
// namespace as implicit; a nil notify notifies no one; a nil log is slog.Default.
func NewAggregator(p Programmer, modes ModeFunc, notify NotifyFunc, log *slog.Logger) *Aggregator {
	if log == nil {
		log = slog.Default()
	}
	return &Aggregator{
		p:        p,
		modes:    modes,
		notify:   notify,
		log:      log.With("component", "edge.router.aggregator"),
		sources:  make(map[string][]Entry),
		verdicts: make(map[Owner]Verdict),
	}
}

// Set replaces source's entries, arbitrates every stored entry, and programs the winners in one Router.Program
// call under the lock. It returns the verdicts of source's entries in their order, then notifies every owner of
// another source whose verdict changed.
func (a *Aggregator) Set(ctx context.Context, source string, entries []Entry) ([]Verdict, error) {
	const op = "edge.router.aggregator.Set"
	if source == "" {
		return nil, fault.Invalidf(op, "route source key must not be empty")
	}
	stored := make([]Entry, len(entries))
	for i, e := range entries {
		if err := validOwner(e); err != nil {
			return nil, fault.Invalidf(op, "source %q entry %d: %s", source, i, err)
		}
		e.Host = canonicalHost(e.Host)
		stored[i] = e
	}

	a.mu.Lock()
	sources := maps.Clone(a.sources)
	if len(entries) == 0 {
		delete(sources, source)
	} else {
		sources[source] = stored
	}
	routesSet := a.routesSet || source == routeSource
	slots, verdicts, programmed, err := a.arbitrate(ctx, sources, routesSet)
	if err != nil {
		a.mu.Unlock()
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "read namespace exposure mode (source %q)", source)
	}
	a.sources, a.routesSet = sources, routesSet
	if err := a.p.Program(ctx, programmed); err != nil {
		a.mu.Unlock()
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "program aggregated edge table (%d entries, source %q)", len(programmed), source)
	}
	next := make(map[Owner]Verdict, len(verdicts))
	var mine []Verdict
	var changed []Owner
	for i, v := range verdicts {
		next[v.Owner] = v
		if slots[i].source == source {
			mine = append(mine, v)
		} else if prev, ok := a.verdicts[v.Owner]; !ok || prev != v {
			changed = append(changed, v.Owner)
		}
	}
	a.verdicts = next
	a.mu.Unlock()

	if a.notify != nil {
		for _, o := range changed {
			a.notify(o)
		}
	}
	return mine, nil
}

func validOwner(e Entry) error {
	switch {
	case e.Owner.Name == "" || e.Owner.Kind == "":
		return fmt.Errorf("owner must name a kind and an object")
	case e.Owner.Namespace != e.Namespace:
		return fmt.Errorf("owner namespace %q differs from entry namespace %q", e.Owner.Namespace, e.Namespace)
	}
	if _, ok := kindRank(e.Owner.Kind); !ok {
		return fmt.Errorf("kind %q has no edge rank", e.Owner.Kind)
	}
	return nil
}

// slot is one stored entry and the source it came from.
type slot struct {
	source string
	entry  Entry
	rank   int
}

// heldClaim is a programmed rule's claim on a (host, path): nil methods claim every method.
type heldClaim struct {
	methods map[string]bool
	owner   Owner
}

// arbitrate rules on every entry of sources in (namespace, name, kind rank) order: ReservedPath, else
// HostRequired, else EdgeStarting, else RouteConflict, else programmed. It returns the slots in source-key order,
// each slot's verdict, and the programmed entries in ruling order. The caller must hold a.mu.
func (a *Aggregator) arbitrate(ctx context.Context, sources map[string][]Entry, routesSet bool) ([]slot, []Verdict, []Entry, error) {
	keys := make([]string, 0, len(sources))
	for k := range sources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var slots []slot
	for _, k := range keys {
		for _, e := range sources[k] {
			rank, _ := kindRank(e.Owner.Kind)
			slots = append(slots, slot{source: k, entry: e, rank: rank})
		}
	}
	order := make([]int, len(slots))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		x, y := slots[order[i]], slots[order[j]]
		if x.entry.Owner.Namespace != y.entry.Owner.Namespace {
			return x.entry.Owner.Namespace < y.entry.Owner.Namespace
		}
		if x.entry.Owner.Name != y.entry.Owner.Name {
			return x.entry.Owner.Name < y.entry.Owner.Name
		}
		return x.rank < y.rank
	})

	modes := map[v1.NamespaceName]v1.ExposureMode{}
	held := map[string][]heldClaim{}
	verdicts := make([]Verdict, len(slots))
	programmed := make([]Entry, 0, len(slots))
	for _, i := range order {
		s := &slots[i]
		v := Verdict{Owner: s.entry.Owner}
		switch p, reserved := reservedPath(s.entry); {
		case reserved:
			v.Reason, v.Message = reasonReservedPath, fmt.Sprintf("path %q is reserved for claims without a host", p)
		default:
			mode, err := a.modeOf(ctx, modes, s.entry)
			if err != nil {
				return nil, nil, nil, err
			}
			switch winner, conflict := firstConflict(held, s.entry); {
			case mode == v1.ExposureExplicit:
				v.Reason, v.Message = reasonHostRequired, "an explicit-mode namespace requires a host"
			case s.rank > 0 && !routesSet:
				v.Reason, v.Message = reasonEdgeStarting, "held until the Route table loads"
			case conflict:
				v.Reason = reasonRouteConflict
				v.Message = fmt.Sprintf("conflicts with %s %s/%s on (host, path, method)", winner.Kind, winner.Namespace, winner.Name)
			default:
				hold(held, s.entry)
				programmed = append(programmed, s.entry)
			}
		}
		verdicts[i] = v
	}
	return slots, verdicts, programmed, nil
}

// modeOf returns the exposure mode that applies to a host-less entry (implicit for one with a host), reading each
// namespace at most once per arbitration.
func (a *Aggregator) modeOf(ctx context.Context, modes map[v1.NamespaceName]v1.ExposureMode, e Entry) (v1.ExposureMode, error) {
	if e.Host != "" || a.modes == nil {
		return v1.ExposureImplicit, nil
	}
	if m, ok := modes[e.Namespace]; ok {
		return m, nil
	}
	m, err := a.modes(ctx, e.Namespace)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		m = v1.ExposureImplicit
	case err != nil:
		return "", err
	}
	m = m.Normalized()
	modes[e.Namespace] = m
	return m, nil
}

// reservedPath reports the first rule of a host-less entry that would shadow the by-name /function/<name> form,
// or a host-less CatalogService rule at "/" (ADR-0176 Decision 5). The cleaned path is checked.
func reservedPath(e Entry) (string, bool) {
	if e.Host != "" {
		return "", false
	}
	for _, r := range e.Rules {
		p := path.Clean(r.Path)
		if p == functionPath || strings.HasPrefix(p, functionPath+"/") {
			return r.Path, true
		}
		if p == "/" && e.Owner.Kind == v1.KindCatalogService {
			return r.Path, true
		}
	}
	return "", false
}

func claimKey(host, path string) string { return host + "\x00" + path }

// firstConflict returns the owner of the first held claim one of e's rules intersects: same host and path, and
// method sets that intersect (an empty set is every method). An owner never conflicts with itself.
func firstConflict(held map[string][]heldClaim, e Entry) (Owner, bool) {
	for _, r := range e.Rules {
		for _, h := range held[claimKey(e.Host, matchedPath(r))] {
			if h.owner != e.Owner && methodsIntersect(h.methods, r.Methods) {
				return h.owner, true
			}
		}
	}
	return Owner{}, false
}

func hold(held map[string][]heldClaim, e Entry) {
	for _, r := range e.Rules {
		k := claimKey(e.Host, matchedPath(r))
		held[k] = append(held[k], heldClaim{methods: r.Methods, owner: e.Owner})
	}
}

func methodsIntersect(a, b map[string]bool) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	for m, ok := range a {
		if ok && b[m] {
			return true
		}
	}
	return false
}
