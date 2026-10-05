package identity

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	cataloggw "github.com/pyvvo/funcd/internal/catalog/gateway"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func setup(t *testing.T) (store.Store, *Reconciler) {
	t.Helper()
	st := store.New(memory.New())
	r, err := NewReconciler(ReconcilerDeps{Store: st})
	require.NoError(t, err)
	return st, r
}

func newIdentity(t *testing.T, st store.Store, ns, name string) *v1.Identity {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindIdentity)
	require.True(t, ok)
	id := obj.(*v1.Identity)
	id.Namespace, id.Name, id.ResourceGroup = v1.NamespaceName(ns), v1.ObjectName(name), "rg1"
	id.Spec.Type = v1.IdentityTypeExternal
	created, err := st.Create(context.Background(), id)
	require.NoError(t, err)
	return created.(*v1.Identity)
}

func reconcile(t *testing.T, r *Reconciler, ns, name string) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), controller.Request{
		GVK: v1.KindIdentity.GVK(), Namespace: v1.NamespaceName(ns), Name: v1.ObjectName(name),
	})
	require.NoError(t, err)
}

func getIdentity(t *testing.T, st store.Store, ns, name string) *v1.Identity {
	t.Helper()
	o, err := st.Get(context.Background(), v1.KindIdentity.GVK(), v1.NamespaceName(ns), v1.ObjectName(name))
	require.NoError(t, err)
	return o.(*v1.Identity)
}

func getSecret(t *testing.T, st store.Store, ns, name string) *v1.Secret {
	t.Helper()
	o, err := st.Get(context.Background(), v1.KindSecret.GVK(), v1.NamespaceName(ns), v1.ObjectName(name))
	require.NoError(t, err)
	return o.(*v1.Secret)
}

// scenario: identity-issues-credential — reconcile creates an OWNED Secret with the keypair, registers
// it (ExternalKeys resolves the issued access key to its secret), and reports Ready + the access key id.
func TestScenarioIdentityIssuesCredential(t *testing.T) {
	t.Parallel()
	st, r := setup(t)
	newIdentity(t, st, "data", "releve-dropper")
	reconcile(t, r, "data", "releve-dropper")

	sec := getSecret(t, st, "data", "releve-dropper")
	require.NotEmpty(t, sec.Spec.Data[secretKeyAccessKeyID])
	require.NotEmpty(t, sec.Spec.Data[secretKeySecretAccessKey])
	require.Len(t, sec.OwnerReferences, 1, "the credential Secret is owned by the Identity (cascade on delete)")
	require.Equal(t, v1.KindIdentity, sec.OwnerReferences[0].Kind)
	require.True(t, sec.OwnerReferences[0].Controller)

	id := getIdentity(t, st, "data", "releve-dropper")
	require.Equal(t, v1.PhaseReady, id.Status.Phase)
	require.Equal(t, s3gateway.IdentityAccessKey("data", "releve-dropper"), id.Status.AccessKeyID)
	require.True(t, strings.HasPrefix(string(sec.Spec.Data[secretKeyCatalogToken]), id.Status.AccessKeyID+"."),
		"the catalog token carries the Identity's access key id as its owner prefix")

	ext := NewExternalKeys(st)
	secret, ns, ok := ext.Lookup(id.Status.AccessKeyID)
	require.True(t, ok, "an issued Identity key resolves via ExternalKeys")
	require.Equal(t, "data", ns)
	require.Equal(t, string(sec.Spec.Data[secretKeySecretAccessKey]), secret)
}

// scenario: identity-credential-rotated — bumping spec.rotate reissues the secret (access key stable).
func TestScenarioIdentityCredentialRotated(t *testing.T) {
	t.Parallel()
	st, r := setup(t)
	newIdentity(t, st, "data", "rot")
	reconcile(t, r, "data", "rot")
	secret1 := string(getSecret(t, st, "data", "rot").Spec.Data[secretKeySecretAccessKey])
	access1 := getIdentity(t, st, "data", "rot").Status.AccessKeyID

	id := getIdentity(t, st, "data", "rot")
	id.Spec.Rotate = 1
	_, err := st.Update(context.Background(), id)
	require.NoError(t, err)
	reconcile(t, r, "data", "rot")

	secret2 := string(getSecret(t, st, "data", "rot").Spec.Data[secretKeySecretAccessKey])
	require.NotEqual(t, secret1, secret2, "rotation issues a NEW secret")
	after := getIdentity(t, st, "data", "rot")
	require.Equal(t, int64(1), after.Status.ObservedRotate)
	require.Equal(t, access1, after.Status.AccessKeyID, "the access key id is stable across rotation")
}

// scenario: identity-deleted-revokes — a deleted Identity's key no longer authenticates.
func TestScenarioIdentityDeletedRevokes(t *testing.T) {
	t.Parallel()
	st, r := setup(t)
	newIdentity(t, st, "data", "gone")
	reconcile(t, r, "data", "gone")
	ext := NewExternalKeys(st)
	access := s3gateway.IdentityAccessKey("data", "gone")
	_, _, ok := ext.Lookup(access)
	require.True(t, ok)

	cur := getIdentity(t, st, "data", "gone")
	require.NoError(t, st.Delete(context.Background(), v1.KindIdentity.GVK(), "data", "gone", cur.ResourceVersion))
	// reconcile the deletion (no error, nothing to tear down)
	reconcile(t, r, "data", "gone")

	_, _, ok = ext.Lookup(access)
	require.False(t, ok, "a deleted Identity's key stops authenticating")
}

func catalogToken(t *testing.T, st store.Store, ns, name string) string {
	t.Helper()
	return string(getSecret(t, st, ns, name).Spec.Data[secretKeyCatalogToken])
}

// scenario: identity-token-resolves — the catalogToken the reconciler mints into the owned Secret resolves
// to the Identity at the catalog PEP proxy.
func TestScenarioIdentityTokenResolves(t *testing.T) {
	t.Parallel()
	st, r := setup(t)
	newIdentity(t, st, "data", "analyst")
	reconcile(t, r, "data", "analyst")

	ref, ok := cataloggw.NewCatalogKeys([]byte("node-master"), st).PrincipalFor(catalogToken(t, st, "data", "analyst"))
	require.True(t, ok)
	require.Equal(t, auth.EntityRef{Type: v1.KindIdentity, Namespace: "data", Name: "analyst"}, ref)
}

// scenario: identity-token-rotation — a spec.rotate bump re-issues the token under the same prefix with a
// new random part; the new token resolves and the old one is refused.
func TestScenarioIdentityTokenRotation(t *testing.T) {
	t.Parallel()
	st, r := setup(t)
	newIdentity(t, st, "data", "rot")
	reconcile(t, r, "data", "rot")
	t1 := catalogToken(t, st, "data", "rot")

	id := getIdentity(t, st, "data", "rot")
	id.Spec.Rotate = 1
	_, err := st.Update(context.Background(), id)
	require.NoError(t, err)
	reconcile(t, r, "data", "rot")
	t2 := catalogToken(t, st, "data", "rot")

	prefix1, random1, found1 := strings.Cut(t1, ".")
	prefix2, random2, found2 := strings.Cut(t2, ".")
	require.True(t, found1)
	require.True(t, found2)
	require.Equal(t, prefix1, prefix2, "rotation keeps the owner prefix")
	require.Equal(t, getIdentity(t, st, "data", "rot").Status.AccessKeyID, prefix2)
	require.NotEqual(t, random1, random2, "rotation mints a new random part")

	keys := cataloggw.NewCatalogKeys([]byte("node-master"), st)
	ref, ok := keys.PrincipalFor(t2)
	require.True(t, ok, "the re-issued token resolves")
	require.Equal(t, auth.EntityRef{Type: v1.KindIdentity, Namespace: "data", Name: "rot"}, ref)
	_, ok = keys.PrincipalFor(t1)
	require.False(t, ok, "the rotated-out token is refused")
}
