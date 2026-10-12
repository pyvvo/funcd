// Package roles wires the ADR-0136 RolesAssignment model to the Cedar PDP: a store-backed WriterLister
// that feeds the generalized single-writer forbid's `writers` set, and a compile helper that turns
// RolesAssignments into the synthetic read/query/invoke permits the PolicySource aggregates.
package roles

import (
	"context"
	"slices"

	cedartypes "github.com/cedar-policy/cedar-go/types"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
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
	return l.writers(ctx, ns, func(g cedar.WriterGrant) bool {
		if sqlOnly(g) {
			return false
		}
		switch sc := g.Scope; sc.Kind {
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
	return l.writers(ctx, ns, func(g cedar.WriterGrant) bool {
		if sqlOnly(g) {
			return false
		}
		switch sc := g.Scope; sc.Kind {
		case v1.ScopeKindNamespace:
			return true
		case v1.ScopeKindKVStore:
			return sc.Name == kvStore || sc.Name == kvStore+"/"+table
		default:
			return false
		}
	})
}

// sqlOnly reports whether a grant writes only SQL databases, so it makes no KV or blob writer (ADR-0233).
func sqlOnly(g cedar.WriterGrant) bool {
	return !slices.ContainsFunc(g.Writes, func(a string) bool { return a != string(auth.ActionSQLWrite) })
}

// SQLWriters returns the principals whose grant covering the SQL database holds sql::write (ADR-0233): a role
// writing only KV or blob, such as Contributor, makes no database writer.
func (l *Lister) SQLWriters(ctx context.Context, ns v1.NamespaceName, database string) []cedartypes.EntityUID {
	return l.writers(ctx, ns, func(g cedar.WriterGrant) bool {
		if !slices.Contains(g.Writes, string(auth.ActionSQLWrite)) {
			return false
		}
		switch sc := g.Scope; sc.Kind {
		case v1.ScopeKindNamespace:
			return true
		case v1.ScopeKindSQLDatabase:
			return sc.Name == database
		default:
			return false
		}
	})
}

// writers lists the namespace's RolesAssignments and collects the writer principals whose scope covers.
func (l *Lister) writers(ctx context.Context, ns v1.NamespaceName, covers func(cedar.WriterGrant) bool) []cedartypes.EntityUID {
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
			if covers(g) {
				out = append(out, g.Principal)
			}
		}
	}
	return out
}

// CompilePolicies compiles every RolesAssignment in the store into synthetic read/query/invoke Cedar
// permits (ADR-0136) — appended by the PolicySource alongside user Policies + EgressPolicies. Write
// grants are NOT compiled here (they gate the forbid via the Lister above). It also returns its List's
// collection resourceVersion, the store-wide one, so the policy cache recompiles on change (ADR-0202).
func CompilePolicies(ctx context.Context, s store.Store) ([]v1.Policy, string, error) {
	lst, err := s.List(ctx, v1.KindRolesAssignment.GVK(), store.ListOptions{})
	if err != nil {
		return nil, "", err
	}
	resolver := cedar.NewRoleResolver(s)
	var out []v1.Policy
	for _, obj := range lst.Items {
		ra, ok := obj.(*v1.RolesAssignment)
		if !ok {
			continue
		}
		pols, cerr := cedar.CompileRolesAssignment(ctx, ra, resolver)
		if cerr != nil {
			return nil, "", cerr
		}
		out = append(out, pols...)
	}
	return out, lst.ResourceVersion, nil
}
