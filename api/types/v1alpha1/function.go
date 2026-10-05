package v1alpha1

import (
	"time"

	"github.com/pyvvo/funcd/api/fault"
)

// Function is a namespaced resource representing a deployable function/agent.
// Status-bearing. Behavioral spec fields owned by F10–F14.
//
// NOTE: `json:",inline"` flattens TypeMeta because stdlib encoding/json treats an empty
// tag name as "anonymous → promote fields" (the `inline` option itself is a no-op in
// stdlib; it's a yaml/k8s-codec convention). ObjectMeta keeps the name "metadata", so
// it nests. The roundtrip test guards this — do not rename the TypeMeta tag.
type Function struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       FunctionSpec   `json:"spec"`
	Status     FunctionStatus `json:"status,omitempty"`
}

// The bounds of an external invoke's response deadline (ADR-0151): FunctionSpec.Timeout and invoke.defaultTimeout.
const (
	MaxInvokeTimeout     = time.Hour
	DefaultInvokeTimeout = 60 * time.Second
)

// FunctionSpec holds the desired state. Behavioral fields are appended by feature ADRs:
//
//	runtime               → F12 (runtime port)
//	handler/artifact/shape → F13 (function contract & lifecycle)
//	scaling                → F11 (scale-to-zero)  [this field, ADR-0016]
//	triggers               → F10/F16 (routes / eventing)
//	services               → F14 (KV) and the service pattern
type FunctionSpec struct {
	// Scaling configures scale-to-zero / idle reclaim (ADR-0016, F11). V1 honors
	// MinReplicas (0 enables scale-to-zero) and IdleTimeout (0 disables reclaim);
	// MaxReplicas is recorded for forward-compatibility (1→N autoscaling is V3).
	Scaling Scaling `json:"scaling,omitempty"`
	// Runtime is the runtime class / language image (e.g. "nodejs22", "python312") — F13/ADR-0020.
	// Typed RuntimeName so its DNS-1123 constraint reaches the OpenAPI schema (ADR-0048).
	Runtime RuntimeName `json:"runtime,omitempty"`
	// Handler is the entrypoint the shim resolves at materialization (e.g. "app.handler") — F13.
	Handler string `json:"handler,omitempty" pattern:"^[A-Za-z_][A-Za-z0-9_.]*$"`
	// Image is the source artifact OCI ref (oci-layout:// · file:// · a bare registry ref; the tag at the
	// string end), pulled + served by the runtime — F13/ADR-0020, flattened by ADR-0097 (was spec.artifact.uri).
	Image string `json:"image,omitempty"`
	// ImageDigest is the content digest pinned into the stamped Revision ("what was validated ships"),
	// system-set at materialization (ADR-0035) — ADR-0097 (was spec.artifact.digest).
	ImageDigest string `json:"imageDigest,omitempty" pattern:"^sha256:[a-f0-9]{64}$"`
	// Replicas is the manual replica count (>=0); the effective count also honors Scaling + the
	// activator's wake (ADR-0016/0020) — F13.
	Replicas int `json:"replicas,omitempty" minimum:"0" maximum:"15"`
	// Timeout bounds an external invoke until its response starts; 0 ⇒ invoke.defaultTimeout. External
	// invokes only: links and steps keep their own limits (ADR-0151).
	Timeout time.Duration `json:"timeout,omitempty" minimum:"0" maximum:"3600000000000"`
	// Pooling is the per-function worker-pooling opt-in (ADR-0046, F28). Empty ⇒ solo (own
	// worker, the default). Functions sharing (namespace, runtime, Pooling.Worker) and the same
	// access (bindings, owned data, grants) co-locate as handlers in one pool worker; status.pool
	// names it.
	Pooling Pooling `json:"pooling,omitempty"`
	// Secrets names the Secret resources in this function's namespace whose Data is injected
	// into the worker as env vars at materialization (ADR-0057, F15 last mile). Each named
	// Secret's Data keys become env-var names; reserved FUNCD_* keys are never overridable.
	// Empty ⇒ no secret injection. Only the NAMES are persisted; resolved values are never
	// stored on the Function (they live only in the worker's env). Resolution is PDP-authorized
	// (ADR-0022/0018); an unauthorized or missing Secret fails the function closed (not Ready).
	Secrets []ObjectName `json:"secrets,omitempty"`
	// Config names the ConfigMap resources in this function's namespace whose Data is injected
	// into the worker as env vars at materialization (ADR-0093), mirroring Secrets but for
	// NON-sensitive config (a plain store read, no PDP). Config is merged BEFORE Secrets, so a
	// bound Secret overrides a config default. Reserved FUNCD_* keys are never overridable.
	// Empty ⇒ no config injection. Fails closed (not Ready, ConfigResolveFailed) if a named
	// ConfigMap is missing.
	Config []ObjectName `json:"config,omitempty"`
	// Links declares synchronous fn-to-fn RPC dependencies (ADR-0064, F33): each binds a local
	// alias to a target Function in this namespace, callable from the handler as
	// context.invoke(alias, input). The alias is the capability — with no matching link, invoke
	// fails closed. Empty ⇒ no links. Cross-resource validity (target exists, acyclic) is an
	// admission (ADR-0063); only the structural rules are checked in Validate.
	Links []FunctionLink `json:"links,omitempty"`
	// KV declares this function's KV bindings (ADR-0073, F42): each binds a local alias to a (store,
	// table) sub-domain in this namespace, reachable from the handler as context.kv.<verb>(alias, …).
	// The binding IS the capability — with no matching entry, context.kv on that alias is Forbidden
	// (default-deny). Mirrors spec.links (wrangler-style: naming lives on the consumer). Empty ⇒ no
	// KV access. Cross-resource validity (the store/table exist) is an admission (ADR-0063); only the
	// structural rules (alias is a unique DNS-1123 label) are checked in Validate.
	KV []FunctionKV `json:"kv,omitempty"`
	// Blob declares this function's blob (S3) bindings (ADR-0080, F47): each binds a local alias to a
	// (bucket, prefix) sub-domain in this namespace, reachable from the handler / its DuckDB as
	// s3://<bucket>/<prefix>/…. The binding IS the read capability — with no matching entry, blob access
	// on that alias is Forbidden (default-deny); writes require this function to be the prefix's owner.
	// Mirrors spec.kv (wrangler-style: naming lives on the consumer). Empty ⇒ no blob access.
	// Cross-resource validity (the bucket/prefix exist) is an admission; only the structural rules
	// (alias is a unique DNS-1123 label) are checked in Validate.
	Blob []FunctionBlob `json:"blob,omitempty"`
	// Catalogs declares this function's CatalogService consumer bindings (ADR-0091, F61): each binds a
	// local alias to a CatalogService in this namespace, injected into the worker as
	// FUNCD_CATALOG_<ALIAS>_URL (the catalog's status.endpoint) + FUNCD_CATALOG_<ALIAS>_TOKEN (its
	// QUACK_TOKEN). Declaring the binding IS the grant — only a declaring function receives the token;
	// it is never surfaced in CatalogService.status. The reconcile requeues (fail-closed) until the
	// bound catalog is Ready. Empty ⇒ no catalog consumption. Cross-resource validity (the catalog
	// exists) is an admission; only the structural rules (alias is a unique DNS-1123 label) are Validate.
	Catalogs []FunctionCatalog `json:"catalogs,omitempty"`
}

// FunctionCatalog binds this function to a CatalogService it consumes (ADR-0091, F61). Declaring the
// binding is the grant: funcd injects FUNCD_CATALOG_<ALIAS>_URL/_TOKEN, and (V2) grants egress.
type FunctionCatalog struct {
	// Alias names the FUNCD_CATALOG_<ALIAS>_* env pair; a DNS-1123 label, unique within Catalogs.
	Alias string `json:"alias" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	// Catalog is the name of a CatalogService in this function's namespace.
	Catalog ObjectName `json:"catalog"`
}

// FunctionLink declares one synchronous RPC dependency: a local alias bound to a target Function
// in the same namespace (ADR-0064, F33). The latest-Ready revision of the target is called.
type FunctionLink struct {
	// Alias is the local name the handler passes to invoke; a DNS-1123 label, unique within Links.
	Alias string `json:"alias" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	// Target is the name of a Function in this function's namespace.
	Target ObjectName `json:"target"`
	// Timeout bounds the synchronous wait (incl. a cold-start wake); int64 ns, 0 ⇒ platform
	// default, ≤5m.
	Timeout time.Duration `json:"timeout,omitempty" minimum:"0" maximum:"300000000000"`
}

// FunctionKV declares one KV binding (ADR-0073, F42): a local alias bound to a (store, table)
// sub-domain in the same namespace. The handler reaches it as context.kv.<verb>(alias, …); reads are
// coarse-allowed for any bound same-namespace caller, writes require this function to be the table's
// owner. Mirrors FunctionLink (the wrangler/spec.links convention).
type FunctionKV struct {
	// Alias is the local handle the handler passes to context.kv; a DNS-1123 label, unique within KV.
	Alias string `json:"alias" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	// Store is the name of a KVStore in this function's namespace.
	Store ObjectName `json:"store"`
	// Table is a sub-domain (KVStore.spec.tables[].name) of that store; a DNS-1123 label.
	Table string `json:"table" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
}

// FunctionBlob declares one blob (S3) binding (ADR-0080, F47): a local alias bound to a (bucket, prefix)
// sub-domain in the same namespace. The binding is the read grant; writes require this function to be the
// prefix's owner. Mirrors FunctionKV (the wrangler/spec.kv convention).
type FunctionBlob struct {
	// Alias is the local handle; a DNS-1123 label, unique within Blob.
	Alias string `json:"alias" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	// Bucket is the name of a Bucket in this function's namespace.
	Bucket ObjectName `json:"bucket"`
	// Prefix is a sub-domain (BucketPrefix.name) of that bucket; a DNS-1123 label.
	Prefix string `json:"prefix" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
}

// Pooling is the per-function pooling opt-in (ADR-0046, F28). Named "Pooling" (not
// "Placement") to stay clear of scheduler.Placement (node selection, ADR-0017): pooling
// answers which shared worker within a node a function joins.
type Pooling struct {
	// Worker is an owner-chosen worker id. Empty ⇒ solo (own worker). Functions sharing
	// (namespace, runtime, Worker) co-locate as handlers in one worker_threads pool worker.
	Worker string `json:"worker,omitempty"`
}

// Scaling is the F11 scale-to-zero policy on a Function (ADR-0016). Field validation
// (non-negative, MinReplicas ≤ MaxReplicas) is the API server's admission job (P-L/F07);
// the activator reads these defensively.
type Scaling struct {
	MinReplicas int           `json:"minReplicas,omitempty" minimum:"0" maximum:"15"`             // 0 enables scale-to-zero
	MaxReplicas int           `json:"maxReplicas,omitempty" minimum:"0" maximum:"15"`             // recorded; 1→N enforcement is V3
	IdleTimeout time.Duration `json:"idleTimeout,omitempty" minimum:"0" maximum:"86400000000000"` // reclaim delay, int64 ns; 0≤≤24h, 0 disables
}

// FunctionStatus holds the observed state. Behavioral fields appended by F11/F13.
type FunctionStatus struct {
	Status          `json:",inline"`
	Replicas        int    `json:"replicas,omitempty"`        // workers that listened and get calls (ADR-0161)
	CurrentRevision string `json:"currentRevision,omitempty"` // the latest stamped Revision name (ADR-0020)
	// ServingRevision is the Revision whose workers receive the calls (ADR-0143); empty until the first deploy serves,
	// and again once no replica is desired.
	ServingRevision string `json:"servingRevision,omitempty"`
	// DrainingRevision is the Revision demoted at the last switch while any of its workers remain, and DrainingSince
	// the time of that switch (ADR-0143); both are cleared together.
	DrainingRevision string     `json:"drainingRevision,omitempty"`
	DrainingSince    *time.Time `json:"drainingSince,omitempty"`
	// Pool is the pool worker a pooled Function runs in, "<runtime>/<worker>/<access>"; empty when solo.
	Pool string `json:"pool,omitempty"`
}

// GroupVersionKind returns the constant GVK for Function.
func (f *Function) GroupVersionKind() GroupVersionKind { return KindFunction.GVK() }

// PrincipalObject is a resolved principal-backing object for the cedar capability registry (ADR-0116):
// a *Function or a *CatalogService whose declared bindings (spec.kv/links/blob) are materialized onto
// the Cedar principal. It is a CLOSED interface — its marker method is unexported, so only types in
// this package implement it, which lets the registry's Bind funcs type-switch on it without any.
type PrincipalObject interface{ isPrincipalObject() }

// isPrincipalObject marks *Function as a PrincipalObject (ADR-0116): a resolved principal-backing
// object whose spec.kv/links/blob bindings the capability registry materializes onto the Cedar
// principal.
func (f *Function) isPrincipalObject() {}

// Validate performs envelope validation via the shared validateMeta helper, then the
// Function-spec semantic rules JSON Schema can't express (ADR-0046, ADR-0048): Pooling.Worker
// is a DNS-1123 label when set; the cross-field and conditional-presence checks in
// FunctionSpec.Validate. Field-local constraints (runtime/handler/uri patterns, replica bounds)
// are schema-enforced at the edge and are not re-checked here.
func (f *Function) Validate() error {
	if err := validateMeta(f.TypeMeta, &f.ObjectMeta, KindFunction); err != nil {
		return err
	}
	if w := f.Spec.Pooling.Worker; w != "" && !dnsLabel.MatchString(w) {
		return fault.Invalidf("Function.Validate", "spec.pooling.worker %q is not a valid DNS-1123 label", w)
	}
	return f.Spec.Validate()
}

// Validate enforces the FunctionSpec rule JSON Schema can't express (ADR-0048): the Scaling
// cross-field bound (minReplicas ≤ maxReplicas when maxReplicas > 0). It deliberately does NOT
// re-check field PRESENCE (runtime/handler/artifact non-empty) — that is the materialization
// shape gate's job (ADR-0020: NewBasicValidator + the shim write ShapeValid:False and block
// Ready), and re-checking it here would duplicate a layer (one constraint, one layer). Field
// FORMAT (handler/digest patterns, replica/duration bounds) is schema-enforced at the edge.
func (s *FunctionSpec) Validate() error {
	const op = "FunctionSpec.Validate"
	if s.Scaling.MaxReplicas > 0 && s.Scaling.MinReplicas > s.Scaling.MaxReplicas {
		return fault.Invalidf(op, "spec.scaling.minReplicas (%d) must not exceed maxReplicas (%d)",
			s.Scaling.MinReplicas, s.Scaling.MaxReplicas)
	}
	// Link structural rules (ADR-0064): alias is a DNS-1123 label, unique within Links; target is a
	// DNS-1123 label. Cross-resource rules (target exists, no cycle, no self-link) are an admission.
	seen := make(map[string]bool, len(s.Links))
	for _, l := range s.Links {
		if !dnsLabel.MatchString(l.Alias) {
			return fault.Invalidf(op, "spec.links alias %q is not a valid DNS-1123 label", l.Alias)
		}
		if seen[l.Alias] {
			return fault.Invalidf(op, "spec.links alias %q is duplicated", l.Alias)
		}
		seen[l.Alias] = true
		if !dnsLabel.MatchString(string(l.Target)) {
			return fault.Invalidf(op, "spec.links[%s].target %q is not a valid Function name", l.Alias, l.Target)
		}
	}
	// KV binding structural rules (ADR-0073): alias is a DNS-1123 label, unique within KV; table is a
	// DNS-1123 label. Cross-resource rules (the store/table exist) are an admission.
	kvSeen := make(map[string]bool, len(s.KV))
	for _, b := range s.KV {
		if !dnsLabel.MatchString(b.Alias) {
			return fault.Invalidf(op, "spec.kv alias %q is not a valid DNS-1123 label", b.Alias)
		}
		if kvSeen[b.Alias] {
			return fault.Invalidf(op, "spec.kv alias %q is duplicated", b.Alias)
		}
		kvSeen[b.Alias] = true
		if !dnsLabel.MatchString(b.Table) {
			return fault.Invalidf(op, "spec.kv[%s].table %q is not a valid DNS-1123 label", b.Alias, b.Table)
		}
	}
	// Blob binding structural rules (ADR-0080): alias is a DNS-1123 label, unique within Blob; prefix is a
	// DNS-1123 label. Cross-resource rules (the bucket/prefix exist) are an admission.
	blobSeen := make(map[string]bool, len(s.Blob))
	for _, b := range s.Blob {
		if !dnsLabel.MatchString(b.Alias) {
			return fault.Invalidf(op, "spec.blob alias %q is not a valid DNS-1123 label", b.Alias)
		}
		if blobSeen[b.Alias] {
			return fault.Invalidf(op, "spec.blob alias %q is duplicated", b.Alias)
		}
		blobSeen[b.Alias] = true
		if !dnsLabel.MatchString(b.Prefix) {
			return fault.Invalidf(op, "spec.blob[%s].prefix %q is not a valid DNS-1123 label", b.Alias, b.Prefix)
		}
	}
	// Catalog binding structural rules (ADR-0091): alias is a DNS-1123 label, unique within Catalogs;
	// catalog is a DNS-1123 label. Cross-resource validity (the CatalogService exists) is an admission.
	catSeen := make(map[string]bool, len(s.Catalogs))
	for _, c := range s.Catalogs {
		if !dnsLabel.MatchString(c.Alias) {
			return fault.Invalidf(op, "spec.catalogs alias %q is not a valid DNS-1123 label", c.Alias)
		}
		if catSeen[c.Alias] {
			return fault.Invalidf(op, "spec.catalogs alias %q is duplicated", c.Alias)
		}
		catSeen[c.Alias] = true
		if !dnsLabel.MatchString(string(c.Catalog)) {
			return fault.Invalidf(op, "spec.catalogs[%s].catalog %q is not a valid CatalogService name", c.Alias, c.Catalog)
		}
	}
	// Config binding structural rules (ADR-0093): each names a ConfigMap in this namespace and
	// must be a valid DNS-1123 ObjectName (mirrors CatalogService.spec.config). Cross-resource
	// existence is the reconciler's plain store read (fail-closed, ConfigResolveFailed), not Validate.
	for _, cm := range s.Config {
		if !dnsLabel.MatchString(string(cm)) {
			return fault.Invalidf(op, "spec.config entry %q is not a valid DNS-1123 name", cm)
		}
	}
	return nil
}

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (f *Function) GetStatus() *Status { return &f.Status.Status }
