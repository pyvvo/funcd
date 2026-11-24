package v1alpha1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	huma "github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
)

// Addon registers an add-on built outside funcd (ADR-0332, FEAT-0018 F258): the runtimes it ships, each an image
// pinned by digest and the workloads it serves, one of which may run the namespaced kind its objects take (ADR-0344).
// It is cluster-scoped, so only an admin reads or writes one.
type Addon struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       AddonSpec   `json:"spec"`
	Status     AddonStatus `json:"status,omitempty"`
}

// AddonSpec lists what an Addon ships; at most one entry runs the Addon's kind (ADR-0344 Decision 1). The rules that
// read the store or the config, the runtimes' names, the plurals, the images' registry, the schema and the env
// expressions, are the control plane's.
type AddonSpec struct {
	Runtimes []ShippedRuntime `json:"runtimes" minItems:"1" doc:"The runtimes that the add-on ships, each an image and the workloads it serves. At most one entry runs the add-on's kind; an Addon whose entries run none adds no kind, and may gain one once."`
}

// ShippedRuntime is one runtime an Addon ships (ADR-0344 Decisions 1 to 5): an image, the workloads it serves, and the
// attributes those workloads take.
type ShippedRuntime struct {
	Name       RuntimeName      `json:"name" doc:"The runtime's name, which a Function's or a Job's spec.runtime names: a DNS label that is none of funcd's runtimes (nodejs22, python314, duckdb, rqlite), does not start with node or python, is no key of runtime.containerd.imageOverride, and that no other Addon ships."`
	Image      string           `json:"image,omitempty" pattern:"^[^:@\\s]+(:[0-9]+/[^:@\\s]+)?@sha256:[a-f0-9]{64}$" doc:"The image, pinned by digest as <repository>@sha256:<64 hex digits>, from a registry that addons.registries lists, and pulled as an engine's. A registry taken out of that list stops no worker or engine, and refuses the Addon's next change."`
	Workloads  []Workload       `json:"workloads" minItems:"1" doc:"What the image serves: Function and Job, which Functions and Jobs may name in spec.runtime, or Engine, alone, which runs one engine of the image per object of the entry's kind."`
	Attributes *AddonAttributes `json:"attributes,omitempty" doc:"The entry's fields for its workloads: an Engine entry's kind, config, settings and engine fields. A Function or Job entry takes none."`
}

// Workload is a kind of work a shipped runtime serves (ADR-0344 Decision 2); F270's ADR adds Engineless.
type Workload string

const (
	WorkloadFunction Workload = "Function"
	WorkloadJob      Workload = "Job"
	WorkloadEngine   Workload = "Engine"
)

// Schema carries Workload's enum into the generated OpenAPI (ADR-0048).
func (Workload) Schema(huma.Registry) *huma.Schema {
	return enumSchema(string(WorkloadFunction), string(WorkloadJob), string(WorkloadEngine))
}

// WorkloadOf is the workload that an object of kind k runs as on a shipped runtime: Function for a Function, Job for a
// Job; "" for any other kind.
func WorkloadOf(k Kind) Workload {
	switch k {
	case KindFunction:
		return WorkloadFunction
	case KindJob:
		return WorkloadJob
	}
	return ""
}

// AddonAttributes hold an entry's fields for its workloads (ADR-0344 Decision 3): an Engine entry's kind, config and
// settings (ADR-0340) and ADR-0332's engine fields, port and readiness required.
type AddonAttributes struct {
	Kind         string            `json:"kind,omitempty" pattern:"^[A-Z][A-Za-z0-9]{1,62}$" doc:"The namespaced kind that the entry runs: an upper-case letter, then 1 to 62 letters or digits. Its objects are served at /apis/funcd.io/v1alpha1/namespaces/<namespace>/<plural>, where the plural is the kind in lower case with an s. funcd refuses a kind that is its own, or whose plural is a path funcd serves or another Addon's plural, whatever the case. An Addon gains its kind once: it cannot change or go."`
	Config       json.RawMessage   `json:"config,omitempty" doc:"The JSON Schema of the objects' spec.config, within funcd's contract profile: closed objects, and no $ref. funcd refuses an object whose config breaks it; an object stored before a change that its config breaks waits in Pending with ConfigInvalid, with no engine."`
	Settings     []AddonSettings   `json:"settings,omitempty" doc:"Kinds of settings objects that the add-on adds beside kind. An object of one runs no engine: the kind's Function applies the objects of each target through the target's engine."`
	Port         int               `json:"port,omitempty" minimum:"1" maximum:"65535" doc:"The port that the engine serves on, in its own network namespace; an Engine entry requires it. Only funcd reaches it."`
	BasePath     string            `json:"basePath,omitempty" doc:"The path under which the edge serves an object's engine, which the engine gets as it was called. It begins with /, does not end with / unless it is /, and holds no empty, . or .. segment. Empty is /."`
	PrivatePaths []string          `json:"privatePaths,omitempty" doc:"Paths under basePath that the edge answers with 404, however a caller spells them; funcd reaches them at the engine's own address. Each has basePath's form and lies under it."`
	Readiness    AddonReadiness    `json:"readiness,omitzero" doc:"How funcd learns that an object's engine is ready; an Engine entry requires it."`
	Storage      *AddonStorage     `json:"storage,omitempty" doc:"A directory that outlives each object's engine, kept across a replaced engine and funcd's restarts. Unset, the engine keeps nothing."`
	Env          map[string]string `json:"env,omitempty" doc:"The engine's environment. Each value is a literal, or a single ${{ }} expression over config, such as ${{ config.issuer }}, which funcd checks against the config schema when the Addon is applied. A value that is not a string reaches the engine as its JSON; one that fails holds the object in Pending with EnvInvalid."`
	SecretKeys   []string          `json:"secretKeys,omitempty" doc:"The keys of an object's bound Secrets that reach the engine's environment. A bound Secret's other keys do not."`
	Snapshot     *AddonSnapshot    `json:"snapshot,omitempty" doc:"Where the engine writes its own consistent copies, which a Backup of an object copies. Unset, a Backup that names an object of the add-on fails with reason NoSnapshot, and a resourceGroup scope leaves the add-on's objects out."`
	Restore      *AddonRestore     `json:"restore,omitempty" doc:"How the engine loads a copy at its start, which an in-place Restore of an object puts in place. Unset, an object of the add-on cannot be restored."`
}

// Serves reports whether the entry serves workload w.
func (rt ShippedRuntime) Serves(w Workload) bool { return slices.Contains(rt.Workloads, w) }

// RunsKind reports whether the entry runs the Addon's kind: its workloads hold Engine.
func (rt ShippedRuntime) RunsKind() bool { return rt.Serves(WorkloadEngine) }

// KindRuntime returns the entry that runs the kind; Kind, Kinds and AddonBasePath read through it.
func (s AddonSpec) KindRuntime() (ShippedRuntime, bool) {
	for _, rt := range s.Runtimes {
		if rt.RunsKind() {
			return rt, true
		}
	}
	return ShippedRuntime{}, false
}

// KindAttributes are the attributes of the entry that runs the kind; empty when the Addon adds no kind.
func (s AddonSpec) KindAttributes() AddonAttributes {
	if rt, ok := s.KindRuntime(); ok && rt.Attributes != nil {
		return *rt.Attributes
	}
	return AddonAttributes{}
}

// Kind is the kind the Addon adds, "" when no entry runs one (ADR-0344 Decision 10).
func (s AddonSpec) Kind() string { return s.KindAttributes().Kind }

// Shipped is the entry named name, and whether the Addon ships it.
func (s AddonSpec) Shipped(name RuntimeName) (ShippedRuntime, bool) {
	for _, rt := range s.Runtimes {
		if rt.Name == name {
			return rt, true
		}
	}
	return ShippedRuntime{}, false
}

// AddonSettings is a kind of settings objects (ADR-0340): each object runs no engine and names its target, an object of
// the Addon's kind in its namespace, whose settings the kind's Function applies (ADR-0341).
type AddonSettings struct {
	Kind        string                `json:"kind" doc:"The settings kind, a kind name as the Addon's kind is, served at its plural."`
	Target      string                `json:"target" doc:"The string field under spec.config that names the object's target, such as spec.config.provider."`
	Resync      Duration              `json:"resync,omitempty" doc:"How often funcd sends a target's objects again with no change, from 1s to 24h; unset is 10m."`
	Function    AddonSettingsFunction `json:"function" doc:"The Function that applies a target's objects, named as a Workflow step names its Function. A target may name its own in its spec.functions."`
	Schema      json.RawMessage       `json:"schema" doc:"The JSON Schema of the objects' spec.config, within funcd's contract profile. It must require the target field at each level."`
	Constraints AddonConstraints      `json:"constraints,omitzero" doc:"What an object must keep at admission, beside its schema."`
}

// AddonSettingsFunction names a settings kind's Function, as a Workflow step's function.ref names one (ADR-0094).
type AddonSettingsFunction struct {
	Ref ObjectName `json:"ref" doc:"The name of an existing Function in the target's namespace, which funcd calls with each target's objects. funcd does not create or change it, and the objects wait while it is missing."`
}

// AddonConstraints are a settings kind's admission rules, each checked under the namespace's admission lock.
type AddonConstraints struct {
	Count     AddonCount  `json:"count,omitzero" doc:"How many objects of the kind a target or a namespace holds at most; 0 is no limit."`
	Unique    []string    `json:"unique,omitempty" doc:"Fields under spec.config whose value no two objects of the kind in a namespace share."`
	Immutable []string    `json:"immutable,omitempty" doc:"Fields under spec.config that a replace cannot change."`
	Rules     []AddonRule `json:"rules,omitempty" doc:"Conditions over object, oldObject and others that a write must meet."`
}

// AddonCount bounds the objects of a settings kind.
type AddonCount struct {
	PerTarget    int `json:"perTarget,omitempty" minimum:"0" doc:"The most objects of the kind that name one target."`
	PerNamespace int `json:"perNamespace,omitempty" minimum:"0" doc:"The most objects of the kind in a namespace."`
}

// AddonRule is a ${{ }} condition that an object of a settings kind must meet.
type AddonRule struct {
	Rule    string `json:"rule" doc:"A single ${{ }} condition over object, oldObject (on a replace) and others."`
	Message string `json:"message" doc:"What a refusal says."`
}

// The bounds and the default of a settings kind's resync.
const (
	MinAddonResync     = Duration(time.Second)
	MaxAddonResync     = Duration(24 * time.Hour)
	DefaultAddonResync = Duration(10 * time.Minute)
)

// EffectiveResync is resync, DefaultAddonResync when unset.
func (s *AddonSettings) EffectiveResync() time.Duration {
	if s.Resync == 0 {
		return time.Duration(DefaultAddonResync)
	}
	return time.Duration(s.Resync)
}

// SettingsKind is the settings kind named kind, compared as SameAddonKind compares; nil when the Addon adds none.
func (s AddonSpec) SettingsKind(kind string) *AddonSettings {
	settings := s.KindAttributes().Settings
	for i := range settings {
		if SameAddonKind(settings[i].Kind, kind) {
			return &settings[i]
		}
	}
	return nil
}

// Kinds are every kind the Addon adds: the kind its kind entry runs, then that entry's settings kinds; none for an
// Addon whose entries run no kind.
func (s AddonSpec) Kinds() []string { return s.KindAttributes().kinds() }

// kinds are the entry's kind, then its settings kinds; none when it runs no kind.
func (at AddonAttributes) kinds() []string {
	if at.Kind == "" {
		return nil
	}
	out := []string{at.Kind}
	for _, st := range at.Settings {
		out = append(out, st.Kind)
	}
	return out
}

// ConfigField is the segments under spec.config of a field path such as spec.config.provider, and whether p is one.
func ConfigField(p string) ([]string, bool) {
	rest, ok := strings.CutPrefix(p, "spec.config.")
	if !ok || rest == "" {
		return nil, false
	}
	segs := strings.Split(rest, ".")
	return segs, !slices.Contains(segs, "")
}

// validateSettings checks each settings kind under field, an entry's attributes: its name, distinct from every other
// kind of the Addon, its target and constraint paths under spec.config, its Function's name, its resync and its
// required schema.
func (at *AddonAttributes) validateSettings(op, field string) error {
	for i, st := range at.Settings {
		field := fmt.Sprintf("%s.settings[%d]", field, i)
		switch {
		case !addonKind.MatchString(st.Kind):
			return fault.Invalidf(op, "%s.kind %q is not a kind name", field, st.Kind)
		case funcdKind(st.Kind):
			return fault.Invalidf(op, "%s.kind %q is a kind of funcd's own", field, st.Kind)
		case slices.ContainsFunc(at.kinds()[:i+1], func(k string) bool { return SameAddonKind(k, st.Kind) }):
			return fault.Invalidf(op, "%s.kind %q is another kind of the Addon, whatever the case", field, st.Kind)
		case !dnsLabel.MatchString(string(st.Function.Ref)):
			return fault.Invalidf(op, "%s.function.ref %q is not a valid DNS-1123 name", field, st.Function.Ref)
		case len(bytes.TrimSpace(st.Schema)) == 0:
			return fault.Invalidf(op, "%s.schema is required", field)
		case st.Resync != 0 && (st.Resync < MinAddonResync || st.Resync > MaxAddonResync):
			return fault.Invalidf(op, "%s.resync %s is not from %s to %s", field, st.Resync, MinAddonResync, MaxAddonResync)
		case st.Constraints.Count.PerTarget < 0 || st.Constraints.Count.PerNamespace < 0:
			return fault.Invalidf(op, "%s.constraints.count must not be negative", field)
		}
		if _, ok := ConfigField(st.Target); !ok {
			return fault.Invalidf(op, "%s.target %q is not a field under spec.config", field, st.Target)
		}
		for _, p := range slices.Concat(st.Constraints.Unique, st.Constraints.Immutable) {
			if _, ok := ConfigField(p); !ok {
				return fault.Invalidf(op, "%s.constraints field %q is not a field under spec.config", field, p)
			}
		}
		for j, r := range st.Constraints.Rules {
			if r.Rule == "" || r.Message == "" {
				return fault.Invalidf(op, "%s.constraints.rules[%d] needs a rule and a message", field, j)
			}
		}
		if c := bytes.TrimSpace(st.Schema); len(c) > 0 && c[0] != '{' {
			return fault.Invalidf(op, "%s.schema must be a JSON Schema object", field)
		}
	}
	return nil
}

// AddonSnapshot is the folder an add-on's engine writes its own copies to (ADR-0333 Decision 1).
type AddonSnapshot struct {
	Path string `json:"path" doc:"The folder in the engine where it writes its copies: under storage.path, and neither restore.path nor inside it. A Backup copies the newest regular file there whose name does not end in ~ and that has not changed for 2 s, as it is."`
}

// AddonRestore is how an add-on's engine loads a copy at its start (ADR-0333 Decision 1).
type AddonRestore struct {
	Path    string            `json:"path" doc:"The folder in the engine where funcd puts the copy that the next start loads: under storage.path, and neither snapshot.path nor inside it. funcd empties it before it writes the copy, and removes the copy once the engine that loaded it is Ready."`
	Env     map[string]string `json:"env,omitempty" doc:"The environment of the start that loads the copy, laid over env. Each value is a literal, or a single ${{ }} expression over config and restore, such as ${{ \"file:\" + restore.file }}: restore.file is the copy's path in the engine."`
	Timeout Duration          `json:"timeout,omitempty" doc:"How long an in-place Restore waits for the engine to load the copy and start again without restore.env, from 1s to 1h; unset is 10m. Past it, the Restore fails with reason RestoreTimedOut and the namespace stays held."`
}

// The bounds and the default of restore.timeout (ADR-0333 Decision 1): an engine's start time is its own.
const (
	MinAddonRestoreTimeout     = Duration(time.Second)
	MaxAddonRestoreTimeout     = Duration(time.Hour)
	DefaultAddonRestoreTimeout = Duration(10 * time.Minute)
)

// EffectiveTimeout is restore.timeout, DefaultAddonRestoreTimeout when unset.
func (r *AddonRestore) EffectiveTimeout() time.Duration {
	if r.Timeout == 0 {
		return time.Duration(DefaultAddonRestoreTimeout)
	}
	return time.Duration(r.Timeout)
}

// StorageDir is p, a folder in the engine under storage.path, relative to storage.path: the folder an engine-files
// call names (ADR-0333's AddonFiles). It is "" when p does not lie under storage.path, which Validate refuses.
func (at AddonAttributes) StorageDir(p string) string {
	if at.Storage == nil {
		return ""
	}
	base := path.Clean(at.Storage.Path)
	if base != "/" {
		base += "/"
	}
	rel, _ := strings.CutPrefix(p, base)
	if rel == p {
		return ""
	}
	return rel
}

// AddonReadiness is an add-on engine's readiness probe.
type AddonReadiness struct {
	Path string `json:"path" doc:"The path that funcd sends GET to on the engine's port. The object turns Ready once it answers 200."`
}

// AddonStorage is an add-on engine's durable directory (ADR-0231).
type AddonStorage struct {
	Path string `json:"path" doc:"Where the directory is mounted in the engine, an absolute path."`
	UID  int    `json:"uid,omitempty" minimum:"0" doc:"The user ID that owns the directory: the user the engine's image runs as."`
	GID  int    `json:"gid,omitempty" minimum:"0" doc:"The group ID that owns the directory."`
}

// AddonStatus is the shared Status and the current revision (ADR-0339 Decision 1): each object reports its own state.
type AddonStatus struct {
	Status `json:",inline"`
	// CurrentRevision is the revision a new or moved object pins.
	CurrentRevision ObjectName `json:"currentRevision,omitempty" doc:"The AddonRevision of the Addon's latest spec: an object created, or edited outside an App, pins it, and an App's new revision records it. Empty until funcd has stamped the Addon's first revision."`
}

// GroupVersionKind returns the constant GVK for Addon.
func (a *Addon) GroupVersionKind() GroupVersionKind { return KindAddon.GVK() }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (a *Addon) GetStatus() *Status { return &a.Status.Status }

// addonKind is an add-on's kind name (ADR-0332 Decision 1).
var addonKind = regexp.MustCompile(`^[A-Z][A-Za-z0-9]{1,62}$`)

// addonImage is an image pinned by digest, with no tag: a registry port is the only colon before the digest.
var addonImage = regexp.MustCompile(`^[^:@\s]+(:[0-9]+/[^:@\s]+)?@sha256:[a-f0-9]{64}$`)

// IsAddonKind reports whether name may be an add-on's kind: it has the form of one, and funcd has no kind of that name,
// whatever the case.
func IsAddonKind(name string) bool {
	return addonKind.MatchString(name) && !funcdKind(name)
}

// funcdKind reports whether name is one of funcd's kinds, its authorization-only ones included, without case.
func funcdKind(name string) bool {
	return slices.ContainsFunc(append(AllKinds(), KindS3Identity, KindNetDestination), func(k Kind) bool {
		return strings.EqualFold(string(k), name)
	})
}

// AddonPlural is the path plural of an add-on's kind: the kind in lower case with an s (ADR-0332 Decision 1).
func AddonPlural(kind string) string { return strings.ToLower(kind) + "s" }

// SameAddonKind reports whether two add-on kinds are one: kinds are compared without case, by their plural, as the API
// finds an object's Addon (ADR-0332 Decision 1).
func SameAddonKind(a, b string) bool { return AddonPlural(a) == AddonPlural(b) }

// AddonBasePath is the path the Addon's engine is served under: its kind entry's basePath, else /.
func (s AddonSpec) AddonBasePath() string { return s.KindAttributes().basePath() }

// basePath is the entry's basePath, else /.
func (at AddonAttributes) basePath() string {
	if at.BasePath == "" {
		return "/"
	}
	return at.BasePath
}

// Validate checks the rules of an Addon that need nothing but the object (ADR-0344 Decisions 1 to 5): each entry's
// name, workloads and image, at most one entry that runs a kind, and the attributes each entry's workloads take, an
// Engine entry's kind, port, paths, probe, storage, environment's names, settings and copies (ADR-0332 Decision 1). The
// control plane checks the rest when it is applied.
func (a *Addon) Validate() error {
	const op = "Addon.Validate"
	if err := validateMeta(a.TypeMeta, &a.ObjectMeta, KindAddon); err != nil {
		return err
	}
	if len(a.Spec.Runtimes) == 0 {
		return fault.Invalidf(op, "spec.runtimes is empty: an Addon ships at least one runtime")
	}
	kindEntry := -1
	for i, rt := range a.Spec.Runtimes {
		field := fmt.Sprintf("spec.runtimes[%d]", i)
		if err := rt.validate(op, field); err != nil {
			return err
		}
		for j := range i {
			if a.Spec.Runtimes[j].Name == rt.Name {
				return fault.Invalidf(op, "%s.name %q is the name of spec.runtimes[%d] too", field, rt.Name, j)
			}
		}
		if !rt.RunsKind() {
			continue
		}
		if kindEntry >= 0 {
			return fault.Invalidf(op, "%s runs a kind, as spec.runtimes[%d] does: at most one entry runs the Addon's kind", field, kindEntry)
		}
		kindEntry = i
	}
	return nil
}

// validate checks one entry under field (ADR-0344 Decisions 2 to 5): its name is a DNS label that does not choose one
// of funcd's runtime families, its workloads are known, each listed once, with Engine alone, its image is pinned by
// digest, and it sets only the attributes its workloads take.
func (rt ShippedRuntime) validate(op, field string) error {
	if rt.Name.Validate() != nil {
		return fault.Invalidf(op, "%s.name %q is not a DNS label", field, rt.Name)
	}
	if strings.HasPrefix(string(rt.Name), "node") || strings.HasPrefix(string(rt.Name), "python") {
		return fault.Invalidf(op, "%s.name %q starts with node or python, whose names choose funcd's own runtime families", field, rt.Name)
	}
	if len(rt.Workloads) == 0 {
		return fault.Invalidf(op, "%s.workloads is empty: an entry serves at least one workload", field)
	}
	for j, w := range rt.Workloads {
		switch {
		case w != WorkloadFunction && w != WorkloadJob && w != WorkloadEngine:
			return fault.Invalidf(op, "%s.workloads[%d] %q is not Function, Job or Engine", field, j, w)
		case slices.Contains(rt.Workloads[:j], w):
			return fault.Invalidf(op, "%s.workloads[%d] %q is listed twice", field, j, w)
		}
	}
	if rt.RunsKind() && len(rt.Workloads) > 1 {
		return fault.Invalidf(op, "%s.workloads holds Engine beside another workload: an Engine entry stands alone", field)
	}
	switch {
	case rt.Image == "":
		return fault.Invalidf(op, "%s.image is required: an entry that serves %s runs an image", field, rt.Workloads[0])
	case !addonImage.MatchString(rt.Image):
		return fault.Invalidf(op, "%s.image %q is not pinned by digest: <repository>@sha256:<64 hex digits>", field, rt.Image)
	}
	set, err := rt.Attributes.set()
	if err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "%s.attributes", field)
	}
	for _, k := range set {
		if !rt.takes(k) {
			return fault.Invalidf(op, "%s.attributes.%s is not an attribute that a %s entry takes", field, k, rt.Workloads[0])
		}
	}
	if !rt.RunsKind() {
		return nil
	}
	if rt.Attributes == nil {
		return fault.Invalidf(op, "%s.attributes is required: an Engine entry names its kind, port and readiness", field)
	}
	return rt.Attributes.validateEngine(op, field+".attributes")
}

// takes reports whether the entry's workloads take the attribute named key (ADR-0344 Decision 3): an Engine entry its
// kind, config, settings and engine fields; a Function or Job entry none yet.
func (rt ShippedRuntime) takes(key string) bool {
	if !rt.RunsKind() {
		return false
	}
	switch key {
	case "kind", "config", "settings", "port", "basePath", "privatePaths", "readiness", "storage", "env", "secretKeys",
		"snapshot", "restore":
		return true
	}
	return false
}

// set are the JSON names of the attributes at sets, sorted; none for nil.
func (at *AddonAttributes) set() ([]string, error) {
	if at == nil {
		return nil, nil
	}
	b, err := json.Marshal(at)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil, err
	}
	return slices.Sorted(maps.Keys(fields)), nil
}

// validateEngine checks an Engine entry's attributes under field (ADR-0332 Decision 1): the kind and the port it
// requires, the paths, the probe it requires, the storage, the config, the environment's names, the settings kinds
// (ADR-0340) and the copies (ADR-0333).
func (at *AddonAttributes) validateEngine(op, field string) error {
	switch {
	case at.Kind == "":
		return fault.Invalidf(op, "%s.kind is required: an Engine entry runs the Addon's kind", field)
	case !addonKind.MatchString(at.Kind):
		return fault.Invalidf(op, "%s.kind %q is not a kind name: an upper-case letter, then 1 to 62 letters or digits", field, at.Kind)
	case funcdKind(at.Kind):
		return fault.Invalidf(op, "%s.kind %q is a kind of funcd's own", field, at.Kind)
	case at.Port < 1 || at.Port > maxPort:
		return fault.Invalidf(op, "%s.port %d is not a port from 1 to %d", field, at.Port, maxPort)
	}
	if at.BasePath != "" {
		if err := validateServedPath(op, field+".basePath", at.BasePath); err != nil {
			return err
		}
		if UnderAuthPath(at.BasePath) {
			return fault.Invalidf(op, "%s.basePath %q lies under %s, the edge's own path on every host", field, at.BasePath, AuthPath)
		}
	}
	base := at.basePath()
	for i, p := range at.PrivatePaths {
		pf := fmt.Sprintf("%s.privatePaths[%d]", field, i)
		if err := validateServedPath(op, pf, p); err != nil {
			return err
		}
		if !underPath(p, base) {
			return fault.Invalidf(op, "%s %q is not under %s.basePath %q", pf, p, field, base)
		}
	}
	if !strings.HasPrefix(at.Readiness.Path, "/") {
		return fault.Invalidf(op, "%s.readiness.path %q must begin with /", field, at.Readiness.Path)
	}
	if st := at.Storage; st != nil && !path.IsAbs(st.Path) {
		return fault.Invalidf(op, "%s.storage.path %q must be absolute", field, st.Path)
	}
	if st := at.Storage; st != nil && (st.UID < 0 || st.GID < 0) {
		return fault.Invalidf(op, "%s.storage uid and gid must not be negative", field)
	}
	if c := bytes.TrimSpace(at.Config); len(c) > 0 && c[0] != '{' {
		return fault.Invalidf(op, "%s.config must be a JSON Schema object", field)
	}
	for _, k := range slices.Sorted(maps.Keys(at.Env)) {
		if err := validateEnvKey(op, field+".env key", k); err != nil {
			return err
		}
	}
	for _, k := range at.SecretKeys {
		if err := validateEnvKey(op, field+".secretKeys entry", k); err != nil {
			return err
		}
	}
	if err := at.validateSettings(op, field); err != nil {
		return err
	}
	return at.validateCopies(op, field)
}

// validateCopies checks snapshot and restore under field, an entry's attributes (ADR-0333 Decision 1): both need
// storage, each is a folder under storage.path, and neither lies inside the other, since hiqlite copies its restore
// file into its snapshot folder and truncates it when both are the same file. restore.timeout lies between its bounds.
func (at *AddonAttributes) validateCopies(op, field string) error {
	folders := map[string]string{}
	if at.Snapshot != nil {
		folders[field+".snapshot.path"] = at.Snapshot.Path
	}
	if at.Restore != nil {
		folders[field+".restore.path"] = at.Restore.Path
	}
	for _, f := range slices.Sorted(maps.Keys(folders)) {
		p := folders[f]
		if at.Storage == nil {
			return fault.Invalidf(op, "%s needs %s.storage: the engine's copies lie in its storage", f, field)
		}
		if err := validateServedPath(op, f, p); err != nil {
			return err
		}
		if at.StorageDir(p) == "" {
			return fault.Invalidf(op, "%s %q is not a folder under %s.storage.path %q", f, p, field, at.Storage.Path)
		}
	}
	if at.Snapshot != nil && at.Restore != nil && (underPath(at.Snapshot.Path, at.Restore.Path) || underPath(at.Restore.Path, at.Snapshot.Path)) {
		return fault.Invalidf(op, "%s.snapshot.path %q and %s.restore.path %q are not apart: neither may be the other or lie inside it",
			field, at.Snapshot.Path, field, at.Restore.Path)
	}
	if r := at.Restore; r != nil {
		for _, k := range slices.Sorted(maps.Keys(r.Env)) {
			if err := validateEnvKey(op, field+".restore.env key", k); err != nil {
				return err
			}
		}
		if r.Timeout != 0 && (r.Timeout < MinAddonRestoreTimeout || r.Timeout > MaxAddonRestoreTimeout) {
			return fault.Invalidf(op, "%s.restore.timeout %s is not from %s to %s", field, r.Timeout, MinAddonRestoreTimeout, MaxAddonRestoreTimeout)
		}
	}
	return nil
}

// maxPort is the largest TCP port.
const maxPort = 65535

// validateServedPath refuses a path the edge could not match as it is spelled: one that does not begin with /, ends
// with / unless it is /, or holds an empty, . or .. segment.
func validateServedPath(op, field, p string) error {
	if !strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return fault.Invalidf(op, "%s %q must begin with /, not end with / unless it is /, and hold no empty, . or .. segment", field, p)
	}
	return nil
}

// underPath reports whether p is base or lies under it, segment by segment.
func underPath(p, base string) bool {
	return base == "/" || p == base || strings.HasPrefix(p, base+"/")
}

// AddonObject is how funcd stores an object of an add-on's kind (ADR-0332 Decision 2): the API shows it under the
// add-on's kind, Spec.Kind, and stores it under this one, so funcd's own set of kinds stays fixed. One name is one
// object per namespace, whatever its kind.
type AddonObject struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       AddonObjectSpec   `json:"spec"`
	Status     AddonObjectStatus `json:"status,omitempty"`
}

// AddonObjectSpec is what a user declares in an object of an add-on's kind.
type AddonObjectSpec struct {
	Kind      string                `json:"kind,omitempty" doc:"The add-on's kind, which funcd sets from the path the object is written at."`
	Config    json.RawMessage       `json:"config,omitempty" doc:"The object's settings, which the schema in its Addon's spec.config checks. Unset is an empty object."`
	Bindings  AddonObjectBindings   `json:"bindings,omitempty" doc:"What the object's engine uses."`
	Ingress   *Ingress              `json:"ingress,omitempty" doc:"Exposes the engine at the edge, on ingress.host under its Addon's basePath, while the object is Ready. Its own ingress.auth alone decides the stance, open when unset; ingress.path and the authenticated mode are refused."`
	Resources ResourceSpec          `json:"resources,omitempty" doc:"The cpu and memory of the object's engine. An empty cpu or memory sets no limit, and a change replaces the running engine."`
	Deletion  DeletionPolicy        `json:"deletion,omitempty" doc:"What becomes of the engine's storage once the object is deleted and its engine is gone: retain, the default, moves it into the quarantine for storage.quarantineRetention, from which it comes back by hand with funcd stopped; delete removes it. A new object of the same name starts empty either way."`
	Functions []AddonObjectFunction `json:"functions,omitempty" doc:"The object's own Function for a settings kind of its Addon, in place of the kind's function.ref, so each copy of an App template applies its own objects. A settings object names none."`
}

// AddonObjectFunction names a target's own Function for one settings kind (ADR-0341 Decision 3).
type AddonObjectFunction struct {
	Kind string     `json:"kind" doc:"A settings kind of the object's Addon, named once."`
	Ref  ObjectName `json:"ref" doc:"A Function of the object's namespace that binds the object in its bindings.addon, which funcd calls with the object's settings objects of the kind. In an App, a Function the App declares."`
}

// AddonObjectBindings are what an add-on object's engine uses (ADR-0332 Decision 4).
type AddonObjectBindings struct {
	Secret []ObjectName `json:"secret,omitempty" doc:"Secrets of the namespace whose keys, those that the Addon's spec.secretKeys lists, reach the engine's environment. funcd asks Cedar, as the object, for secret::read on each: a missing Secret holds the object in Pending with SecretNotFound, and a forbidden one with BindingResolveFailed. A changed value replaces the engine."`
}

// AddonObjectStatus is the observed state of an add-on object.
type AddonObjectStatus struct {
	Status  `json:",inline"`
	Address string             `json:"address,omitempty" doc:"The engine's own address, its host and port, which only funcd reaches."`
	Restore *AddonRestoreState `json:"restore,omitempty" doc:"The in-place Restore whose copy the engine loads: it goes once an engine started without restore.env is Ready, or once that Restore has failed or is gone. While it stands, the namespace is held."`
	// AddonRevision is the revision the engine runs, and AppRevision, for an App's part, the App revision whose record
	// it pins (ADR-0339 Decisions 2 and 3).
	AddonRevision ObjectName `json:"addonRevision,omitempty" doc:"The AddonRevision whose spec the engine runs. A newer revision of the Addon moves no object: an edit of the object moves it to the Addon's current revision, and for an App's part, the App's revision moves it."`
	AppRevision   ObjectName `json:"appRevision,omitempty" doc:"For an App's part, the AppRevision whose record for its kind it pins: the App's latest revision once that one is applied. A part moves forward only."`
}

// AddonRestoreState is the copy an in-place Restore placed for an add-on object's engine (ADR-0333 Decisions 3 and 4).
type AddonRestoreState struct {
	Restore ObjectName `json:"restore" doc:"The Restore that placed the copy."`
	File    string     `json:"file" doc:"The copy's file name in the Addon's restore.path."`
	State   string     `json:"state" enum:"Loading,Loaded" doc:"Loading while the engine starts with restore.env to load the copy; Loaded once the copy is gone, while the engine starts again without restore.env."`
}

// The states of an add-on object's status.restore (ADR-0333 Decision 4).
const (
	AddonRestoreLoading = "Loading"
	AddonRestoreLoaded  = "Loaded"
)

// addonFile is a snapshot's or a copy's file name: one path segment (ADR-0333 Decision 6).
var addonFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// IsAddonFile reports whether name may be the file of an engine's snapshot or of a Restore's copy: one path segment
// that begins with a letter or a digit, of letters, digits, dots, underscores and dashes.
func IsAddonFile(name string) bool { return addonFile.MatchString(name) }

// GroupVersionKind is the stored kind, AddonObject, unless the object carries an add-on's kind, as a client's object
// does: the SDK and funcdctl address it at that kind's plural.
func (o *AddonObject) GroupVersionKind() GroupVersionKind {
	if o.Kind == "" || o.Kind == KindAddonObject {
		return KindAddonObject.GVK()
	}
	return o.Kind.GVK()
}

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (o *AddonObject) GetStatus() *Status { return &o.Status.Status }

// isPrincipalObject marks *AddonObject as a cedar PrincipalObject: the object asks for each of its Secrets as itself
// (ADR-0332 Decision 4), as a Lake does.
func (o *AddonObject) isPrincipalObject() {}

// AddonKind is the add-on kind the object belongs to: Spec.Kind as stored, else the kind a client's object carries.
func (o *AddonObject) AddonKind() string {
	if o.Spec.Kind != "" {
		return o.Spec.Kind
	}
	if k := o.Kind; k != KindAddonObject {
		return string(k)
	}
	return ""
}

// Validate checks an add-on object's envelope, under its stored kind or the add-on's kind it carries, and the rules
// that need no Addon (ADR-0332 Decisions 2, 4 and 5): its Secret names, its ingress, which takes neither a path nor
// the authenticated mode, its deletion policy (ADR-0333 Decision 5) and its resources. The schema of its config is its
// Addon's, which the control plane checks.
func (o *AddonObject) Validate() error {
	const op = "AddonObject.Validate"
	kind := o.GroupVersionKind().Kind
	if kind != KindAddonObject && !IsAddonKind(string(kind)) {
		return fault.Invalidf(op, "kind %q is not an add-on's kind", kind)
	}
	if err := validateMeta(o.TypeMeta, &o.ObjectMeta, kind); err != nil {
		return err
	}
	switch k := o.AddonKind(); {
	case !IsAddonKind(k):
		return fault.Invalidf(op, "spec.kind %q is not an add-on's kind", k)
	case kind != KindAddonObject && k != string(kind):
		return fault.Invalidf(op, "spec.kind %q differs from the object's kind %q", k, kind)
	}
	if c := bytes.TrimSpace(o.Spec.Config); len(c) > 0 && c[0] != '{' {
		return fault.Invalidf(op, "spec.config must be a JSON object")
	}
	for _, s := range o.Spec.Bindings.Secret {
		if !dnsLabel.MatchString(string(s)) {
			return fault.Invalidf(op, "spec.bindings.secret entry %q is not a valid DNS-1123 name", s)
		}
	}
	for i, f := range o.Spec.Functions {
		switch {
		case !IsAddonKind(f.Kind):
			return fault.Invalidf(op, "spec.functions[%d].kind %q is not an add-on's kind", i, f.Kind)
		case !dnsLabel.MatchString(string(f.Ref)):
			return fault.Invalidf(op, "spec.functions[%d].ref %q is not a valid DNS-1123 name", i, f.Ref)
		}
	}
	if in := o.Spec.Ingress; in != nil {
		if in.Path != "" {
			return fault.Invalidf(op, "spec.ingress.path is not allowed: the edge serves the engine at its Addon's basePath")
		}
		if in.AuthMode() == AuthAuthenticated {
			return fault.Invalidf(op, "spec.ingress.auth.mode authenticated is not allowed: an add-on's entry runs no edge check of funcd's credentials")
		}
		if err := in.validate(op, "spec.ingress"); err != nil {
			return err
		}
		if err := in.ValidateOIDCHost(op, "spec.ingress", o.Namespace); err != nil {
			return err
		}
	}
	switch o.Spec.Deletion {
	case "", DeletionRetain, DeletionDelete:
	default:
		return fault.Invalidf(op, "spec.deletion %q must be %q or %q", o.Spec.Deletion, DeletionRetain, DeletionDelete)
	}
	return o.Spec.Resources.Validate(op, "spec.resources")
}
