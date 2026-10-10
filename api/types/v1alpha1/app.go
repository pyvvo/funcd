package v1alpha1

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	huma "github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
)

// maxAppNameLength keeps a later <app>-<number> name a DNS label (ADR-0199 Decision 1).
const maxAppNameLength = 52

// maxAppConfigMapNameLength keeps a defined ConfigMap's stored name, <name>-<10 hex>, a DNS label (ADR-0213 Decision 2).
const maxAppConfigMapNameLength = 52

// App is a namespaced, status-bearing resource that declares a whole app in typed sections (ADR-0199, F113): the
// App reconciler writes its parts, reports one status and prunes what a new spec drops; the owner GC removes the tree
// on delete and keeps the data of a retained store.
type App struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       AppSpec   `json:"spec"`
	Status     AppStatus `json:"status,omitempty"`
}

// AppSpec is the App's parts, one section per kind: configMaps first, so a ConfigMap is written before the parts that
// name it (ADR-0213 Decision 4), then the other sections in this order (Decision 2).
type AppSpec struct {
	// Version is a free label that funcd does not interpret.
	Version string `json:"version,omitempty"`
	// Paused stops every write of the App (ADR-0212); it is not part of an AppRevision.
	Paused       bool             `json:"paused,omitempty"`
	KV           []AppKVStore     `json:"kv,omitempty"`
	Buckets      []AppBucket      `json:"buckets,omitempty"`
	Functions    []AppFunction    `json:"functions,omitempty"`
	Workflows    []AppWorkflow    `json:"workflows,omitempty"`
	EventSources []AppEventSource `json:"eventSources,omitempty"`
	Sensors      []AppSensor      `json:"sensors,omitempty"`
	Routes       []AppRoute       `json:"routes,omitempty"`
	Sites        []AppSite        `json:"sites,omitempty"`
	Catalogs     []AppCatalog     `json:"catalogs,omitempty"`
	ConfigMaps   []AppConfigMap   `json:"configMaps,omitempty"`
	// Secrets declares the Secrets the parts name, never their values (ADR-0213 Decision 6).
	Secrets []AppSecret `json:"secrets,omitempty"`
	// Hooks names the Functions of this App called before its parts change and after a new revision is current
	// (ADR-0214).
	Hooks *AppHooks `json:"hooks,omitempty"`
}

// AppHooks are the App's lifecycle hooks (ADR-0214 Decision 1), each list called in order: preApply before the parts
// change, postApply once the new revision is current.
type AppHooks struct {
	PreApply  []AppHook `json:"preApply,omitempty"`
	PostApply []AppHook `json:"postApply,omitempty"`
}

// AppHook names the functions entry of this App, not a ref, that a hook point calls.
type AppHook struct {
	Function ObjectName `json:"function"`
}

// AppHookInput is the data of each hook call of one AppRevision, fixed at its stamp (ADR-0214 Decision 2).
type AppHookInput struct {
	Event       string     `json:"event" enum:"install,upgrade,rollback"`
	App         ObjectName `json:"app"`
	From        ObjectName `json:"from,omitempty"`
	To          ObjectName `json:"to"`
	FromVersion string     `json:"fromVersion,omitempty"`
	ToVersion   string     `json:"toVersion,omitempty"`
}

// WithoutPause returns s with Paused cleared: what the stamp compares, an AppRevision freezes and rollback compares
// (ADR-0212 Decision 3).
func (s AppSpec) WithoutPause() AppSpec {
	s.Paused = false
	return s
}

// AppKVStore is a kv entry: a KVStore the App declares by name, or only the ref of an existing one.
type AppKVStore struct {
	Name ObjectName `json:"name,omitempty"`
	Ref  ObjectName `json:"ref,omitempty"`
	// Deletion is retain (or empty, the default: the store and its data outlive the App) or delete.
	Deletion    DeletionPolicy `json:"deletion,omitempty"`
	KVStoreSpec `json:",inline"`
}

// AppBucket is a buckets entry: a Bucket the App declares by name, or only the ref of an existing one.
type AppBucket struct {
	Name ObjectName `json:"name,omitempty"`
	Ref  ObjectName `json:"ref,omitempty"`
	// Deletion is retain (or empty, the default: the Bucket and its objects outlive the App) or delete.
	Deletion   DeletionPolicy `json:"deletion,omitempty"`
	BucketSpec `json:",inline"`
}

// AppFunction is a functions entry: a Function the App declares by name, or only the ref of an existing one.
type AppFunction struct {
	Name         ObjectName `json:"name,omitempty"`
	Ref          ObjectName `json:"ref,omitempty"`
	FunctionSpec `json:",inline"`
}

// AppWorkflow is a workflows entry: a Workflow the App declares by name, or only the ref of an existing one.
type AppWorkflow struct {
	Name         ObjectName `json:"name,omitempty"`
	Ref          ObjectName `json:"ref,omitempty"`
	WorkflowSpec `json:",inline"`
}

// AppEventSource is an eventSources entry: an EventSource the App declares by name, or only the ref of an existing one.
type AppEventSource struct {
	Name            ObjectName `json:"name,omitempty"`
	Ref             ObjectName `json:"ref,omitempty"`
	EventSourceSpec `json:",inline"`
}

// AppSensor is a sensors entry: a Sensor the App declares by name, or only the ref of an existing one.
type AppSensor struct {
	Name       ObjectName `json:"name,omitempty"`
	Ref        ObjectName `json:"ref,omitempty"`
	SensorSpec `json:",inline"`
}

// AppRoute is a routes entry: a Route the App declares by name, or only the ref of an existing one.
type AppRoute struct {
	Name      ObjectName `json:"name,omitempty"`
	Ref       ObjectName `json:"ref,omitempty"`
	RouteSpec `json:",inline"`
}

// AppSite is a sites entry: a Site the App declares by name, or only the ref of an existing one.
type AppSite struct {
	Name     ObjectName `json:"name,omitempty"`
	Ref      ObjectName `json:"ref,omitempty"`
	SiteSpec `json:",inline"`
}

// AppCatalog is a catalogs entry: a CatalogService the App declares by name, or only the ref of an existing one.
type AppCatalog struct {
	Name               ObjectName `json:"name,omitempty"`
	Ref                ObjectName `json:"ref,omitempty"`
	CatalogServiceSpec `json:",inline"`
}

// AppConfigMap is a configMaps entry: a ConfigMap the App defines, stored as AppConfigMapName(name, spec), or only the
// ref of an existing one (ADR-0213 Decision 2).
type AppConfigMap struct {
	Name          ObjectName `json:"name,omitempty"`
	Ref           ObjectName `json:"ref,omitempty"`
	ConfigMapSpec `json:",inline"`
}

// AppSecret declares a Secret of the App's namespace that a part names, and the keys the parts read (ADR-0213
// Decision 6). The App never holds, writes or owns the Secret.
type AppSecret struct {
	Name        ObjectName `json:"name"`
	Description string     `json:"description,omitempty"`
	Keys        []string   `json:"keys"`
}

// AppConfigMapName is the stored name of a configMaps entry the App defines (ADR-0213 Decision 2): "<name>-" and 10 hex
// characters, the first 5 bytes of SHA-256 over json.Marshal(spec), which writes the data keys sorted, so the name
// follows the data alone.
func AppConfigMapName(name ObjectName, spec ConfigMapSpec) ObjectName {
	b, _ := json.Marshal(spec) // a struct of a map[string]string always marshals
	sum := sha256.Sum256(b)
	return ObjectName(fmt.Sprintf("%s-%x", name, sum[:5]))
}

// Schema is ConfigMapSpec's schema plus name and ref (see entrySchema).
func (AppConfigMap) Schema(r huma.Registry) *huma.Schema {
	s := entrySchema(r, reflect.TypeFor[ConfigMapSpec](), false)
	s.Properties["name"].Description = "The ConfigMap's name, at most 52 characters. It is stored as <name>-<hash of data>, " +
		"and the config of each part that names it is set to that stored name."
	return s
}

// Schema is closed: name and at least one key, each an env-var name; description is free text (ADR-0213 Decision 6).
func (AppSecret) Schema(huma.Registry) *huma.Schema {
	one := 1
	name := dnsLabelSchema()
	name.Description = "The name of a Secret in the App's namespace. The platform holds its value; the App never writes it."
	return &huma.Schema{
		Type: huma.TypeObject,
		Properties: map[string]*huma.Schema{
			"name":        name,
			"description": {Type: huma.TypeString, Description: "What the Secret is for, for the installer who provides it."},
			"keys": {
				Type:        huma.TypeArray,
				Description: "The keys the parts read: the Secret's data must hold each of them.",
				MinItems:    &one,
				Items:       &huma.Schema{Type: huma.TypeString, Pattern: envName.String()},
			},
		},
		Required:             []string{"name", "keys"},
		AdditionalProperties: false,
	}
}

// Schema is KVStoreSpec's schema plus name, ref and deletion (see entrySchema).
func (AppKVStore) Schema(r huma.Registry) *huma.Schema {
	return entrySchema(r, reflect.TypeFor[KVStoreSpec](), true)
}

// Schema is BucketSpec's schema plus name, ref and deletion (see entrySchema).
func (AppBucket) Schema(r huma.Registry) *huma.Schema {
	return entrySchema(r, reflect.TypeFor[BucketSpec](), true)
}

// Schema is FunctionSpec's schema plus name and ref (see entrySchema).
func (AppFunction) Schema(r huma.Registry) *huma.Schema {
	return entrySchema(r, reflect.TypeFor[FunctionSpec](), false)
}

// Schema is WorkflowSpec's schema plus name and ref (see entrySchema).
func (AppWorkflow) Schema(r huma.Registry) *huma.Schema {
	return entrySchema(r, reflect.TypeFor[WorkflowSpec](), false)
}

// Schema is EventSourceSpec's schema plus name and ref (see entrySchema).
func (AppEventSource) Schema(r huma.Registry) *huma.Schema {
	return entrySchema(r, reflect.TypeFor[EventSourceSpec](), false)
}

// Schema is SensorSpec's schema plus name and ref (see entrySchema).
func (AppSensor) Schema(r huma.Registry) *huma.Schema {
	return entrySchema(r, reflect.TypeFor[SensorSpec](), false)
}

// Schema is RouteSpec's schema plus name and ref (see entrySchema).
func (AppRoute) Schema(r huma.Registry) *huma.Schema {
	return entrySchema(r, reflect.TypeFor[RouteSpec](), false)
}

// Schema is SiteSpec's schema plus name and ref (see entrySchema).
func (AppSite) Schema(r huma.Registry) *huma.Schema {
	return entrySchema(r, reflect.TypeFor[SiteSpec](), false)
}

// Schema is CatalogServiceSpec's schema plus name and ref (see entrySchema).
func (AppCatalog) Schema(r huma.Registry) *huma.Schema {
	return entrySchema(r, reflect.TypeFor[CatalogServiceSpec](), false)
}

// MarshalJSON writes a ref entry as its ref alone (see marshalEntry).
func (e AppConfigMap) MarshalJSON() ([]byte, error) {
	return marshalEntry(entryHead{Name: e.Name, Ref: e.Ref}, e.ConfigMapSpec)
}

// MarshalJSON writes a ref entry as its ref alone (see marshalEntry).
func (e AppKVStore) MarshalJSON() ([]byte, error) {
	return marshalEntry(entryHead{e.Name, e.Ref, e.Deletion}, e.KVStoreSpec)
}

// MarshalJSON writes a ref entry as its ref alone (see marshalEntry).
func (e AppBucket) MarshalJSON() ([]byte, error) {
	return marshalEntry(entryHead{e.Name, e.Ref, e.Deletion}, e.BucketSpec)
}

// MarshalJSON writes a ref entry as its ref alone (see marshalEntry).
func (e AppFunction) MarshalJSON() ([]byte, error) {
	return marshalEntry(entryHead{Name: e.Name, Ref: e.Ref}, e.FunctionSpec)
}

// MarshalJSON writes a ref entry as its ref alone (see marshalEntry).
func (e AppWorkflow) MarshalJSON() ([]byte, error) {
	return marshalEntry(entryHead{Name: e.Name, Ref: e.Ref}, e.WorkflowSpec)
}

// MarshalJSON writes a ref entry as its ref alone (see marshalEntry).
func (e AppEventSource) MarshalJSON() ([]byte, error) {
	return marshalEntry(entryHead{Name: e.Name, Ref: e.Ref}, e.EventSourceSpec)
}

// MarshalJSON writes a ref entry as its ref alone (see marshalEntry).
func (e AppSensor) MarshalJSON() ([]byte, error) {
	return marshalEntry(entryHead{Name: e.Name, Ref: e.Ref}, e.SensorSpec)
}

// MarshalJSON writes a ref entry as its ref alone (see marshalEntry).
func (e AppRoute) MarshalJSON() ([]byte, error) {
	return marshalEntry(entryHead{Name: e.Name, Ref: e.Ref}, e.RouteSpec)
}

// MarshalJSON writes a ref entry as its ref alone (see marshalEntry).
func (e AppSite) MarshalJSON() ([]byte, error) {
	return marshalEntry(entryHead{Name: e.Name, Ref: e.Ref}, e.SiteSpec)
}

// MarshalJSON writes a ref entry as its ref alone (see marshalEntry).
func (e AppCatalog) MarshalJSON() ([]byte, error) {
	return marshalEntry(entryHead{Name: e.Name, Ref: e.Ref}, e.CatalogServiceSpec)
}

// AppStatus is the observed state (ADR-0199 Decision 5): phase Deploying, Ready, Degraded or Failed (ADR-0200
// Decision 6), the Ready condition, the current and latest AppRevision, the state of each child and the last part the
// App wrote back (ADR-0212).
type AppStatus struct {
	Status          `json:",inline"`
	CurrentRevision ObjectName `json:"currentRevision,omitempty"`
	LatestRevision  ObjectName `json:"latestRevision,omitempty"`
	// Version is the spec.version of the current revision.
	Version      string       `json:"version,omitempty"`
	Children     []AppChild   `json:"children,omitempty"`
	LastSelfHeal *AppSelfHeal `json:"lastSelfHeal,omitempty"`
}

// AppSelfHeal is the last part the App wrote back (ADR-0212 Decision 2).
type AppSelfHeal struct {
	Kind Kind       `json:"kind"`
	Name ObjectName `json:"name"`
	At   Timestamp  `json:"at"`
}

// AppChild is a declared part, or a dropped object not yet deleted (state Pruning), with its state and reason.
type AppChild struct {
	Kind   Kind          `json:"kind"`
	Name   ObjectName    `json:"name"`
	State  AppChildState `json:"state"`
	Reason string        `json:"reason,omitempty"`
}

// AppChildState is the state of an App's child (Decision 5).
type AppChildState string

// The states of an App's child: a part counts toward the App's Ready condition; a Pruning object does not.
const (
	AppChildReady      AppChildState = "Ready"
	AppChildNotStarted AppChildState = "NotStarted"
	AppChildPending    AppChildState = "Pending"
	AppChildPruning    AppChildState = "Pruning"
)

// Schema carries AppChildState's enum into the generated OpenAPI (ADR-0048).
func (AppChildState) Schema(huma.Registry) *huma.Schema {
	return enumSchema(string(AppChildReady), string(AppChildNotStarted), string(AppChildPending), string(AppChildPruning))
}

// GroupVersionKind returns the constant GVK for App.
func (a *App) GroupVersionKind() GroupVersionKind { return KindApp.GVK() }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (a *App) GetStatus() *Status { return &a.Status.Status }

// Parts returns each declared entry, in section order, as its kind's object: the entry's name and spec, the App's
// namespace and resource group, and no owner references. A ref entry is not a part.
func (a *App) Parts() []Object {
	var out []Object
	for _, e := range a.entries() {
		if e.part != nil {
			out = append(out, e.part)
		}
	}
	return out
}

// Refs returns the object each ref entry names, in section order.
func (a *App) Refs() []ObjectRef {
	var out []ObjectRef
	for _, e := range a.entries() {
		if e.head.Ref != "" {
			out = append(out, ObjectRef{Kind: e.kind, Namespace: a.Namespace, Name: e.head.Ref})
		}
	}
	return out
}

// Validate enforces the store-free rules of Decision 3, each refusal naming its field path: a name of at most 52
// characters; each entry either a name or a ref alone; deletion retain or delete; no name repeated within a section;
// a defined ConfigMap name of at most 52 characters and no two entries with one stored name (ADR-0213 Decision 2);
// each part valid as its kind; no entry named like an object another part's reconciler writes; the secrets rules
// (ADR-0213 Decision 6). The rules that need the store (quotas, a ref to an object this App controls) and the
// undeclared-Secret rule (ADR-0213 Decision 7) are the app-parts admission's.
func (a *App) Validate() error {
	const op = "App.Validate"
	if err := validateMeta(a.TypeMeta, &a.ObjectMeta, KindApp); err != nil {
		return err
	}
	if len(a.Name) > maxAppNameLength {
		return fault.Invalidf(op, "metadata.name %q is longer than %d characters", a.Name, maxAppNameLength)
	}
	seen := make(map[ObjectRef]string)
	stored := make(map[ObjectRef]string)
	declared := make(map[ObjectRef]string)
	for _, e := range a.entries() {
		if err := e.validate(op); err != nil {
			return err
		}
		id := ObjectRef{Kind: e.kind, Name: cmp.Or(e.head.Name, e.head.Ref)}
		if prev, dup := seen[id]; dup {
			return fault.Invalidf(op, "%s repeats the name %q of %s", e.path, id.Name, prev)
		}
		seen[id] = e.path
		if e.kind == KindConfigMap && e.part != nil && len(e.head.Name) > maxAppConfigMapNameLength {
			return fault.Invalidf(op, "%s.name %q is longer than %d characters", e.path, e.head.Name, maxAppConfigMapNameLength)
		}
		obj := ObjectRef{Kind: e.kind, Name: e.object()}
		if prev, dup := stored[obj]; dup {
			return fault.Invalidf(op, "%s and %s are both stored as %s/%s", prev, e.path, obj.Kind, obj.Name)
		}
		stored[obj] = e.path
		if e.part == nil {
			continue
		}
		declared[id] = e.path
		if err := e.part.Validate(); err != nil {
			return partError(op, e.path, err)
		}
	}
	if err := a.validateWriters(op, declared); err != nil {
		return err
	}
	if err := a.validateSecrets(op); err != nil {
		return err
	}
	return a.validateHooks(op)
}

// validateHooks refuses a hook that names no functions entry of this App or a ref entry, a name twice in one list, and
// a pre-hook Function linking to a functions entry that is neither a ref nor a pre-hook: that entry is written after
// the pre-hooks, so the link would reach no Function on an install and the old spec on an upgrade (ADR-0214 Decision
// 1).
func (a *App) validateHooks(op string) error {
	h := a.Spec.Hooks
	if h == nil {
		return nil
	}
	declared := make(map[ObjectName]*AppFunction, len(a.Spec.Functions))
	for i := range a.Spec.Functions {
		if f := &a.Spec.Functions[i]; f.Ref == "" {
			declared[f.Name] = f
		}
	}
	for _, point := range []struct {
		name  string
		hooks []AppHook
	}{{"preApply", h.PreApply}, {"postApply", h.PostApply}} {
		seen := make(map[ObjectName]string, len(point.hooks))
		for i, hk := range point.hooks {
			path := fmt.Sprintf("spec.hooks.%s[%d].function", point.name, i)
			if declared[hk.Function] == nil {
				return fault.Invalidf(op, "%s %q is not a function of this App", path, hk.Function)
			}
			if prev, dup := seen[hk.Function]; dup {
				return fault.Invalidf(op, "%s repeats %q of %s", path, hk.Function, prev)
			}
			seen[hk.Function] = path
		}
	}
	pre := make(map[ObjectName]bool, len(h.PreApply))
	for _, hk := range h.PreApply {
		pre[hk.Function] = true
	}
	for i, hk := range h.PreApply {
		for _, l := range declared[hk.Function].Links {
			if declared[l.Target] != nil && !pre[l.Target] {
				return fault.Invalidf(op, "spec.hooks.preApply[%d].function %q links to %q, written after the pre-hooks", i, hk.Function, l.Target)
			}
		}
	}
	return nil
}

// validateSecrets refuses a secrets entry whose name is not a DNS label or repeats another's, or whose keys are empty,
// repeat a key or hold one that is not an env-var name (ADR-0213 Decision 6).
func (a *App) validateSecrets(op string) error {
	names := make(map[ObjectName]string, len(a.Spec.Secrets))
	for i, s := range a.Spec.Secrets {
		path := fmt.Sprintf("spec.secrets[%d]", i)
		if !dnsLabel.MatchString(string(s.Name)) {
			return fault.Invalidf(op, "%s.name %q is not a valid DNS-1123 label", path, s.Name)
		}
		if prev, dup := names[s.Name]; dup {
			return fault.Invalidf(op, "%s repeats the name %q of %s", path, s.Name, prev)
		}
		names[s.Name] = path
		if len(s.Keys) == 0 {
			return fault.Invalidf(op, "%s.keys is empty: declare at least one key the parts read", path)
		}
		keys := make(map[string]string, len(s.Keys))
		for j, k := range s.Keys {
			field := fmt.Sprintf("%s.keys[%d]", path, j)
			if err := validateEnvKey(op, field, k); err != nil {
				return err
			}
			if prev, dup := keys[k]; dup {
				return fault.Invalidf(op, "%s repeats the key %q of %s", field, k, prev)
			}
			keys[k] = field
		}
	}
	return nil
}

// validateWriters refuses an entry named like an object another part's reconciler writes, since the two would
// overwrite each other (Decision 3): a Site's Route (<site>) and Bucket (bucket.name), a Workflow's step Function
// (<workflow>-<step>) and kv stores. declared maps each declared entry's object to its path.
func (a *App) validateWriters(op string, declared map[ObjectRef]string) error {
	clash := func(field string, kind Kind, name ObjectName) error {
		if p, ok := declared[ObjectRef{Kind: kind, Name: name}]; ok {
			return fault.Invalidf(op, "%s and %s.name both write %s/%s: one object would have two writers", field, p, kind, name)
		}
		return nil
	}
	for i := range a.Spec.Sites {
		s := &a.Spec.Sites[i]
		if s.Ref != "" {
			continue
		}
		if err := clash(fmt.Sprintf("spec.sites[%d].name", i), KindRoute, s.Name); err != nil {
			return err
		}
		if err := clash(fmt.Sprintf("spec.sites[%d].bucket.name", i), KindBucket, s.Bucket.Name); err != nil {
			return err
		}
	}
	for i := range a.Spec.Workflows {
		w := &a.Spec.Workflows[i]
		if w.Ref != "" {
			continue
		}
		for j := range w.Steps {
			st := &w.Steps[j]
			if st.Function == nil || st.Function.Image == "" {
				continue
			}
			if err := clash(fmt.Sprintf("spec.workflows[%d].steps[%d].name", i, j), KindFunction, StepFunctionName(w.Name, st.Name)); err != nil {
				return err
			}
		}
		for j := range w.KV {
			if err := clash(fmt.Sprintf("spec.workflows[%d].kv[%d].name", i, j), KindKVStore, w.KV[j].Name); err != nil {
				return err
			}
		}
	}
	return nil
}

// appEntry is one section entry: its field path, its kind, the fields every section shares, whether it sets a spec
// field, and its part (nil for a ref entry).
type appEntry struct {
	path     string
	kind     Kind
	head     entryHead
	setsSpec bool
	part     Object
}

// entries lists every section entry in section order, configMaps first (ADR-0213 Decision 4). Each part is built from
// a copy of its entry's spec in which every config name of a defined configMaps entry is that entry's stored name
// (ADR-0213 Decision 3); the App's spec stays as declared.
func (a *App) entries() []appEntry {
	meta := func(n ObjectName) ObjectMeta {
		return ObjectMeta{Name: n, Namespace: a.Namespace, ResourceGroup: a.ResourceGroup}
	}
	s := &a.Spec
	var out []appEntry
	stored := make(map[ObjectName]ObjectName, len(s.ConfigMaps))
	for i, e := range s.ConfigMaps {
		name := e.Name
		if e.Ref == "" && e.Name != "" {
			name = AppConfigMapName(e.Name, e.ConfigMapSpec)
			stored[e.Name] = name
		}
		out = append(out, newEntry("configMaps", i, entryHead{Name: e.Name, Ref: e.Ref}, e.ConfigMapSpec,
			&ConfigMap{TypeMeta: typeMetaFor(KindConfigMap), ObjectMeta: meta(name), Spec: e.ConfigMapSpec}))
	}
	for i, e := range s.KV {
		out = append(out, newEntry("kv", i, entryHead{e.Name, e.Ref, e.Deletion}, e.KVStoreSpec,
			&KVStore{TypeMeta: typeMetaFor(KindKVStore), ObjectMeta: meta(e.Name), Spec: e.KVStoreSpec}))
	}
	for i, e := range s.Buckets {
		out = append(out, newEntry("buckets", i, entryHead{e.Name, e.Ref, e.Deletion}, e.BucketSpec,
			&Bucket{TypeMeta: typeMetaFor(KindBucket), ObjectMeta: meta(e.Name), Spec: e.BucketSpec}))
	}
	for i, e := range s.Functions {
		spec := e.FunctionSpec
		spec.Config = repoint(spec.Config, stored)
		out = append(out, newEntry("functions", i, entryHead{Name: e.Name, Ref: e.Ref}, e.FunctionSpec,
			&Function{TypeMeta: typeMetaFor(KindFunction), ObjectMeta: meta(e.Name), Spec: spec}))
	}
	for i, e := range s.Workflows {
		out = append(out, newEntry("workflows", i, entryHead{Name: e.Name, Ref: e.Ref}, e.WorkflowSpec,
			&Workflow{TypeMeta: typeMetaFor(KindWorkflow), ObjectMeta: meta(e.Name), Spec: repointSteps(e.WorkflowSpec, stored)}))
	}
	for i, e := range s.EventSources {
		out = append(out, newEntry("eventSources", i, entryHead{Name: e.Name, Ref: e.Ref}, e.EventSourceSpec,
			&EventSource{TypeMeta: typeMetaFor(KindEventSource), ObjectMeta: meta(e.Name), Spec: e.EventSourceSpec}))
	}
	for i, e := range s.Sensors {
		out = append(out, newEntry("sensors", i, entryHead{Name: e.Name, Ref: e.Ref}, e.SensorSpec,
			&Sensor{TypeMeta: typeMetaFor(KindSensor), ObjectMeta: meta(e.Name), Spec: e.SensorSpec}))
	}
	for i, e := range s.Routes {
		out = append(out, newEntry("routes", i, entryHead{Name: e.Name, Ref: e.Ref}, e.RouteSpec,
			&Route{TypeMeta: typeMetaFor(KindRoute), ObjectMeta: meta(e.Name), Spec: e.RouteSpec}))
	}
	for i, e := range s.Sites {
		out = append(out, newEntry("sites", i, entryHead{Name: e.Name, Ref: e.Ref}, e.SiteSpec,
			&Site{TypeMeta: typeMetaFor(KindSite), ObjectMeta: meta(e.Name), Spec: e.SiteSpec}))
	}
	for i, e := range s.Catalogs {
		spec := e.CatalogServiceSpec
		spec.Config = repoint(spec.Config, stored)
		out = append(out, newEntry("catalogs", i, entryHead{Name: e.Name, Ref: e.Ref}, e.CatalogServiceSpec,
			&CatalogService{TypeMeta: typeMetaFor(KindCatalogService), ObjectMeta: meta(e.Name), Spec: spec}))
	}
	return out
}

// repoint returns names with each name of a defined configMaps entry replaced by its stored name, in a new slice when
// one is replaced, so the entry's spec is not changed (ADR-0213 Decision 3).
func repoint(names []ObjectName, stored map[ObjectName]ObjectName) []ObjectName {
	var out []ObjectName
	for i, n := range names {
		if s, ok := stored[n]; ok {
			if out == nil {
				out = slices.Clone(names)
			}
			out[i] = s
		}
	}
	if out == nil {
		return names
	}
	return out
}

// repointSteps returns spec with each image step's function.config repointed on copies of its steps, the config that
// the Workflow copies into the step Function (ADR-0213 Decision 3).
func repointSteps(spec WorkflowSpec, stored map[ObjectName]ObjectName) WorkflowSpec {
	if len(stored) == 0 {
		return spec
	}
	spec.Steps = slices.Clone(spec.Steps)
	for j := range spec.Steps {
		if f := spec.Steps[j].Function; f != nil && f.Image != "" {
			fc := *f
			fc.Config = repoint(f.Config, stored)
			spec.Steps[j].Function = &fc
		}
	}
	return spec
}

func newEntry[S partSpec](section string, i int, head entryHead, spec S, part Object) appEntry {
	e := appEntry{
		path:     fmt.Sprintf("spec.%s[%d]", section, i),
		kind:     part.GroupVersionKind().Kind,
		head:     head,
		setsSpec: !reflect.ValueOf(spec).IsZero(),
	}
	if head.Ref == "" {
		e.part = part
	}
	return e
}

// object is the name of the object the entry names: its part's, which a defined ConfigMap stores under
// AppConfigMapName, or its ref.
func (e *appEntry) object() ObjectName {
	if e.part != nil {
		return e.part.GetObjectMeta().Name
	}
	return e.head.Ref
}

// validate checks the entry's own fields: a name, or a ref and nothing else (Decision 2).
func (e *appEntry) validate(op string) error {
	h := e.head
	switch {
	case h.Name == "" && h.Ref == "":
		return fault.Invalidf(op, "%s sets neither name nor ref", e.path)
	case h.Ref != "" && (h.Name != "" || h.Deletion != "" || e.setsSpec):
		return fault.Invalidf(op, "%s sets ref and another field: a ref entry only names an existing %s", e.path, e.kind)
	case h.Ref != "":
		if !dnsLabel.MatchString(string(h.Ref)) {
			return fault.Invalidf(op, "%s.ref %q is not a valid DNS-1123 label", e.path, h.Ref)
		}
		return nil
	}
	if !dnsLabel.MatchString(string(h.Name)) {
		return fault.Invalidf(op, "%s.name %q is not a valid DNS-1123 label", e.path, h.Name)
	}
	switch h.Deletion {
	case "", DeletionRetain, DeletionDelete:
		return nil
	default:
		return fault.Invalidf(op, "%s.deletion %q must be %q or %q", e.path, h.Deletion, DeletionRetain, DeletionDelete)
	}
}

// partError puts a part's refusal at its entry's path: a kind's Validate names fields from the root of its own
// spec, which the App holds at the entry (spec.tables[0].name of a KVStore is spec.kv[0].tables[0].name).
func partError(op, path string, err error) error {
	msg := err.Error()
	var fe *fault.Error
	if errors.As(err, &fe) {
		msg = strings.TrimPrefix(msg, fe.Op+": ")
	}
	if rooted := strings.ReplaceAll(msg, "spec.", path+"."); rooted != msg {
		return fault.Invalidf(op, "%s", rooted)
	}
	return fault.Invalidf(op, "%s: %s", path, msg)
}

// entryHead is the fields a section entry adds to its kind's spec.
type entryHead struct {
	Name     ObjectName     `json:"name,omitempty"`
	Ref      ObjectName     `json:"ref,omitempty"`
	Deletion DeletionPolicy `json:"deletion,omitempty"`
}

// partSpec is the spec of a kind that has a section.
type partSpec interface {
	ConfigMapSpec | KVStoreSpec | BucketSpec | FunctionSpec | WorkflowSpec | EventSourceSpec | SensorSpec | RouteSpec |
		SiteSpec | CatalogServiceSpec
}

// marshalEntry writes an entry's head, then its spec's fields only when the spec is set: a ref entry goes out as
// {"ref": …}, where a zero spec would carry required nested fields (a Site's bucket.name) the API schema refuses.
func marshalEntry[S partSpec](head entryHead, spec S) ([]byte, error) {
	h, err := json.Marshal(head)
	if err != nil || reflect.ValueOf(spec).IsZero() {
		return h, err
	}
	b, err := json.Marshal(spec)
	switch {
	case err != nil:
		return nil, err
	case len(h) == len("{}"):
		return b, nil
	case len(b) == len("{}"):
		return h, nil
	}
	return append(append(h[:len(h)-1:len(h)-1], ','), b[1:]...), nil
}

// entrySchema is a section entry's schema (Decision 2): the kind's spec fields plus name, ref and, for a store,
// deletion; additionalProperties false and no required list, since a ref entry sets no spec field.
func entrySchema(r huma.Registry, spec reflect.Type, deletion bool) *huma.Schema {
	base := r.Schema(spec, true, "")
	if base.Ref != "" {
		base = r.SchemaFromRef(base.Ref)
	}
	props := make(map[string]*huma.Schema, len(base.Properties)+3)
	maps.Copy(props, base.Properties)
	name := dnsLabelSchema()
	name.Description = "The part's name. The object takes the App's namespace and resource group."
	ref := dnsLabelSchema()
	ref.Description = "The name of an existing object of this kind in the App's namespace. A ref entry sets no other field."
	props["name"], props["ref"] = name, ref
	if deletion {
		d := DeletionPolicy("").Schema(r)
		d.Description = "retain (the default) keeps the store and its data when the App drops it or is deleted; delete removes them."
		props["deletion"] = d
	}
	return &huma.Schema{Type: huma.TypeObject, Properties: props, AdditionalProperties: false}
}
