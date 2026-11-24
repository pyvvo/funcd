package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/distribution/reference"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/runtime/embedimg"
	addonsvc "github.com/pyvvo/funcd/internal/services/addon"
	"github.com/pyvvo/funcd/internal/store"
)

// ===== Addon (cluster-scoped, admins only) — ADR-0332 Decision 1 =====

func registerAddon(api huma.API, h Handlers) {
	writes := []ErrorCase{
		problem(fault.Invalid, "The Addon's kind or a settings kind is a kind of funcd's, another kind of the Addon or a path "+
			"that funcd serves, whatever the case; a replace changes or drops the kind; or a runtime's name is one of funcd's "+
			"runtimes or a key of runtime.containerd.imageOverride.",
			"Choose another kind or runtime name. A new kind needs a new Addon."),
		problem(fault.Invalid, "The registry of a runtime's image is not one that addons.registries lists.",
			"Use an image from a listed registry, or ask the operator to list its registry."),
		problem(fault.Invalid, "The kind entry's config is not a JSON Schema in funcd's contract profile, or an env value is "+
			"not a literal or a single ${{ }} expression that the schema types.", "Correct the field that the detail names."),
	}
	// A replace keeps the kind but can add a settings kind or a runtime that another Addon registers (ADR-0340
	// Decision 2, ADR-0344 Decision 4).
	taken := problem(fault.Conflict, "Another Addon registers the same kind or the same plural, whatever the case, or "+
		"ships a runtime of the same name.", "Choose another kind or runtime name, or change the other Addon.")
	registerClusterCRUD(api, clusterRoutes[v1.Addon]{
		kind: "Addon", kinds: "Addons", path: "addons",
		list: h.ListAddons, create: h.CreateAddon, get: h.GetAddon, replace: h.ReplaceAddon, remove: h.DeleteAddon,
		cases: &kindCases{create: append(slices.Clone(writes), taken), replace: append(slices.Clone(writes), taken)},
	})
}

// ===== AddonRevision (cluster-scoped, read-only, admins only) — ADR-0339 Decision 1 =====

func registerAddonRevision(api huma.API, h Handlers) {
	registerClusterRead(api, clusterRoutes[v1.AddonRevision]{
		kind: "AddonRevision", kinds: "AddonRevisions", path: "addonrevisions",
		list: h.ListAddonRevisions, get: h.GetAddonRevision, cases: &kindCases{},
	})
}

func (h *storeHandlers) GetAddonRevision(ctx context.Context, name v1.ObjectName) (v1.AddonRevision, error) {
	return typedObj[v1.AddonRevision](h.getObj(ctx, v1.KindAddonRevision, "", name))
}

func (h *storeHandlers) ListAddonRevisions(ctx context.Context) ([]v1.AddonRevision, error) {
	return typedObjs[v1.AddonRevision](h.listObj(ctx, v1.KindAddonRevision, ""))
}

func (h *storeHandlers) GetAddon(ctx context.Context, name v1.ObjectName) (v1.Addon, error) {
	return typedObj[v1.Addon](h.getObj(ctx, v1.KindAddon, "", name))
}

func (h *storeHandlers) CreateAddon(ctx context.Context, a v1.Addon) (v1.Addon, error) {
	return typedObj[v1.Addon](h.createObj(ctx, v1.KindAddon, &a))
}

func (h *storeHandlers) ListAddons(ctx context.Context) ([]v1.Addon, error) {
	return typedObjs[v1.Addon](h.listObj(ctx, v1.KindAddon, ""))
}

func (h *storeHandlers) ReplaceAddon(ctx context.Context, name v1.ObjectName, a v1.Addon) (v1.Addon, error) {
	return typedObj[v1.Addon](h.replaceObj(ctx, v1.KindAddon, "", name, &a))
}

func (h *storeHandlers) DeleteAddon(ctx context.Context, name v1.ObjectName, rv string) error {
	return h.deleteObjIf(ctx, v1.KindAddon, "", name, rv)
}

// AddonRules are the rules of an Addon write that read the store or the config (ADR-0332 Decisions 1, 4 and 6,
// ADR-0344 Decisions 4 and 10).
type AddonRules struct {
	Store admission.StoreReader // reads the other Addons
	// Registries are addons.registries: the registries an Addon's images may come from; none refuses every Addon.
	Registries []string
	// AnyRegistry lets the images come from any registry, as under funcdctl dev (ADR-0334 Decision 5).
	AnyRegistry bool
	// Reserved are the path segments funcd serves (ReservedPlurals): an add-on's plural among them is refused.
	Reserved []string
	// Overrides are the runtime names that runtime.containerd.imageOverride maps: no Addon ships a runtime of one
	// (ADR-0344 Decision 4).
	Overrides []string
}

// NewAddonAdmissions returns the Validating admissions of an Addon write, and of a settings object's. Every kind an
// Addon adds, its kind and its settings kinds, is neither funcd's nor another Addon's, without case, and no runtime it
// ships is another Addon's, on a create and on an update: that check reads every Addon, so it is marked, and two writes
// take the cluster-scoped admission lock (ADR-0147) and cannot register one plural or one runtime name (ADR-0340
// Decision 2, ADR-0344 Decision 4). Every write keeps the kind an Addon has, ships no runtime of funcd's own name or of
// an override's, names its images from a listed registry, passes its kind entry's config schema through the contract
// profile and has each env value a literal or one ${{ }} expression that the schema types, restore.env's reading
// restore.file too (ADR-0333 Decision 1); a settings kind's schema, paths and rules pass CheckSettings. A settings
// object passes its kind's constraints under the namespace's lock.
func NewAddonAdmissions(r AddonRules) []admission.Admission {
	return []admission.Admission{addonUnique{r}, addonRules{r}, addonObject{r}, addonSettings{r}}
}

type addonUnique struct{ AddonRules }

func (addonUnique) Name() string           { return "addon-unique" }
func (addonUnique) Phase() admission.Phase { return admission.Validating }
func (addonUnique) ReadsNamespace() bool   { return true }

func (addonUnique) Handles(gvk v1.GroupVersionKind, op admission.Operation) bool {
	return gvk == v1.KindAddon.GVK() && (op == admission.Create || op == admission.Update)
}

func (a addonUnique) Admit(ctx context.Context, req admission.Request) (v1.Object, error) {
	const op = "admission.addon-unique"
	ad, ok := req.Object.(*v1.Addon)
	if !ok {
		return req.Object, nil
	}
	for _, kind := range ad.Spec.Kinds() {
		plural := v1.AddonPlural(kind)
		if slices.Contains(a.Reserved, plural) {
			return nil, fault.Invalidf(op, "the plural %q of the kind %q is a path that funcd serves", plural, kind)
		}
	}
	others, err := a.Store.List(ctx, v1.KindAddon.GVK(), "")
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list Addons")
	}
	for _, o := range others {
		other, ok := o.(*v1.Addon)
		if !ok || other.Name == ad.Name {
			continue
		}
		for _, kind := range ad.Spec.Kinds() {
			for _, taken := range other.Spec.Kinds() {
				if v1.SameAddonKind(kind, taken) {
					return nil, fault.Conflictf(op, "Addon %q registers kind %q, of the plural %q", other.Name, taken, v1.AddonPlural(taken))
				}
			}
		}
		for _, rt := range ad.Spec.Runtimes {
			if _, ships := other.Spec.Shipped(rt.Name); ships {
				return nil, fault.Conflictf(op, "Addon %q ships the runtime %q", other.Name, rt.Name)
			}
		}
	}
	return req.Object, nil
}

type addonRules struct{ AddonRules }

func (addonRules) Name() string           { return "addon-rules" }
func (addonRules) Phase() admission.Phase { return admission.Validating }

func (addonRules) Handles(gvk v1.GroupVersionKind, op admission.Operation) bool {
	return gvk == v1.KindAddon.GVK() && (op == admission.Create || op == admission.Update)
}

func (a addonRules) Admit(_ context.Context, req admission.Request) (v1.Object, error) {
	const op = "admission.addon-rules"
	ad, ok := req.Object.(*v1.Addon)
	if !ok {
		return req.Object, nil
	}
	if old, ok := req.Old.(*v1.Addon); ok && old.Spec.Kind() != "" {
		switch {
		case ad.Spec.Kind() == "":
			return nil, fault.Invalidf(op, "the kind %q cannot go: an Addon keeps the kind it gained", old.Spec.Kind())
		case old.Spec.Kind() != ad.Spec.Kind():
			return nil, fault.Invalidf(op, "the kind cannot change from %q to %q: a new kind needs a new Addon", old.Spec.Kind(), ad.Spec.Kind())
		case old.Spec.Engineless() != ad.Spec.Engineless():
			return nil, fault.Invalidf(op, "the kind %q cannot change between Engine and Engineless: its objects keep how they run", ad.Spec.Kind())
		}
	}
	for i, rt := range ad.Spec.Runtimes {
		field := fmt.Sprintf("spec.runtimes[%d]", i)
		switch {
		case slices.Contains(embedimg.Runtimes(), string(rt.Name)):
			return nil, fault.Invalidf(op, "%s.name %q is one of funcd's own runtimes (%s)", field, rt.Name, strings.Join(embedimg.Runtimes(), ", "))
		case slices.Contains(a.Overrides, string(rt.Name)):
			return nil, fault.Invalidf(op, "%s.name %q is a key of runtime.containerd.imageOverride", field, rt.Name)
		case rt.Image == "":
			continue // an Engineless entry runs no image (ADR-0345 Decision 1)
		}
		registries := a.Registries
		if host, _ := addonImageHost(rt.Image); a.AnyRegistry {
			registries = []string{host}
		}
		if !AddonImageAllowed(rt.Image, registries) {
			return nil, fault.Invalidf(op, "%s.image %q does not come from a registry that addons.registries lists (%s)",
				field, rt.Image, strings.Join(a.Registries, ", "))
		}
	}
	at, field := ad.Spec.KindAttributes(), kindField(ad.Spec)
	if err := contract.Check(at.Config); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "%s.config", field)
	}
	if err := addonsvc.CheckEnv(at.Env, at.Config); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "%s.env", field)
	}
	if err := addonsvc.CheckRestoreEnv(at.Restore, at.Config); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "%s.restore.env", field)
	}
	for i := range at.Settings {
		if err := addonsvc.CheckSettings(&at.Settings[i]); err != nil {
			return nil, fault.Wrapf(err, fault.Invalid, op, "%s.settings[%d]", field, i)
		}
	}
	return req.Object, nil
}

// kindField is the path of the kind entry's attributes, spec.runtimes[<i>].attributes, which the kind's refusals name.
func kindField(s v1.AddonSpec) string {
	for i, rt := range s.Runtimes {
		if rt.RunsKind() {
			return fmt.Sprintf("spec.runtimes[%d].attributes", i)
		}
	}
	return "spec.runtimes"
}

// addonObject checks an add-on object's write against its Addon, as the API's routes do before any admission: its kind
// is the one an Addon registers, its config fits that kind's schema, and a settings object sets no engine field. An
// App's part reaches the store with no route, so app-parts refuses it here with 400, naming its entry, each property
// and each keyword (ADR-0337 Decision 3).
type addonObject struct{ AddonRules }

func (addonObject) Name() string           { return "addon-object" }
func (addonObject) Phase() admission.Phase { return admission.Validating }

func (addonObject) Handles(gvk v1.GroupVersionKind, op admission.Operation) bool {
	return gvk == v1.KindAddonObject.GVK() && (op == admission.Create || op == admission.Update)
}

func (a addonObject) Admit(ctx context.Context, req admission.Request) (v1.Object, error) {
	const op = "admission.addon-object"
	o, ok := req.Object.(*v1.AddonObject)
	if !ok {
		return req.Object, nil
	}
	addons, err := a.Store.List(ctx, v1.KindAddon.GVK(), "")
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list Addons")
	}
	var k servedKind
	for _, obj := range addons {
		ad, ok := obj.(*v1.Addon)
		switch {
		case !ok:
		case v1.SameAddonKind(ad.Spec.Kind(), o.Spec.Kind):
			k = servedKind{addon: ad}
		case ad.Spec.SettingsKind(o.Spec.Kind) != nil:
			k = servedKind{addon: ad, settings: ad.Spec.SettingsKind(o.Spec.Kind)}
		}
	}
	switch {
	case k.addon == nil:
		return nil, fault.Invalidf(op, "no Addon registers the kind %q", o.Spec.Kind)
	case k.name() != o.Spec.Kind:
		return nil, fault.Invalidf(op, "spec.kind %q is written %q by Addon %q", o.Spec.Kind, k.name(), k.addon.Name)
	}
	if err := engineFields(k, o.Spec); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "%s", o.Name)
	}
	if err := settingsFunctions(k, o.Spec.Functions); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "%s", o.Name)
	}
	v, err := contract.Compile(k.schema())
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "compile the %s schema of Addon %q", k.name(), k.addon.Name)
	}
	parts, err := addonsvc.Breaks(v, o.Spec.Config)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "spec.config")
	}
	if len(parts) > 0 {
		return nil, fault.Invalidf(op, "spec.config does not fit the %s schema of Addon %s: %s", k.name(), k.addon.Name,
			strings.Join(parts, "; "))
	}
	return req.Object, nil
}

// addonSettings checks a write of a settings object against its kind's constraints (ADR-0340 Decision 4): it reads the
// namespace's other objects of the kind, so it takes the namespace's admission lock (ADR-0147), and two writes cannot
// both pass a count or a unique field.
type addonSettings struct{ AddonRules }

func (addonSettings) Name() string           { return "addon-settings" }
func (addonSettings) Phase() admission.Phase { return admission.Validating }
func (addonSettings) ReadsNamespace() bool   { return true }

func (addonSettings) Handles(gvk v1.GroupVersionKind, op admission.Operation) bool {
	return gvk == v1.KindAddonObject.GVK() && (op == admission.Create || op == admission.Update)
}

func (a addonSettings) Admit(ctx context.Context, req admission.Request) (v1.Object, error) {
	const op = "admission.addon-settings"
	o, ok := req.Object.(*v1.AddonObject)
	if !ok {
		return req.Object, nil
	}
	addons, err := a.Store.List(ctx, v1.KindAddon.GVK(), "")
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list Addons")
	}
	var st *v1.AddonSettings
	for _, obj := range addons {
		if ad, ok := obj.(*v1.Addon); ok && st == nil {
			st = ad.Spec.SettingsKind(o.Spec.Kind)
		}
	}
	if st == nil {
		return req.Object, nil
	}
	objs, err := a.Store.List(ctx, v1.KindAddonObject.GVK(), o.Namespace)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list the add-on objects of %s", o.Namespace)
	}
	var others []*v1.AddonObject
	for _, obj := range objs {
		if x, ok := obj.(*v1.AddonObject); ok && x.Name != o.Name && v1.SameAddonKind(x.Spec.Kind, st.Kind) {
			others = append(others, x)
		}
	}
	old, _ := req.Old.(*v1.AddonObject)
	if err := addonsvc.AdmitSettings(st, o, old, others); err != nil {
		return nil, err
	}
	return req.Object, nil
}

// AddonImageAllowed reports whether an Addon may name image (ADR-0332 Decision 6): it is pinned by digest, with no tag,
// and its registry, its host after docker.io normalization as internal/runtime/containerd/image_ref.go normalizes
// it, is one of registries. The containerd driver pulls such an image (cmd/funcd's Pullable).
func AddonImageAllowed(image string, registries []string) bool {
	host, pinned := addonImageHost(image)
	return pinned && slices.ContainsFunc(registries, func(r string) bool { return strings.EqualFold(r, host) })
}

// addonImageHost is image's registry, its host after docker.io normalization, and whether image is pinned by digest
// with no tag.
func addonImageHost(image string) (host string, pinned bool) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", false
	}
	_, digested := named.(reference.Digested)
	_, tagged := named.(reference.Tagged)
	return reference.Domain(named), digested && !tagged
}

// ReservedPlurals are the path segments funcd serves under /apis/funcd.io/v1alpha1, a namespace's included: the
// plurals of its kinds and its other routes, such as deadletters and volumes. An add-on whose plural is one would be
// shadowed by funcd's own route, so the Addon is refused.
func ReservedPlurals() []string {
	var out []string
	for p := range SpecAPI().OpenAPI().Paths {
		rest, ok := strings.CutPrefix(p, apiPrefix+"/")
		if !ok {
			continue
		}
		segs := strings.Split(rest, "/")
		seg := segs[0]
		if seg == "namespaces" && len(segs) > 2 {
			seg = segs[2]
		}
		if !strings.Contains(seg, "{") && !slices.Contains(out, seg) {
			out = append(out, seg)
		}
	}
	slices.Sort(out)
	return out
}

// apiPrefix is the path every control-plane route of a kind lies under.
const apiPrefix = "/apis/funcd.io/v1alpha1"

// ===== The objects of the add-ons' kinds (namespaced) — ADR-0332 Decision 2 =====

type addonList struct {
	Namespace v1.NamespaceName `path:"namespace" doc:"The namespace whose objects the answer lists."`
	Plural    string           `path:"plural" doc:"The plural of an add-on's kind: the kind in lower case with an s."`
}

type addonItem struct {
	Namespace v1.NamespaceName `path:"namespace" doc:"The object's namespace."`
	Plural    string           `path:"plural" doc:"The plural of the object's add-on kind: the kind in lower case with an s."`
	Name      v1.ObjectName    `path:"name" doc:"The object's name."`
}

type addonDelete struct {
	IfMatchParams
	Namespace v1.NamespaceName `path:"namespace" doc:"The object's namespace."`
	Plural    string           `path:"plural" doc:"The plural of the object's add-on kind: the kind in lower case with an s."`
	Name      v1.ObjectName    `path:"name" doc:"The object's name."`
}

// addonDeleteOutput is a delete's answer: 202 with the object an engineless kind holds at its deletion, else 204 with
// no body (ADR-0345 Decision 6).
type addonDeleteOutput struct {
	Status int
	Body   *v1.AddonObject
}

type addonCreate struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace" doc:"The object's namespace."`
	Plural    string           `path:"plural" doc:"The plural of the object's add-on kind: the kind in lower case with an s."`
	Body      v1.AddonObject
}

type addonReplace struct {
	IfMatchParams
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace" doc:"The object's namespace."`
	Plural    string           `path:"plural" doc:"The plural of the object's add-on kind: the kind in lower case with an s."`
	Name      v1.ObjectName    `path:"name" doc:"The object's name."`
	Body      v1.AddonObject
}

// registerAddonObjects serves the objects of every add-on's kind at /apis/funcd.io/v1alpha1/namespaces/{namespace}/
// {plural}[/{name}], with a namespaced kind's verbs. A typed route's static segment takes its path first, and an
// Addon whose plural is one is refused, so a plural of funcd's never reaches here.
func registerAddonObjects(api huma.API, h Handlers) {
	ops := addonObjectOperations()
	ops.list.Middlewares = huma.Middlewares{typedPathFirst(api, false)}
	ops.create.Middlewares = huma.Middlewares{typedPathFirst(api, false)}
	ops.get.Middlewares = huma.Middlewares{typedPathFirst(api, true)}
	ops.replace.Middlewares = huma.Middlewares{typedPathFirst(api, true)}
	ops.remove.Middlewares = huma.Middlewares{typedPathFirst(api, true)}
	huma.Register(api, ops.list, func(ctx context.Context, in *addonList) (*listOutput[v1.AddonObject], error) {
		items, err := h.ListAddonObjects(ctx, in.Namespace, in.Plural)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listOutput[v1.AddonObject]{Body: items}, nil
	})
	huma.Register(api, ops.create, func(ctx context.Context, in *addonCreate) (*bodyOutput[v1.AddonObject], error) {
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		return addonRespond(h.CreateAddonObject(withDryRun(ctx, in.DryRun), in.Namespace, in.Plural, in.Body))
	})
	huma.Register(api, ops.get, func(ctx context.Context, in *addonItem) (*bodyOutput[v1.AddonObject], error) {
		return addonRespond(h.GetAddonObject(ctx, in.Namespace, in.Plural, in.Name))
	})
	huma.Register(api, ops.replace, func(ctx context.Context, in *addonReplace) (*bodyOutput[v1.AddonObject], error) {
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		return addonRespond(h.ReplaceAddonObject(withDryRun(ctx, in.DryRun), in.Namespace, in.Plural, in.Name, in.Body))
	})
	huma.Register(api, ops.remove, func(ctx context.Context, in *addonDelete) (*addonDeleteOutput, error) {
		held, err := h.DeleteAddonObject(ctx, in.Namespace, in.Plural, in.Name, in.rv)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		if held == nil {
			return &addonDeleteOutput{Status: http.StatusNoContent}, nil
		}
		return &addonDeleteOutput{Status: http.StatusAccepted, Body: held}, nil
	})
}

// typedPathFirst answers a request that a path of funcd's own reached here only because that path lacks its method, as
// the router would without the add-on routes: 405 with the path's methods in Allow, as a read-only kind's POST gets
// (ADR-0172 Decision 2), or 404 when funcd serves the segment but not this path. A plural of funcd's is never an
// add-on's (ADR-0332 Decision 1), so no add-on object is lost to it. It reads the API's paths at each request, so a
// route registered after this one counts.
func typedPathFirst(api huma.API, item bool) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		segment := apiPrefix + "/namespaces/{namespace}/" + ctx.Param("plural")
		path := segment
		if item {
			path += "/{name}"
		}
		paths := api.OpenAPI().Paths
		if pi, ok := paths[path]; ok {
			ctx.SetHeader("Allow", strings.Join(pathMethods(pi), ", "))
			_ = huma.WriteErr(api, ctx, http.StatusMethodNotAllowed, "method "+ctx.Method()+" is not allowed")
			return
		}
		for p := range paths {
			if strings.HasPrefix(p, segment+"/") || p == segment {
				u := ctx.URL()
				_ = huma.WriteErr(api, ctx, http.StatusNotFound, "no route for "+ctx.Method()+" "+u.EscapedPath())
				return
			}
		}
		next(ctx)
	}
}

// pathMethods are the methods a path serves, in the order the router's 405 lists them.
func pathMethods(pi *huma.PathItem) []string {
	var out []string
	for _, m := range []struct {
		name string
		op   *huma.Operation
	}{{http.MethodGet, pi.Get}, {http.MethodHead, pi.Head}, {http.MethodPost, pi.Post}, {http.MethodPut, pi.Put},
		{http.MethodPatch, pi.Patch}, {http.MethodDelete, pi.Delete}, {http.MethodOptions, pi.Options}} {
		if m.op != nil {
			out = append(out, m.name)
		}
	}
	return out
}

// addonObjectOperations are the described operations of the add-on objects' routes, worded for an object of any
// add-on's kind.
func addonObjectOperations() restOperations {
	base := apiPrefix + "/namespaces/{namespace}/{plural}"
	ops := restOperationsOf("AddonObject", "AddonObjects", base, base+"/{name}", true, nil)
	v := verbCases{kind: "object", kinds: "objects", namespaced: true}
	unregistered := problem(fault.NotFound, "No Addon registers the kind whose plural the path names.",
		"Register the add-on first, or check the plural: the kind in lower case with an s.")
	writes := []ErrorCase{unregistered,
		blank(http.StatusUnprocessableEntity, "spec.config breaks the config schema of the Addon's kind entry; the detail "+
			"names the property.", "Correct spec.config to fit the add-on's schema."),
		problem(fault.Invalid, "The body's kind is not the kind whose plural the path names.",
			"Send the object to its own kind's plural."),
	}
	ops.list = Describe(ops.list, "List the objects of an add-on's kind in a namespace", slices.Concat(v.list(),
		[]ErrorCase{problem(fault.NotFound, "No Addon registers the kind, and the namespace holds no object of it.",
			"Check the plural, or register the add-on.")})...)
	ops.create = Describe(ops.create, "Create an object of an add-on's kind", v.create(append(writes,
		problem(fault.Invalid, "The namespace already holds the most add-on objects that run an engine that "+
			"addons.maxObjectsPerNamespace allows, or the most settings objects that addons.maxSettingsPerNamespace allows.",
			"Delete an object that the namespace no longer needs, or ask the operator to raise the cap.")))...)
	ops.get = Describe(ops.get, "Read an object of an add-on's kind", v.get()...)
	ops.replace = Describe(ops.replace, "Replace an object of an add-on's kind", v.replace(writes)...)
	ops.remove = Describe(ops.remove, "Delete an object of an add-on's kind", v.remove(nil)...)
	// An engineless kind's object with deletion: delete stays, deleting, until its Function has destroyed what it
	// holds: 202 with it; any other object goes, 204 (ADR-0345 Decision 6).
	ops.remove.DefaultStatus = http.StatusAccepted
	ops.remove.Responses["204"] = &huma.Response{Description: "The object is deleted."}
	return ops
}

func addonRespond(o v1.AddonObject, err error) (*bodyOutput[v1.AddonObject], error) {
	if err != nil {
		return nil, wrapFaultError(err)
	}
	return &bodyOutput[v1.AddonObject]{Body: o}, nil
}

// addons reads every Addon, as the server: a user who writes an object needs no right to read its Addon.
func (h *storeHandlers) addons(ctx context.Context) ([]*v1.Addon, error) {
	objs, err := listReader{h.store}.List(ctx, v1.KindAddon.GVK(), "")
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "controlplane.addons", "list Addons")
	}
	out := make([]*v1.Addon, 0, len(objs))
	for _, o := range objs {
		if a, ok := o.(*v1.Addon); ok {
			out = append(out, a)
		}
	}
	return out, nil
}

// servedKind is the kind a plural names: an Addon's own kind, or one of its settings kinds (ADR-0340 Decision 3).
type servedKind struct {
	addon    *v1.Addon
	settings *v1.AddonSettings // nil for the Addon's own kind
}

func (k servedKind) name() string {
	if k.settings != nil {
		return k.settings.Kind
	}
	return k.addon.Spec.Kind()
}

func (k servedKind) schema() json.RawMessage {
	if k.settings != nil {
		return k.settings.Schema
	}
	return k.addon.Spec.KindAttributes().Config
}

// kindFor is the kind of plural, fault.NotFound when no Addon adds it.
func (h *storeHandlers) kindFor(ctx context.Context, plural string) (servedKind, error) {
	all, err := h.addons(ctx)
	if err != nil {
		return servedKind{}, err
	}
	for _, a := range all {
		at := a.Spec.KindAttributes()
		if at.Kind == "" {
			continue // a runtime-only Addon adds no kind (ADR-0344 Decision 10)
		}
		if v1.AddonPlural(at.Kind) == plural {
			return servedKind{addon: a}, nil
		}
		for i := range at.Settings {
			if v1.AddonPlural(at.Settings[i].Kind) == plural {
				return servedKind{addon: a, settings: &at.Settings[i]}, nil
			}
		}
	}
	return servedKind{}, fault.NotFoundf("controlplane.addon", "no Addon registers a kind of the plural %q", plural)
}

// addonFor is the Addon that adds the kind of plural, fault.NotFound when none does.
func (h *storeHandlers) addonFor(ctx context.Context, plural string) (*v1.Addon, error) {
	k, err := h.kindFor(ctx, plural)
	return k.addon, err
}

// storeEngineKinds is the EngineKinds of the stored Addons, resolved as the add-on objects' routes resolve a plural.
type storeEngineKinds struct{ store store.Store }

func (k storeEngineKinds) EngineKind(ctx context.Context, plural string) (string, error) {
	sk, err := (&storeHandlers{store: k.store}).kindFor(ctx, plural)
	if err != nil {
		return "", err
	}
	if sk.settings != nil {
		return "", fault.NotFoundf("controlplane.logs", "the plural %q is a settings kind's, whose objects run no engine", plural)
	}
	return sk.addon.Spec.Kind(), nil
}

// ofPlural reports whether a stored object is of the add-on kind whose plural the path names.
func ofPlural(obj v1.Object, plural string) (*v1.AddonObject, bool) {
	o, ok := obj.(*v1.AddonObject)
	return o, ok && v1.AddonPlural(o.Spec.Kind) == plural
}

// shown is a stored object under its add-on's kind, as the API shows it.
func shown(obj v1.Object, err error) (v1.AddonObject, error) {
	if err != nil {
		return v1.AddonObject{}, err
	}
	o, ok := obj.(*v1.AddonObject)
	if !ok {
		return v1.AddonObject{}, fault.Internalf("controlplane.addon", "%T is not an AddonObject", obj)
	}
	out := *o
	out.TypeMeta = v1.TypeMeta{APIVersion: v1.KindAddonObject.GVK().APIVersion(), Kind: v1.Kind(o.Spec.Kind)}
	return out, nil
}

// stored turns a user's object of a's kind into its stored form: its kind is a's, its spec.kind empty or a's, and its
// config fits a's schema (ADR-0332 Decision 2). A config that breaks the schema is the body's 422, naming each property.
func stored(k servedKind, o v1.AddonObject) (*v1.AddonObject, error) {
	const op = "controlplane.addonObject"
	kind := k.name()
	if string(o.Kind) != kind {
		return nil, fault.Invalidf(op, "body kind %q is not %q, the kind of the path's plural", o.Kind, kind)
	}
	if o.Spec.Kind != "" && o.Spec.Kind != kind {
		return nil, fault.Invalidf(op, "spec.kind %q is not %q: funcd sets it from the path", o.Spec.Kind, kind)
	}
	if err := engineFields(k, o.Spec); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "%s", o.Name)
	}
	if err := settingsFunctions(k, o.Spec.Functions); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "%s", o.Name)
	}
	v, err := contract.Compile(k.schema())
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "compile the %s schema of Addon %q", kind, k.addon.Name)
	}
	config := o.Spec.Config
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	details, err := v.Validate(config)
	if err != nil {
		return nil, err
	}
	if len(details) > 0 {
		errs := make([]error, len(details))
		for i, d := range details {
			errs[i] = &huma.ErrorDetail{Message: d.Message, Location: "body.spec.config" + strings.ReplaceAll(d.Path, "/", ".")}
		}
		return nil, huma.Error422UnprocessableEntity("spec.config does not fit the "+kind+" schema", errs...)
	}
	out := o
	out.TypeMeta = v1.TypeMeta{APIVersion: v1.KindAddonObject.GVK().APIVersion(), Kind: v1.KindAddonObject}
	out.Spec.Kind = kind
	return &out, nil
}

// engineFields refuses an engine field, and a target's spec.functions, on an object of a settings kind (ADR-0340
// Decision 3, ADR-0341 Decision 3).
func engineFields(k servedKind, s v1.AddonObjectSpec) error {
	if k.settings == nil {
		return nil
	}
	if len(s.Bindings.Secret) > 0 || s.Ingress != nil || s.Resources != (v1.ResourceSpec{}) || s.Deletion != "" || len(s.Functions) > 0 {
		return fault.Invalidf("controlplane.addonObject", "a %s object runs no engine: spec.bindings, spec.ingress, "+
			"spec.resources, spec.deletion and spec.functions are not allowed", k.name())
	}
	return nil
}

// settingsFunctions checks an object's own Functions (ADR-0341 Decision 3, ADR-0345 Decision 3): each entry names a
// settings kind of the object's Addon as it is now, or the object's own kind when that kind is engineless, once.
// engineFields refuses them on a settings object.
func settingsFunctions(k servedKind, fns []v1.AddonObjectFunction) error {
	const op = "controlplane.addonObject"
	for i, f := range fns {
		own := k.settings == nil && k.addon.Spec.Engineless() && v1.SameAddonKind(f.Kind, k.addon.Spec.Kind())
		if k.addon.Spec.SettingsKind(f.Kind) == nil && !own {
			return fault.Invalidf(op, "spec.functions[%d].kind %q is neither a settings kind of Addon %q nor the object's own engineless kind",
				i, f.Kind, k.addon.Name)
		}
		for j := range i {
			if v1.SameAddonKind(fns[j].Kind, f.Kind) {
				return fault.Invalidf(op, "spec.functions[%d].kind %q is named by spec.functions[%d] too", i, f.Kind, j)
			}
		}
	}
	return nil
}

func (h *storeHandlers) ListAddonObjects(ctx context.Context, ns v1.NamespaceName, plural string) ([]v1.AddonObject, error) {
	objs, err := h.listObj(ctx, v1.KindAddonObject, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.AddonObject, 0, len(objs))
	for _, obj := range objs {
		if _, ok := ofPlural(obj, plural); ok {
			o, serr := shown(obj, nil)
			if serr != nil {
				return nil, serr
			}
			out = append(out, o)
		}
	}
	if len(out) == 0 {
		if _, err := h.addonFor(ctx, plural); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (h *storeHandlers) GetAddonObject(ctx context.Context, ns v1.NamespaceName, plural string, name v1.ObjectName) (v1.AddonObject, error) {
	obj, err := h.getObj(ctx, v1.KindAddonObject, ns, name)
	if fault.KindOf(err) == fault.NotFound {
		return v1.AddonObject{}, notOfPlural(ns, plural, name)
	}
	if err != nil {
		return v1.AddonObject{}, err
	}
	if _, ok := ofPlural(obj, plural); !ok {
		return v1.AddonObject{}, notOfPlural(ns, plural, name)
	}
	return shown(obj, nil)
}

func notOfPlural(ns v1.NamespaceName, plural string, name v1.ObjectName) error {
	return fault.NotFoundf("controlplane.addonObject", "object %s/%s of the plural %q not found", ns, name, plural)
}

func (h *storeHandlers) CreateAddonObject(ctx context.Context, ns v1.NamespaceName, plural string, o v1.AddonObject) (v1.AddonObject, error) {
	if err := h.authorize(ctx, auth.VerbCreate, v1.KindAddonObject, ns); err != nil {
		return v1.AddonObject{}, err
	}
	k, err := h.kindFor(ctx, plural)
	if err != nil {
		return v1.AddonObject{}, err
	}
	obj, err := stored(k, o)
	if err != nil {
		return v1.AddonObject{}, err
	}
	return shown(h.createObj(ctx, v1.KindAddonObject, obj))
}

func (h *storeHandlers) ReplaceAddonObject(ctx context.Context, ns v1.NamespaceName, plural string, name v1.ObjectName, o v1.AddonObject) (v1.AddonObject, error) {
	if err := h.authorize(ctx, auth.VerbUpdate, v1.KindAddonObject, ns); err != nil {
		return v1.AddonObject{}, err
	}
	k, err := h.kindFor(ctx, plural)
	if err != nil {
		return v1.AddonObject{}, err
	}
	obj, err := stored(k, o)
	if err != nil {
		return v1.AddonObject{}, err
	}
	return shown(h.replaceObjIf(ctx, v1.KindAddonObject, ns, name, obj, func(_ context.Context, cur v1.Object) error {
		if _, ok := ofPlural(cur, plural); !ok {
			return notOfPlural(ns, plural, name)
		}
		return nil
	}))
}

// DeleteAddonObject removes the object through gc.DeleteAddonObject, which holds an engineless one with deletion:
// delete and returns it, deleting, as again for one held already (ADR-0345 Decision 6). If-Match is checked as for any
// delete.
func (h *storeHandlers) DeleteAddonObject(ctx context.Context, ns v1.NamespaceName, plural string, name v1.ObjectName, rv string) (*v1.AddonObject, error) {
	var held *v1.AddonObject
	err := h.deleteWith(ctx, v1.KindAddonObject, ns, name, rv, func(_ context.Context, old v1.Object) error {
		if _, ok := ofPlural(old, plural); !ok {
			return notOfPlural(ns, plural, name)
		}
		return nil
	}, func(ctx context.Context, old v1.Object) error {
		o, ok := old.(*v1.AddonObject)
		if !ok {
			return fault.Internalf("controlplane.addonObject", "%T is not an AddonObject", old)
		}
		var err error
		held, err = gc.DeleteAddonObject(ctx, h.store, o)
		return err
	})
	if err != nil || held == nil {
		return nil, err
	}
	shownHeld, err := shown(held, nil)
	if err != nil {
		return nil, err
	}
	return &shownHeld, nil
}
