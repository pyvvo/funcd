package catalog_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controller"
	catalogsvc "github.com/green-0-rabbit/funcd/internal/services/catalog"
	"github.com/green-0-rabbit/funcd/internal/store"
	storemem "github.com/green-0-rabbit/funcd/internal/store/memory"
)

// mkCatalogService builds a VALID CatalogService: the catalog (lakehouse/gold) is always present as
// a spec.blob binding (CatalogService.Validate requires the catalog prefix to be a declared blob
// binding so the engine gets the per-fn S3 keypair). Extra blob bindings the caller passes are
// appended; a caller-supplied {lakehouse,gold} binding is not duplicated.
func mkCatalogService(name string, blob ...v1.FunctionBlob) *v1.CatalogService {
	cs := &v1.CatalogService{}
	cs.TypeMeta = v1.TypeMeta{APIVersion: v1.KindCatalogService.GVK().APIVersion(), Kind: v1.KindCatalogService}
	cs.Name, cs.Namespace, cs.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	cs.Spec.Catalog = v1.CatalogRef{Bucket: "lakehouse", Prefix: "gold"}
	hasCatalogBinding := false
	for _, b := range blob {
		if b.Bucket == cs.Spec.Catalog.Bucket && b.Prefix == cs.Spec.Catalog.Prefix {
			hasCatalogBinding = true
			break
		}
	}
	if !hasCatalogBinding {
		cs.Spec.Blob = append(cs.Spec.Blob, v1.FunctionBlob{Alias: "catalog", Bucket: "lakehouse", Prefix: "gold"})
	}
	cs.Spec.Blob = append(cs.Spec.Blob, blob...)
	return cs
}

// backingFn is the deterministic name the reconciler materializes: "<cs-name>-duckdb".
func backingFn(csName string) v1.ObjectName { return v1.ObjectName(csName + "-duckdb") }

func reconcileOnce(t *testing.T, st store.Store, name string) {
	t.Helper()
	r, err := catalogsvc.NewReconciler(catalogsvc.ReconcilerDeps{Store: st})
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), controller.Request{GVK: v1.KindCatalogService.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
	require.NoError(t, err)
}

// noRoutesExist asserts no Route resource exists in the namespace — the CatalogService is exposed via
// the EXISTING function→gateway path (ADR-0013), so the reconciler must program NO Route.
func noRoutesExist(t *testing.T, st store.Store) {
	t.Helper()
	routes, err := st.List(context.Background(), v1.KindRoute.GVK(), store.ListOptions{Namespace: "default"})
	require.NoError(t, err)
	require.Empty(t, routes.Items, "the reconciler programs NO Route resource — the function→gateway path exposes the engine")
}

// scenario: catalog-service-deploys — a CatalogService reconciles into a backing min-replica=1
// `duckdb` Function carrying the declared spec.blob; NO Route resource is created (the function is
// exposed via the existing function→gateway path).
func TestReconcile_catalog_service_deploys(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	_, err := st.Create(ctx, mkCatalogService("lake",
		v1.FunctionBlob{Alias: "gold", Bucket: "lakehouse", Prefix: "gold"}))
	require.NoError(t, err)

	reconcileOnce(t, st, "lake")

	obj, err := st.Get(ctx, v1.KindFunction.GVK(), "default", backingFn("lake"))
	require.NoError(t, err, "a backing Function must exist")
	fn := obj.(*v1.Function)
	require.Equal(t, v1.RuntimeName("duckdb"), fn.Spec.Runtime, "backing runtime is duckdb")
	require.Equal(t, 1, fn.Spec.Scaling.MinReplicas, "min-replica=1 (no scale-to-zero)")
	require.Equal(t, 1, fn.Spec.Replicas, "exactly one replica (single catalog writer)")
	require.Equal(t, []v1.FunctionBlob{{Alias: "gold", Bucket: "lakehouse", Prefix: "gold"}}, fn.Spec.Blob,
		"the CatalogService's spec.blob is projected onto the backing Function (ADR-0085 keypair injection)")

	// status reflects the materialized function + published Quack endpoint (its standard ingress path).
	csObj, err := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
	require.NoError(t, err)
	cs := csObj.(*v1.CatalogService)
	require.Equal(t, backingFn("lake"), cs.Status.Function)
	require.Equal(t, "/function/lake-duckdb", cs.Status.Endpoint)

	noRoutesExist(t, st)
}

// scenario: min-replica-pinned — the materialized Function has MinReplicas==1 (no scale-to-zero); it
// is the single writer of its catalog, always reachable.
func TestReconcile_min_replica_pinned(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	_, err := st.Create(ctx, mkCatalogService("lake"))
	require.NoError(t, err)

	reconcileOnce(t, st, "lake")

	obj, err := st.Get(ctx, v1.KindFunction.GVK(), "default", backingFn("lake"))
	require.NoError(t, err)
	fn := obj.(*v1.Function)
	require.Equal(t, 1, fn.Spec.Scaling.MinReplicas, "pinned minReplicas=1 — no scale-to-zero")
	require.Equal(t, 1, fn.Spec.Replicas)
}

// scenario: unauthorized-denied — the reconciler creates NO Route/public bypass: the function is
// exposed via the standard function→gateway path, which carries the ingress auth middleware (ADR-0013).
// So after reconcile no Route resource exists. The actual 403 for an unauthorized client is the
// gateway's, tested live on the node-gated lane.
func TestReconcile_unauthorized_denied(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	_, err := st.Create(ctx, mkCatalogService("lake"))
	require.NoError(t, err)

	reconcileOnce(t, st, "lake")
	noRoutesExist(t, st)
}

// scenario (delete path) — a deleted (absent) CatalogService best-effort deletes its backing
// Function; a missing backing Function is fine (idempotent).
func TestReconcile_delete_removes_backing_function(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	_, err := st.Create(ctx, mkCatalogService("lake"))
	require.NoError(t, err)
	reconcileOnce(t, st, "lake")
	_, err = st.Get(ctx, v1.KindFunction.GVK(), "default", backingFn("lake"))
	require.NoError(t, err, "backing function present after deploy")

	// delete the CatalogService, then reconcile — the backing Function is reclaimed.
	require.NoError(t, st.Delete(ctx, v1.KindCatalogService.GVK(), "default", "lake", ""))
	reconcileOnce(t, st, "lake")
	_, err = st.Get(ctx, v1.KindFunction.GVK(), "default", backingFn("lake"))
	require.Error(t, err, "backing function reclaimed on CatalogService delete")

	// a second delete-reconcile is a no-op (idempotent — missing backing function is fine).
	reconcileOnce(t, st, "lake")
}

// DEFERRED node-gated scenarios (ADR-0086): the live-DuckDB scenarios need the native DuckDB image (a
// separate process) on real containerd, so they run on the homebox/Lima FUNCD_IT=1 lane (the ADR-0080
// s3gateway precedent), NOT this in-process suite:
//   - query-over-quack              — a Quack SELECT reads bound Parquet via the F47 S3 surface.
//   - write-creates-ducklake-snapshot — an INSERT writes Parquet + a DuckLake catalog snapshot on blob.
//   - tenant-isolation              — A's endpoint reading B's bucket → 403 (the F47 keypair, cryptographic).
//   - arbitrary-url-confined        — a non-funcd URL is refused by the shim lockdown.
//   - catalog-persists-across-restart — the catalog (loaded from blob) survives a replica restart.
