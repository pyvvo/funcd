package gateway

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

// raPolicies is a PolicySource that compiles the store's RolesAssignments into catalog::query permits
// (the read-shaped grant path) — the inline analog of roles.CompilePolicies, kept here so the gateway
// tests do not import a sibling service.
type raPolicies struct{ s store.Store }

func (r raPolicies) Policies(ctx context.Context) ([]v1.Policy, string, error) {
	lst, err := r.s.List(ctx, v1.KindRolesAssignment.GVK(), store.ListOptions{})
	if err != nil {
		return nil, "", err
	}
	resolver := cedar.NewRoleResolver(r.s)
	var out []v1.Policy
	var maxRV string
	for _, obj := range lst.Items {
		ra, ok := obj.(*v1.RolesAssignment)
		if !ok {
			continue
		}
		if ra.ResourceVersion > maxRV {
			maxRV = ra.ResourceVersion
		}
		pols, cerr := cedar.CompileRolesAssignment(ctx, ra, resolver)
		if cerr != nil {
			return nil, "", cerr
		}
		out = append(out, pols...)
	}
	return out, maxRV, nil
}

// buildPDP builds a Cedar PDP over st with the catalog capability (mirrors roles/writers_test.go's
// buildPDP): the builtin_catalog.cedar binding-permit for Functions + the compiled Catalog-scoped
// RolesAssignment permits for Identities.
func buildPDP(t *testing.T, st store.Store) auth.Authorizer {
	t.Helper()
	reg, err := cedar.NewRegistry(
		[]cedar.Capability{cedar.CatalogCapability()},
		[]cedar.PrincipalSource{cedar.FunctionPrincipalSource(), cedar.CatalogServicePrincipalSource()},
	)
	require.NoError(t, err)
	ep, err := reg.EntityProvider(st)
	require.NoError(t, err)
	pdp, err := cedar.New(cedar.Deps{Entities: ep, Policies: raPolicies{st}, Builtins: reg.Builtins()})
	require.NoError(t, err)
	return pdp
}

func createObj(t *testing.T, st store.Store, obj v1.Object) {
	t.Helper()
	_, err := st.Create(context.Background(), obj)
	require.NoError(t, err)
}

// seedCatalogWorld seeds a Function "analytics" bound to catalog "lake" (spec.catalogs), an unbound
// Function "reporting", and the two CatalogServices "lake"/"other" — all in namespace "data".
func seedCatalogWorld(t *testing.T, st store.Store) {
	t.Helper()
	createObj(t, st, &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "analytics", Namespace: "data", ResourceGroup: "rg1"},
		Spec:       v1.FunctionSpec{Catalogs: []v1.FunctionCatalog{{Alias: "lake", Catalog: "lake"}}},
	})
	createObj(t, st, &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "reporting", Namespace: "data", ResourceGroup: "rg1"},
	})
}

func assignRA(t *testing.T, st store.Store, name string, spec v1.RolesAssignmentSpec) {
	t.Helper()
	createObj(t, st, &v1.RolesAssignment{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindRolesAssignment.GVK().APIVersion(), Kind: v1.KindRolesAssignment},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "data", ResourceGroup: "rg1"},
		Spec:       spec,
	})
}

func canQuery(t *testing.T, pdp auth.Authorizer, kind v1.PrincipalKind, name, catalog string) bool {
	t.Helper()
	pk := v1.KindIdentity
	if kind == v1.PrincipalKindFunction {
		pk = v1.KindFunction
	}
	principal := auth.EntityRef{Type: pk, Namespace: "data", Name: v1.ObjectName(name)}
	res := auth.EntityRef{Type: v1.KindCatalogService, Namespace: "data", Name: v1.ObjectName(catalog)}
	d, err := pdp.Authorize(context.Background(), auth.Request{
		Action:   auth.ActionCatalogQuery,
		Resource: &res,
		Identity: auth.Identity{Principal: &principal},
	})
	require.NoError(t, err)
	return d.Allowed
}

// scenario: internal-fn-query-granted — a Function bound to catalog `lake` via spec.catalogs is permitted
// catalog::query on `lake` (the binding compiles to a permit via builtin_catalog.cedar).
func TestScenarioInternalFnQueryGranted(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedCatalogWorld(t, st)
	pdp := buildPDP(t, st)

	require.True(t, canQuery(t, pdp, v1.PrincipalKindFunction, "analytics", "lake"),
		"a Function bound to `lake` via spec.catalogs is permitted catalog::query on it")
}

// scenario: internal-fn-unbound-denied — a Function NOT bound to `lake` is default-denied (forgot to
// declare the binding fails closed).
func TestScenarioInternalFnUnboundDenied(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedCatalogWorld(t, st)
	pdp := buildPDP(t, st)

	require.False(t, canQuery(t, pdp, v1.PrincipalKindFunction, "reporting", "lake"),
		"an unbound Function has no catalog::query grant ⇒ default-deny")
	require.False(t, canQuery(t, pdp, v1.PrincipalKindFunction, "analytics", "other"),
		"a Function's binding does not grant a catalog it did not bind")
}

// scenario: external-identity-query-granted — an Identity with a Catalog Query Reader RolesAssignment
// scoped to `lake` is permitted catalog::query on it.
func TestScenarioExternalIdentityQueryGranted(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedCatalogWorld(t, st)
	assignRA(t, st, "analyst-can-query-lake", v1.RolesAssignmentSpec{
		Principal:   &v1.PrincipalRef{Kind: v1.PrincipalKindIdentity, Name: "analyst"},
		Assignments: []v1.AssignmentEntry{{RoleRef: v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: "Catalog Query Reader"}, Scope: &v1.ScopeRef{Kind: v1.ScopeKindCatalog, Name: "lake"}}},
	})
	pdp := buildPDP(t, st)

	require.True(t, canQuery(t, pdp, v1.PrincipalKindIdentity, "analyst", "lake"),
		"an Identity granted Catalog Query Reader @ lake is permitted catalog::query on it")
}

// scenario: external-identity-query-denied — an Identity with NO catalog::query grant is denied.
func TestScenarioExternalIdentityQueryDenied(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedCatalogWorld(t, st)
	pdp := buildPDP(t, st)

	require.False(t, canQuery(t, pdp, v1.PrincipalKindIdentity, "guest", "lake"),
		"an Identity with no RolesAssignment is default-denied (the request never reaches the engine)")
}

// scenario: scope-bounds-the-grant — a Catalog Query Reader scoped to `lake` cannot query `other`.
func TestScenarioScopeBoundsTheGrant(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedCatalogWorld(t, st)
	assignRA(t, st, "analyst-can-query-lake", v1.RolesAssignmentSpec{
		Principal:   &v1.PrincipalRef{Kind: v1.PrincipalKindIdentity, Name: "analyst"},
		Assignments: []v1.AssignmentEntry{{RoleRef: v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: "Catalog Query Reader"}, Scope: &v1.ScopeRef{Kind: v1.ScopeKindCatalog, Name: "lake"}}},
	})
	pdp := buildPDP(t, st)

	require.True(t, canQuery(t, pdp, v1.PrincipalKindIdentity, "analyst", "lake"), "the grant permits its scope")
	require.False(t, canQuery(t, pdp, v1.PrincipalKindIdentity, "analyst", "other"),
		"the grant does not leak past its scope (other is not lake)")
}

// scenario: unknown-credential-denied — an unresolvable catalog token yields no principal (PrincipalFor
// ok=false); the shared engine token is never reachable this way.
func TestScenarioUnknownCredentialDenied(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	keys := NewCatalogKeys([]byte("node-master"), st)

	_, ok := keys.PrincipalFor("this-is-not-a-valid-token")
	require.False(t, ok, "an unstructured token resolves to no principal")
	_, ok = keys.PrincipalFor("AAAA.BBBB")
	require.False(t, ok, "a dot-shaped but non-decoding/wrong-MAC token resolves to no principal")
}

// scenario: forged-function-token-denied — a token whose (ns, fn) prefix is valid but whose MAC was
// computed under the WRONG master fails hmac.Equal ⇒ no principal; the SAME (ns, fn) under the right
// master verifies (a decodable prefix alone never authenticates a Function).
func TestScenarioForgedFunctionTokenDenied(t *testing.T) {
	t.Parallel()
	master := []byte("the-real-node-master-secret")
	wrong := []byte("an-attacker-guessed-master")

	forged, forgedErr := DeriveCatalogToken(wrong, "data", "analytics")
	require.NoError(t, forgedErr)
	honest, honestErr := DeriveCatalogToken(master, "data", "analytics")
	require.NoError(t, honestErr)

	keys := NewCatalogKeys(master, nil) // no store: Function path only

	_, ok := keys.PrincipalFor(forged)
	require.False(t, ok, "a valid-looking (ns, fn) prefix with a MAC under the wrong master is rejected")

	ref, ok := keys.PrincipalFor(honest)
	require.True(t, ok, "the token derived under the right master verifies")
	require.Equal(t, v1.KindFunction, ref.Type)
	require.Equal(t, v1.NamespaceName("data"), ref.Namespace)
	require.Equal(t, v1.ObjectName("analytics"), ref.Name)
}

// TestCatalogKeysResolvesMintedIdentityToken confirms the store-lookup half: a minted catalogToken in an
// Identity's credential Secret resolves to the Identity principal (the ADR-0088 IdentityAccessKey analog).
func TestCatalogKeysResolvesMintedIdentityToken(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	minted := "MINTED-RANDOM-IDENTITY-CATALOG-TOKEN"
	createObj(t, st, &v1.Identity{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindIdentity.GVK().APIVersion(), Kind: v1.KindIdentity},
		ObjectMeta: v1.ObjectMeta{Name: "analyst", Namespace: "data", ResourceGroup: "rg1"},
		Spec:       v1.IdentitySpec{Type: v1.IdentityTypeExternal, CredentialSecretName: "analyst-cred"},
	})
	createObj(t, st, &v1.Secret{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret},
		ObjectMeta: v1.ObjectMeta{Name: "analyst-cred", Namespace: "data", ResourceGroup: "rg1"},
		Spec:       v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: map[string][]byte{catalogTokenSecretKey: []byte(minted)}},
	})
	keys := NewCatalogKeys([]byte("node-master"), st)

	ref, ok := keys.PrincipalFor(minted)
	require.True(t, ok, "a minted catalogToken in the Identity's Secret resolves to the Identity")
	require.Equal(t, v1.KindIdentity, ref.Type)
	require.Equal(t, v1.ObjectName("analyst"), ref.Name)
}
