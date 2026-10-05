package gateway

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/cedar"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
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
// Identity's credential Secret resolves to the Identity principal.
func TestCatalogKeysResolvesMintedIdentityToken(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	minted := IdentityCatalogToken("data", "analyst", testRandomPart(t))
	seedIdentityCredential(t, st, "data", "analyst", "analyst-cred", minted)
	keys := NewCatalogKeys([]byte("node-master"), st)

	ref, ok := keys.PrincipalFor(minted)
	require.True(t, ok, "a minted catalogToken in the Identity's Secret resolves to the Identity")
	require.Equal(t, v1.KindIdentity, ref.Type)
	require.Equal(t, v1.ObjectName("analyst"), ref.Name)
}

// countingStore counts the metastore reads the catalog token resolver makes.
type countingStore struct {
	store.Store
	gets  atomic.Int64
	lists atomic.Int64
}

func (c *countingStore) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	c.gets.Add(1)
	return c.Store.Get(ctx, gvk, ns, name)
}

func (c *countingStore) List(ctx context.Context, gvk v1.GroupVersionKind, opts store.ListOptions) (store.List, error) {
	c.lists.Add(1)
	return c.Store.List(ctx, gvk, opts)
}

func (c *countingStore) reset() {
	c.gets.Store(0)
	c.lists.Store(0)
}

// issueIdentity seeds an Identity and its owned credential Secret holding a minted catalog token, as the
// identity reconciler issues them, and returns the token.
func issueIdentity(t *testing.T, st store.Store, ns v1.NamespaceName, name v1.ObjectName) string {
	t.Helper()
	token := IdentityCatalogToken(ns, name, testRandomPart(t))
	seedIdentityCredential(t, st, ns, name, name, token)
	return token
}

// seedIdentityCredential seeds Identity ns/name naming credential Secret secretName, and that Secret holding token
// under the Identity's controller ref, as the identity reconciler writes it.
func seedIdentityCredential(t *testing.T, st store.Store, ns v1.NamespaceName, name, secretName v1.ObjectName, token string) {
	t.Helper()
	id, err := st.Create(context.Background(), &v1.Identity{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindIdentity.GVK().APIVersion(), Kind: v1.KindIdentity},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: ns, ResourceGroup: "rg1"},
		Spec:       v1.IdentitySpec{Type: v1.IdentityTypeExternal, CredentialSecretName: secretName},
	})
	require.NoError(t, err)
	createObj(t, st, &v1.Secret{
		TypeMeta: v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret},
		ObjectMeta: v1.ObjectMeta{Name: secretName, Namespace: ns, ResourceGroup: "rg1", OwnerReferences: []v1.OwnerReference{{
			ObjectRef: v1.ObjectRef{Kind: v1.KindIdentity, Namespace: ns, Name: name}, UID: id.GetObjectMeta().UID, Controller: true,
		}}},
		Spec: v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: map[string][]byte{catalogTokenSecretKey: []byte(token)}},
	})
}

// A catalog token resolves only through a Secret its Identity controls: an Identity naming an API Secret that holds a
// token with its own prefix, and an Identity re-created before its Secret is re-issued, resolve to no principal.
func TestIdentityTokenResolvesOnlyThroughItsOwnSecret(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	keys := NewCatalogKeys([]byte("node-master"), st)
	planted := IdentityCatalogToken("data", "x2", testRandomPart(t))
	createObj(t, st, &v1.Identity{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindIdentity.GVK().APIVersion(), Kind: v1.KindIdentity},
		ObjectMeta: v1.ObjectMeta{Name: "x2", Namespace: "data", ResourceGroup: "rg1"},
		Spec:       v1.IdentitySpec{Type: v1.IdentityTypeExternal, CredentialSecretName: "u"},
	})
	createObj(t, st, &v1.Secret{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret},
		ObjectMeta: v1.ObjectMeta{Name: "u", Namespace: "data", ResourceGroup: "rg1"},
		Spec:       v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: map[string][]byte{catalogTokenSecretKey: []byte(planted)}},
	})
	_, ok := keys.PrincipalFor(planted)
	require.False(t, ok, "x2 resolves through an API Secret it does not control")

	old := issueIdentity(t, st, "data", "analyst")
	_, ok = keys.PrincipalFor(old)
	require.True(t, ok)
	require.NoError(t, st.Delete(context.Background(), v1.KindIdentity.GVK(), "data", "analyst", ""))
	createObj(t, st, &v1.Identity{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindIdentity.GVK().APIVersion(), Kind: v1.KindIdentity},
		ObjectMeta: v1.ObjectMeta{Name: "analyst", Namespace: "data", ResourceGroup: "rg1"},
		Spec:       v1.IdentitySpec{Type: v1.IdentityTypeExternal},
	})
	_, ok = keys.PrincipalFor(old)
	require.False(t, ok, "a re-created analyst resolves through the Secret of the deleted analyst")
}

// proxyStatus posts a handshake carrying token through the catalog proxy fronting data/lake and reports
// the status and whether the engine was reached.
func proxyStatus(t *testing.T, keys CatalogKeys, pdp auth.Authorizer, token string) (int, bool) {
	t.Helper()
	stub := &engineStub{}
	up := httptest.NewServer(stub.handler())
	defer up.Close()
	target := auth.EntityRef{Type: v1.KindCatalogService, Namespace: "data", Name: "lake"}
	front := httptest.NewServer(NewCatalogProxy(keys, pdp, EngineTarget{Catalog: target, Upstream: up.URL, EngineToken: engineToken}))
	defer front.Close()
	resp, err := http.Post(front.URL, "application/octet-stream", bytes.NewReader(makeHandshake(token)))
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode, stub.hit
}

// scenario: identity-token-lookup-cost-constant — the last-created Identity's token resolves with exactly
// two store reads and no List, at 1 and at 500 Identities.
func TestScenarioIdentityTokenLookupCostConstant(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 500} {
		t.Run(fmt.Sprintf("identities=%d", n), func(t *testing.T) {
			t.Parallel()
			cs := &countingStore{Store: store.New(memory.New())}
			var last string
			var lastName v1.ObjectName
			for i := range n {
				lastName = v1.ObjectName(fmt.Sprintf("id-%d", i))
				last = issueIdentity(t, cs, v1.NamespaceName(fmt.Sprintf("ns-%d", i%7)), lastName)
			}
			lastNS := v1.NamespaceName(fmt.Sprintf("ns-%d", (n-1)%7))
			keys := NewCatalogKeys([]byte("node-master"), cs)
			cs.reset()

			ref, ok := keys.PrincipalFor(last)
			require.True(t, ok)
			require.Equal(t, auth.EntityRef{Type: v1.KindIdentity, Namespace: lastNS, Name: lastName}, ref)
			require.Equal(t, int64(2), cs.gets.Load(), "the Identity and its credential Secret")
			require.Zero(t, cs.lists.Load(), "no List, whatever the number of Identities")
		})
	}
}

// scenario: garbage-token-refused-without-reads — a token that is neither a funcd JWT nor prefixed by a
// canonical owner resolves to no principal, the proxy answers 403, and the store is not read.
func TestScenarioGarbageTokenRefusedWithoutReads(t *testing.T) {
	t.Parallel()
	inner := store.New(memory.New())
	cs := &countingStore{Store: inner}
	for i := range 500 {
		issueIdentity(t, cs, "data", v1.ObjectName(fmt.Sprintf("id-%d", i)))
	}
	keys := NewCatalogKeys([]byte("node-master"), cs)
	pdp := buildPDP(t, inner)

	garbage := map[string]string{
		"unstructured":                           "garbage-unresolvable-token",
		"old 44-character format":                testRandomPart(t),
		"bare access key id":                     s3gateway.IdentityAccessKey("data", "id-0"),
		"FUNCID prefix over a non-DNS name":      s3gateway.IdentityAccessKey("Data", "id_0") + "." + testRandomPart(t),
		"non-canonical encoding of a real owner": nonCanonicalPrefix(t, "data", "id-0") + "." + testRandomPart(t),
	}
	for label, token := range garbage {
		cs.reset()
		_, ok := keys.PrincipalFor(token)
		require.False(t, ok, label)
		code, hit := proxyStatus(t, keys, pdp, token)
		require.Equal(t, http.StatusForbidden, code, label)
		require.False(t, hit, label)
		require.Zero(t, cs.gets.Load(), "%s: no Get", label)
		require.Zero(t, cs.lists.Load(), "%s: no List", label)
	}
}

// scenario: forged-owner-prefix-denied — a prefix naming ops/admin over analyst's own random part, or a
// guessed one, resolves to no principal; a prefix grants nothing without the owner's random part.
func TestScenarioForgedOwnerPrefixDenied(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	analyst := issueIdentity(t, st, "data", "analyst")
	admin := issueIdentity(t, st, "ops", "admin")
	keys := NewCatalogKeys([]byte("node-master"), st)
	pdp := buildPDP(t, st)

	_, analystRandom, found := strings.Cut(analyst, ".")
	require.True(t, found)
	forged := map[string]string{
		"analyst's own random part": IdentityCatalogToken("ops", "admin", analystRandom),
		"a guessed random part":     IdentityCatalogToken("ops", "admin", testRandomPart(t)),
	}
	for label, token := range forged {
		_, ok := keys.PrincipalFor(token)
		require.False(t, ok, label)
		code, hit := proxyStatus(t, keys, pdp, token)
		require.Equal(t, http.StatusForbidden, code, label)
		require.False(t, hit, label)
	}

	ref, ok := keys.PrincipalFor(admin)
	require.True(t, ok)
	require.Equal(t, auth.EntityRef{Type: v1.KindIdentity, Namespace: "ops", Name: "admin"}, ref)
	ref, ok = keys.PrincipalFor(analyst)
	require.True(t, ok)
	require.Equal(t, auth.EntityRef{Type: v1.KindIdentity, Namespace: "data", Name: "analyst"}, ref)
}
