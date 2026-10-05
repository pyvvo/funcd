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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// PoolKey identifies one shared worker: functions sharing all four co-locate in one pool worker. Pooling never
// consults the resourceGroup for placement (ADR-0046 Decision 2) beyond the grant it carries into AccessHash — a
// different worker id, runtime, namespace or access is a different worker.
type PoolKey struct {
	Namespace v1.NamespaceName
	Runtime   string
	Worker    string // the owner-chosen worker id (FunctionSpec.Pooling.Worker)
	// AccessHash is AccessHashOf the member's access: Functions that reach different data or hold different grants
	// never share a process.
	AccessHash string
}

// String is the status.pool form, "<runtime>/<worker>/<access>".
func (k PoolKey) String() string { return k.Runtime + "/" + k.Worker + "/" + k.AccessHash }

// ParsePool parses the status.pool form back into a key of namespace ns. A worker id is a DNS-1123 label
// (Function.Validate), so the form has exactly three non-empty "/"-segments; ok is false otherwise.
func ParsePool(ns v1.NamespaceName, s string) (key PoolKey, ok bool) {
	parts := strings.Split(s, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return PoolKey{}, false
	}
	return PoolKey{Namespace: ns, Runtime: parts[0], Worker: parts[1], AccessHash: parts[2]}, true
}

// accessDoc is the canonical document AccessHashOf digests: every binding a member reaches data or a peer through,
// the data it owns, and the grants it holds outside its spec.
type accessDoc struct {
	KV       []v1.FunctionKV      `json:"kv"`
	Blob     []v1.FunctionBlob    `json:"blob"`
	Catalogs []v1.FunctionCatalog `json:"catalogs"`
	Links    []accessLink         `json:"links"`
	Secrets  []v1.ObjectName      `json:"secrets"`
	Config   []v1.ObjectName      `json:"config"`
	Owned    []string             `json:"owned"`  // "kv/<store>/<table>" owned and bound; "blob/<bucket>/<prefix>" owned
	Grants   []string             `json:"grants"` // "group/<rg>", "<kind>/<object>[#<j>]@<sha256>", by what is granted
}

// accessLink is a link as access counts it: its timeout grants nothing.
type accessLink struct {
	Alias  string        `json:"alias"`
	Target v1.ObjectName `json:"target"`
}

// accessHashLen is how many hex digits of the SHA-256 a key keeps.
const accessHashLen = 16

// AccessHashOf is the first 16 hex digits of the SHA-256 of spec's canonical access document: KV, Blob, Catalogs
// and Links sorted by alias, Secrets and Config in declared order, owned and grants sorted. nil counts as empty, so
// a Function with no access has one fixed hash. Pure.
func AccessHashOf(spec v1.FunctionSpec, owned, grants []string) string {
	doc := accessDoc{
		KV:       append([]v1.FunctionKV{}, spec.KV...),
		Blob:     append([]v1.FunctionBlob{}, spec.Blob...),
		Catalogs: append([]v1.FunctionCatalog{}, spec.Catalogs...),
		Links:    make([]accessLink, 0, len(spec.Links)),
		Secrets:  append([]v1.ObjectName{}, spec.Secrets...),
		Config:   append([]v1.ObjectName{}, spec.Config...),
		Owned:    sortedCopy(owned),
		Grants:   sortedCopy(grants),
	}
	for _, l := range spec.Links {
		doc.Links = append(doc.Links, accessLink{Alias: l.Alias, Target: l.Target})
	}
	sort.SliceStable(doc.KV, func(i, j int) bool { return doc.KV[i].Alias < doc.KV[j].Alias })
	sort.SliceStable(doc.Blob, func(i, j int) bool { return doc.Blob[i].Alias < doc.Blob[j].Alias })
	sort.SliceStable(doc.Catalogs, func(i, j int) bool { return doc.Catalogs[i].Alias < doc.Catalogs[j].Alias })
	sort.SliceStable(doc.Links, func(i, j int) bool { return doc.Links[i].Alias < doc.Links[j].Alias })
	data, err := json.Marshal(doc)
	if err != nil {
		return "" // a struct of strings cannot fail to marshal
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:accessHashLen]
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// Assignment is where a function lands. Pooled==false ⇒ a solo worker (Key is the zero
// value). Rejected==true ⇒ pooled but over the cap: hold the function NotReady with Reason.
type Assignment struct {
	Pooled   bool
	Key      PoolKey
	Rejected bool
	Reason   string
}

// Assigner places a function given its key, every function declaring that key — fn included, Ready or not — and
// the per-pool limit. The V1 driver is the explicit worker-id policy (NewAssigner); the automatic/threshold policy is
// the future second driver behind this port (ADR-0046 Open questions).
type Assigner interface {
	// Assign places fn under key (KeyOf with fn's access). Solo when fn.Spec.Pooling.Worker == "". Otherwise members
	// are ordered by name, the first `limit` are admitted (Pooled), the rest Rejected with a clear reason.
	// Deterministic — a pure function of declared membership, stable under reconcile order.
	Assign(fn *v1.Function, key PoolKey, sameKey []*v1.Function, limit int) (Assignment, error)
}

// KeyOf returns the pool key a function declares given its owned data and grants (AccessHashOf), and whether it
// opts into pooling at all (a non-empty worker id). It is the canonical grouping used by both the policy and the
// reconciler so the two never disagree on what "same key" means.
func KeyOf(fn *v1.Function, owned, grants []string) (PoolKey, bool) {
	w := fn.Spec.Pooling.Worker
	if w == "" {
		return PoolKey{}, false
	}
	return PoolKey{Namespace: fn.Namespace, Runtime: string(fn.Spec.Runtime), Worker: w, AccessHash: AccessHashOf(fn.Spec, owned, grants)}, true
}

// greedyAssigner is the explicit-worker-id driver: it admits same-key members in name order
// up to the limit. "Greedy" only in the trivial sense that it fills a single declared worker
// id in order — it never invents an id or bin-packs across ids (that is the rejected
// auto-bin-pack alternative, ADR-0046 Alternatives).
type greedyAssigner struct{}

// NewAssigner returns the V1 explicit-worker-id pooling policy.
func NewAssigner() Assigner { return greedyAssigner{} }

func (greedyAssigner) Assign(fn *v1.Function, key PoolKey, sameKey []*v1.Function, limit int) (Assignment, error) {
	const op = "pooling.Assign"
	if fn == nil {
		return Assignment{}, fault.Invalidf(op, "function must not be nil")
	}
	if fn.Spec.Pooling.Worker == "" {
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
			Reason:   poolFullReason(key, limit),
		}, nil
	}
	return Assignment{Pooled: true, Key: key}, nil
}

// membersOfKey returns the functions in sameKey that declare key's namespace, runtime and worker id, sorted by name
// (the deterministic admission order). The caller passes members of key's access; their access is not recomputed
// here, as it depends on objects outside the spec. It never mutates the caller's slice.
func membersOfKey(key PoolKey, sameKey []*v1.Function) []*v1.Function {
	out := make([]*v1.Function, 0, len(sameKey))
	for _, m := range sameKey {
		if m == nil {
			continue
		}
		if m.Spec.Pooling.Worker == key.Worker && m.Namespace == key.Namespace && string(m.Spec.Runtime) == key.Runtime {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// poolFullReason is the PoolFull condition message (ADR-0046 Decision 3).
func poolFullReason(key PoolKey, limit int) string {
	return "pool " + key.String() + " is full (" + strconv.Itoa(limit) + "); use another pooling.worker"
}
