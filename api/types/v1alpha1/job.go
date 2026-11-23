package v1alpha1

import (
	"slices"
	"time"

	"github.com/pyvvo/funcd/api/fault"
)

// Job is a namespaced, status-bearing resource: work that runs to completion, built as a Function is from the fields
// it shares with one, whose each execution is a JobRun (ADR-0347 Decision 1).
type Job struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       JobSpec   `json:"spec"`
	Status     JobStatus `json:"status,omitempty"`
}

// The bounds of a Job's spec.timeout and spec.concurrency (ADR-0347 Decision 1): a timeout as a Workflow run's, and a
// concurrency as a Function's replicas.
const (
	MaxJobTimeout     = MaxWorkflowTimeout
	MaxJobConcurrency = 15
)

// JobSpec holds a Function's fields with their meaning and checks, then the Job's own: Timeout bounds each call of a
// run, and Concurrency is how many of its runs may hold a worker at once (ADR-0347 Decision 1). It has no scaling,
// replicas, pooling, protocol or ingress.
type JobSpec struct {
	Runtime            RuntimeName         `json:"runtime,omitempty" doc:"The runtime that runs the code, such as nodejs22 or python314. A Job without one is not Ready, with ShapeInvalid, and one whose runtime this node cannot run is not Ready, with RuntimeUnavailable."`
	Handler            string              `json:"handler,omitempty" pattern:"^[A-Za-z_][A-Za-z0-9_.]*$" doc:"The name of the export that the shim calls once for each run, such as handle. A Job without one, or whose code has no such export, is not Ready, with ShapeInvalid."`
	Image              string              `json:"image,omitempty" doc:"The OCI reference of the code's artifact: a registry reference with a tag or a digest, or an oci-layout:// reference to a layout on the server's disk. A Job whose image is missing, or cannot be resolved or pulled, starts no run."`
	ImageDigest        string              `json:"imageDigest,omitempty" pattern:"^sha256:[a-f0-9]{64}$" doc:"The sha256 digest that funcd pulls the image by and checks it against. Unset, funcd resolves the image to its digest when it creates the Revision of a new spec and keeps that digest, so a tag that moves later changes nothing."`
	Resources          ResourceSpec        `json:"resources,omitzero" doc:"The CPU and memory caps of each run's worker; a field left empty takes functions.defaultResources. Only the containerd runtime enforces them."`
	KVStores           []KVStoreEntry      `json:"kv,omitempty" doc:"The KVStores the Job declares: by name it creates and owns one, and a store of that name that it does not own holds it in Pending with StoreNotOwned; by ref it uses an existing one. spec.bindings.kv binds only a declared store."`
	Buckets            []BucketEntry       `json:"buckets,omitempty" doc:"The Buckets the Job declares: by name it creates and owns one, and a Bucket of that name that it does not own holds it in Pending with StoreNotOwned; by ref it uses an existing one. spec.bindings.blob binds only a declared Bucket."`
	SQLDatabases       []SQLDatabaseEntry  `json:"sql,omitempty" doc:"The SQLDatabases the Job declares: by name it creates and owns one, and a database of that name that it does not own holds it in Pending with StoreNotOwned; by ref it uses an existing one. spec.bindings.sql binds only a declared database."`
	Lakes              []LakeEntry         `json:"lakes,omitempty" doc:"The Lakes the Job declares: by name it creates one that goes with the Job, by ref it uses an existing one. spec.bindings.lake binds only a declared Lake, and a Lake declared by name declares nothing at its own root: it binds the Job's buckets, configMaps and secrets."`
	ConfigMaps         []ConfigMapEntry    `json:"configMaps,omitempty" doc:"The ConfigMaps the Job declares: by name it defines one from its data, which goes with the Job, by ref it uses an existing one. spec.bindings.config binds only a declared ConfigMap."`
	SecretDeclarations []SecretDeclaration `json:"secrets,omitempty" doc:"The Secrets of the namespace that the bindings may name, each with the keys the code reads; spec.bindings.secret names only a declared Secret. The Job waits in Pending, with SecretNotFound or SecretKeyMissing, until each one exists with every declared key."`
	Volumes            []VolumeEntry       `json:"volumes,omitempty" doc:"The directories each run's worker mounts. Apply refuses a Job with one unless runtime.mode is containerd and volumes.capacity or volumes.capacityBytes is set. A local volume is the Job's own, never a same-named Function's, and allows a concurrency of at most 1."`
	Bindings           FunctionBindings    `json:"bindings,omitzero" doc:"What the code may use: KV tables, Bucket prefixes, SQL databases, Lakes and Functions, each under an alias, and ConfigMaps and Secrets. A binding is the grant: a call on an alias with no binding is refused with 403. Each store, Lake, ConfigMap and Secret it names must be declared at the root, in this spec's kv, buckets, sql, lakes, configMaps or secrets."`
	Timeout            Duration            `json:"timeout,omitempty" doc:"The bound on each call of a run's handler, its preview's and its apply's, from its worker's start: 0s to 168h; 0s or unset means none. A run past it ends Failed with RunTimedOut, and its worker stops."`
	Concurrency        int                 `json:"concurrency,omitempty" minimum:"1" maximum:"15" doc:"How many of the Job's runs may hold a worker at once, those Previewing included: 1 to 15; unset means 1, and a local volume allows only 1. A run past it waits in Pending with Queued."`
	Approval           JobApproval         `json:"approval,omitzero" doc:"Whether a run waits for a person's approval of its preview before it runs. Unset, every run previews and waits."`
}

// The bounds and default of a Job's spec.approval.expiry (ADR-0348 Decision 1).
const (
	MinJobApprovalExpiry     = Duration(time.Minute)
	MaxJobApprovalExpiry     = Duration(168 * time.Hour)
	DefaultJobApprovalExpiry = Duration(24 * time.Hour)
)

// JobApproval is how a Job's runs are approved (ADR-0348 Decision 1): with Bypass a run runs at once, without it a run
// previews first and waits for a person's approval of that preview.
type JobApproval struct {
	Bypass bool     `json:"bypass,omitempty" doc:"When true, a run runs at once, with no preview and no approval. Unset or false, a run first calls the code's preview, then waits in AwaitingApproval until a person approves or rejects it. A Job for unattended work sets it."`
	Expiry Duration `json:"expiry,omitempty" doc:"How long a preview stays valid for its approval: 1m to 168h; unset means 24h. A preview older than this when it is approved, or when its run's slot frees after the approval, is previewed again: the run runs when the new preview is the same document, and waits for a new approval when it differs."`
}

// ExpiryOrDefault is the approval's expiry, 24h when unset.
func (a JobApproval) ExpiryOrDefault() Duration {
	if a.Expiry == 0 {
		return DefaultJobApprovalExpiry
	}
	return a.Expiry
}

// JobStatus holds the observed state: the Revision of the latest spec, and the input and output schemas of its image.
type JobStatus struct {
	Status          `json:",inline"`
	CurrentRevision ObjectName        `json:"currentRevision,omitempty" doc:"The Revision of the latest spec, named <job>-v<generation>. A run pins the current one when it starts, and keeps it until it ends."`
	Contract        *WorkflowContract `json:"contract,omitempty" doc:"The input and output schemas of the current Revision's image, read by its digest. A new run's input is checked against the input schema; an image with none holds the Job with ContractMissing."`
}

// GroupVersionKind returns the constant GVK for Job.
func (j *Job) GroupVersionKind() GroupVersionKind { return KindJob.GVK() }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (j *Job) GetStatus() *Status { return &j.Status.Status }

// Validate runs the envelope check, the Function's checks over the fields a Job shares with it, and the bounds of the
// Job's own: spec.timeout 0s to 168h, and spec.concurrency 1 to 15, at most 1 with a local volume (ADR-0347 Decision 1).
func (j *Job) Validate() error {
	const op = "Job.Validate"
	if err := validateMeta(j.TypeMeta, &j.ObjectMeta, KindJob); err != nil {
		return err
	}
	shared := j.Spec.FunctionSpec()
	if err := validateNestedLakes(op, "spec.lakes", "a Job", shared.Lakes); err != nil {
		return err
	}
	roots := rootSections{ConfigMaps: shared.ConfigMaps, KV: shared.KVStores, Buckets: shared.Buckets,
		SQL: shared.SQLDatabases, Lakes: shared.Lakes, Secrets: shared.SecretDeclarations}
	if err := roots.validate(op, j.ObjectMeta); err != nil {
		return err
	}
	if err := shared.Validate(); err != nil {
		return err
	}
	if err := CheckDuration(op, "spec.timeout", j.Spec.Timeout, 0, MaxJobTimeout); err != nil {
		return err
	}
	c := j.Spec.Concurrency
	if c < 0 || c > MaxJobConcurrency {
		return fault.Invalidf(op, "spec.concurrency %d is outside 1 to %d", c, MaxJobConcurrency)
	}
	if i := slices.IndexFunc(j.Spec.Volumes, func(v VolumeEntry) bool { return v.Type == VolumeLocal }); i >= 0 && c > 1 {
		return fault.Invalidf(op, "spec.concurrency %d with spec.volumes[%d], a local volume: a local volume allows at most 1 run at once", c, i)
	}
	if e := j.Spec.Approval.Expiry; e != 0 {
		return CheckDuration(op, "spec.approval.expiry", e, MinJobApprovalExpiry, MaxJobApprovalExpiry)
	}
	return nil
}

// isPrincipalObject marks *Job as a PrincipalObject (ADR-0351 Decision 1): the principal of its runs' workers, whose
// bindings the capability registry materializes as a Function's.
func (j *Job) isPrincipalObject() {}

// FunctionSpec is the Job's fields that a Function shares, as a FunctionSpec, so that the Function's checks and hooks
// run over them unchanged; the Function's own fields stay zero (ADR-0347 Decisions 1 and 5).
func (s *JobSpec) FunctionSpec() FunctionSpec {
	return FunctionSpec{Runtime: s.Runtime, Handler: s.Handler, Image: s.Image, ImageDigest: s.ImageDigest,
		Resources: s.Resources, KVStores: s.KVStores, Buckets: s.Buckets, SQLDatabases: s.SQLDatabases, Lakes: s.Lakes,
		ConfigMaps: s.ConfigMaps, SecretDeclarations: s.SecretDeclarations, Volumes: s.Volumes, Bindings: s.Bindings}
}
