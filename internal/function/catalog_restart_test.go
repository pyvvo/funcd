package function_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	cataloggw "github.com/pyvvo/funcd/internal/catalog/gateway"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/runtime"
)

// TestIssue662_ConsumerWaitsForTheLiveCatalogProxy: a restarted daemon runs no catalog proxy until the CatalogService
// reconciler ensures one, on a new port, yet the stored CatalogService is still Ready with the old proxy's URL. A
// consumer pass that runs first must not start a worker with that dead URL.
func TestIssue662_ConsumerWaitsForTheLiveCatalogProxy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	proxies := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(nil, nil), nil, nil)
	t.Cleanup(proxies.Shutdown)
	h := newShimHarness(t, http.StatusOK, false, func(d *function.Deps) { d.CatalogProxies = proxies })

	obj, ok := v1.NewObject(v1.KindCatalogService)
	require.True(t, ok)
	cs := obj.(*v1.CatalogService)
	cs.Name, cs.Namespace, cs.ResourceGroup = "lake", "default", "rg1"
	cs.Spec.Blob = []v1.FunctionBlob{{Alias: "lakehouse", Bucket: "lakehouse", Prefix: "gold"}}
	cs.Spec.Catalog = v1.CatalogRef{Bucket: "lakehouse", Prefix: "gold"}
	cs.Status.Phase = v1.PhaseReady
	cs.Status.Endpoint = "127.0.0.1:1"
	_, err := h.st.Create(ctx, cs)
	require.NoError(t, err)
	h.create(t, "reader", func(fn *v1.Function) {
		fn.Spec.Catalogs = []v1.FunctionCatalog{{Alias: "lake", Catalog: "lake"}}
	})

	h.reconcile(t, "reader")
	require.Zero(t, h.rt.creates, "no worker starts with the URL of a proxy this daemon does not run")
	require.Equal(t, "CatalogNotReady", h.condition(t, "reader", "Ready").Reason)

	url, err := proxies.Ensure(auth.EntityRef{Type: v1.KindCatalogService, Namespace: "default", Name: "lake"}, "http://127.0.0.1:9", "engine-token")
	require.NoError(t, err)
	require.NotEqual(t, cs.Status.Endpoint, url)
	h.reconcile(t, "reader")
	require.Zero(t, h.rt.creates, "the stored endpoint is not the live proxy's URL yet")

	obj, err = h.st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
	require.NoError(t, err)
	cs = obj.(*v1.CatalogService)
	cs.Status.Endpoint = url
	_, err = h.st.Update(ctx, cs)
	require.NoError(t, err)
	h.reconcile(t, "reader")
	require.Equal(t, 1, h.rt.creates)
	spec := h.rt.specOf(runtime.NewInstanceID("default", "reader", v1.ObjectName(h.getFn(t, "reader").Status.CurrentRevision), 0))
	require.Equal(t, url, spec.Env["FUNCD_CATALOG_LAKE_URL"])
}
