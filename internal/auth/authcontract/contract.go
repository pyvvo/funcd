// Package authcontract is the shared conformance suite for the auth.Authorizer port
// (ADR-0018): Run asserts the default-deny namespace-RBAC guarantee against any driver
// — admin spans namespaces + cluster-scoped kinds, developer writes only in its
// namespaces, viewer is read-only and never reads a Secret, and an unknown principal is denied. The built-in
// rbac driver runs it; the cedar-go V2 driver will inherit it.
package authcontract

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// Run asserts the Authorizer port guarantee against a.
func Run(t *testing.T, a auth.Authorizer) {
	t.Helper()
	ctx := context.Background()

	admin := auth.Identity{Subject: "root", Role: auth.RoleAdmin}
	dev := auth.Identity{Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}}
	viewer := auth.Identity{Subject: "obs", Role: auth.RoleViewer, Namespaces: []v1.NamespaceName{"team-a"}}
	stranger := auth.Identity{Subject: "nobody"}

	must := func(id auth.Identity, verb auth.Verb, kind v1.Kind, ns v1.NamespaceName, want bool, msg string) {
		t.Helper()
		dec, err := a.Authorize(ctx, auth.Request{Identity: id, Verb: verb, Kind: kind, Namespace: ns})
		require.NoError(t, err)
		require.Equal(t, want, dec.Allowed, "%s (reason=%q)", msg, dec.Reason)
	}

	// admin: cluster-wide + cluster-scoped kinds.
	must(admin, auth.VerbCreate, v1.KindFunction, "team-a", true, "admin creates in team-a")
	must(admin, auth.VerbCreate, v1.KindFunction, "team-b", true, "admin creates in team-b")
	must(admin, auth.VerbCreate, v1.KindNamespace, "", true, "admin creates a cluster-scoped Namespace")

	// developer: read+write in its namespace; denied elsewhere + on cluster-scoped kinds.
	must(dev, auth.VerbCreate, v1.KindFunction, "team-a", true, "developer writes in team-a")
	must(dev, auth.VerbGet, v1.KindFunction, "team-a", true, "developer reads in team-a")
	must(dev, auth.VerbCreate, v1.KindFunction, "team-b", false, "developer denied in team-b")
	must(dev, auth.VerbCreate, v1.KindNamespace, "", false, "developer denied on cluster-scoped kind")

	// viewer: read-only in its namespace.
	must(viewer, auth.VerbGet, v1.KindFunction, "team-a", true, "viewer reads in team-a")
	must(viewer, auth.VerbList, v1.KindFunction, "team-a", true, "viewer lists in team-a")
	must(viewer, auth.VerbCreate, v1.KindFunction, "team-a", false, "viewer denied write")
	must(viewer, auth.VerbDelete, v1.KindFunction, "team-a", false, "viewer denied delete")
	must(viewer, auth.VerbGet, v1.KindSecret, "team-a", false, "viewer denied get Secret")
	must(viewer, auth.VerbList, v1.KindSecret, "team-a", false, "viewer denied list Secret")

	// default-deny: unknown role / unscoped principal.
	must(stranger, auth.VerbGet, v1.KindFunction, "team-a", false, "unknown role denied")
}
