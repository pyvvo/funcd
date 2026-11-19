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

	"github.com/pyvvo/funcd/api/fault"
)

// Addon registers an add-on built outside funcd (ADR-0332, FEAT-0018 F258): an engine image pinned by digest and the
// namespaced kind its objects take. It is cluster-scoped, so only an admin reads or writes one.
type Addon struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       AddonSpec   `json:"spec"`
	Status     AddonStatus `json:"status,omitempty"`
}

// AddonSpec is what an add-on declares: the kind it adds and the engine that runs each object of it (ADR-0332
// Decision 1). The rules that read the store or the config, the plurals, the image's registry, the schema and the env
// expressions, are the control plane's.
type AddonSpec struct {
	Kind         string            `json:"kind" pattern:"^[A-Z][A-Za-z0-9]{1,62}$" doc:"The namespaced kind that the add-on adds: an upper-case letter, then 1 to 62 letters or digits. Its objects are served at /apis/funcd.io/v1alpha1/namespaces/<namespace>/<plural>, where the plural is the kind in lower case with an s. funcd refuses a kind that is its own, or whose plural is a path funcd serves or another Addon's plural, whatever the case. It cannot change."`
	Image        string            `json:"image" pattern:"^[^:@\\s]+(:[0-9]+/[^:@\\s]+)?@sha256:[a-f0-9]{64}$" doc:"The engine image, pinned by digest as <repository>@sha256:<64 hex digits>, from a registry that addons.registries lists. A registry taken out of that list stops no engine, and refuses the Addon's next change."`
	Runtimes     []string          `json:"runtimes,omitempty" enum:"containerd" doc:"The runtime.mode values that the engine runs on. Only containerd is admitted, which is also the default, since the process runtime runs no image. On a server whose mode the list lacks, each object waits in Pending with RuntimeUnavailable."`
	Port         int               `json:"port" minimum:"1" maximum:"65535" doc:"The port that the engine serves on, in its own network namespace. Only funcd reaches it."`
	BasePath     string            `json:"basePath,omitempty" doc:"The path under which the edge serves an object's engine, which the engine gets as it was called. It begins with /, does not end with / unless it is /, and holds no empty, . or .. segment. Empty is /."`
	PrivatePaths []string          `json:"privatePaths,omitempty" doc:"Paths under basePath that the edge answers with 404, however a caller spells them; funcd reaches them at the engine's own address. Each has basePath's form and lies under it."`
	Readiness    AddonReadiness    `json:"readiness" doc:"How funcd learns that an object's engine is ready."`
	Storage      *AddonStorage     `json:"storage,omitempty" doc:"A directory that outlives each object's engine, kept across a replaced engine and funcd's restarts. Unset, the engine keeps nothing."`
	Config       json.RawMessage   `json:"config,omitempty" doc:"The JSON Schema of the objects' spec.config, within funcd's contract profile: closed objects, and no $ref. funcd refuses an object whose config breaks it; an object stored before a change that its config breaks waits in Pending with ConfigInvalid, with no engine."`
	Env          map[string]string `json:"env,omitempty" doc:"The engine's environment. Each value is a literal, or a single ${{ }} expression over config, such as ${{ config.issuer }}, which funcd checks against the config schema when the Addon is applied. A value that is not a string reaches the engine as its JSON; one that fails holds the object in Pending with EnvInvalid."`
	SecretKeys   []string          `json:"secretKeys,omitempty" doc:"The keys of an object's bound Secrets that reach the engine's environment. A bound Secret's other keys do not."`
	Snapshot     *AddonSnapshot    `json:"snapshot,omitempty" doc:"Where the engine writes its own consistent copies, which a Backup of an object copies. Unset, a Backup that names an object of the add-on fails with reason NoSnapshot, and a resourceGroup scope leaves the add-on's objects out."`
	Restore      *AddonRestore     `json:"restore,omitempty" doc:"How the engine loads a copy at its start, which an in-place Restore of an object puts in place. Unset, an object of the add-on cannot be restored."`
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
func (s *AddonSpec) StorageDir(p string) string {
	if s.Storage == nil {
		return ""
	}
	base := path.Clean(s.Storage.Path)
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

// AddonStatus is the shared Status only, as RuntimeClassStatus is: each object reports its own state.
type AddonStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for Addon.
func (a *Addon) GroupVersionKind() GroupVersionKind { return KindAddon.GVK() }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (a *Addon) GetStatus() *Status { return &a.Status.Status }

// addonKind is an add-on's kind name (ADR-0332 Decision 1).
var addonKind = regexp.MustCompile(`^[A-Z][A-Za-z0-9]{1,62}$`)

// addonImage is an image pinned by digest, with no tag: a registry port is the only colon before the digest.
var addonImage = regexp.MustCompile(`^[^:@\s]+(:[0-9]+/[^:@\s]+)?@sha256:[a-f0-9]{64}$`)

// AddonRuntimeContainerd is the one runtime.mode an Addon admits, and its default: the process runtime runs no image.
const AddonRuntimeContainerd = "containerd"

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

// AddonBasePath is the path an Addon's engine is served under: its basePath, else /.
func (s *AddonSpec) AddonBasePath() string {
	if s.BasePath == "" {
		return "/"
	}
	return s.BasePath
}

// Runtime reports whether the engine runs on runtime.mode mode: runtimes holds it, or lists none and mode is
// containerd.
func (s *AddonSpec) Runtime(mode string) bool {
	if len(s.Runtimes) == 0 {
		return mode == AddonRuntimeContainerd
	}
	return slices.Contains(s.Runtimes, mode)
}

// Validate checks the rules of an Addon that need nothing but the object (ADR-0332 Decision 1): the kind and the
// image's form, the runtimes, the port, the paths, the probe, the storage and the environment's names. The control
// plane checks the rest when it is applied.
func (a *Addon) Validate() error {
	const op = "Addon.Validate"
	if err := validateMeta(a.TypeMeta, &a.ObjectMeta, KindAddon); err != nil {
		return err
	}
	s := &a.Spec
	switch {
	case !addonKind.MatchString(s.Kind):
		return fault.Invalidf(op, "spec.kind %q is not a kind name: an upper-case letter, then 1 to 62 letters or digits", s.Kind)
	case funcdKind(s.Kind):
		return fault.Invalidf(op, "spec.kind %q is a kind of funcd's own", s.Kind)
	case !addonImage.MatchString(s.Image):
		return fault.Invalidf(op, "spec.image %q is not pinned by digest: <repository>@sha256:<64 hex digits>", s.Image)
	case s.Port < 1 || s.Port > maxPort:
		return fault.Invalidf(op, "spec.port %d is not a port from 1 to %d", s.Port, maxPort)
	}
	for _, r := range s.Runtimes {
		if r != AddonRuntimeContainerd {
			return fault.Invalidf(op, "spec.runtimes holds %q: an add-on runs only on %s", r, AddonRuntimeContainerd)
		}
	}
	if s.BasePath != "" {
		if err := validateServedPath(op, "spec.basePath", s.BasePath); err != nil {
			return err
		}
		if UnderAuthPath(s.BasePath) {
			return fault.Invalidf(op, "spec.basePath %q lies under %s, the edge's own path on every host", s.BasePath, AuthPath)
		}
	}
	base := s.AddonBasePath()
	for i, p := range s.PrivatePaths {
		field := fmt.Sprintf("spec.privatePaths[%d]", i)
		if err := validateServedPath(op, field, p); err != nil {
			return err
		}
		if !underPath(p, base) {
			return fault.Invalidf(op, "%s %q is not under spec.basePath %q", field, p, base)
		}
	}
	if !strings.HasPrefix(s.Readiness.Path, "/") {
		return fault.Invalidf(op, "spec.readiness.path %q must begin with /", s.Readiness.Path)
	}
	if st := s.Storage; st != nil && !path.IsAbs(st.Path) {
		return fault.Invalidf(op, "spec.storage.path %q must be absolute", st.Path)
	}
	if st := s.Storage; st != nil && (st.UID < 0 || st.GID < 0) {
		return fault.Invalidf(op, "spec.storage uid and gid must not be negative")
	}
	if c := bytes.TrimSpace(s.Config); len(c) > 0 && c[0] != '{' {
		return fault.Invalidf(op, "spec.config must be a JSON Schema object")
	}
	for _, k := range slices.Sorted(maps.Keys(s.Env)) {
		if err := validateEnvKey(op, "spec.env key", k); err != nil {
			return err
		}
	}
	for _, k := range s.SecretKeys {
		if err := validateEnvKey(op, "spec.secretKeys entry", k); err != nil {
			return err
		}
	}
	return s.validateCopies(op)
}

// validateCopies checks snapshot and restore (ADR-0333 Decision 1): both need storage, each is a folder under
// storage.path, and neither lies inside the other, since hiqlite copies its restore file into its snapshot folder and
// truncates it when both are the same file. restore.timeout lies between its bounds.
func (s *AddonSpec) validateCopies(op string) error {
	folders := map[string]string{}
	if s.Snapshot != nil {
		folders["spec.snapshot.path"] = s.Snapshot.Path
	}
	if s.Restore != nil {
		folders["spec.restore.path"] = s.Restore.Path
	}
	for _, field := range slices.Sorted(maps.Keys(folders)) {
		p := folders[field]
		if s.Storage == nil {
			return fault.Invalidf(op, "%s needs spec.storage: the engine's copies lie in its storage", field)
		}
		if err := validateServedPath(op, field, p); err != nil {
			return err
		}
		if s.StorageDir(p) == "" {
			return fault.Invalidf(op, "%s %q is not a folder under spec.storage.path %q", field, p, s.Storage.Path)
		}
	}
	if s.Snapshot != nil && s.Restore != nil && (underPath(s.Snapshot.Path, s.Restore.Path) || underPath(s.Restore.Path, s.Snapshot.Path)) {
		return fault.Invalidf(op, "spec.snapshot.path %q and spec.restore.path %q are not apart: neither may be the other or lie inside it",
			s.Snapshot.Path, s.Restore.Path)
	}
	if r := s.Restore; r != nil {
		for _, k := range slices.Sorted(maps.Keys(r.Env)) {
			if err := validateEnvKey(op, "spec.restore.env key", k); err != nil {
				return err
			}
		}
		if r.Timeout != 0 && (r.Timeout < MinAddonRestoreTimeout || r.Timeout > MaxAddonRestoreTimeout) {
			return fault.Invalidf(op, "spec.restore.timeout %s is not from %s to %s", r.Timeout, MinAddonRestoreTimeout, MaxAddonRestoreTimeout)
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
	Kind      string              `json:"kind,omitempty" doc:"The add-on's kind, which funcd sets from the path the object is written at."`
	Config    json.RawMessage     `json:"config,omitempty" doc:"The object's settings, which the schema in its Addon's spec.config checks. Unset is an empty object."`
	Bindings  AddonObjectBindings `json:"bindings,omitempty" doc:"What the object's engine uses."`
	Ingress   *Ingress            `json:"ingress,omitempty" doc:"Exposes the engine at the edge, on ingress.host under its Addon's basePath, while the object is Ready. Its own ingress.auth alone decides the stance, open when unset; ingress.path and the authenticated mode are refused."`
	Resources ResourceSpec        `json:"resources,omitempty" doc:"The cpu and memory of the object's engine. An empty cpu or memory sets no limit, and a change replaces the running engine."`
	Deletion  DeletionPolicy      `json:"deletion,omitempty" doc:"What becomes of the engine's storage once the object is deleted and its engine is gone: retain, the default, moves it into the quarantine for storage.quarantineRetention, from which it comes back by hand with funcd stopped; delete removes it. A new object of the same name starts empty either way."`
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
