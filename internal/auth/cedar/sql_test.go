package cedar_test

import (
	"context"
	"testing"

	cedartypes "github.com/cedar-policy/cedar-go/types"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/cedar"
)

// sqlMeta holds Functions and SQLDatabases of "default".
type sqlMeta struct {
	fns map[v1.ObjectName]*v1.Function
	dbs map[v1.ObjectName]*v1.SQLDatabase
}

func (m sqlMeta) Get(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if ns == "default" {
		switch gvk.Kind {
		case v1.KindFunction:
			if f, ok := m.fns[name]; ok {
				return f, nil
			}
		case v1.KindSQLDatabase:
			if d, ok := m.dbs[name]; ok {
				return d, nil
			}
		}
	}
	return nil, fault.NotFoundf("sqlMeta.Get", "%s %s/%s not found", gvk.Kind, ns, name)
}

// writerList grants SQL writes to the listed Functions of default/todo-db.
type writerList []v1.ObjectName

func (writerList) BlobWriters(context.Context, v1.NamespaceName, string, string) []cedartypes.EntityUID {
	return nil
}
func (writerList) KVWriters(context.Context, v1.NamespaceName, string, string) []cedartypes.EntityUID {
	return nil
}
func (w writerList) SQLWriters(_ context.Context, ns v1.NamespaceName, db string) []cedartypes.EntityUID {
	if db != "todo-db" {
		return nil
	}
	out := make([]cedartypes.EntityUID, 0, len(w))
	for _, fn := range w {
		out = append(out, cedartypes.NewEntityUID("Function", cedartypes.String(string(ns)+"/"+string(fn))))
	}
	return out
}

func sqlFn(name v1.ObjectName, dbs ...v1.ObjectName) *v1.Function {
	f := &v1.Function{ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}}
	for _, d := range dbs {
		f.Spec.SQL = append(f.Spec.SQL, v1.FunctionSQL{Alias: "app", Database: d})
	}
	return f
}

func sqlDriver(t *testing.T, writers writerList, policies ...v1.Policy) auth.Authorizer {
	t.Helper()
	m := sqlMeta{
		fns: map[v1.ObjectName]*v1.Function{
			"todo-api":     sqlFn("todo-api", "todo-db"),
			"todo-report":  sqlFn("todo-report", "todo-db"),
			"todo-migrate": sqlFn("todo-migrate", "todo-db"),
			"stranger":     sqlFn("stranger"),
		},
		dbs: map[v1.ObjectName]*v1.SQLDatabase{"todo-db": {
			ObjectMeta: v1.ObjectMeta{Name: "todo-db", Namespace: "default", ResourceGroup: "rg1"},
			Spec:       v1.SQLDatabaseSpec{Owner: "todo-api"},
		}},
	}
	reg, err := cedar.NewRegistry([]cedar.Capability{cedar.SQLCapabilityWithWriters(writers)}, []cedar.PrincipalSource{cedar.FunctionPrincipalSource()})
	require.NoError(t, err)
	ep, err := reg.EntityProvider(m)
	require.NoError(t, err)
	d, err := cedar.New(cedar.Deps{Entities: ep, Policies: fixedPolicies{policies: policies, rev: "1"}, Builtins: reg.Builtins()})
	require.NoError(t, err)
	return d
}

func sqlAllowed(t *testing.T, a auth.Authorizer, fn v1.ObjectName, action auth.Action, db v1.ObjectName) bool {
	t.Helper()
	return authorize(t, a, fnPrincipal("default", fn), action, &auth.EntityRef{Type: v1.KindSQLDatabase, Namespace: "default", Name: db}).Allowed
}

// The built-ins (ADR-0233 Decision 5): a binding grants sql::read, sql::write needs the owner or a writer grant.
func TestSQLBuiltins(t *testing.T) {
	a := sqlDriver(t, writerList{"todo-migrate"})
	require.True(t, sqlAllowed(t, a, "todo-report", auth.ActionSQLRead, "todo-db"), "a binding grants read")
	require.False(t, sqlAllowed(t, a, "stranger", auth.ActionSQLRead, "todo-db"), "no binding, no read")
	require.True(t, sqlAllowed(t, a, "todo-api", auth.ActionSQLWrite, "todo-db"), "the owner writes")
	require.True(t, sqlAllowed(t, a, "todo-migrate", auth.ActionSQLWrite, "todo-db"), "a writer grant writes")
	require.False(t, sqlAllowed(t, a, "todo-report", auth.ActionSQLWrite, "todo-db"), "a binding never grants write")
	require.False(t, sqlAllowed(t, a, "todo-api", auth.ActionSQLWrite, "gone-db"), "a missing database has no writers")
}

// An operator Policy naming sql::write on a SQLDatabase validates and revokes the owner's write.
func TestSQLPolicyRevokes(t *testing.T) {
	text := `forbid(principal, action == Action::"sql::write", resource == SQLDatabase::"default/todo-db");`
	require.NoError(t, cedar.ValidateCedarInNamespace("default", text))
	require.Error(t, cedar.ValidateCedarInNamespace("team", text), "a SQLDatabase is namespaced")
	a := sqlDriver(t, nil, v1.Policy{ObjectMeta: v1.ObjectMeta{Name: "no-writes", Namespace: "default"}, Spec: v1.PolicySpec{Cedar: text}})
	require.False(t, sqlAllowed(t, a, "todo-api", auth.ActionSQLWrite, "todo-db"))
	require.True(t, sqlAllowed(t, a, "todo-api", auth.ActionSQLRead, "todo-db"))
}

// The SQL roles (ADR-0233 Decision 6): a SQL Data Reader grant on a database or the namespace compiles to a read
// permit; SQL Data Writer is a writer grant; Reader, Contributor and Owner grant no SQL.
func TestSQLRoles(t *testing.T) {
	ctx := context.Background()
	roles := cedar.NewRoleResolver(sqlMeta{})
	ra := func(role string, scope v1.ScopeRef) *v1.RolesAssignment {
		return &v1.RolesAssignment{
			ObjectMeta: v1.ObjectMeta{Name: "grant", Namespace: "default", ResourceGroup: "rg1"},
			Spec: v1.RolesAssignmentSpec{
				Principal:   &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: "stranger"},
				Assignments: []v1.AssignmentEntry{{RoleRef: v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: role}, Scope: &scope}},
			},
		}
	}
	db := v1.ScopeRef{Kind: v1.ScopeKindSQLDatabase, Name: "todo-db"}
	nsScope := v1.ScopeRef{Kind: v1.ScopeKindNamespace}
	for _, scope := range []v1.ScopeRef{db, nsScope} {
		pols, err := cedar.CompileRolesAssignment(ctx, ra("SQL Data Reader", scope), roles)
		require.NoError(t, err)
		require.Len(t, pols, 1)
		a := sqlDriver(t, nil, pols...)
		require.True(t, sqlAllowed(t, a, "stranger", auth.ActionSQLRead, "todo-db"), "%s scope", scope.Kind)
	}
	other, err := cedar.CompileRolesAssignment(ctx, ra("SQL Data Reader", db), roles)
	require.NoError(t, err)
	require.False(t, sqlAllowed(t, sqlDriver(t, nil, other...), "stranger", auth.ActionSQLRead, "audit-db"), "the grant stays on its database")

	grants := cedar.WriterGrants(ctx, ra("SQL Data Writer", db), roles)
	require.Len(t, grants, 1)
	require.Equal(t, []string{"sql::write"}, grants[0].Writes)
	for _, role := range []string{"Reader", "Contributor", "Owner"} {
		for _, g := range cedar.WriterGrants(ctx, ra(role, nsScope), roles) {
			require.NotContains(t, g.Writes, "sql::write", "%s grants no SQL write", role)
		}
		pols, err := cedar.CompileRolesAssignment(ctx, ra(role, nsScope), roles)
		require.NoError(t, err)
		require.False(t, sqlAllowed(t, sqlDriver(t, nil, pols...), "stranger", auth.ActionSQLRead, "todo-db"), "%s grants no SQL read", role)
	}
}
