package cedar

import (
	"context"
	_ "embed"
	"fmt"
	"sync"

	cedar "github.com/cedar-policy/cedar-go"
	cedartypes "github.com/cedar-policy/cedar-go/types"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// The always-on built-in rules the driver ships (NOT user Policies) are authored as Cedar in the
// `.cedar` files alongside this one and embedded — far more legible/editable than inline Go strings.
// builtin_kv.cedar: kv::write single-writer. builtin_kv_read.cedar (ADR-0076): a declared spec.kv
// binding grants kv::read (the read-side link-as-grant; unbound reads stay default-deny).
// builtin_invoke.cedar: ADR-0064's link-as-grant preserved as a built-in permit (a declared link
// grants invoke; defense-in-depth). All are concatenated into the built-in PolicySet in compile().

//go:embed builtin_kv.cedar
var builtinKVPolicies string

//go:embed builtin_kv_read.cedar
var builtinKVReadPolicies string

//go:embed builtin_invoke.cedar
var builtinInvokePolicies string

// PolicySource supplies the user Policy resources the driver compiles (ADR-0074). The driver
// compiles the built-in rules + every user Policy into one cached PolicySet, recompiled when the
// source's revision changes.
type PolicySource interface {
	// Policies returns all Policy resources and an opaque revision; the driver recompiles only when
	// the revision changes (cheap cache invalidation without diffing policy text).
	Policies(ctx context.Context) (policies []v1.Policy, revision string, err error)
}

// policyCache compiles + caches the PolicySet (built-ins + user Policies), recompiling on a
// revision change (ADR-0074: the compiled PolicySet is cached on the hot path).
type policyCache struct {
	src PolicySource

	mu       sync.Mutex
	revision string
	ps       *cedar.PolicySet
	loaded   bool
}

// Get returns the current compiled PolicySet, recompiling if the source revision changed.
func (c *policyCache) Get(ctx context.Context) (*cedar.PolicySet, error) {
	const op = "cedar.policyCache.Get"
	policies, rev, err := c.src.Policies(ctx)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "load policies")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && c.revision == rev {
		return c.ps, nil
	}
	ps, err := compile(policies)
	if err != nil {
		return nil, err
	}
	c.ps, c.revision, c.loaded = ps, rev, true
	return ps, nil
}

// compile builds a PolicySet from the built-in rules + the user Policies (ADR-0074). A user Policy
// that fails to parse is an Internal fault — admission rejects bad Cedar before persistence, so a
// stored Policy compiling is an invariant; a compile failure here means corruption.
func compile(policies []v1.Policy) (*cedar.PolicySet, error) {
	const op = "cedar.compile"
	ps, err := cedar.NewPolicySetFromBytes("builtin", []byte(builtinKVPolicies+"\n"+builtinKVReadPolicies+"\n"+builtinInvokePolicies))
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
