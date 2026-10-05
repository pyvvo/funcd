package v1alpha1

import (
	"github.com/pyvvo/funcd/api/fault"
)

// CatalogService is a namespaced add-on-provider instance (ADR-0086, F48): a governed SQL
// catalog + query engine (DuckLake + DuckDB + Quack) over the Parquet a tenant already stores
// on funcd's blob. Modeled on KVStore — a status-bearing namespaced resource whose reconciler
// materializes a backing min-replica=1 `duckdb` Function (the declared spec.blob bindings, so
// ADR-0085 injects the per-fn S3 keypair). The engine is the deployed Function, exposed through
// the EXISTING function→gateway path (ADR-0013) — there is NO Route resource. DuckDB reads/writes
// Parquet only through the F47 S3 surface under binding-as-grant; its DuckLake catalog lives on
// the same blob (a reserved `_ducklake/catalog.db` key in an owned prefix), loaded + checkpointed
// by the single replica. cgo never touches the daemon — DuckDB runs out-of-process in the curated
// image.
type CatalogService struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       CatalogServiceSpec   `json:"spec"`
	Status     CatalogServiceStatus `json:"status,omitempty"`
}

// CatalogServiceSpec is the desired state of a CatalogService (ADR-0086): the lakehouse blob
// bindings the engine may read/write, the (bucket, prefix) the DuckLake catalog syncs under, and
// the engine's recorded resource sizing.
type CatalogServiceSpec struct {
	// Blob binds the lakehouse buckets the engine may read/write — the SAME shape as
	// Function.spec.blob (ADR-0080), projected onto the backing Function so ADR-0085 injects the
	// per-fn keypair. The prefix the catalog lives under must be OWNED by this service's function
	// (single-writer).
	Blob []FunctionBlob `json:"blob"`
	// Catalog names the bound (bucket, prefix) the DuckLake catalog syncs under (a `_ducklake/` key);
	// the prefix must be OWNED by this service's function (single-writer). The SQLite catalog file
	// lives at <prefix>/_ducklake/catalog.db.
	Catalog CatalogRef `json:"catalog"`
	// Resources sizes the engine (cpu/mem); DuckDB is memory-hungry. The provider-runtime maps it
	// onto runtime.Limits when numerically parseable (ADR-0087), else it is recorded forward-compat.
	Resources ResourceSpec `json:"resources,omitempty"`
	// Secrets names the Secret resources in this namespace whose Data is injected into the engine
	// as env vars (ADR-0087, reusing the ADR-0057 FunctionSpec.Secrets convention verbatim): the
	// Quack token (QUACK_TOKEN). Each named Secret's Data keys become engine env-var names;
	// reserved FUNCD_* keys are never overridable. Only the NAMES are persisted; resolved values
	// live only in the engine's env. Resolution is PDP-authorized (ADR-0022/0057); an unauthorized
	// or missing Secret fails the service closed (not Ready). Empty ⇒ no secret injection.
	Secrets []ObjectName `json:"secrets,omitempty"`
	// Config names the ConfigMap resources in this namespace whose Data is injected into the engine
	// as env vars (ADR-0087): engine tuning (DUCKDB_*). Same env-injection shape as Secrets, but
	// from non-sensitive ConfigMap Data. Empty ⇒ no config injection.
	Config []ObjectName `json:"config,omitempty"`
	// Ingress declares OPT-IN external edge exposure of this catalog's catalog::query PEP proxy
	// (ADR-0138). Nil ⇒ internal-only (no edge route — today's default). Set ⇒ once Ready, the
	// reconciler programs an ingress route to the PEP PROXY (never the engine), so an external caller
	// is PEP'd on catalog::query exactly like an internal one. Exposure is a network path, not a grant:
	// default-deny is preserved (an external Identity still needs a RolesAssignment).
	Ingress *CatalogIngress `json:"ingress,omitempty"`
}

// CatalogIngress declares external edge exposure of a CatalogService's catalog::query PEP proxy
// (ADR-0138). huma derives the schema from these tags (no hand-written Schema(), like CatalogRef).
type CatalogIngress struct {
	// PathPrefix is the edge path the proxy is exposed at, e.g. "/catalog/lake". Required (leading "/").
	PathPrefix string `json:"pathPrefix"`
	// Host is an optional exact host match ("" = any host), mirroring gateway.Route.Host (V1).
	Host string `json:"host,omitempty"`
}

// CatalogRef names the bound (bucket, prefix) the DuckLake catalog syncs under (ADR-0086). The
// prefix must be OWNED by this service's function (single-writer); the SQLite catalog file lives at
// <prefix>/_ducklake/catalog.db. Cross-resource validity (the bucket/prefix exist, owner is this
// service's function) is the catalog-blob-validity admission's job, NOT Validate.
type CatalogRef struct {
	// Bucket is the name of a Bucket in this service's namespace.
	Bucket ObjectName `json:"bucket"`
	// Prefix is a sub-domain (BucketPrefix.name) of that bucket; a DNS-1123 label. The SQLite catalog
	// lives at <prefix>/_ducklake/catalog.db.
	Prefix string `json:"prefix" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
}

// ResourceSpec sizes the backing engine (cpu/mem). RECORDED ONLY on the CatalogService (ADR-0086):
// FunctionSpec has no resources field today, so this is forward-compat (the same posture as
// Scaling.MaxReplicas) and is never projected onto the materialized Function.
type ResourceSpec struct {
	// CPU is a recorded CPU request/limit hint (e.g. "2"); forward-compat only.
	CPU string `json:"cpu,omitempty"`
	// Memory is a recorded memory request/limit hint (e.g. "4Gi"); forward-compat only.
	Memory string `json:"memory,omitempty"`
}

// CatalogServiceStatus is the observed state of a CatalogService (ADR-0086): the reconciler reflects
// readiness (Ready once the backing duckdb Function is up), the materialized backing Function, and
// the published Quack endpoint. It embeds the shared Status (Phase + Conditions) so the controller's
// generic write-back seam (StatusObject.GetStatus) points at the live status — there is NO separate
// /status route.
type CatalogServiceStatus struct {
	Status `json:",inline"`
	// Function is the materialized backing duckdb Function (computed by the reconciler).
	Function ObjectName `json:"function,omitempty"`
	// Endpoint is the published Quack URL — the backing function's ingress path (computed).
	Endpoint string `json:"endpoint,omitempty"`
	// ProxyPort is the port of the catalog's PEP proxy listener (ADR-0162), recorded so a restarted daemon binds the
	// same port again and the URL consumers hold keeps reaching this catalog (computed).
	ProxyPort int `json:"proxyPort,omitempty"`
}

// GroupVersionKind returns the constant GVK for CatalogService.
func (c *CatalogService) GroupVersionKind() GroupVersionKind { return KindCatalogService.GVK() }

// isPrincipalObject marks *CatalogService as a cedar PrincipalObject (ADR-0116/0088): an add-on provider
// resolved as an S3 principal (its spec.blob are its blobBindings) when no same-named Function exists.
// The marker keeps the cedar.PrincipalObject interface closed (only api/types implements it).
func (c *CatalogService) isPrincipalObject() {}

// Validate performs envelope validation via the shared validateMeta helper, then the
// CatalogServiceSpec rules JSON Schema can't express (mirrors KVStore.Validate / FunctionSpec.Validate's
// blob loop): each spec.blob entry's alias + prefix is a DNS-1123 label and the alias is unique within
// Blob; Catalog.Prefix is a DNS-1123 label; and the catalog (bucket, prefix) is one of the spec.blob
// bindings (so the engine gets the per-fn S3 keypair and can write its own catalog — ADR-0086). Cross-
// resource validity (the bucket/prefix actually EXIST in the namespace) is the catalog-blob-validity
// admission, NOT structural Validate.
func (c *CatalogService) Validate() error {
	const op = "CatalogService.Validate"
	if err := validateMeta(c.TypeMeta, &c.ObjectMeta, KindCatalogService); err != nil {
		return err
	}
	seen := make(map[string]bool, len(c.Spec.Blob))
	for _, b := range c.Spec.Blob {
		if !dnsLabel.MatchString(b.Alias) {
			return fault.Invalidf(op, "spec.blob alias %q is not a valid DNS-1123 label", b.Alias)
		}
		if seen[b.Alias] {
			return fault.Invalidf(op, "spec.blob alias %q is duplicated", b.Alias)
		}
		seen[b.Alias] = true
		if !dnsLabel.MatchString(b.Prefix) {
			return fault.Invalidf(op, "spec.blob[%s].prefix %q is not a valid DNS-1123 label", b.Alias, b.Prefix)
		}
	}
	if !dnsLabel.MatchString(c.Spec.Catalog.Prefix) {
		return fault.Invalidf(op, "spec.catalog.prefix %q is not a valid DNS-1123 label", c.Spec.Catalog.Prefix)
	}
	// The catalog (bucket, prefix) must be one of the spec.blob bindings (ADR-0086: "the bound
	// (bucket, prefix) ... OWNED by this service's function"). This is load-bearing for durability:
	// the backing Function injects the per-fn S3 keypair only when spec.blob is non-empty (ADR-0085),
	// and the engine writes its own DuckLake catalog via that keypair — so the catalog prefix must be
	// a declared, owned blob binding or the VACUUM INTO→whole-object-Put checkpoint is 403-denied.
	// (This also implies spec.blob is non-empty whenever a Catalog is set.) It is an intra-object
	// rule (no store needed); cross-resource existence is the catalog-blob-validity admission.
	catalogBound := false
	for _, b := range c.Spec.Blob {
		if b.Bucket == c.Spec.Catalog.Bucket && b.Prefix == c.Spec.Catalog.Prefix {
			catalogBound = true
			break
		}
	}
	if !catalogBound {
		return fault.Invalidf(op, "spec.catalog (%q/%q) must be one of spec.blob bindings (the catalog prefix must be a bound, owned blob binding so the engine can write it)",
			c.Spec.Catalog.Bucket, c.Spec.Catalog.Prefix)
	}
	// spec.secrets / spec.config each name a Secret / ConfigMap in this namespace whose Data is
	// injected into the engine env (ADR-0087, the ADR-0057 convention). Structural Validate checks
	// only that each is a valid DNS-1123 ObjectName; cross-resource existence + read authorization
	// is the reconciler's resolution step (PDP-authorized, ADR-0057), not Validate.
	for _, s := range c.Spec.Secrets {
		if !dnsLabel.MatchString(string(s)) {
			return fault.Invalidf(op, "spec.secrets entry %q is not a valid DNS-1123 name", s)
		}
	}
	for _, cm := range c.Spec.Config {
		if !dnsLabel.MatchString(string(cm)) {
			return fault.Invalidf(op, "spec.config entry %q is not a valid DNS-1123 name", cm)
		}
	}
	// spec.ingress (ADR-0138) opts the catalog into external edge exposure. Structural Validate checks
	// only that the declared edge path is a rooted prefix; the route is programmed (to the PEP proxy)
	// by the reconciler once Ready.
	if c.Spec.Ingress != nil {
		if c.Spec.Ingress.PathPrefix == "" || c.Spec.Ingress.PathPrefix[0] != '/' {
			return fault.Invalidf(op, "spec.ingress.pathPrefix %q must be a non-empty rooted path (leading %q)", c.Spec.Ingress.PathPrefix, "/")
		}
	}
	return nil
}

// GetStatus returns the shared Status pointer, implementing StatusObject (the controller's write-back seam).
func (c *CatalogService) GetStatus() *Status { return &c.Status.Status }
