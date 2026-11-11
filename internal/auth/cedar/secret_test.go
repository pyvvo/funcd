package cedar_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/cedar"
)

// secretMeta holds the Functions, Lakes and custom Roles of "default".
type secretMeta struct {
	fns   map[v1.ObjectName]*v1.Function
	lakes map[v1.ObjectName]*v1.Lake
	roles map[v1.ObjectName]*v1.Role
}

func (m secretMeta) Get(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if ns == "default" {
		switch gvk.Kind {
		case v1.KindFunction:
			if f, ok := m.fns[name]; ok {
				return f, nil
			}
		case v1.KindLake:
			if l, ok := m.lakes[name]; ok {
				return l, nil
			}
		case v1.KindRole:
			if r, ok := m.roles[name]; ok {
				return r, nil
			}
		}
	}
	return nil, fault.NotFoundf("secretMeta.Get", "%s %s/%s not found", gvk.Kind, ns, name)
}

func secretFn(name v1.ObjectName, secrets ...v1.ObjectName) *v1.Function {
	f := &v1.Function{ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}}
	f.Spec.Bindings.Secret = secrets
	return f
}

// newSecretMeta binds api-keys to checkout and reports, nothing to stranger, and quack to Lake lake.
func newSecretMeta() secretMeta {
	lake := &v1.Lake{ObjectMeta: v1.ObjectMeta{Name: "lake", Namespace: "default", ResourceGroup: "rg1"}}
	lake.Spec.Bindings.Secret = []v1.ObjectName{"quack"}
	return secretMeta{
		fns: map[v1.ObjectName]*v1.Function{
			"checkout": secretFn("checkout", "api-keys"),
			"reports":  secretFn("reports", "api-keys"),
			"stranger": secretFn("stranger"),
		},
		lakes: map[v1.ObjectName]*v1.Lake{"lake": lake},
		roles: map[v1.ObjectName]*v1.Role{"secret-reader": {
			ObjectMeta: v1.ObjectMeta{Name: "secret-reader", Namespace: "default"},
			Spec:       v1.RoleSpec{Actions: []string{string(auth.ActionSecretRead)}},
		}},
	}
}

// secretDriver is the PDP over the secret capability, with kv beside it as in the root registry.
func secretDriver(t *testing.T, m secretMeta, policies ...v1.Policy) auth.Authorizer {
	t.Helper()
	reg, err := cedar.NewRegistry([]cedar.Capability{cedar.KVCapability(), cedar.SecretCapability()},
		[]cedar.PrincipalSource{cedar.FunctionPrincipalSource(), cedar.LakePrincipalSource()})
	require.NoError(t, err)
	ep, err := reg.EntityProvider(m)
	require.NoError(t, err)
	d, err := cedar.New(cedar.Deps{Entities: ep, Policies: fixedPolicies{policies: policies, rev: "1"}, Builtins: reg.Builtins()})
	require.NoError(t, err)
	return d
}

func secretRead(t *testing.T, a auth.Authorizer, principal *auth.EntityRef, secret v1.ObjectName) auth.Decision {
	t.Helper()
	return authorize(t, a, principal, auth.ActionSecretRead, &auth.EntityRef{Type: v1.KindSecret, Namespace: "default", Name: secret})
}

func lakeRef(name v1.ObjectName) *auth.EntityRef {
	return &auth.EntityRef{Type: v1.KindLake, Namespace: "default", Name: name}
}

func namespacePolicy(name v1.ObjectName, text string) v1.Policy {
	return v1.Policy{ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default"}, Spec: v1.PolicySpec{Cedar: text}}
}

// The built-in permit (ADR-0321 Decision 2): a Function or a Lake reads a Secret it binds, and no other.
func TestSecretCapability_BindingIsTheGrant(t *testing.T) {
	t.Parallel()
	a := secretDriver(t, newSecretMeta())
	require.True(t, secretRead(t, a, fnPrincipal("default", "checkout"), "api-keys").Allowed, "a bound Secret")
	d := secretRead(t, a, fnPrincipal("default", "stranger"), "api-keys")
	require.False(t, d.Allowed, "a Secret the Function does not bind")
	require.Equal(t, "cedar default-deny: no permitting policy", d.Reason)
	require.False(t, secretRead(t, a, fnPrincipal("default", "checkout"), "quack").Allowed, "another Secret")
	require.False(t, secretRead(t, a, fnPrincipal("default", "gone"), "api-keys").Allowed, "a missing Function binds nothing")
	require.True(t, secretRead(t, a, lakeRef("lake"), "quack").Allowed, "a Lake reads the Secret its engine binds")
	require.False(t, secretRead(t, a, lakeRef("lake"), "api-keys").Allowed, "and only that one")
}

// A namespace Policy forbid revokes one Function's read of one Secret, its sibling's stays (ADR-0321 Decision 2), and
// admission takes the action and refuses another namespace's Secret (Decision 1, ADR-0177 Decision 3).
func TestSecretCapability_PolicyForbidRevokesOneRead(t *testing.T) {
	t.Parallel()
	text := `forbid(principal == Function::"default/reports", action == Action::"secret::read", resource == Secret::"default/api-keys");`
	require.NoError(t, cedar.ValidateCedarInNamespace("default", text))
	require.Error(t, cedar.ValidateCedarInNamespace("team", text), "a Secret is namespaced")
	a := secretDriver(t, newSecretMeta(), namespacePolicy("no-stripe-for-reports", text))
	d := secretRead(t, a, fnPrincipal("default", "reports"), "api-keys")
	require.False(t, d.Allowed)
	require.True(t, strings.HasPrefix(d.Reason, "cedar: forbidden by policy "), d.Reason)
	require.True(t, secretRead(t, a, fnPrincipal("default", "checkout"), "api-keys").Allowed, "the sibling still reads")

	lake := `forbid(principal == Lake::"default/lake", action == Action::"secret::read", resource);`
	require.NoError(t, cedar.ValidateCedarInNamespace("default", lake))
	require.False(t, secretRead(t, secretDriver(t, newSecretMeta(), namespacePolicy("no-lake", lake)), lakeRef("lake"), "quack").Allowed)
}

// scenario: secret-roles-grant-nothing — Reader, Contributor and Owner carry no secret::read, so their namespace grant
// opens no Secret a Function does not bind; a custom Role that lists secret::read never lifts a forbid.
func TestScenarioSecretRolesGrantNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newSecretMeta()
	roles := cedar.NewRoleResolver(m)
	grant := func(fn v1.ObjectName, ref v1.RoleRef) []v1.Policy {
		ra := &v1.RolesAssignment{
			ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName("grant-" + string(fn)), Namespace: "default", ResourceGroup: "rg1"},
			Spec: v1.RolesAssignmentSpec{
				Principal:   &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: fn},
				Assignments: []v1.AssignmentEntry{{RoleRef: ref, Scope: &v1.ScopeRef{Kind: v1.ScopeKindNamespace}}},
			},
		}
		pols, err := cedar.CompileRolesAssignment(ctx, ra, roles)
		require.NoError(t, err)
		return pols
	}
	for _, role := range []string{"Reader", "Contributor", "Owner"} {
		pols := grant("stranger", v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: role})
		require.NotEmpty(t, pols, "%s compiles to a grant", role)
		require.NotContains(t, pols[0].Spec.Cedar, string(auth.ActionSecretRead), "%s holds no secret::read", role)
		require.False(t, secretRead(t, secretDriver(t, m, pols...), fnPrincipal("default", "stranger"), "api-keys").Allowed,
			"%s opens no Secret", role)
	}

	custom := grant("reports", v1.RoleRef{Kind: v1.RoleRefKindRole, Name: "secret-reader"})
	require.Contains(t, custom[0].Spec.Cedar, string(auth.ActionSecretRead), "the custom Role compiles to a secret::read permit")
	forbid := namespacePolicy("no-stripe-for-reports",
		`forbid(principal == Function::"default/reports", action == Action::"secret::read", resource == Secret::"default/api-keys");`)
	require.False(t, secretRead(t, secretDriver(t, m, append(custom, forbid)...), fnPrincipal("default", "reports"), "api-keys").Allowed,
		"a grant never lifts a forbid")
}
