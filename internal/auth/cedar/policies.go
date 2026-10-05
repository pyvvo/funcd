package cedar

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	cedar "github.com/cedar-policy/cedar-go"
	cedartypes "github.com/cedar-policy/cedar-go/types"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// The always-on built-in rules the driver ships (NOT user Policies) are authored as Cedar in the
// `.cedar` files embedded per-capability (each Capability owns its Builtin text, ADR-0116) and
// concatenated into the built-in PolicySet by the registry's Builtins() in compile().

// PolicySource supplies the user Policy resources the driver compiles (ADR-0074). The driver
// compiles the built-in rules + each namespace's user Policies into one cached PolicySet per namespace
// (ADR-0177), recompiled when the source's revision changes.
type PolicySource interface {
	// Policies returns all Policy resources and an opaque revision; the driver recompiles only when
	// the revision changes (cheap cache invalidation without diffing policy text).
	Policies(ctx context.Context) (policies []v1.Policy, revision string, err error)
}

// compiledPolicies is one immutable cache generation: the compiled PolicySets + the source revision they
// were built from. Held behind an atomic.Pointer so readers on the hot path load it lock-free and a
// rebuild swaps a fresh generation in atomically (ADR-0117 §4a — one live version + one transient during
// build, the old GC'd once the last reader drops it; no stale accumulation).
type compiledPolicies struct {
	revision string
	builtin  *cedar.PolicySet                      // built-ins only
	byNS     map[v1.NamespaceName]*cedar.PolicySet // built-ins + one namespace's user and synthetic Policies
}

// set picks the PolicySet a request evaluates against (ADR-0177 Decision 2): a same-namespace request
// gets its namespace's set, any other request the built-ins only.
func (c *compiledPolicies) set(principalNS, resourceNS v1.NamespaceName) *cedar.PolicySet {
	if !sameNamespace(principalNS, resourceNS) {
		return c.builtin
	}
	if ps, ok := c.byNS[principalNS]; ok {
		return ps
	}
	return c.builtin
}

// sameNamespace reports whether a request stays inside one namespace (ADR-0177 Decision 1).
func sameNamespace(principalNS, resourceNS v1.NamespaceName) bool {
	return principalNS != "" && principalNS == resourceNS
}

// policyCache compiles + caches the PolicySets (built-ins + user Policies + compiled synthetics),
// recompiling only on a revision change (ADR-0074/0117). Reads are lock-free (atomic load); the mutex
// single-flights the recompile so a burst of concurrent misses compiles once.
type policyCache struct {
	src      PolicySource
	builtins string // the always-on built-in Cedar text (a Registry's Builtins()); added to every set
	cur      atomic.Pointer[compiledPolicies]
	mu       sync.Mutex // guards the recompile only (single-flight), never the read path
}

// For returns the set a request evaluates against (ADR-0177 Decision 2), recompiling if the source
// revision changed. The fast path (revision unchanged) is a lock-free atomic load; only a revision change
// takes the single-flight lock.
func (c *policyCache) For(ctx context.Context, principalNS, resourceNS v1.NamespaceName) (*cedar.PolicySet, error) {
	const op = "cedar.policyCache.For"
	policies, rev, err := c.src.Policies(ctx)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "load policies")
	}

	if cur := c.cur.Load(); cur != nil && cur.revision == rev {
		return cur.set(principalNS, resourceNS), nil // lock-free hot path
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if cur := c.cur.Load(); cur != nil && cur.revision == rev { // re-check under the lock (a peer may have built it)
		return cur.set(principalNS, resourceNS), nil
	}
	builtin, byNS, err := compile(c.builtins, policies)
	if err != nil {
		return nil, err
	}
	next := &compiledPolicies{revision: rev, builtin: builtin, byNS: byNS}
	c.cur.Store(next) // atomic swap; the old generation is GC'd
	return next.set(principalNS, resourceNS), nil
}

// compile builds the built-ins-only PolicySet and, per namespace, a set of the built-ins plus that
// namespace's user and synthetic Policies (ADR-0074, ADR-0177). The built-ins are parsed once and their
// parsed policies shared by every set. A user Policy that fails to parse is an Internal fault — admission
// rejects bad Cedar before persistence, so a stored Policy compiling is an invariant; a compile failure
// here means corruption.
func compile(builtins string, policies []v1.Policy) (builtin *cedar.PolicySet, byNS map[v1.NamespaceName]*cedar.PolicySet, err error) {
	const op = "cedar.compile"
	builtin, err = cedar.NewPolicySetFromBytes("builtin", []byte(builtins))
	if err != nil {
		return nil, nil, fault.Wrapf(err, fault.Internal, op, "compile built-in policies")
	}
	byNS = map[v1.NamespaceName]*cedar.PolicySet{}
	for i := range policies {
		p := &policies[i]
		list, perr := cedar.NewPolicyListFromBytes(string(p.Name), []byte(p.Spec.Cedar))
		if perr != nil {
			return nil, nil, fault.Wrapf(perr, fault.Internal, op, "compile Policy %q/%q", p.Namespace, p.Name)
		}
		ps, ok := byNS[p.Namespace]
		if !ok {
			ps = withBuiltins(builtin)
			byNS[p.Namespace] = ps
		}
		for j, pol := range list {
			id := cedartypes.PolicyID(fmt.Sprintf("%s/%s#%d", p.Namespace, p.Name, j))
			ps.Add(id, pol)
		}
	}
	return builtin, byNS, nil
}

func withBuiltins(builtin *cedar.PolicySet) *cedar.PolicySet {
	ps := cedar.NewPolicySet()
	for id, pol := range builtin.All() {
		ps.Add(id, pol)
	}
	return ps
}
