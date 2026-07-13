package cedar

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	cedar "github.com/cedar-policy/cedar-go"
	cedartypes "github.com/cedar-policy/cedar-go/types"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// The always-on built-in rules the driver ships (NOT user Policies) are authored as Cedar in the
// `.cedar` files embedded per-capability (each Capability owns its Builtin text, ADR-0116) and
// concatenated into the built-in PolicySet by the registry's Builtins() in compile().

// PolicySource supplies the user Policy resources the driver compiles (ADR-0074). The driver
// compiles the built-in rules + every user Policy into one cached PolicySet, recompiled when the
// source's revision changes.
type PolicySource interface {
	// Policies returns all Policy resources and an opaque revision; the driver recompiles only when
	// the revision changes (cheap cache invalidation without diffing policy text).
	Policies(ctx context.Context) (policies []v1.Policy, revision string, err error)
}

// compiledPolicies is one immutable cache generation: the compiled PolicySet + the source revision it
// was built from. Held behind an atomic.Pointer so readers on the hot path load it lock-free and a
// rebuild swaps a fresh generation in atomically (ADR-0117 §4a — one live version + one transient during
// build, the old GC'd once the last reader drops it; no stale accumulation).
type compiledPolicies struct {
	revision string
	ps       *cedar.PolicySet
}

// policyCache compiles + caches the PolicySet (built-ins + user Policies + compiled EgressPolicies),
// recompiling only on a revision change (ADR-0074/0117). Reads are lock-free (atomic load); the mutex
// single-flights the recompile so a burst of concurrent misses compiles once.
type policyCache struct {
	src      PolicySource
	builtins string // the always-on built-in Cedar text (a Registry's Builtins()); compiled with every user Policy
	cur      atomic.Pointer[compiledPolicies]
	mu       sync.Mutex // guards the recompile only (single-flight), never the read path
}

// Get returns the current compiled PolicySet, recompiling if the source revision changed. The fast path
// (revision unchanged) is a lock-free atomic load; only a revision change takes the single-flight lock.
func (c *policyCache) Get(ctx context.Context) (*cedar.PolicySet, error) {
	const op = "cedar.policyCache.Get"
	policies, rev, err := c.src.Policies(ctx)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "load policies")
	}

	if cur := c.cur.Load(); cur != nil && cur.revision == rev {
		return cur.ps, nil // lock-free hot path
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if cur := c.cur.Load(); cur != nil && cur.revision == rev { // re-check under the lock (a peer may have built it)
		return cur.ps, nil
	}
	ps, err := compile(c.builtins, policies)
	if err != nil {
		return nil, err
	}
	c.cur.Store(&compiledPolicies{revision: rev, ps: ps}) // atomic swap; the old generation is GC'd
	return ps, nil
}

// compile builds a PolicySet from the built-in rules + the user Policies (ADR-0074). A user Policy
// that fails to parse is an Internal fault — admission rejects bad Cedar before persistence, so a
// stored Policy compiling is an invariant; a compile failure here means corruption.
func compile(builtins string, policies []v1.Policy) (*cedar.PolicySet, error) {
	const op = "cedar.compile"
	ps, err := cedar.NewPolicySetFromBytes("builtin", []byte(builtins))
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "compile built-in policies")
	}
	for i := range policies {
		p := &policies[i]
		list, perr := cedar.NewPolicyListFromBytes(string(p.Name), []byte(p.Spec.Cedar))
		if perr != nil {
			return nil, fault.Wrapf(perr, fault.Internal, op, "compile Policy %q/%q", p.Namespace, p.Name)
		}
		for j, pol := range list {
			id := cedartypes.PolicyID(fmt.Sprintf("%s/%s#%d", p.Namespace, p.Name, j))
			ps.Add(id, pol)
		}
	}
	return ps, nil
}
