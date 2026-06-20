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

// TestRbacAuthorizationMatrix is the exhaustive policy matrix — the parametrized form of the named
// scenarios above: role × verb × kind-scope × namespace, each row asserting the explicit allow/deny the
// ADR-0018 rules dictate. It closes the gaps the per-scenario tests left: every verb (incl. update),
// all four cluster-scoped kinds against each non-admin role, out-of-namespace for BOTH developer and
// viewer, and the unknown-role default-deny.
func TestRbacAuthorizationMatrix(t *testing.T) {
	t.Parallel()
	a := rbac.New()

	const home, other = v1.NamespaceName("team-a"), v1.NamespaceName("team-b")
	bound := []v1.NamespaceName{home}

	for _, tc := range []struct {
		name string
		role auth.Role
		ns   []v1.NamespaceName
		verb auth.Verb
		kind v1.Kind
		tgt  v1.NamespaceName
		want bool
	}{
		// admin — every verb, any namespace, and every cluster-scoped kind.
		{"admin/get/ns", auth.RoleAdmin, nil, auth.VerbGet, v1.KindFunction, home, true},
		{"admin/list/ns", auth.RoleAdmin, nil, auth.VerbList, v1.KindFunction, home, true},
		{"admin/create/ns", auth.RoleAdmin, nil, auth.VerbCreate, v1.KindFunction, home, true},
		{"admin/update/ns", auth.RoleAdmin, nil, auth.VerbUpdate, v1.KindFunction, home, true},
		{"admin/delete/ns", auth.RoleAdmin, nil, auth.VerbDelete, v1.KindFunction, home, true},
		{"admin/cluster/namespace", auth.RoleAdmin, nil, auth.VerbCreate, v1.KindNamespace, "", true},
		{"admin/cluster/runtimeclass", auth.RoleAdmin, nil, auth.VerbCreate, v1.KindRuntimeClass, "", true},
		{"admin/cluster/workernode", auth.RoleAdmin, nil, auth.VerbDelete, v1.KindWorkerNode, "", true},
		{"admin/cluster/gateway", auth.RoleAdmin, nil, auth.VerbUpdate, v1.KindGateway, "", true},

		// developer — read+write in a bound namespace (ALL verbs), denied out-of-ns and on cluster kinds.
		{"dev/get/in-ns", auth.RoleDeveloper, bound, auth.VerbGet, v1.KindFunction, home, true},
		{"dev/list/in-ns", auth.RoleDeveloper, bound, auth.VerbList, v1.KindFunction, home, true},
		{"dev/create/in-ns", auth.RoleDeveloper, bound, auth.VerbCreate, v1.KindFunction, home, true},
		{"dev/update/in-ns", auth.RoleDeveloper, bound, auth.VerbUpdate, v1.KindFunction, home, true},
		{"dev/delete/in-ns", auth.RoleDeveloper, bound, auth.VerbDelete, v1.KindFunction, home, true},
		{"dev/get/out-of-ns", auth.RoleDeveloper, bound, auth.VerbGet, v1.KindFunction, other, false},
		{"dev/create/out-of-ns", auth.RoleDeveloper, bound, auth.VerbCreate, v1.KindFunction, other, false},
		{"dev/cluster/namespace", auth.RoleDeveloper, bound, auth.VerbCreate, v1.KindNamespace, "", false},
		{"dev/cluster/runtimeclass", auth.RoleDeveloper, bound, auth.VerbGet, v1.KindRuntimeClass, "", false},
		{"dev/cluster/workernode", auth.RoleDeveloper, bound, auth.VerbCreate, v1.KindWorkerNode, "", false},
		{"dev/cluster/gateway", auth.RoleDeveloper, bound, auth.VerbUpdate, v1.KindGateway, "", false},

		// viewer — read-only in a bound namespace; denied writes, out-of-ns, and cluster kinds.
		{"viewer/get/in-ns", auth.RoleViewer, bound, auth.VerbGet, v1.KindFunction, home, true},
		{"viewer/list/in-ns", auth.RoleViewer, bound, auth.VerbList, v1.KindFunction, home, true},
		{"viewer/create/in-ns", auth.RoleViewer, bound, auth.VerbCreate, v1.KindFunction, home, false},
		{"viewer/update/in-ns", auth.RoleViewer, bound, auth.VerbUpdate, v1.KindFunction, home, false},
		{"viewer/delete/in-ns", auth.RoleViewer, bound, auth.VerbDelete, v1.KindFunction, home, false},
		{"viewer/get/out-of-ns", auth.RoleViewer, bound, auth.VerbGet, v1.KindFunction, other, false},
		{"viewer/cluster/namespace", auth.RoleViewer, bound, auth.VerbGet, v1.KindNamespace, "", false},
		{"viewer/cluster/runtimeclass", auth.RoleViewer, bound, auth.VerbGet, v1.KindRuntimeClass, "", false},
		{"viewer/cluster/workernode", auth.RoleViewer, bound, auth.VerbGet, v1.KindWorkerNode, "", false},
		{"viewer/cluster/gateway", auth.RoleViewer, bound, auth.VerbGet, v1.KindGateway, "", false},

		// unknown role — default-deny everything.
		{"unknown/get/ns", auth.Role("robot"), bound, auth.VerbGet, v1.KindFunction, home, false},
		{"unknown/create/ns", auth.Role("robot"), bound, auth.VerbCreate, v1.KindFunction, home, false},
		{"unknown/cluster", auth.Role("robot"), bound, auth.VerbGet, v1.KindNamespace, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := auth.Identity{Subject: "s", Role: tc.role, Namespaces: tc.ns}
			require.Equal(t, tc.want, allowed(t, a, id, tc.verb, tc.kind, tc.tgt),
				"role=%s verb=%s kind=%s ns=%q", tc.role, tc.verb, tc.kind, tc.tgt)
		})
	}
}
