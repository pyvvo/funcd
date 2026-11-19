package controlplane

import (
	"context"
	"encoding/json"
	"errors"
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
	addonsvc "github.com/pyvvo/funcd/internal/services/addon"
)

// ===== Addon (cluster-scoped, admins only) — ADR-0332 Decision 1 =====

func registerAddon(api huma.API, h Handlers) {
	writes := []ErrorCase{
		problem(fault.Invalid, "spec.kind is a kind of funcd's, or its plural is a path that funcd serves, whatever the "+
			"case; or a replace changes spec.kind.", "Choose another kind. A new kind needs a new Addon."),
		problem(fault.Invalid, "The registry of spec.image is not one that addons.registries lists.",
			"Use an image from a listed registry, or ask the operator to list its registry."),
		problem(fault.Invalid, "spec.config is not a JSON Schema in funcd's contract profile, or an env value is not a "+
			"literal or a single ${{ }} expression that the schema types.", "Correct the field that the detail names."),
	}
	// Only a create can take another Addon's kind or plural: a replace keeps its kind.
	taken := problem(fault.Conflict, "Another Addon registers the same kind or the same plural, whatever the case.",
		"Choose another kind, or change the other Addon.")
	registerClusterCRUD(api, clusterRoutes[v1.Addon]{
		kind: "Addon", kinds: "Addons", path: "addons",
		list: h.ListAddons, create: h.CreateAddon, get: h.GetAddon, replace: h.ReplaceAddon, remove: h.DeleteAddon,
		cases: &kindCases{create: append(slices.Clone(writes), taken), replace: writes},
	})
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

// AddonRules are the rules of an Addon write that read the store or the config (ADR-0332 Decisions 1, 4 and 6).
type AddonRules struct {
	Store admission.StoreReader // reads the other Addons
	// Registries are addons.registries: the registries an Addon's image may come from; none refuses every Addon.
	Registries []string
	// Reserved are the path segments funcd serves (ReservedPlurals): an add-on's plural among them is refused.
	Reserved []string
}

// NewAddonAdmissions returns the Validating admissions of an Addon write. A create's kind and plural are neither
// funcd's nor another Addon's, without case: that one reads every Addon, so it is marked, and two creates take the
// cluster-scoped admission lock (ADR-0147) and cannot register one plural. Every write keeps its kind, names an image
// from a listed registry, passes its config schema through the contract profile and has each env value a literal or
// one ${{ }} expression that the schema types, restore.env's reading restore.file too (ADR-0333 Decision 1): a change
// takes no lock, and its kind, kept, keeps its plural unique.
func NewAddonAdmissions(r AddonRules) []admission.Admission {
	return []admission.Admission{addonUnique{r}, addonRules{r}}
}

type addonUnique struct{ AddonRules }

func (addonUnique) Name() string           { return "addon-unique" }
func (addonUnique) Phase() admission.Phase { return admission.Validating }
func (addonUnique) ReadsNamespace() bool   { return true }

func (addonUnique) Handles(gvk v1.GroupVersionKind, op admission.Operation) bool {
	return gvk == v1.KindAddon.GVK() && op == admission.Create
}

func (a addonUnique) Admit(ctx context.Context, req admission.Request) (v1.Object, error) {
	const op = "admission.addon-unique"
	ad, ok := req.Object.(*v1.Addon)
	if !ok {
		return req.Object, nil
	}
	plural := v1.AddonPlural(ad.Spec.Kind)
	if slices.Contains(a.Reserved, plural) {
		return nil, fault.Invalidf(op, "the plural %q of spec.kind %q is a path that funcd serves", plural, ad.Spec.Kind)
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
		if strings.EqualFold(other.Spec.Kind, ad.Spec.Kind) || v1.AddonPlural(other.Spec.Kind) == plural {
			return nil, fault.Conflictf(op, "Addon %q registers kind %q, of the plural %q", other.Name, other.Spec.Kind, plural)
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
	if old, ok := req.Old.(*v1.Addon); ok && old.Spec.Kind != ad.Spec.Kind {
		return nil, fault.Invalidf(op, "spec.kind cannot change from %q to %q: a new kind needs a new Addon", old.Spec.Kind, ad.Spec.Kind)
	}
	if !AddonImageAllowed(ad.Spec.Image, a.Registries) {
		return nil, fault.Invalidf(op, "spec.image %q does not come from a registry that addons.registries lists (%s)",
			ad.Spec.Image, strings.Join(a.Registries, ", "))
	}
	if err := contract.Check(ad.Spec.Config); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "spec.config")
	}
	if err := addonsvc.CheckEnv(ad.Spec.Env, ad.Spec.Config); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "spec.env")
	}
	if err := addonsvc.CheckRestoreEnv(ad.Spec.Restore, ad.Spec.Config); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "spec.restore.env")
	}
	return req.Object, nil
}

// AddonImageAllowed reports whether an Addon may name image (ADR-0332 Decision 6): it is pinned by digest, with no tag,
// and its registry, its host after docker.io normalization as internal/runtime/containerd/image_ref.go normalizes
// it, is one of registries. The containerd driver pulls such an image (cmd/funcd's Pullable).
func AddonImageAllowed(image string, registries []string) bool {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return false
	}
	_, digested := named.(reference.Digested)
	_, tagged := named.(reference.Tagged)
	host := reference.Domain(named)
	return digested && !tagged && slices.ContainsFunc(registries, func(r string) bool { return strings.EqualFold(r, host) })
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
			return nil, addonFault(err)
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
	huma.Register(api, ops.remove, func(ctx context.Context, in *addonDelete) (*struct{}, error) {
		return nil, addonFault(h.DeleteAddonObject(ctx, in.Namespace, in.Plural, in.Name, in.rv))
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
		blank(http.StatusUnprocessableEntity, "spec.config breaks the schema of the Addon's spec.config; the detail "+
			"names the property.", "Correct spec.config to fit the add-on's schema."),
		problem(fault.Invalid, "The body's kind is not the kind whose plural the path names.",
			"Send the object to its own kind's plural."),
	}
	ops.list = Describe(ops.list, "List the objects of an add-on's kind in a namespace", slices.Concat(v.list(),
		[]ErrorCase{problem(fault.NotFound, "No Addon registers the kind, and the namespace holds no object of it.",
			"Check the plural, or register the add-on.")})...)
	ops.create = Describe(ops.create, "Create an object of an add-on's kind", v.create(append(writes,
		problem(fault.Invalid, "The namespace already holds the most add-on objects that addons.maxObjectsPerNamespace "+
			"allows.", "Delete an object that the namespace no longer needs, or ask the operator to raise the cap.")))...)
	ops.get = Describe(ops.get, "Read an object of an add-on's kind", v.get()...)
	ops.replace = Describe(ops.replace, "Replace an object of an add-on's kind", v.replace(writes)...)
	ops.remove = Describe(ops.remove, "Delete an object of an add-on's kind", v.remove(nil)...)
	return ops
}

// addonFault keeps a huma error, the 422 of a config that breaks its schema, and maps every other through api/fault.
func addonFault(err error) error {
	var fe *faultError
	if errors.As(err, &fe) {
		return fe
	}
	return wrapFaultError(err)
}

func addonRespond(o v1.AddonObject, err error) (*bodyOutput[v1.AddonObject], error) {
	if err != nil {
		return nil, addonFault(err)
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

// addonFor is the Addon whose kind has plural, fault.NotFound when none registers it.
func (h *storeHandlers) addonFor(ctx context.Context, plural string) (*v1.Addon, error) {
	all, err := h.addons(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range all {
		if v1.AddonPlural(a.Spec.Kind) == plural {
			return a, nil
		}
	}
	return nil, fault.NotFoundf("controlplane.addon", "no Addon registers a kind of the plural %q", plural)
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
func stored(a *v1.Addon, o v1.AddonObject) (*v1.AddonObject, error) {
	const op = "controlplane.addonObject"
	if string(o.Kind) != a.Spec.Kind {
		return nil, fault.Invalidf(op, "body kind %q is not %q, the kind of the path's plural", o.Kind, a.Spec.Kind)
	}
	if o.Spec.Kind != "" && o.Spec.Kind != a.Spec.Kind {
		return nil, fault.Invalidf(op, "spec.kind %q is not %q: funcd sets it from the path", o.Spec.Kind, a.Spec.Kind)
	}
	v, err := contract.Compile(a.Spec.Config)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "compile the config schema of Addon %q", a.Name)
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
		return nil, huma.Error422UnprocessableEntity("spec.config does not fit the "+a.Spec.Kind+" schema", errs...)
	}
	out := o
	out.TypeMeta = v1.TypeMeta{APIVersion: v1.KindAddonObject.GVK().APIVersion(), Kind: v1.KindAddonObject}
	out.Spec.Kind = a.Spec.Kind
	return &out, nil
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
	a, err := h.addonFor(ctx, plural)
	if err != nil {
		return v1.AddonObject{}, err
	}
	obj, err := stored(a, o)
	if err != nil {
		return v1.AddonObject{}, err
	}
	return shown(h.createObj(ctx, v1.KindAddonObject, obj))
}

func (h *storeHandlers) ReplaceAddonObject(ctx context.Context, ns v1.NamespaceName, plural string, name v1.ObjectName, o v1.AddonObject) (v1.AddonObject, error) {
	if err := h.authorize(ctx, auth.VerbUpdate, v1.KindAddonObject, ns); err != nil {
		return v1.AddonObject{}, err
	}
	a, err := h.addonFor(ctx, plural)
	if err != nil {
		return v1.AddonObject{}, err
	}
	obj, err := stored(a, o)
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

func (h *storeHandlers) DeleteAddonObject(ctx context.Context, ns v1.NamespaceName, plural string, name v1.ObjectName, rv string) error {
	return h.deleteGuarded(ctx, v1.KindAddonObject, ns, name, rv, func(_ context.Context, old v1.Object) error {
		if _, ok := ofPlural(old, plural); !ok {
			return notOfPlural(ns, plural, name)
		}
		return nil
	})
}
