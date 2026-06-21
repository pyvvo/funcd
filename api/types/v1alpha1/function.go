package v1alpha1

import (
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
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
	// Artifact is the source artifact (JS bundle / Python wheel), pinned by digest into the Revision — F13.
	Artifact ArtifactRef `json:"artifact,omitempty"`
	// Replicas is the manual replica count (>=0); the effective count also honors Scaling + the
	// activator's wake (ADR-0016/0020) — F13.
	Replicas int `json:"replicas,omitempty" minimum:"0" maximum:"15"`
	// Pooling is the per-function worker-pooling opt-in (ADR-0046, F28). Empty ⇒ solo (own
	// worker, the default). Functions sharing (namespace, runtime, Pooling.Worker) co-locate
	// as handlers in one worker_threads pool worker.
	Pooling Pooling `json:"pooling,omitempty"`
	// Secrets names the Secret resources in this function's namespace whose Data is injected
	// into the worker as env vars at materialization (ADR-0057, F15 last mile). Each named
	// Secret's Data keys become env-var names; reserved FUNCD_* keys are never overridable.
	// Empty ⇒ no secret injection. Only the NAMES are persisted; resolved values are never
	// stored on the Function (they live only in the worker's env). Resolution is PDP-authorized
	// (ADR-0022/0018); an unauthorized or missing Secret fails the function closed (not Ready).
	Secrets []ObjectName `json:"secrets,omitempty"`
	// Links declares synchronous fn-to-fn RPC dependencies (ADR-0064, F33): each binds a local
	// alias to a target Function in this namespace, callable from the handler as
	// context.invoke(alias, input). The alias is the capability — with no matching link, invoke
	// fails closed. Empty ⇒ no links. Cross-resource validity (target exists, acyclic) is an
	// admission (ADR-0063); only the structural rules are checked in Validate.
	Links []FunctionLink `json:"links,omitempty"`
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

// Pooling is the per-function pooling opt-in (ADR-0046, F28). Named "Pooling" (not
// "Placement") to stay clear of scheduler.Placement (node selection, ADR-0017): pooling
// answers which shared worker within a node a function joins.
type Pooling struct {
	// Worker is an owner-chosen worker id. Empty ⇒ solo (own worker). Functions sharing
	// (namespace, runtime, Worker) co-locate as handlers in one worker_threads pool worker.
	Worker string `json:"worker,omitempty"`
}

// ArtifactRef points at a function's source artifact (ADR-0020, F13): where it lives and
// the content digest pinned into the stamped Revision ("what was validated ships").
type ArtifactRef struct {
	URI    string `json:"uri,omitempty"` // oci-layout:// · file:// · or a bare registry ref (no scheme) — non-empty only (Validate); the materializer parses it (ADR-0031)
	Digest string `json:"digest,omitempty" pattern:"^sha256:[a-f0-9]{64}$"`
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
	Replicas        int    `json:"replicas,omitempty"`        // running worker count (ADR-0020)
	CurrentRevision string `json:"currentRevision,omitempty"` // the latest stamped Revision name (ADR-0020)
}

// GroupVersionKind returns the constant GVK for Function.
func (f *Function) GroupVersionKind() GroupVersionKind { return KindFunction.GVK() }

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
	return nil
}

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (f *Function) GetStatus() *Status { return &f.Status.Status }
