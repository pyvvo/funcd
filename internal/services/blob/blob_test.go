package blob_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/auth/rbac"
	iblob "github.com/green-0-rabbit/funcd/internal/blob"
	"github.com/green-0-rabbit/funcd/internal/blob/gocloud"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/services"
	svcblob "github.com/green-0-rabbit/funcd/internal/services/blob"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
)

func newFacade(t *testing.T) *svcblob.Facade {
	t.Helper()
	bkt, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bkt.Close() })
	f, err := svcblob.NewFacade(svcblob.FacadeDeps{Bucket: bkt, Authorizer: rbac.New()})
	require.NoError(t, err)
	return f
}

func dev(ns v1.NamespaceName) auth.Identity {
	return auth.Identity{Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{ns}}
}
func viewer(ns v1.NamespaceName) auth.Identity {
	return auth.Identity{Subject: "obs", Role: auth.RoleViewer, Namespaces: []v1.NamespaceName{ns}}
}

// scenario: blob-facade-roundtrips.
func TestScenarioBlobFacadeRoundtrips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFacade(t)

	require.NoError(t, f.Put(ctx, dev("team-a"), "team-a", "files", "report.txt", []byte("hello")))
	v, err := f.Get(ctx, dev("team-a"), "team-a", "files", "report.txt")
	require.NoError(t, err)
	require.Equal(t, []byte("hello"), v)

	keys, err := f.List(ctx, dev("team-a"), "team-a", "files", "")
	require.NoError(t, err)
	require.Equal(t, []string{"report.txt"}, keys, "tenant prefix stripped")

	require.NoError(t, f.Delete(ctx, dev("team-a"), "team-a", "files", "report.txt"))
	_, err = f.Get(ctx, dev("team-a"), "team-a", "files", "report.txt")
	require.Error(t, err, "deleted object is absent")
}

// scenario: blob-facade-prefixes-by-namespace-and-binding.
func TestScenarioBlobFacadePrefixesByNamespaceAndBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFacade(t)

	require.NoError(t, f.Put(ctx, dev("team-a"), "team-a", "files", "k", []byte("a-val")))
	require.NoError(t, f.Put(ctx, dev("team-b"), "team-b", "files", "k", []byte("b-val")))

	va, err := f.Get(ctx, dev("team-a"), "team-a", "files", "k")
	require.NoError(t, err)
	require.Equal(t, []byte("a-val"), va, "no cross-tenant collision")
	vb, err := f.Get(ctx, dev("team-b"), "team-b", "files", "k")
	require.NoError(t, err)
	require.Equal(t, []byte("b-val"), vb)
}

// scenario: blob-facade-authorizes-each-access.
func TestScenarioBlobFacadeAuthorizesEachAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFacade(t)

	// developer scoped to team-a accessing team-b → Forbidden, before the bucket.
	require.Equal(t, fault.Forbidden, fault.KindOf(f.Put(ctx, dev("team-a"), "team-b", "files", "k", []byte("x"))))
	_, gerr := f.Get(ctx, dev("team-a"), "team-b", "files", "k")
	require.Equal(t, fault.Forbidden, fault.KindOf(gerr))

	// viewer may not write in its own namespace.
	require.Equal(t, fault.Forbidden, fault.KindOf(f.Put(ctx, viewer("team-a"), "team-a", "files", "k", []byte("x"))))
}

// scenario: blob-facade-presigns — authorized presign passes the facade; a viewer is
// denied a presigned PUT (the capability is a write — no escalation).
func TestScenarioBlobFacadePresigns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFacade(t)

	// authorized dev requesting a GET presign: the facade authorizes (not Forbidden) and
	// delegates to the driver (memblob may not implement signing — that's the driver, not
	// the facade; what matters here is the facade did not deny it).
	url, err := f.SignedURL(ctx, dev("team-a"), "team-a", "files", "k", iblob.SignOptions{Method: iblob.SignGet})
	if err != nil {
		require.NotEqual(t, fault.Forbidden, fault.KindOf(err), "an authorized presign is not denied by the facade")
	} else {
		require.NotEmpty(t, url)
	}

	// viewer denied a presigned PUT (write capability) — the M1 security fix.
	_, perr := f.SignedURL(ctx, viewer("team-a"), "team-a", "files", "k", iblob.SignOptions{Method: iblob.SignPut})
	require.Equal(t, fault.Forbidden, fault.KindOf(perr), "viewer cannot mint a presigned write URL")
}

// scenario: blob-service-reconciles-to-ready — the blob handler on the ADR-0019 dispatcher.
func TestScenarioBlobServiceReconcilesToReady(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())

	obj, ok := v1.NewObject(v1.KindService)
	require.True(t, ok)
	svc := obj.(*v1.Service)
	svc.Name = "myblob"
	svc.Namespace = "default"
	svc.ResourceGroup = "rg1"
	svc.Spec.Type = v1.ServiceTypeBlob
	svc.Spec.Blob = &v1.BlobServiceSpec{Binding: "files"}
	_, err := st.Create(ctx, svc)
	require.NoError(t, err)

	d, err := services.NewDispatcher(st, nil, svcblob.NewHandler())
	require.NoError(t, err)
	_, err = d.Reconcile(ctx, controller.Request{GVK: v1.KindService.GVK(), Namespace: "default", Name: "myblob"})
	require.NoError(t, err)

	got, err := st.Get(ctx, v1.KindService.GVK(), "default", "myblob")
	require.NoError(t, err)
	require.Equal(t, v1.PhaseReady, got.(*v1.Service).Status.Phase)
}
