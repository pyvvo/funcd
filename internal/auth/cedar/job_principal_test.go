package cedar_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/cedar"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// jobAndNamesake stores, in namespace team, a Job tofu and a Function tofu that bind the same table, prefix, Secret
// and database, one of each owned by the Job and one by the Function.
func jobAndNamesake(t *testing.T) store.Store {
	t.Helper()
	st := store.New(memory.New())
	bindings := v1.FunctionBindings{
		KV:     []v1.FunctionKV{{Alias: "jobs", Store: "state", Table: "jobs"}, {Alias: "fns", Store: "state", Table: "fns"}},
		Blob:   []v1.FunctionBlob{{Alias: "tf", Bucket: "tf", Prefix: "state"}, {Alias: "fn", Bucket: "tf", Prefix: "fn"}},
		Secret: []v1.ObjectName{"creds", "fn-creds"},
		SQL:    []v1.FunctionSQL{{Alias: "db", Database: "db"}},
	}
	meta := func(name v1.ObjectName) v1.ObjectMeta {
		return v1.ObjectMeta{Name: name, Namespace: "team", ResourceGroup: "rg1"}
	}
	tm := func(k v1.Kind) v1.TypeMeta { return v1.TypeMeta{APIVersion: k.GVK().APIVersion(), Kind: k} }
	for _, o := range []v1.Object{
		&v1.Job{TypeMeta: tm(v1.KindJob), ObjectMeta: meta("tofu"), Spec: v1.JobSpec{Runtime: "nodejs22", Handler: "handle", Image: "oci://x/tofu:1", Bindings: bindings}},
		&v1.Function{TypeMeta: tm(v1.KindFunction), ObjectMeta: meta("tofu"), Spec: v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: "oci://x/fn:1", Bindings: bindings}},
		&v1.KVStore{TypeMeta: tm(v1.KindKVStore), ObjectMeta: meta("state"), Spec: v1.KVStoreSpec{Tables: []v1.KVTable{
			{Name: "jobs", Owner: v1.JobOwner("tofu")}, {Name: "fns", Owner: v1.FunctionOwner("tofu")}}}},
		&v1.Bucket{TypeMeta: tm(v1.KindBucket), ObjectMeta: meta("tf"), Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{
			{Name: "state", Owner: v1.JobOwner("tofu")}, {Name: "fn", Owner: v1.FunctionOwner("tofu")}}}},
		&v1.Secret{TypeMeta: tm(v1.KindSecret), ObjectMeta: meta("creds"), Spec: v1.SecretSpec{Owner: v1.JobOwner("tofu")}},
		&v1.Secret{TypeMeta: tm(v1.KindSecret), ObjectMeta: meta("fn-creds"), Spec: v1.SecretSpec{Owner: v1.FunctionOwner("tofu")}},
		&v1.SQLDatabase{TypeMeta: tm(v1.KindSQLDatabase), ObjectMeta: meta("db"), Spec: v1.SQLDatabaseSpec{Owner: v1.JobOwner("tofu")}},
	} {
		_, err := st.Create(context.Background(), o)
		require.NoError(t, err, "seed %s %s", o.GroupVersionKind().Kind, o.GetName())
	}
	return st
}

func jobDriver(t *testing.T, st store.Store, policies ...v1.Policy) auth.Authorizer {
	t.Helper()
	ep, err := cedar.NewEntityProvider(st)
	require.NoError(t, err)
	d, err := cedar.New(cedar.Deps{Entities: ep, Policies: fixedPolicies{policies: policies, rev: "1"}})
	require.NoError(t, err)
	return d
}

func allowed(t *testing.T, a auth.Authorizer, kind v1.Kind, action auth.Action, resource *auth.EntityRef) bool {
	t.Helper()
	return authorize(t, a, &auth.EntityRef{Type: kind, Namespace: "team", Name: "tofu"}, action, resource).Allowed
}

// ADR-0351 Decisions 1 and 3: a Job is its own principal, its bindings materialized as a Function's, and an owner that
// names it grants the Job alone the write: never the Function of its name, and the Job never writes what that Function
// owns.
func TestJobIsItsOwnPrincipal(t *testing.T) {
	t.Parallel()
	a := jobDriver(t, jobAndNamesake(t))
	kv := func(table string) *auth.EntityRef {
		return &auth.EntityRef{Type: v1.KindKVStore, Namespace: "team", Name: "state", Path: table}
	}
	prefix := func(p string) *auth.EntityRef {
		return &auth.EntityRef{Type: v1.KindBucket, Namespace: "team", Name: "tf", Path: p}
	}
	secret := func(name v1.ObjectName) *auth.EntityRef {
		return &auth.EntityRef{Type: v1.KindSecret, Namespace: "team", Name: name}
	}
	db := &auth.EntityRef{Type: v1.KindSQLDatabase, Namespace: "team", Name: "db"}
	for _, tc := range []struct {
		what     string
		action   auth.Action
		resource *auth.EntityRef
		job, fn  bool
	}{
		{"a table the Job owns", auth.ActionKVWrite, kv("jobs"), true, false},
		{"a table the Function owns", auth.ActionKVWrite, kv("fns"), false, true},
		{"a bound table", auth.ActionKVRead, kv("jobs"), true, true},
		{"a prefix the Job owns", auth.ActionS3Write, prefix("state"), true, false},
		{"a prefix the Function owns", auth.ActionS3Write, prefix("fn"), false, true},
		{"a Secret the Job owns", auth.ActionSecretWrite, secret("creds"), true, false},
		{"a Secret the Function owns", auth.ActionSecretWrite, secret("fn-creds"), false, true},
		{"a bound Secret", auth.ActionSecretRead, secret("creds"), true, true},
		{"a database the Job owns", auth.ActionSQLWrite, db, true, false},
	} {
		require.Equal(t, tc.job, allowed(t, a, v1.KindJob, tc.action, tc.resource), "the Job, %s: %s", tc.action, tc.what)
		require.Equal(t, tc.fn, allowed(t, a, v1.KindFunction, tc.action, tc.resource), "the Function, %s: %s", tc.action, tc.what)
	}
}

// ADR-0351 Decision 1: a namespace Policy names a Job as Job::"<ns>/<name>": a forbid of the Job's secret::read leaves
// the Function of its name reading.
func TestPolicyNamesAJob(t *testing.T) {
	t.Parallel()
	text := `forbid(principal == Job::"team/tofu", action == Action::"secret::read", resource);`
	require.NoError(t, cedar.ValidateCedarInNamespace("team", text), "a Policy may name a Job")
	pol := v1.Policy{ObjectMeta: v1.ObjectMeta{Name: "no-tofu-reads", Namespace: "team", ResourceGroup: "rg1"}, Spec: v1.PolicySpec{Cedar: text}}
	a := jobDriver(t, jobAndNamesake(t), pol)
	creds := &auth.EntityRef{Type: v1.KindSecret, Namespace: "team", Name: "creds"}
	require.False(t, allowed(t, a, v1.KindJob, auth.ActionSecretRead, creds))
	require.True(t, allowed(t, a, v1.KindFunction, auth.ActionSecretRead, creds))
}

// ADR-0351 Decision 6: an appliesTo item names a Function or a Job by kind and name, and its rules apply to that one
// alone; an empty appliesTo applies to every Function of the namespace and to no Job.
func TestEgressAppliesToKinds(t *testing.T) {
	t.Parallel()
	rules := []v1.EgressRule{{To: v1.EgressTo{Domains: []string{"api.example.com"}}, Ports: []int{443}}}
	ep := func(appliesTo ...v1.AppliesToRef) *v1.EgressPolicy {
		return &v1.EgressPolicy{ObjectMeta: v1.ObjectMeta{Name: "egress", Namespace: "team", ResourceGroup: "rg1"},
			Spec: v1.EgressPolicySpec{AppliesTo: appliesTo, Rules: rules}}
	}
	job := auth.EntityRef{Type: v1.KindJob, Namespace: "team", Name: "tofu"}
	fn := auth.EntityRef{Type: v1.KindFunction, Namespace: "team", Name: "tofu"}

	named := ep(v1.AppliesToRef{Kind: "Job", Name: "tofu"})
	pols, err := cedar.CompileEgressPolicy("team", named, nil)
	require.NoError(t, err)
	require.Len(t, pols, 1)
	require.Contains(t, pols[0].Spec.Cedar, `principal == Job::"team/tofu"`)
	require.NotContains(t, pols[0].Spec.Cedar, `Function::"team/tofu"`)
	require.Equal(t, []string{"api.example.com"}, cedar.EgressDomains(named, job, ""))
	require.Empty(t, cedar.EgressDomains(named, fn, ""), "the Job's policy is not its namesake's")

	whole := ep()
	pols, err = cedar.CompileEgressPolicy("team", whole, nil)
	require.NoError(t, err)
	require.True(t, strings.Contains(pols[0].Spec.Cedar, "principal is Function"), pols[0].Spec.Cedar)
	require.Empty(t, cedar.EgressDomains(whole, job, ""), "an empty appliesTo reaches no Job")
	require.Equal(t, []string{"api.example.com"}, cedar.EgressDomains(whole, fn, ""))
}
