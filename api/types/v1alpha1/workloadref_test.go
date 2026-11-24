package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

// ADR-0351 Decision 3: every owner names its kind and its name, on the wire as {kind, name}, and each field takes only
// its kinds: a table and a Secret a Function or a Job, a database and a prefix a Lake too, at a store's root and where a
// workload declares it.
func TestOwnersNameTheirKind(t *testing.T) {
	t.Parallel()
	meta := ObjectMeta{Name: "x", Namespace: "team", ResourceGroup: "rg1"}
	tm := func(k Kind) TypeMeta { return TypeMeta{APIVersion: k.GVK().APIVersion(), Kind: k} }
	secret := func(o WorkloadRef) error {
		return (&Secret{TypeMeta: tm(KindSecret), ObjectMeta: meta, Spec: SecretSpec{Owner: o}}).Validate()
	}
	db := func(o WorkloadRef) error {
		return (&SQLDatabase{TypeMeta: tm(KindSQLDatabase), ObjectMeta: meta, Spec: SQLDatabaseSpec{Owner: o}}).Validate()
	}
	prefix := func(o WorkloadRef) error {
		return (&Bucket{TypeMeta: tm(KindBucket), ObjectMeta: meta, Spec: BucketSpec{Prefixes: []BucketPrefix{{Name: "p", Owner: o}}}}).Validate()
	}
	kvTable := func(o WorkloadRef) error {
		return (&KVStore{TypeMeta: tm(KindKVStore), ObjectMeta: meta, Spec: KVStoreSpec{Tables: []KVTable{{Name: "t", Owner: o}}}}).Validate()
	}
	declaredTable := func(o WorkloadRef) error {
		return (&Function{TypeMeta: tm(KindFunction), ObjectMeta: meta, Spec: FunctionSpec{Runtime: "nodejs22", Handler: "handle",
			Image: "oci://x/fn:1", KVStores: []KVStoreEntry{{Name: "state", KVStoreSpec: KVStoreSpec{Tables: []KVTable{{Name: "t", Owner: o}}}}}}}).Validate()
	}
	for _, tc := range []struct {
		what  string
		check func(WorkloadRef) error
		ok    []WorkloadRef
		not   []WorkloadRef
	}{
		{"a Secret's owner", secret, []WorkloadRef{FunctionOwner("tofu"), JobOwner("tofu"), {}}, []WorkloadRef{LakeOwner("tofu"), {Kind: KindApp, Name: "tofu"}}},
		{"a database's owner", db, []WorkloadRef{FunctionOwner("tofu"), JobOwner("tofu"), LakeOwner("tofu")}, []WorkloadRef{{Kind: KindSite, Name: "tofu"}, JobOwner("Not_A_Label")}},
		{"a prefix's owner", prefix, []WorkloadRef{FunctionOwner("tofu"), JobOwner("tofu"), LakeOwner("tofu")}, []WorkloadRef{{Kind: KindWorkflow, Name: "tofu"}}},
		{"a table's owner", kvTable, []WorkloadRef{FunctionOwner("tofu"), JobOwner("tofu"), {}},
			[]WorkloadRef{LakeOwner("tofu"), {Kind: KindApp, Name: "tofu"}, FunctionOwner("Not_A_Label")}},
		{"a declared table's owner", declaredTable, []WorkloadRef{FunctionOwner("x"), JobOwner("tofu"), {}}, []WorkloadRef{LakeOwner("tofu")}},
	} {
		for _, o := range tc.ok {
			require.NoError(t, tc.check(o), "%s %v", tc.what, o)
		}
		for _, o := range tc.not {
			err := tc.check(o)
			require.Equal(t, fault.Invalid, fault.KindOf(err), "%s %v: %v", tc.what, o, err)
		}
	}

	var table KVTable
	require.NoError(t, json.Unmarshal([]byte(`{"name":"t","owner":{"kind":"Job","name":"tofu"}}`), &table))
	require.Equal(t, JobOwner("tofu"), table.Owner)
	require.Error(t, json.Unmarshal([]byte(`{"name":"t","owner":"tofu"}`), &table), "a bare name is no owner any more")
	b, err := json.Marshal(KVTable{Name: "t"})
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"t"}`, string(b), "no owner is left out")
}

// ADR-0351 Decision 6 and ADR-0352 Decision 1: an appliesTo item names a Function, a Job or an add-on object by kind and
// name, and reads on the wire as before; another funcd kind, a kind of any case that is no kind name, or an item that
// names nothing, is refused. Which add-on kinds count is the admission's.
func TestAppliesToNamesItsKind(t *testing.T) {
	t.Parallel()
	ep := func(refs ...AppliesToRef) error {
		return (&EgressPolicy{TypeMeta: TypeMeta{APIVersion: KindEgressPolicy.GVK().APIVersion(), Kind: KindEgressPolicy},
			ObjectMeta: ObjectMeta{Name: "egress", Namespace: "team", ResourceGroup: "rg1"},
			Spec:       EgressPolicySpec{AppliesTo: refs, Rules: []EgressRule{{To: EgressTo{Domains: []string{"api.example.com"}}}}}}).Validate()
	}
	require.NoError(t, ep(AppliesToRef{Kind: "Function", Name: "etl"}, AppliesToRef{Kind: "Job", Name: "tofu"},
		AppliesToRef{Kind: "Tunnel", Name: "public"}))
	for _, ref := range []AppliesToRef{{Kind: "Lake", Name: "lake"}, {}, {Kind: "Job", Name: "Tofu"}, {Kind: "FUNCTION", Name: "etl"},
		{Kind: "tunnel", Name: "public"}, {Kind: "Secret", Name: "s"}, {Name: "public"}} {
		err := ep(ref)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%v: %v", ref, err)
		require.True(t, strings.Contains(err.Error(), "spec.appliesTo[0]"), err.Error())
	}

	var spec EgressPolicySpec
	require.NoError(t, json.Unmarshal([]byte(`{"rules":[],"appliesTo":[{"kind":"Job","name":"tofu"},{"kind":"Tunnel","name":"public"}]}`), &spec))
	require.Equal(t, []AppliesToRef{{Kind: "Job", Name: "tofu"}, {Kind: "Tunnel", Name: "public"}}, spec.AppliesTo)
}

// ADR-0351 Decision 5: a RolesAssignment may name a Job as its principal.
func TestRolesAssignmentNamesAJob(t *testing.T) {
	t.Parallel()
	ra := &RolesAssignment{TypeMeta: TypeMeta{APIVersion: KindRolesAssignment.GVK().APIVersion(), Kind: KindRolesAssignment},
		ObjectMeta: ObjectMeta{Name: "tofu-writes", Namespace: "team", ResourceGroup: "rg1"},
		Spec: RolesAssignmentSpec{Assignments: []AssignmentEntry{{Principal: &PrincipalRef{Kind: PrincipalKindJob, Name: "tofu"},
			RoleRef: RoleRef{Kind: RoleRefKindBuiltin, Name: "KV Data Writer"}, Scope: &ScopeRef{Kind: ScopeKindNamespace}}}}}
	require.NoError(t, ra.Validate())
}
