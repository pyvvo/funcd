package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

func TestSQLDatabaseValidate(t *testing.T) {
	db := func(cpu, memory string) *v1.SQLDatabase {
		d := &v1.SQLDatabase{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSQLDatabase.GVK().APIVersion(), Kind: v1.KindSQLDatabase}}
		d.Name, d.Namespace, d.ResourceGroup = "todo-db", "default", "rg1"
		d.Spec.Resources = v1.ResourceSpec{CPU: cpu, Memory: memory}
		return d
	}
	for _, ok := range [][2]string{{"", ""}, {"0.5", "256Mi"}, {"2", "1Gi"}, {"", "1048576"}} {
		require.NoError(t, db(ok[0], ok[1]).Validate(), "%v", ok)
	}
	for _, bad := range [][2]string{{"", "256MB"}, {"", "lots"}, {"-1", ""}, {"0", ""}, {"half", ""}} {
		require.Equal(t, fault.Invalid, fault.KindOf(db(bad[0], bad[1]).Validate()), "%v", bad)
	}
}

func TestSQLBindingValidate(t *testing.T) {
	fn := func(sql ...v1.FunctionSQL) error {
		f := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
		f.Name, f.Namespace, f.ResourceGroup = "todo-api", "default", "rg1"
		f.Spec = v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: "oci-layout://x:1", SQL: sql}
		return f.Validate()
	}
	require.NoError(t, fn(v1.FunctionSQL{Alias: "app", Database: "todo-db"}, v1.FunctionSQL{Alias: "audit", Database: "todo-db"}))
	for _, bad := range [][]v1.FunctionSQL{
		{{Alias: "App", Database: "todo-db"}},
		{{Alias: "app", Database: "Todo_DB"}},
		{{Alias: "app", Database: "a"}, {Alias: "app", Database: "b"}},
	} {
		require.Equal(t, fault.Invalid, fault.KindOf(fn(bad...)), "%v", bad)
	}

	d := &v1.SQLDatabase{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSQLDatabase.GVK().APIVersion(), Kind: v1.KindSQLDatabase}}
	d.Name, d.Namespace, d.ResourceGroup = "todo-db", "default", "rg1"
	d.Spec.Owner = "todo-api"
	require.NoError(t, d.Validate())
	d.Spec.Owner = "Todo API"
	require.Equal(t, fault.Invalid, fault.KindOf(d.Validate()))

	ra := &v1.RolesAssignment{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRolesAssignment.GVK().APIVersion(), Kind: v1.KindRolesAssignment}}
	ra.Name, ra.Namespace, ra.ResourceGroup = "writes", "default", "rg1"
	ra.Spec = v1.RolesAssignmentSpec{
		Principal:   &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: "todo-migrate"},
		Assignments: []v1.AssignmentEntry{{RoleRef: v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: "SQL Data Writer"}, Scope: &v1.ScopeRef{Kind: v1.ScopeKindSQLDatabase, Name: "todo-db"}}},
	}
	require.NoError(t, ra.Validate())
}
