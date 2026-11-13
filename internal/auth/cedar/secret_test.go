package cedar_test

import (
	"context"
	"strings"
	"testing"

	cedartypes "github.com/cedar-policy/cedar-go/types"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/cedar"
)

// secretMeta holds the Functions, Lakes, Secrets and custom Roles of "default".
type secretMeta struct {
	fns     map[v1.ObjectName]*v1.Function
	lakes   map[v1.ObjectName]*v1.Lake
	secrets map[v1.ObjectName]*v1.Secret
	roles   map[v1.ObjectName]*v1.Role
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
		case v1.KindSecret:
			if sec, ok := m.secrets[name]; ok {
				return sec, nil
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

// grantedWriters is a WriterLister naming, for each Secret, the Functions a writer-role grant covers.
type grantedWriters map[v1.ObjectName][]v1.ObjectName

func (grantedWriters) BlobWriters(context.Context, v1.NamespaceName, string, string) []cedartypes.EntityUID {
	return nil
}
func (grantedWriters) KVWriters(context.Context, v1.NamespaceName, string, string) []cedartypes.EntityUID {
	return nil
}
func (grantedWriters) SQLWriters(context.Context, v1.NamespaceName, string) []cedartypes.EntityUID {
	return nil
}
func (g grantedWriters) SecretWriters(_ context.Context, ns v1.NamespaceName, secret string) []cedartypes.EntityUID {
	var out []cedartypes.EntityUID
	for _, fn := range g[v1.ObjectName(secret)] {
		out = append(out, cedartypes.NewEntityUID("Function", cedartypes.String(string(ns)+"/"+string(fn))))
	}
	return out
}

// writeMeta is newSecretMeta with Secret oauth, owned by checkout, and Secret plain, owned by no one.
func writeMeta() secretMeta {
	m := newSecretMeta()
	sec := func(name, owner v1.ObjectName) *v1.Secret {
		return &v1.Secret{ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}, Spec: v1.SecretSpec{Owner: owner}}
	}
	m.secrets = map[v1.ObjectName]*v1.Secret{"oauth": sec("oauth", "checkout"), "plain": sec("plain", "")}
	return m
}

// secretWriteDriver is the PDP over the secret capability with writers w, as the root registry builds it.
func secretWriteDriver(t *testing.T, m secretMeta, w cedar.WriterLister, policies ...v1.Policy) auth.Authorizer {
	t.Helper()
	reg, err := cedar.NewRegistry([]cedar.Capability{cedar.KVCapability(), cedar.SecretCapabilityWithWriters(w)},
		[]cedar.PrincipalSource{cedar.FunctionPrincipalSource(), cedar.LakePrincipalSource()})
	require.NoError(t, err)
	ep, err := reg.EntityProvider(m)
	require.NoError(t, err)
	d, err := cedar.New(cedar.Deps{Entities: ep, Policies: fixedPolicies{policies: policies, rev: "1"}, Builtins: reg.Builtins()})
	require.NoError(t, err)
	return d
}

func secretWrite(t *testing.T, a auth.Authorizer, principal *auth.EntityRef, secret v1.ObjectName) auth.Decision {
	t.Helper()
	return authorize(t, a, principal, auth.ActionSecretWrite, &auth.EntityRef{Type: v1.KindSecret, Namespace: "default", Name: secret})
}

// secret::write is permitted only to the Secret's writers, its owner and the writer-role grants, as kv::write is; a
// Secret with neither, or a missing one, has none; a Policy forbid revokes a write (ADR-0327 Decision 1).
func TestSecretCapability_OnlyWritersWrite(t *testing.T) {
	t.Parallel()
	m := writeMeta()
	a := secretWriteDriver(t, m, grantedWriters{"oauth": {"reports"}})
	require.True(t, secretWrite(t, a, fnPrincipal("default", "checkout"), "oauth").Allowed, "the owner")
	require.True(t, secretWrite(t, a, fnPrincipal("default", "reports"), "oauth").Allowed, "a writer-role grant")
	d := secretWrite(t, a, fnPrincipal("default", "stranger"), "oauth")
	require.False(t, d.Allowed, "neither")
	require.Contains(t, d.Reason, "builtin/secret-single-writer")
	require.False(t, secretWrite(t, a, fnPrincipal("default", "checkout"), "plain").Allowed, "no owner and no grant")
	require.False(t, secretWrite(t, a, fnPrincipal("default", "checkout"), "gone").Allowed, "a missing Secret")
	require.False(t, secretWrite(t, a, lakeRef("lake"), "quack").Allowed, "a Lake owns no Secret")
	require.True(t, secretRead(t, a, fnPrincipal("default", "checkout"), "api-keys").Allowed, "reads stay the binding's")

	text := `forbid(principal == Function::"default/checkout", action == Action::"secret::write", resource == Secret::"default/oauth");`
	require.NoError(t, cedar.ValidateCedarInNamespace("default", text))
	d = secretWrite(t, secretWriteDriver(t, m, nil, namespacePolicy("no-writes", text)), fnPrincipal("default", "checkout"), "oauth")
	require.False(t, d.Allowed)
	require.True(t, strings.HasPrefix(d.Reason, "cedar: forbidden by policy "), d.Reason)
}

// The built-in Secret roles (ADR-0327 Decision 3): Secret Data Writer on one Secret is a writer of that Secret, its
// read a permit on it alone; Secret Data Reader makes no writer; a write grant is never a permit.
func TestSecretRoles_WriterJoinsTheWritersOfItsScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	roles := cedar.NewRoleResolver(newSecretMeta())
	grant := func(role string, scope v1.ScopeRef) *v1.RolesAssignment {
		return &v1.RolesAssignment{
			ObjectMeta: v1.ObjectMeta{Name: "grant", Namespace: "default", ResourceGroup: "rg1"},
			Spec: v1.RolesAssignmentSpec{
				Principal:   &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: "reports"},
				Assignments: []v1.AssignmentEntry{{RoleRef: v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: role}, Scope: &scope}},
			},
		}
	}
	writer := grant("Secret Data Writer", v1.ScopeRef{Kind: v1.ScopeKindSecret, Name: "oauth"})
	ws := cedar.WriterGrants(ctx, writer, roles)
	require.Len(t, ws, 1)
	require.Equal(t, []string{string(auth.ActionSecretWrite)}, ws[0].Writes)
	pols, err := cedar.CompileRolesAssignment(ctx, writer, roles)
	require.NoError(t, err)
	require.Len(t, pols, 1)
	require.Contains(t, pols[0].Spec.Cedar, `Secret::"default/oauth"`, "the scope is that one Secret")
	require.Contains(t, pols[0].Spec.Cedar, string(auth.ActionSecretRead))
	require.NotContains(t, pols[0].Spec.Cedar, string(auth.ActionSecretWrite), "a write grant is a writer, never a permit")

	require.Empty(t, cedar.WriterGrants(ctx, grant("Secret Data Reader", v1.ScopeRef{Kind: v1.ScopeKindNamespace}), roles),
		"the reader makes no writer")
}
