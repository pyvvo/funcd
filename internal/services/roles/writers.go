// Package roles wires the ADR-0136 RolesAssignment model to the Cedar PDP: a store-backed WriterLister
// that feeds the generalized single-writer forbid's `writers` set, and a compile helper that turns
// RolesAssignments into the synthetic read/query/invoke permits the PolicySource aggregates.
package roles

import (
	"context"

	cedartypes "github.com/cedar-policy/cedar-go/types"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth/cedar"
	"github.com/pyvvo/funcd/internal/store"
)

// Lister is a store-backed cedar.WriterLister (ADR-0136): it lists a namespace's RolesAssignments and
// returns the writer principals whose writer-role grant covers a given blob prefix / kv table.
type Lister struct {
	store    store.Store
	resolver cedar.RoleResolver
}

var _ cedar.WriterLister = (*Lister)(nil)

// NewLister builds the store-backed WriterLister. store.Store satisfies cedar.MetaReader (same Get), so it
// backs the role resolver directly.
func NewLister(s store.Store) *Lister {
	return &Lister{store: s, resolver: cedar.NewRoleResolver(s)}
}

// BlobWriters returns the principals with a writer-role grant covering the (bucket, prefix).
func (l *Lister) BlobWriters(ctx context.Context, ns v1.NamespaceName, bucket, prefix string) []cedartypes.EntityUID {
	return l.writers(ctx, ns, func(sc v1.ScopeRef) bool {
		switch sc.Kind {
		case v1.ScopeKindNamespace:
			return true
		case v1.ScopeKindBucketPrefix:
			return sc.Name == bucket+"/"+prefix
		default:
			return false
		}
	})
}

// KVWriters returns the principals with a writer-role grant covering the (store, table).
func (l *Lister) KVWriters(ctx context.Context, ns v1.NamespaceName, kvStore, table string) []cedartypes.EntityUID {
	return l.writers(ctx, ns, func(sc v1.ScopeRef) bool {
		switch sc.Kind {
		case v1.ScopeKindNamespace:
			return true
		case v1.ScopeKindKVStore:
			return sc.Name == kvStore || sc.Name == kvStore+"/"+table
		default:
			return false
		}
	})
}

// writers lists the namespace's RolesAssignments and collects the writer principals whose scope covers.
func (l *Lister) writers(ctx context.Context, ns v1.NamespaceName, covers func(v1.ScopeRef) bool) []cedartypes.EntityUID {
	lst, err := l.store.List(ctx, v1.KindRolesAssignment.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		return nil
	}
	var out []cedartypes.EntityUID
	for _, obj := range lst.Items {
		ra, ok := obj.(*v1.RolesAssignment)
		if !ok {
			continue
		}
		for _, g := range cedar.WriterGrants(ctx, ra, l.resolver) {
			if covers(g.Scope) {
				out = append(out, g.Principal)
			}
		}
	}
	return out
}

// CompilePolicies compiles every RolesAssignment in the store into synthetic read/query/invoke Cedar
// permits (ADR-0136) — appended by the PolicySource alongside user Policies + EgressPolicies. Write
// grants are NOT compiled here (they gate the forbid via the Lister above). It also returns the max
// RolesAssignment resourceVersion so the policy cache recompiles on change.
func CompilePolicies(ctx context.Context, s store.Store) ([]v1.Policy, string, error) {
	lst, err := s.List(ctx, v1.KindRolesAssignment.GVK(), store.ListOptions{})
	if err != nil {
		return nil, "", err
	}
	resolver := cedar.NewRoleResolver(s)
	var out []v1.Policy
	var maxRV string
	for _, obj := range lst.Items {
		ra, ok := obj.(*v1.RolesAssignment)
		if !ok {
			continue
		}
		if ra.ResourceVersion > maxRV {
			maxRV = ra.ResourceVersion
		}
		pols, cerr := cedar.CompileRolesAssignment(ctx, ra, resolver)
		if cerr != nil {
			return nil, "", cerr
		}
		out = append(out, pols...)
	}
	return out, maxRV, nil
}
