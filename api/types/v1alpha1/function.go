package v1alpha1

import (
	"fmt"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

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

// The bounds of the API duration fields, checked by each kind's Validate and named in each field's doc (ADR-0194).
const (
	MinTimerInterval   = Duration(100 * time.Millisecond)
	MaxTimerInterval   = Duration(24 * time.Hour)
	MaxLinkTimeout     = Duration(5 * time.Minute)
	MaxIdleTimeout     = Duration(24 * time.Hour)
	MaxWorkflowTimeout = Duration(168 * time.Hour)
	MaxStepTimeout     = Duration(24 * time.Hour)
	MaxRetryBackoff    = Duration(time.Hour)
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
	// Protocol is how the shim calls the code (ADR-0243): "cloudevent", the default, or "http", an http(app) export
	// that serves every request and that only the edge calls.
	Protocol FunctionProtocol `json:"protocol,omitempty"`
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
	Timeout Duration `json:"timeout,omitempty" doc:"Bounds an external invoke until its response starts: 0s to 1h; 0s or unset means invoke.defaultTimeout."`
	// Pooling is the per-function worker-pooling opt-in (ADR-0046, F28). Empty ⇒ solo (own
	// worker, the default). Functions sharing (namespace, runtime, Pooling.Worker) and the same
	// access (bindings, owned data, grants) co-locate as handlers in one pool worker; status.pool
	// names it.
	Pooling Pooling `json:"pooling,omitempty"`
	// Bindings are what the Function's code uses (ADR-0246): each binding is its grant, named like the ctx accessor
	// that reaches it.
	Bindings FunctionBindings `json:"bindings,omitzero"`
	// KVStores, Buckets, SQLDatabases, Lakes and ConfigMaps are what the Function declares at its root
	// (ADR-0248): by name it owns the resource, which carries its marker; by ref it binds an existing one. An owner
	// left out of a store's tables, prefixes or database is the Function (the function-store-owner admission).
	KVStores     []KVStoreEntry     `json:"kv,omitempty"`
	Buckets      []BucketEntry      `json:"buckets,omitempty"`
	SQLDatabases []SQLDatabaseEntry `json:"sql,omitempty"`
	Lakes        []LakeEntry        `json:"lakes,omitempty"`
	ConfigMaps   []ConfigMapEntry   `json:"configMaps,omitempty"`
	// SecretDeclarations are the Secrets the Function's bindings name, with the keys it reads (ADR-0213 Decision 8).
	SecretDeclarations []SecretDeclaration `json:"secrets,omitempty"`
	// Ingress is the Function's edge exposure (ADR-0241): unset, it answers by name at /function/<name> while its
	// namespace is implicit; a host or a path gives it a Route of its own.
	Ingress *Ingress `json:"ingress,omitempty"`
}

// FunctionSQL binds this function to a SQLDatabase of its namespace under a local alias (ADR-0233).
type FunctionSQL struct {
	Alias    string     `json:"alias" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	Database ObjectName `json:"database"`
}

// FunctionLake binds this function to a Lake it consumes (ADR-0091, F61). Declaring the
// binding is the grant: funcd injects FUNCD_LAKE_<ALIAS>_URL/_TOKEN, and (V2) grants egress.
type FunctionLake struct {
	// Alias names the FUNCD_LAKE_<ALIAS>_* env pair; a DNS-1123 label, unique within Lakes.
	Alias string `json:"alias" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	// Lake is the name of a Lake in this function's namespace.
	Lake ObjectName `json:"lake"`
}

// FunctionLink declares one synchronous RPC dependency: a local alias bound to a target Function
// in the same namespace (ADR-0064, F33). The latest-Ready revision of the target is called.
type FunctionLink struct {
	// Alias is the local name the handler passes to invoke; a DNS-1123 label, unique within Links.
	Alias string `json:"alias" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	// Target is the name of a Function in this function's namespace.
	Target ObjectName `json:"target"`
	// Timeout bounds the synchronous wait (incl. a cold-start wake).
	Timeout Duration `json:"timeout,omitempty" doc:"Bounds the synchronous call, a cold-start wake included: 0s to 5m; 0s or unset means the 30s link default."`
}

// FunctionKV declares one KV binding (ADR-0073, F42): a local alias bound to a (store, table)
// sub-domain in the same namespace. The handler reaches it as context.kv.<verb>(alias, …); reads are
// coarse-allowed for any bound same-namespace caller, writes require this function to be the table's
// owner. Mirrors FunctionLink (the wrangler/spec.bindings.links convention).
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
// prefix's owner. Mirrors FunctionKV (the wrangler/spec.bindings.kv convention).
type FunctionBlob struct {
	// Alias is the local handle; a DNS-1123 label, unique within Blob.
	Alias string `json:"alias" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	// Bucket is the name of a Bucket in this function's namespace.
	Bucket ObjectName `json:"bucket"`
	// Prefix is a sub-domain (BucketPrefix.name) of that bucket; a DNS-1123 label.
	Prefix string `json:"prefix" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
}

// FunctionProtocol is how funcd calls a Function's code (ADR-0243).
type FunctionProtocol string

const (
	// ProtocolCloudEvent calls the handler with a CloudEvent, the default.
	ProtocolCloudEvent FunctionProtocol = "cloudevent"
	// ProtocolHTTP calls an http(app) export with each request the edge forwards.
	ProtocolHTTP FunctionProtocol = "http"
)

// Schema renders the huma enum for FunctionProtocol (empty accepted, normalizes to cloudevent).
func (FunctionProtocol) Schema(huma.Registry) *huma.Schema {
	return enumSchema("", string(ProtocolCloudEvent), string(ProtocolHTTP))
}

// Normalized returns the effective protocol, mapping the empty value to cloudevent.
func (p FunctionProtocol) Normalized() FunctionProtocol {
	if p == ProtocolHTTP {
		return ProtocolHTTP
	}
	return ProtocolCloudEvent
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
	MinReplicas int      `json:"minReplicas,omitempty" minimum:"0" maximum:"15"` // 0 enables scale-to-zero
	MaxReplicas int      `json:"maxReplicas,omitempty" minimum:"0" maximum:"15"` // recorded; 1→N enforcement is V3
	IdleTimeout Duration `json:"idleTimeout,omitempty" doc:"The idle time before a worker is reclaimed: 0s to 24h; 0s or unset disables reclaim."`
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
	DrainingSince    *Timestamp `json:"drainingSince,omitempty"`
	// Pool is the pool worker a pooled Function runs in, "<runtime>/<worker>/<access>"; empty when solo.
	Pool string `json:"pool,omitempty"`
}

// GroupVersionKind returns the constant GVK for Function.
func (f *Function) GroupVersionKind() GroupVersionKind { return KindFunction.GVK() }

// PrincipalObject is a resolved principal-backing object for the cedar capability registry (ADR-0116):
// a *Function or a *Lake whose declared bindings (spec.bindings.kv/links/blob) are materialized onto
// the Cedar principal. It is a CLOSED interface — its marker method is unexported, so only types in
// this package implement it, which lets the registry's Bind funcs type-switch on it without any.
type PrincipalObject interface{ isPrincipalObject() }

// isPrincipalObject marks *Function as a PrincipalObject (ADR-0116): a resolved principal-backing
// object whose spec.bindings.kv/links/blob bindings the capability registry materializes onto the Cedar
// principal.
func (f *Function) isPrincipalObject() {}

// Validate performs envelope validation via the shared validateMeta helper, then the
// Function-spec semantic rules JSON Schema can't express (ADR-0046, ADR-0048): Pooling.Worker
// is a DNS-1123 label when set; the cross-field and conditional-presence checks in
// FunctionSpec.Validate. Field-local constraints (runtime/handler/uri patterns, replica bounds)
// are schema-enforced at the edge and are not re-checked here; duration bounds are (ADR-0194).
func (f *Function) Validate() error {
	if err := validateMeta(f.TypeMeta, &f.ObjectMeta, KindFunction); err != nil {
		return err
	}
	if w := f.Spec.Pooling.Worker; w != "" && !dnsLabel.MatchString(w) {
		return fault.Invalidf("Function.Validate", "spec.pooling.worker %q is not a valid DNS-1123 label", w)
	}
	if err := f.Spec.Ingress.validate("Function.Validate", "spec.ingress"); err != nil {
		return err
	}
	// A path under /function/ would shadow a sibling's by-name address (ADR-0241 Decision 3).
	if ing := f.Spec.Ingress; ing != nil && (ing.Path == "/function" || strings.HasPrefix(ing.Path, "/function/")) {
		return fault.Invalidf("Function.Validate", "spec.ingress.path %q must not be /function or under it", ing.Path)
	}
	if err := f.Spec.validateProtocol(); err != nil {
		return err
	}
	s := &f.Spec
	if err := validateNestedLakes("Function.Validate", "spec.lakes", "a Function", s.Lakes); err != nil {
		return err
	}
	roots := rootSections{ConfigMaps: s.ConfigMaps, KV: s.KVStores, Buckets: s.Buckets, SQL: s.SQLDatabases, Lakes: s.Lakes, Secrets: s.SecretDeclarations}
	if err := roots.validate("Function.Validate", f.ObjectMeta); err != nil {
		return err
	}
	return f.Spec.Validate()
}

// Validate enforces the FunctionSpec rule JSON Schema can't express (ADR-0048): the Scaling
// cross-field bound (minReplicas ≤ maxReplicas when maxReplicas > 0) and the duration bounds
// (ADR-0194). It deliberately does NOT re-check field PRESENCE (runtime/handler/artifact non-empty)
// — that is the materialization shape gate's job (ADR-0020: NewBasicValidator + the shim write
// ShapeValid:False and block Ready), and re-checking it here would duplicate a layer (one
// constraint, one layer). Field FORMAT (handler/digest patterns, replica bounds) is schema-enforced
// at the edge.
func (s *FunctionSpec) Validate() error {
	const op = "FunctionSpec.Validate"
	if err := CheckDuration(op, "spec.timeout", s.Timeout, 0, Duration(MaxInvokeTimeout)); err != nil {
		return err
	}
	if err := CheckDuration(op, "spec.scaling.idleTimeout", s.Scaling.IdleTimeout, 0, MaxIdleTimeout); err != nil {
		return err
	}
	for i := range s.Bindings.Links {
		if err := CheckDuration(op, fmt.Sprintf("spec.bindings.links[%d].timeout", i), s.Bindings.Links[i].Timeout, 0, MaxLinkTimeout); err != nil {
			return err
		}
	}
	if s.Scaling.MaxReplicas > 0 && s.Scaling.MinReplicas > s.Scaling.MaxReplicas {
		return fault.Invalidf(op, "spec.scaling.minReplicas (%d) must not exceed maxReplicas (%d)",
			s.Scaling.MinReplicas, s.Scaling.MaxReplicas)
	}
	// Link structural rules (ADR-0064): alias is a DNS-1123 label, unique within Links; target is a
	// DNS-1123 label. Cross-resource rules (target exists, no cycle, no self-link) are an admission.
	seen := make(map[string]bool, len(s.Bindings.Links))
	for _, l := range s.Bindings.Links {
		if !dnsLabel.MatchString(l.Alias) {
			return fault.Invalidf(op, "spec.bindings.links alias %q is not a valid DNS-1123 label", l.Alias)
		}
		if seen[l.Alias] {
			return fault.Invalidf(op, "spec.bindings.links alias %q is duplicated", l.Alias)
		}
		seen[l.Alias] = true
		if !dnsLabel.MatchString(string(l.Target)) {
			return fault.Invalidf(op, "spec.bindings.links[%s].target %q is not a valid Function name", l.Alias, l.Target)
		}
	}
	return s.validateBindings(op, "spec.bindings")
}

// validateProtocol refuses a protocol other than cloudevent and http, and an http Function that is pooled or that runs
// on a runtime outside the node family, the only one whose shim serves HTTP (ADR-0243 Decision 1).
func (s *FunctionSpec) validateProtocol() error {
	const op = "Function.Validate"
	switch s.Protocol {
	case "", ProtocolCloudEvent:
		return nil
	case ProtocolHTTP:
	default:
		return fault.Invalidf(op, "spec.protocol %q is neither cloudevent nor http", s.Protocol)
	}
	if s.Pooling.Worker != "" {
		return fault.Invalidf(op, "spec.protocol http: a Function that serves HTTP is never pooled, so spec.pooling.worker %q is refused", s.Pooling.Worker)
	}
	if !s.Runtime.IsNode() {
		return fault.Invalidf(op, "spec.protocol http needs a node runtime, not %q", s.Runtime)
	}
	return nil
}

// validateBindings checks the kv, blob, lake and config bindings, naming each field under prefix. A Workflow runs
// it on each image step's bindings, which the materializer copies into the step's Function (#927).
func (s *FunctionSpec) validateBindings(op, prefix string) error {
	// KV binding structural rules (ADR-0073): alias is a DNS-1123 label, unique within KV; table is a
	// DNS-1123 label. Cross-resource rules (the store/table exist) are an admission.
	kvSeen := make(map[string]bool, len(s.Bindings.KV))
	for _, b := range s.Bindings.KV {
		if !dnsLabel.MatchString(b.Alias) {
			return fault.Invalidf(op, "%s.kv alias %q is not a valid DNS-1123 label", prefix, b.Alias)
		}
		if kvSeen[b.Alias] {
			return fault.Invalidf(op, "%s.kv alias %q is duplicated", prefix, b.Alias)
		}
		kvSeen[b.Alias] = true
		if !dnsLabel.MatchString(b.Table) {
			return fault.Invalidf(op, "%s.kv[%s].table %q is not a valid DNS-1123 label", prefix, b.Alias, b.Table)
		}
	}
	// Blob binding structural rules (ADR-0080): alias is a DNS-1123 label, unique within Blob; prefix is a
	// DNS-1123 label. Cross-resource rules (the bucket/prefix exist) are an admission.
	blobSeen := make(map[string]bool, len(s.Bindings.Blob))
	for _, b := range s.Bindings.Blob {
		if !dnsLabel.MatchString(b.Alias) {
			return fault.Invalidf(op, "%s.blob alias %q is not a valid DNS-1123 label", prefix, b.Alias)
		}
		if blobSeen[b.Alias] {
			return fault.Invalidf(op, "%s.blob alias %q is duplicated", prefix, b.Alias)
		}
		blobSeen[b.Alias] = true
		if !dnsLabel.MatchString(b.Prefix) {
			return fault.Invalidf(op, "%s.blob[%s].prefix %q is not a valid DNS-1123 label", prefix, b.Alias, b.Prefix)
		}
	}
	// Lake binding structural rules (ADR-0091): alias is a DNS-1123 label, unique within Lakes;
	// lake is a DNS-1123 label. Cross-resource validity (the Lake exists) is an admission.
	cats := make([][2]string, 0, len(s.Bindings.Lake))
	for _, c := range s.Bindings.Lake {
		cats = append(cats, [2]string{c.Alias, string(c.Lake)})
	}
	if err := checkAliasTargets(op, prefix, "lake", "lake", "Lake", cats); err != nil {
		return err
	}
	// SQL binding structural rules (ADR-0233): alias is a DNS-1123 label, unique within SQL; database is a
	// DNS-1123 name. The database's existence is reconcile-time (SQLDatabaseNotFound).
	dbs := make([][2]string, 0, len(s.Bindings.SQL))
	for _, b := range s.Bindings.SQL {
		dbs = append(dbs, [2]string{b.Alias, string(b.Database)})
	}
	if err := checkAliasTargets(op, prefix, "sql", "database", "SQLDatabase", dbs); err != nil {
		return err
	}
	// Config binding structural rules (ADR-0093): each names a ConfigMap in this namespace and
	// must be a valid DNS-1123 ObjectName (mirrors Lake.spec.bindings.config). Cross-resource
	// existence is the reconciler's plain store read (fail-closed, ConfigResolveFailed), not Validate.
	for _, cm := range s.Bindings.Config {
		if !dnsLabel.MatchString(string(cm)) {
			return fault.Invalidf(op, "%s.config entry %q is not a valid DNS-1123 name", prefix, cm)
		}
	}
	return nil
}

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (f *Function) GetStatus() *Status { return &f.Status.Status }

// checkAliasTargets checks a binding list of (alias, target) pairs: each alias a DNS-1123 label, unique in the list,
// and each target a DNS-1123 name of kind.
func checkAliasTargets(op, prefix, section, field, kind string, pairs [][2]string) error {
	seen := make(map[string]bool, len(pairs))
	for _, p := range pairs {
		alias, target := p[0], p[1]
		if !dnsLabel.MatchString(alias) {
			return fault.Invalidf(op, "%s.%s alias %q is not a valid DNS-1123 label", prefix, section, alias)
		}
		if seen[alias] {
			return fault.Invalidf(op, "%s.%s alias %q is duplicated", prefix, section, alias)
		}
		seen[alias] = true
		if !dnsLabel.MatchString(target) {
			return fault.Invalidf(op, "%s.%s[%s].%s %q is not a valid %s name", prefix, section, alias, field, target, kind)
		}
	}
	return nil
}
