package v1alpha1

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"

	huma "github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
)

// maxAppNameLength keeps a later <app>-<number> name a DNS label (ADR-0199 Decision 1).
const maxAppNameLength = 52

// App is a namespaced, status-bearing resource that declares a whole app in typed sections (ADR-0199, F113): the
// App reconciler writes its parts, reports one status and prunes what a new spec drops; the owner GC removes the tree
// on delete and keeps the data of a retained store.
type App struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       AppSpec   `json:"spec"`
	Status     AppStatus `json:"status,omitempty"`
}

// AppSpec is the App's parts, one section per kind, applied in this order (Decision 2).
type AppSpec struct {
	// Version is a free label that funcd does not interpret.
	Version      string           `json:"version,omitempty"`
	KV           []AppKVStore     `json:"kv,omitempty"`
	Buckets      []AppBucket      `json:"buckets,omitempty"`
	Functions    []AppFunction    `json:"functions,omitempty"`
	Workflows    []AppWorkflow    `json:"workflows,omitempty"`
	EventSources []AppEventSource `json:"eventSources,omitempty"`
	Sensors      []AppSensor      `json:"sensors,omitempty"`
	Routes       []AppRoute       `json:"routes,omitempty"`
	Sites        []AppSite        `json:"sites,omitempty"`
	Catalogs     []AppCatalog     `json:"catalogs,omitempty"`
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

// AppStatus is the observed state (Decision 5): phase Deploying, Ready or Degraded, the Ready condition, and the
// state of each child.
type AppStatus struct {
	Status   `json:",inline"`
	Children []AppChild `json:"children,omitempty"`
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
// each part valid as its kind; no entry named like an object another part's reconciler writes. The rules that need
// the store (quotas, a ref to an object this App controls) are the app-parts admission's.
func (a *App) Validate() error {
	const op = "App.Validate"
	if err := validateMeta(a.TypeMeta, &a.ObjectMeta, KindApp); err != nil {
		return err
	}
	if len(a.Name) > maxAppNameLength {
		return fault.Invalidf(op, "metadata.name %q is longer than %d characters", a.Name, maxAppNameLength)
	}
	seen := make(map[ObjectRef]string)
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
		if e.part == nil {
			continue
		}
		declared[id] = e.path
		if err := e.part.Validate(); err != nil {
			return partError(op, e.path, err)
		}
	}
	return a.validateWriters(op, declared)
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

// entries lists every section entry in section order.
func (a *App) entries() []appEntry {
	meta := func(n ObjectName) ObjectMeta {
		return ObjectMeta{Name: n, Namespace: a.Namespace, ResourceGroup: a.ResourceGroup}
	}
	s := &a.Spec
	var out []appEntry
	for i, e := range s.KV {
		out = append(out, newEntry("kv", i, entryHead{e.Name, e.Ref, e.Deletion}, e.KVStoreSpec,
			&KVStore{TypeMeta: typeMetaFor(KindKVStore), ObjectMeta: meta(e.Name), Spec: e.KVStoreSpec}))
	}
	for i, e := range s.Buckets {
		out = append(out, newEntry("buckets", i, entryHead{e.Name, e.Ref, e.Deletion}, e.BucketSpec,
			&Bucket{TypeMeta: typeMetaFor(KindBucket), ObjectMeta: meta(e.Name), Spec: e.BucketSpec}))
	}
	for i, e := range s.Functions {
		out = append(out, newEntry("functions", i, entryHead{Name: e.Name, Ref: e.Ref}, e.FunctionSpec,
			&Function{TypeMeta: typeMetaFor(KindFunction), ObjectMeta: meta(e.Name), Spec: e.FunctionSpec}))
	}
	for i, e := range s.Workflows {
		out = append(out, newEntry("workflows", i, entryHead{Name: e.Name, Ref: e.Ref}, e.WorkflowSpec,
			&Workflow{TypeMeta: typeMetaFor(KindWorkflow), ObjectMeta: meta(e.Name), Spec: e.WorkflowSpec}))
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
		out = append(out, newEntry("catalogs", i, entryHead{Name: e.Name, Ref: e.Ref}, e.CatalogServiceSpec,
			&CatalogService{TypeMeta: typeMetaFor(KindCatalogService), ObjectMeta: meta(e.Name), Spec: e.CatalogServiceSpec}))
	}
	return out
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
	KVStoreSpec | BucketSpec | FunctionSpec | WorkflowSpec | EventSourceSpec | SensorSpec | RouteSpec | SiteSpec |
		CatalogServiceSpec
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
