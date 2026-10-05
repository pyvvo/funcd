package identity

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func reconcileResult(t *testing.T, r *Reconciler, name string) controller.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), controller.Request{GVK: v1.KindIdentity.GVK(), Namespace: "ns1", Name: v1.ObjectName(name)})
	require.NoError(t, err)
	return res
}

func identityNamed(t *testing.T, st store.Store, name, secretName string) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindIdentity)
	id := obj.(*v1.Identity)
	id.Namespace, id.Name, id.ResourceGroup = "ns1", v1.ObjectName(name), "rg1"
	id.Spec.Type = v1.IdentityTypeExternal
	id.Spec.CredentialSecretName = v1.ObjectName(secretName)
	_, err := st.Create(context.Background(), id)
	require.NoError(t, err)
}

// scenario: identity-never-takes-anothers-secret — x naming Identity y's Secret, or an API Secret, is NotReady
// SecretNotOwned, and both Secrets keep their ownerRefs and data.
func TestScenarioIdentityNeverTakesAnothersSecret(t *testing.T) {
	st := store.New(memory.New())
	r, err := NewReconciler(ReconcilerDeps{Store: st, SupervisionPeriod: 7 * time.Second})
	require.NoError(t, err)
	identityNamed(t, st, "y", "")
	reconcileResult(t, r, "y")
	sobj, _ := v1.NewObject(v1.KindSecret)
	api := sobj.(*v1.Secret)
	api.Namespace, api.Name, api.ResourceGroup = "ns1", "u", "rg1"
	api.Spec = v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: map[string][]byte{"k": []byte("user")}}
	_, err = st.Create(context.Background(), api)
	require.NoError(t, err)
	before := map[string]*v1.Secret{"y": getSecret(t, st, "ns1", "y"), "u": getSecret(t, st, "ns1", "u")}
	keys := NewExternalKeys(st)
	ySecret, _, ok := keys.Lookup(s3gateway.IdentityAccessKey("ns1", "y"))
	require.True(t, ok)

	for i, target := range []string{"y", "u"} {
		name := []string{"x", "x2"}[i]
		identityNamed(t, st, name, target)
		require.Equal(t, 7*time.Second, reconcileResult(t, r, name).RequeueAfter)
		x := getIdentity(t, st, "ns1", name)
		require.Equal(t, v1.PhasePending, x.Status.Phase)
		cond, ok := x.Status.Conditions.Get(condReady)
		require.True(t, ok)
		require.Equal(t, "SecretNotOwned", cond.Reason)
	}
	for name, b := range before {
		a := getSecret(t, st, "ns1", name)
		require.Equal(t, b.ResourceVersion, a.ResourceVersion, "%s is not written", name)
		require.Equal(t, b.OwnerReferences, a.OwnerReferences)
		require.Equal(t, b.Spec.Data, a.Spec.Data)
	}
	got, _, ok := keys.Lookup(s3gateway.IdentityAccessKey("ns1", "y"))
	require.True(t, ok)
	require.Equal(t, ySecret, got, "y still authenticates with its own secret")
}

// scenario: recreated-identity-gets-fresh-credential — ext deleted and re-applied before collection gets a new UID,
// and its reconciler re-issues the Secret: a new secretAccessKey and catalogToken; the old key no longer authenticates.
func TestScenarioRecreatedIdentityGetsFreshCredential(t *testing.T) {
	st, r := setup(t)
	old := newIdentity(t, st, "ns1", "ext")
	reconcile(t, r, "ns1", "ext")
	oldSec := getSecret(t, st, "ns1", "ext")
	keys := NewExternalKeys(st)
	access := s3gateway.IdentityAccessKey("ns1", "ext")
	oldKey, _, ok := keys.Lookup(access)
	require.True(t, ok)

	require.NoError(t, st.Delete(context.Background(), v1.KindIdentity.GVK(), "ns1", "ext", ""))
	cur := newIdentity(t, st, "ns1", "ext")
	require.NotEqual(t, old.UID, cur.UID)
	reconcile(t, r, "ns1", "ext")

	sec := getSecret(t, st, "ns1", "ext")
	require.Equal(t, oldSec.UID, sec.UID, "re-issued in place")
	require.Equal(t, cur.UID, sec.OwnerReferences[0].UID, "the Secret now names the re-created Identity")
	require.NotEqual(t, oldSec.Spec.Data[secretKeySecretAccessKey], sec.Spec.Data[secretKeySecretAccessKey])
	require.NotEqual(t, oldSec.Spec.Data[secretKeyCatalogToken], sec.Spec.Data[secretKeyCatalogToken])
	newKey, _, ok := keys.Lookup(access)
	require.True(t, ok)
	require.NotEqual(t, oldKey, newKey, "the old secret no longer authenticates")
	require.Equal(t, v1.PhaseReady, getIdentity(t, st, "ns1", "ext").Status.Phase)

	reconcile(t, r, "ns1", "ext")
	require.Equal(t, sec.Spec.Data, getSecret(t, st, "ns1", "ext").Spec.Data, "a same-UID Secret is kept")
}

// Lookup resolves an Identity's key only through a Secret that Identity controls: one naming another Identity's
// Secret or an API Secret, and one re-created before its Secret is re-issued, authenticate with nothing.
func TestLookupResolvesOnlyTheIdentitysOwnSecret(t *testing.T) {
	st, r := setup(t)
	identityNamed(t, st, "y", "")
	reconcileResult(t, r, "y")
	sobj, _ := v1.NewObject(v1.KindSecret)
	api := sobj.(*v1.Secret)
	api.Namespace, api.Name, api.ResourceGroup = "ns1", "u", "rg1"
	api.Spec = v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: map[string][]byte{secretKeySecretAccessKey: []byte("user")}}
	_, err := st.Create(context.Background(), api)
	require.NoError(t, err)
	identityNamed(t, st, "x", "y")
	identityNamed(t, st, "x2", "u")
	keys := NewExternalKeys(st)
	for _, name := range []string{"x", "x2"} {
		_, _, ok := keys.Lookup(s3gateway.IdentityAccessKey("ns1", name))
		require.False(t, ok, "%s resolves through a Secret it does not control", name)
	}
	_, _, ok := keys.Lookup(s3gateway.IdentityAccessKey("ns1", "y"))
	require.True(t, ok)

	require.NoError(t, st.Delete(context.Background(), v1.KindIdentity.GVK(), "ns1", "y", ""))
	identityNamed(t, st, "y", "")
	_, _, ok = keys.Lookup(s3gateway.IdentityAccessKey("ns1", "y"))
	require.False(t, ok, "a re-created y resolves through the Secret of the deleted y")
	reconcileResult(t, r, "y")
	_, _, ok = keys.Lookup(s3gateway.IdentityAccessKey("ns1", "y"))
	require.True(t, ok, "y resolves once its Secret is re-issued")
}
