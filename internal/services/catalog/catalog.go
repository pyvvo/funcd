// Package catalog is the CatalogService reconciler (ADR-0086, F48): the ADR-0019 controller half
// of the DuckLake/DuckDB/Quack add-on provider, with NO in-daemon facade. It owns
// KindCatalogService: it materializes the backing min-replica=1 `duckdb` Function (the declared
// spec.blob bindings, so ADR-0085 injects the per-fn S3 keypair), reflects readiness, and
// publishes the Quack endpoint in status. It writes NO data and programs NO Route — the EXISTING
// function→gateway path (ADR-0013) exposes the engine; DuckDB does all data I/O through the F47 S3
// surface. cgo never touches the daemon — DuckDB runs out-of-process in the curated `duckdb` image.
package catalog

import (
	"log/slog"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/store"
)

// DuckDBRuntime is the curated runtime name the reconciler materializes the backing Function with
// (ADR-0086 / ADR-0054 embedded-image set). The real DuckDB 1.5.4 + ducklake/httpfs/quack image is
// node-built out-of-band; in-process the backing Function never reaches Ready (no real image).
const DuckDBRuntime = "duckdb"

// condReady is the readiness condition the CatalogService reconciler raises (ADR-0086): True once
// the backing duckdb Function is Ready.
const condReady = "Ready"

// ReconcilerDeps configures the KindCatalogService reconciler (ADR-0086). Store is required.
type ReconcilerDeps struct {
	Store  store.Store
	Logger *slog.Logger
}

// Reconciler is the controller.Reconciler for KindCatalogService (ADR-0086): present ⇒ materialize
// the backing duckdb Function + reflect readiness + publish the Quack endpoint; absent (deleted) ⇒
// best-effort delete the backing Function.
type Reconciler struct {
	store  store.Store
	logger *slog.Logger
}

// NewReconciler builds the CatalogService reconciler. Store is required.
func NewReconciler(d ReconcilerDeps) (*Reconciler, error) {
	if d.Store == nil {
		return nil, fault.Invalidf("services.catalog.NewReconciler", "store is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{store: d.Store, logger: logger.With("component", "services.catalog.reconciler")}, nil
}

// backingFunctionName is the name of the backing duckdb Function a CatalogService materializes:
// "<cs-name>-duckdb" (deterministic, single-valued — chosen and used consistently across Reconcile
// and the delete path).
func backingFunctionName(csName string) string {
	return csName + "-duckdb"
}

// quackEndpoint is the published Quack URL for the backing function: its deterministic ingress path
// on the data plane ("/function/<fn>"), the EXISTING function→gateway path (ADR-0013/0033). The
// reconciler programs NO Route — this is just the address the standard function route already serves.
func quackEndpoint(fnName string) string {
	return "/function/" + fnName
}
