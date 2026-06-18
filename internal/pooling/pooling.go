// Package pooling is the pure worker-pooling placement policy (ADR-0046): it maps a
// function to either a solo worker (its own process, the default) or a shared pool worker
// keyed by (namespace, runtime, worker-id). It is the placement counterpart to
// scheduler.Placement (node selection, ADR-0017) — pooling answers WHICH shared worker
// within a node a function joins, never which node.
//
// The policy is pure and deterministic: no I/O, no clock, no store. The reconciler
// (internal/function) lists the same-key functions from the store and serializes the
// admitted members into the pool worker's manifest; this package only decides placement.
package pooling

import (
	"sort"
	"strconv"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// PoolKey identifies one shared worker: functions sharing all three co-locate in one
// worker_threads pool worker. Pooling never consults the resourceGroup (ADR-0046 Decision
// 2) — a different worker id, runtime, or namespace is a different worker.
type PoolKey struct {
	Namespace v1.NamespaceName
	Runtime   string
	Worker    string // the owner-chosen worker id (FunctionSpec.Pooling.Worker)
}

// Assignment is where a function lands. Pooled==false ⇒ a solo worker (Key is the zero
// value). Rejected==true ⇒ pooled but over the cap: hold the function NotReady with Reason.
type Assignment struct {
	Pooled   bool
	Key      PoolKey
	Rejected bool
	Reason   string
}

// Assigner places a function given every function declaring its (namespace, runtime,
// worker-id) — fn included, Ready or not — and the per-pool limit. The V1 driver is the
// explicit worker-id policy (NewAssigner); the automatic/threshold policy is the future
// second driver behind this port (ADR-0046 Open questions).
type Assigner interface {
	// Assign places fn. Solo when fn.Spec.Pooling.Worker == "". Otherwise the key is
	// (fn.Namespace, fn.Spec.Runtime, fn.Spec.Pooling.Worker): members are ordered by name,
	// the first `limit` are admitted (Pooled), the rest Rejected with a clear reason.
	// Deterministic — a pure function of declared membership, stable under reconcile order.
	Assign(fn *v1.Function, sameKey []*v1.Function, limit int) (Assignment, error)
}

// KeyOf returns the pool key a function declares, and whether it opts into pooling at all
// (a non-empty worker id). It is the canonical (ns, runtime, worker) grouping used by both
// the policy and the reconciler so the two never disagree on what "same key" means.
func KeyOf(fn *v1.Function) (PoolKey, bool) {
	w := fn.Spec.Pooling.Worker
	if w == "" {
		return PoolKey{}, false
	}
	return PoolKey{Namespace: fn.Namespace, Runtime: string(fn.Spec.Runtime), Worker: w}, true
}

// greedyAssigner is the explicit-worker-id driver: it admits same-key members in name order
// up to the limit. "Greedy" only in the trivial sense that it fills a single declared worker
// id in order — it never invents an id or bin-packs across ids (that is the rejected
// auto-bin-pack alternative, ADR-0046 Alternatives).
type greedyAssigner struct{}

// NewAssigner returns the V1 explicit-worker-id pooling policy.
func NewAssigner() Assigner { return greedyAssigner{} }

func (greedyAssigner) Assign(fn *v1.Function, sameKey []*v1.Function, limit int) (Assignment, error) {
	const op = "pooling.Assign"
	if fn == nil {
		return Assignment{}, fault.Invalidf(op, "function must not be nil")
	}
	key, pooled := KeyOf(fn)
	if !pooled {
		return Assignment{Pooled: false}, nil // solo — the default, no key.
	}
	if limit < 1 {
		return Assignment{}, fault.Invalidf(op, "pool limit must be >= 1, got %d", limit)
	}

	// Admission is computed over the members that actually share fn's key, ordered by name.
	// The caller passes the declared membership; we defensively re-filter so a stray
	// non-matching member can never shift the cap (the policy stays a pure function of the key).
	members := membersOfKey(key, sameKey)
	rank := -1
	for i, m := range members {
		if m.Name == fn.Name {
			rank = i
			break
		}
	}
	if rank < 0 {
		// fn declares the key but was not in sameKey — treat fn as its own (and only) member.
		// This keeps Assign total: a caller that forgot to include fn still gets a sound answer.
		rank = 0
	}
	if rank >= limit {
		return Assignment{
			Pooled:   true,
			Key:      key,
			Rejected: true,
			Reason:   poolFullReason(key.Worker, limit),
		}, nil
	}
	return Assignment{Pooled: true, Key: key}, nil
}

// membersOfKey returns the functions in sameKey that genuinely share key, sorted by name
// (the deterministic admission order). It never mutates the caller's slice.
func membersOfKey(key PoolKey, sameKey []*v1.Function) []*v1.Function {
	out := make([]*v1.Function, 0, len(sameKey))
	for _, m := range sameKey {
		if m == nil {
			continue
		}
		if mk, ok := KeyOf(m); ok && mk == key {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// poolFullReason is the PoolFull condition message (ADR-0046 Decision 3).
func poolFullReason(worker string, limit int) string {
	return "worker " + worker + " is full (" + strconv.Itoa(limit) + "); use another pooling.worker"
}
