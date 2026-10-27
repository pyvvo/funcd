package v1alpha1

import (
	"strings"

	"github.com/pyvvo/funcd/api/fault"
)

// Lake is a namespaced add-on-provider instance (ADR-0086, F48): a governed SQL
// lake + query engine (DuckLake + DuckDB + Quack) over the Parquet a tenant already stores
// on funcd's blob. Modeled on KVStore — a status-bearing namespaced resource whose reconciler
// materializes a backing min-replica=1 `duckdb` Function (the declared spec.bindings.blob bindings, so
// ADR-0085 injects the per-fn S3 keypair). The engine is the deployed Function, exposed through
// the EXISTING function→gateway path (ADR-0013) — there is NO Route resource. DuckDB reads/writes
// Parquet only through the F47 S3 surface under binding-as-grant; its DuckLake catalog lives in the platform's
// catalog store (ADR-0293), outside every Bucket, loaded + checkpointed by the single replica. cgo never touches the daemon — DuckDB runs out-of-process in the curated
// image.
type Lake struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       LakeSpec   `json:"spec"`
	Status     LakeStatus `json:"status,omitempty"`
}

// LakeSpec is the desired state of a Lake (ADR-0086): the lakehouse blob
// bindings the engine may read/write, the (bucket, prefix) the DuckLake catalog syncs under, and
// the engine's recorded resource sizing.
type LakeSpec struct {
	// Bindings are what the engine uses (ADR-0246): the lakehouse buckets, its ConfigMaps and its Secrets.
	Bindings LakeBindings `json:"bindings"`
	// Location names the bound (bucket, prefix) the Lake's files live under; the prefix must be OWNED by this Lake
	// (single-writer). Its catalog, a DuckDB file (ADR-0271), lives in the catalog store under the same location
	// (ADR-0293).
	Location LakeLocation `json:"location"`
	// Resources sizes the engine (cpu/mem); DuckDB is memory-hungry. Validate refuses a value that does not parse, and
	// the provider-runtime maps it onto runtime.Limits (ADR-0087, ADR-0291 Decision 2).
	Resources ResourceSpec `json:"resources,omitempty"`
	// Ingress declares the edge exposure of this lake's lake::query PEP proxy (ADR-0138, ADR-0241), on only
	// with enabled: true; its default path is /lake/<name>. Once Ready, the reconciler programs an edge
	// entry to the PEP PROXY (never the engine), so an external caller is PEP'd on lake::query exactly like an
	// internal one; auth is refused, since the proxy authenticates. Exposure is a network path, not a grant.
	Ingress *Ingress `json:"ingress,omitempty"`
	// Buckets, ConfigMaps and SecretDeclarations are what a standalone Lake declares at its root (ADR-0251):
	// by name it owns the Bucket or ConfigMap, which carries its marker; by ref it binds an existing one. An owner left
	// out of a prefix is the Lake (the lake-store-owner admission). A lake nested in a Function or an
	// App declares nothing here: its declarer's root holds them.
	Buckets            []BucketEntry       `json:"buckets,omitempty"`
	ConfigMaps         []ConfigMapEntry    `json:"configMaps,omitempty"`
	SecretDeclarations []SecretDeclaration `json:"secrets,omitempty"`
}

// HasRootEntries reports whether the spec declares anything at its root (ADR-0251), which only a standalone
// Lake may.
func (s *LakeSpec) HasRootEntries() bool {
	return len(s.Buckets) > 0 || len(s.ConfigMaps) > 0 || len(s.SecretDeclarations) > 0
}

// rootField names the first root key the spec sets, for the refusal of a nested lake's root entries.
func (s *LakeSpec) rootField() string {
	switch {
	case len(s.Buckets) > 0:
		return "buckets"
	case len(s.ConfigMaps) > 0:
		return "configMaps"
	case len(s.SecretDeclarations) > 0:
		return "secrets"
	}
	return ""
}

// LakeCatalogBucket is the S3 bucket under which the S3 gateway serves the catalog store to each Lake's engine
// (ADR-0293), at the key <bucket>/<prefix>/_ducklake/catalog.db of its location: the dot keeps it out of every Bucket's
// name, which is a DNS label.
const LakeCatalogBucket = "funcd.lake-catalogs"

// LakeCatalogKey is a location's catalog key under LakeCatalogBucket.
func LakeCatalogKey(l LakeLocation) string {
	return string(l.Bucket) + "/" + l.Prefix + "/_ducklake/catalog.db"
}

// LakeLocation names the bound (bucket, prefix) of a Lake's files (ADR-0086). The prefix must be OWNED by this Lake
// (single-writer); the catalog, a DuckDB file (ADR-0271), lives in the catalog store under the same location
// (ADR-0293). Cross-resource validity (the bucket/prefix exist, owner is this Lake) is the reconciler's job, NOT
// Validate.
type LakeLocation struct {
	// Bucket is the name of a Bucket in this service's namespace.
	Bucket ObjectName `json:"bucket"`
	// Prefix is a sub-domain (BucketPrefix.name) of that bucket; a DNS-1123 label.
	Prefix string `json:"prefix" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
}

// LakeStatus is the observed state of a Lake (ADR-0086): the reconciler reflects
// readiness (Ready once the backing duckdb Function is up), the materialized backing Function, and
// the published Quack endpoint. It embeds the shared Status (Phase + Conditions) so the controller's
// generic write-back seam (StatusObject.GetStatus) points at the live status — there is NO separate
// /status route.
type LakeStatus struct {
	Status `json:",inline"`
	// Function is the engine identity, "<name>-duckdb" (computed by the reconciler). No Function object has this
	// name: the engine runs as the provider worker named for the Lake (ADR-0086, repointed at the engine).
	Function ObjectName `json:"function,omitempty"`
	// Endpoint is the published Quack URL — the backing function's ingress path (computed).
	Endpoint string `json:"endpoint,omitempty"`
	// ProxyPort is the port of the lake's PEP proxy listener (ADR-0162), recorded so a restarted daemon binds the
	// same port again and the URL consumers hold keeps reaching this lake (computed).
	ProxyPort int `json:"proxyPort,omitempty"`
}

// GroupVersionKind returns the constant GVK for Lake.
func (c *Lake) GroupVersionKind() GroupVersionKind { return KindLake.GVK() }

// isPrincipalObject marks *Lake as a cedar PrincipalObject (ADR-0116/0088): an add-on provider
// resolved as an S3 principal (its spec.bindings.blob are its blobBindings) when no same-named Function exists.
// The marker keeps the cedar.PrincipalObject interface closed (only api/types implements it).
func (c *Lake) isPrincipalObject() {}

// Validate performs envelope validation via the shared validateMeta helper, then the
// LakeSpec rules JSON Schema can't express (spec.bindings.blob and spec.bindings.config take the Function binding
// checks, FunctionSpec.validateBindings): each spec.bindings.blob entry's alias + prefix is a DNS-1123 label and the
// alias is unique within Blob; Lake.Prefix is a DNS-1123 label; and the lake (bucket, prefix) is one of
// the spec.bindings.blob bindings (so the engine gets the per-fn S3 keypair and can write its own lake — ADR-0086).
// Cross-resource validity (the bucket/prefix actually EXIST in the namespace) is the lake-blob-validity
// admission, NOT structural Validate.
func (c *Lake) Validate() error {
	const op = "Lake.Validate"
	if err := validateMeta(c.TypeMeta, &c.ObjectMeta, KindLake); err != nil {
		return err
	}
	// spec.bindings.blob and spec.bindings.config have the shape of a Function's bindings and take their checks. Each runs at its own
	// place in the order, which decides the error an input that breaks several rules gets.
	if err := (&FunctionSpec{Bindings: FunctionBindings{Blob: c.Spec.Bindings.Blob}}).validateBindings(op, "spec.bindings"); err != nil {
		return err
	}
	if !dnsLabel.MatchString(c.Spec.Location.Prefix) {
		return fault.Invalidf(op, "spec.location.prefix %q is not a valid DNS-1123 label", c.Spec.Location.Prefix)
	}
	// The lake (bucket, prefix) must be one of the spec.bindings.blob bindings (ADR-0086: "the bound
	// (bucket, prefix) ... OWNED by this service's function"). This is load-bearing for durability:
	// the backing Function injects the per-fn S3 keypair only when spec.bindings.blob is non-empty (ADR-0085),
	// and the engine writes its own DuckLake catalog via that keypair — so the lake prefix must be
	// a declared, owned blob binding or the whole-object-Put checkpoint is 403-denied.
	// (This also implies spec.bindings.blob is non-empty whenever a Lake is set.) It is an intra-object
	// rule (no store needed); cross-resource existence is the lake-blob-validity admission.
	lakeBound := false
	for _, b := range c.Spec.Bindings.Blob {
		if b.Bucket == c.Spec.Location.Bucket && b.Prefix == c.Spec.Location.Prefix {
			lakeBound = true
			break
		}
	}
	if !lakeBound {
		return fault.Invalidf(op, "spec.location (%q/%q) must be one of spec.bindings.blob (the lake prefix must be a bound, owned blob binding so the engine can write it)",
			c.Spec.Location.Bucket, c.Spec.Location.Prefix)
	}
	// spec.bindings.secret / spec.bindings.config each name a Secret / ConfigMap in this namespace whose Data is
	// injected into the engine env (ADR-0087, the ADR-0057 convention). Structural Validate checks
	// only that each is a valid DNS-1123 ObjectName; cross-resource existence + read authorization
	// is the reconciler's resolution step (PDP-authorized, ADR-0057), not Validate.
	for _, s := range c.Spec.Bindings.Secret {
		if !dnsLabel.MatchString(string(s)) {
			return fault.Invalidf(op, "spec.bindings.secret entry %q is not a valid DNS-1123 name", s)
		}
	}
	if err := (&FunctionSpec{Bindings: FunctionBindings{Config: c.Spec.Bindings.Config}}).validateBindings(op, "spec.bindings"); err != nil {
		return err
	}
	// spec.ingress (ADR-0241 Decision 9): the shared shape, without auth, since the PEP proxy authenticates.
	if c.Spec.Ingress != nil && c.Spec.Ingress.Auth != nil {
		return fault.Invalidf(op, "spec.ingress.auth is not allowed: the lake::query PEP proxy authenticates the caller")
	}
	if err := c.Spec.Ingress.validate(op, "spec.ingress"); err != nil {
		return err
	}
	if err := c.Spec.Resources.Validate(op, "spec.resources"); err != nil {
		return err
	}
	roots := rootSections{ConfigMaps: c.Spec.ConfigMaps, Buckets: c.Spec.Buckets, Secrets: c.Spec.SecretDeclarations}
	return roots.validate(op, c.ObjectMeta)
}

// validateNestedLakes refuses a lake entry at path that declares stores, ConfigMaps or Secrets of its own
// (ADR-0251 Decision 2): a lake nested in a Function or an App binds what its declarer's root declares. declarer
// names the declarer's kind with its article ("a Function").
func validateNestedLakes(op, path, declarer string, lakes []LakeEntry) error {
	kind := declarer[strings.Index(declarer, " ")+1:]
	for i := range lakes {
		if f := lakes[i].rootField(); f != "" {
			return fault.Invalidf(op, "%s[%d].%s: a lake nested in %s declares nothing at its root; declare it at the %s's root",
				path, i, f, declarer, kind)
		}
	}
	return nil
}

// GetStatus returns the shared Status pointer, implementing StatusObject (the controller's write-back seam).
func (c *Lake) GetStatus() *Status { return &c.Status.Status }
