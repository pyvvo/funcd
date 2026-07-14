package roles

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/auth/cedar"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
)

// polSource is a PolicySource that compiles the store's RolesAssignments (the read/query/invoke permits).
type polSource struct{ s store.Store }

func (p polSource) Policies(ctx context.Context) ([]v1.Policy, string, error) {
	return CompilePolicies(ctx, p.s)
}

func buildPDP(t *testing.T, st store.Store) auth.Authorizer {
	t.Helper()
	reg, err := cedar.NewRegistry(
		[]cedar.Capability{cedar.S3CapabilityWithWriters(NewLister(st)), cedar.KVCapabilityWithWriters(NewLister(st))},
		[]cedar.PrincipalSource{cedar.FunctionPrincipalSource(), cedar.CatalogServicePrincipalSource()},
	)
	require.NoError(t, err)
	ep, err := reg.EntityProvider(st)
	require.NoError(t, err)
	pdp, err := cedar.New(cedar.Deps{Entities: ep, Policies: polSource{st}, Builtins: reg.Builtins()})
	require.NoError(t, err)
	return pdp
}

func create(t *testing.T, st store.Store, obj v1.Object) {
	t.Helper()
	_, err := st.Create(context.Background(), obj)
	require.NoError(t, err)
}

func seedBucket(t *testing.T, st store.Store) {
	t.Helper()
	create(t, st, &v1.Bucket{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket},
		ObjectMeta: v1.ObjectMeta{Name: "releves", Namespace: "data", ResourceGroup: "rg1"},
		Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{
			{Name: "landing"},                   // no owner (the external drop zone)
			{Name: "silver", Owner: "producer"}, // owned by a Function (back-compat)
			{Name: "gold"},                      // no owner
		}},
	})
}

func assign(t *testing.T, st store.Store, name string, spec v1.RolesAssignmentSpec) {
	t.Helper()
	create(t, st, &v1.RolesAssignment{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindRolesAssignment.GVK().APIVersion(), Kind: v1.KindRolesAssignment},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "data", ResourceGroup: "rg1"},
		Spec:       spec,
	})
}

func write(t *testing.T, pdp auth.Authorizer, kind v1.PrincipalKind, name, prefix string) bool {
	return canDo(t, pdp, kind, name, auth.ActionS3Write, prefix)
}
func read(t *testing.T, pdp auth.Authorizer, kind v1.PrincipalKind, name, prefix string) bool {
	return canDo(t, pdp, kind, name, auth.ActionS3Read, prefix)
}
func canDo(t *testing.T, pdp auth.Authorizer, kind v1.PrincipalKind, name string, action auth.Action, prefix string) bool {
	t.Helper()
	pk := v1.KindIdentity
	if kind == v1.PrincipalKindFunction {
		pk = v1.KindFunction
	}
	principal := auth.EntityRef{Type: pk, Namespace: "data", Name: v1.ObjectName(name)}
	res := auth.EntityRef{Type: v1.KindBucket, Namespace: "data", Name: "releves", Path: prefix}
	d, err := pdp.Authorize(context.Background(), auth.Request{Action: action, Resource: &res, Identity: auth.Identity{Principal: &principal}})
	require.NoError(t, err)
	return d.Allowed
}

func writerAt(scopeKind v1.ScopeKind, scopeName string) v1.RolesAssignmentSpec {
	return v1.RolesAssignmentSpec{
		Principal:   &v1.PrincipalRef{Kind: v1.PrincipalKindIdentity, Name: "dropper"},
		Assignments: []v1.AssignmentEntry{{RoleRef: v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: "Blob Data Writer"}, Scope: &v1.ScopeRef{Kind: scopeKind, Name: scopeName}}},
	}
}

// scenario: external-identity-granted-write + unassigned-write-denied + owner-still-writes.
func TestScenarioExternalIdentityGrantedWrite(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedBucket(t, st)
	assign(t, st, "dropper-writes-landing", writerAt(v1.ScopeKindBucketPrefix, "releves/landing"))
	pdp := buildPDP(t, st)

	require.True(t, write(t, pdp, v1.PrincipalKindIdentity, "dropper", "landing"),
		"an external Identity granted Blob Data Writer @ landing CAN write it (no dev relaxation)")
	require.False(t, write(t, pdp, v1.PrincipalKindIdentity, "intruder", "landing"),
		"an unassigned Identity is DENIED write (the forbid fires)")
	require.False(t, write(t, pdp, v1.PrincipalKindIdentity, "dropper", "gold"),
		"the writer grant does not leak past its scope (gold is not landing)")
	require.True(t, write(t, pdp, v1.PrincipalKindFunction, "producer", "silver"),
		"a legacy owner Function still writes its owned prefix — back-compat")
}

// scenario: role-grants-read + scope-bounds-the-grant.
func TestScenarioRoleGrantsReadScoped(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedBucket(t, st)
	// Blob Data Reader scoped to releves/silver only.
	assign(t, st, "reader-silver", v1.RolesAssignmentSpec{
		Principal:   &v1.PrincipalRef{Kind: v1.PrincipalKindIdentity, Name: "dash"},
		Assignments: []v1.AssignmentEntry{{RoleRef: v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: "Blob Data Reader"}, Scope: &v1.ScopeRef{Kind: v1.ScopeKindBucketPrefix, Name: "releves/silver"}}},
	})
	pdp := buildPDP(t, st)

	require.True(t, read(t, pdp, v1.PrincipalKindIdentity, "dash", "silver"), "the read grant permits its scope")
	require.False(t, read(t, pdp, v1.PrincipalKindIdentity, "dash", "gold"), "the read grant does not leak past its scope")
	require.False(t, write(t, pdp, v1.PrincipalKindIdentity, "dash", "silver"), "a reader cannot write")
}

// scenario: namespace-scoped writer + assignment-references-missing-role (fail-closed).
func TestScenarioNamespaceScopeAndMissingRole(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedBucket(t, st)
	assign(t, st, "dropper-ns", writerAt(v1.ScopeKindNamespace, ""))
	// A RolesAssignment naming a non-existent custom Role grants nothing.
	assign(t, st, "bogus", v1.RolesAssignmentSpec{
		Principal:   &v1.PrincipalRef{Kind: v1.PrincipalKindIdentity, Name: "ghost"},
		Assignments: []v1.AssignmentEntry{{RoleRef: v1.RoleRef{Kind: v1.RoleRefKindRole, Name: "does-not-exist"}, Scope: &v1.ScopeRef{Kind: v1.ScopeKindNamespace}}},
	})
	pdp := buildPDP(t, st)

	require.True(t, write(t, pdp, v1.PrincipalKindIdentity, "dropper", "landing"), "a namespace-scoped writer covers any prefix in the namespace")
	require.True(t, write(t, pdp, v1.PrincipalKindIdentity, "dropper", "gold"), "namespace scope covers gold too")
	require.False(t, write(t, pdp, v1.PrincipalKindIdentity, "ghost", "landing"), "an assignment naming a missing role grants nothing (fail-closed)")
}

func seedKV(t *testing.T, st store.Store) {
	t.Helper()
	create(t, st, &v1.KVStore{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore},
		ObjectMeta: v1.ObjectMeta{Name: "counters", Namespace: "data", ResourceGroup: "rg1"},
		Spec:       v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "landing"}, {Name: "owned", Owner: "producer"}}},
	})
}

func kvWrite(t *testing.T, pdp auth.Authorizer, kind v1.PrincipalKind, name, kvStore, table string) bool {
	t.Helper()
	pk := v1.KindIdentity
	if kind == v1.PrincipalKindFunction {
		pk = v1.KindFunction
	}
	principal := auth.EntityRef{Type: pk, Namespace: "data", Name: v1.ObjectName(name)}
	res := auth.EntityRef{Type: v1.KindKVStore, Namespace: "data", Name: v1.ObjectName(kvStore), Path: table}
	d, err := pdp.Authorize(context.Background(), auth.Request{Action: auth.ActionKVWrite, Resource: &res, Identity: auth.Identity{Principal: &principal}})
	require.NoError(t, err)
	return d.Allowed
}

// scenario: kv write path (symmetric to s3) — a KV Data Writer role grants an external Identity write on
// its scope; an unassigned principal is denied; a legacy table owner still writes (back-compat).
func TestScenarioKVWritePath(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedKV(t, st)
	assign(t, st, "dropper-kv", v1.RolesAssignmentSpec{
		Principal:   &v1.PrincipalRef{Kind: v1.PrincipalKindIdentity, Name: "dropper"},
		Assignments: []v1.AssignmentEntry{{RoleRef: v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: "KV Data Writer"}, Scope: &v1.ScopeRef{Kind: v1.ScopeKindKVStore, Name: "counters"}}},
	})
	pdp := buildPDP(t, st)

	require.True(t, kvWrite(t, pdp, v1.PrincipalKindIdentity, "dropper", "counters", "landing"),
		"KV Data Writer @ counters grants an external Identity write")
	require.False(t, kvWrite(t, pdp, v1.PrincipalKindIdentity, "intruder", "counters", "landing"),
		"an unassigned Identity is denied kv write")
	require.True(t, kvWrite(t, pdp, v1.PrincipalKindFunction, "producer", "counters", "owned"),
		"a legacy table owner still writes — back-compat")
}
