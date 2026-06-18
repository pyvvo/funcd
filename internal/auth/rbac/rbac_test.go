package rbac_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/auth/authcontract"
	"github.com/green-0-rabbit/funcd/internal/auth/rbac"
)

func allowed(t *testing.T, a auth.Authorizer, id auth.Identity, verb auth.Verb, kind v1.Kind, ns v1.NamespaceName) bool {
	t.Helper()
	dec, err := a.Authorize(context.Background(), auth.Request{Identity: id, Verb: verb, Kind: kind, Namespace: ns})
	require.NoError(t, err)
	return dec.Allowed
}

// scenario: authorizer-contract-holds — the built-in driver satisfies the port contract.
func TestScenarioAuthorizerContractHolds(t *testing.T) {
	t.Parallel()
	authcontract.Run(t, rbac.New())
}

// scenario: rbac-viewer-is-read-only.
func TestScenarioRbacViewerIsReadOnly(t *testing.T) {
	t.Parallel()
	a := rbac.New()
	viewer := auth.Identity{Subject: "v", Role: auth.RoleViewer, Namespaces: []v1.NamespaceName{"team-a"}}
	require.True(t, allowed(t, a, viewer, auth.VerbGet, v1.KindFunction, "team-a"), "viewer may read")
	require.True(t, allowed(t, a, viewer, auth.VerbList, v1.KindFunction, "team-a"), "viewer may list")
	require.False(t, allowed(t, a, viewer, auth.VerbCreate, v1.KindFunction, "team-a"), "viewer may not create")
	require.False(t, allowed(t, a, viewer, auth.VerbDelete, v1.KindFunction, "team-a"), "viewer may not delete")
}

// scenario: rbac-denies-out-of-namespace.
func TestScenarioRbacDeniesOutOfNamespace(t *testing.T) {
	t.Parallel()
	a := rbac.New()
	dev := auth.Identity{Subject: "d", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}}
	require.True(t, allowed(t, a, dev, auth.VerbCreate, v1.KindFunction, "team-a"), "developer writes in its namespace")
	require.False(t, allowed(t, a, dev, auth.VerbCreate, v1.KindFunction, "team-b"), "developer denied out of namespace")
}

// scenario: admin-spans-namespaces-and-cluster-kinds.
func TestScenarioAdminSpansNamespacesAndClusterKinds(t *testing.T) {
	t.Parallel()
	a := rbac.New()
	admin := auth.Identity{Subject: "a", Role: auth.RoleAdmin}
	require.True(t, allowed(t, a, admin, auth.VerbCreate, v1.KindFunction, "any-ns"), "admin spans namespaces")
	require.True(t, allowed(t, a, admin, auth.VerbCreate, v1.KindNamespace, ""), "admin may create cluster-scoped Namespace")
	require.True(t, allowed(t, a, admin, auth.VerbDelete, v1.KindWorkerNode, ""), "admin may delete cluster-scoped WorkerNode")

	dev := auth.Identity{Subject: "d", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"any-ns"}}
	require.False(t, allowed(t, a, dev, auth.VerbCreate, v1.KindNamespace, ""), "non-admin denied cluster-scoped kind")
}
