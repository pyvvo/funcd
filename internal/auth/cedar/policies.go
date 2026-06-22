package cedar

import (
	"context"
	"fmt"
	"sync"

	cedar "github.com/cedar-policy/cedar-go"
	cedartypes "github.com/cedar-policy/cedar-go/types"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// builtinKVPolicies are the always-on consistency rules the driver ships (ADR-0074), NOT user
// Policies. They make kv::write single-writer: a base permit lets any principal write, and the
// forbid overrides it unless the principal IS the table's owner. Reads have NO built-in permit, so
// they are DEFAULT-DENY until a user Policy grants kv::read. The `resource has owner` guard keeps an
// owner-less table read-only (the forbid still fires when there is no owner to match).
const builtinKVPolicies = `permit(principal, action == Action::"kv::write", resource);
forbid(principal, action == Action::"kv::write", resource)
  unless { resource has owner && principal == resource.owner };`

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
	ps, err := cedar.NewPolicySetFromBytes("builtin", []byte(builtinKVPolicies))
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "compile built-in KV policies")
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
