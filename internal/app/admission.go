package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
)

const admitOp = "admission.app-parts"

// partsAdmission is app-parts (Decision 3).
type partsAdmission struct {
	parts func(admission.StoreReader) []admission.Admission
	r     admission.StoreReader
	reads bool
}

// NewAdmission returns app-parts. parts builds the admissions a direct write passes over the reader it is given;
// app-parts gives it the view holding the App's other parts. It implements admission.NamespaceReading, true when a
// part admission it runs is.
func NewAdmission(parts func(admission.StoreReader) []admission.Admission, r admission.StoreReader) admission.Admission {
	return partsAdmission{parts: parts, r: r, reads: readsNamespace(parts(r))}
}

// readsNamespace reports whether one of as handles a write of a section kind and reads its namespace (ADR-0147).
func readsNamespace(as []admission.Admission) bool {
	for _, a := range as {
		if nr, ok := a.(admission.NamespaceReading); !ok || !nr.ReadsNamespace() {
			continue
		}
		for _, k := range sectionKinds() {
			if a.Handles(k.GVK(), admission.Create) || a.Handles(k.GVK(), admission.Update) {
				return true
			}
		}
	}
	return false
}

func (partsAdmission) Name() string           { return "app-parts" }
func (partsAdmission) Phase() admission.Phase { return admission.Validating }

// ReadsNamespace holds the namespace lock when a part admission reads the namespace (ADR-0147).
func (a partsAdmission) ReadsNamespace() bool { return a.reads }

func (partsAdmission) Handles(gvk v1.GroupVersionKind, op admission.Operation) bool {
	return gvk == v1.KindApp.GVK() && (op == admission.Create || op == admission.Update)
}

// Admit refuses a Secret that a part names and spec.secrets does not declare (ADR-0213 Decision 7) and a ref to an
// object this App controls, then runs each declared part through the part admissions as a create when it is absent,
// else as an update over the stored object, with the App writer's identity, against a view that already holds the
// App's other parts, so a quota counts them together. A refusal names the entry's path.
func (a partsAdmission) Admit(ctx context.Context, req admission.Request) (v1.Object, error) {
	app, ok := req.Object.(*v1.App)
	if !ok {
		return req.Object, nil
	}
	if err := undeclaredSecret(app); err != nil {
		return nil, err
	}
	st := stored{r: a.r, ns: app.Namespace, lists: map[v1.Kind][]v1.Object{}}
	paths := make(map[v1.ObjectRef]string)
	for _, e := range entries(app) {
		if !e.ref {
			paths[e.key(app.Namespace)] = e.path
			continue
		}
		obj, err := st.get(ctx, e.kind, e.name)
		if err != nil {
			return nil, err
		}
		if obj == nil {
			continue // a ref waits for its target (ADR-0121)
		}
		if c, ok := v1.ControllerOf(obj.GetObjectMeta().OwnerReferences); ok && c.Kind == v1.KindApp && c.Name == app.Name {
			return nil, fault.Invalidf(admitOp, "%s.ref: %s/%s is controlled by App/%s; only a store set to deletion: retain can become a ref", e.path, e.kind, e.name, app.Name)
		}
	}
	parts := app.Parts()
	for i, p := range parts {
		k := keyOf(p)
		old, err := st.get(ctx, k.Kind, k.Name)
		if err != nil {
			return nil, err
		}
		r := admission.Request{Operation: admission.Create, GVK: p.GroupVersionKind(), Object: p, Identity: req.Identity}
		if old != nil {
			r.Operation, r.Old = admission.Update, old
		}
		others := slices.Concat(parts[:i], parts[i+1:])
		if _, err := admission.NewPipeline(a.parts(view{r: a.r, over: others})...).Admit(ctx, r); err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), admitOp, "%s (%s)", paths[k], partName(k))
		}
	}
	return req.Object, nil
}

// undeclaredSecret refuses the first name in a declared Function's or CatalogService's spec.secrets, or in a declared
// Workflow's image step function.secrets, that spec.secrets does not declare (ADR-0213 Decision 7). A ref entry, which
// sets no spec field, and a ref step are exempt: the App does not define them. The rule is admission-only, so an App
// stored before ADR-0213 still writes its status (Decision 9).
func undeclaredSecret(app *v1.App) error {
	declared := make(map[v1.ObjectName]bool, len(app.Spec.Secrets))
	for _, s := range app.Spec.Secrets {
		declared[s.Name] = true
	}
	check := func(path string, kind v1.Kind, name v1.ObjectName, secrets []v1.ObjectName) error {
		for j, n := range secrets {
			if !declared[n] {
				return fault.Invalidf(admitOp, "%s.secrets[%d]: %s/%s names Secret %q, which spec.secrets does not declare", path, j, kind, name, n)
			}
		}
		return nil
	}
	s := &app.Spec
	for i, f := range s.Functions {
		if err := check(fmt.Sprintf("spec.functions[%d]", i), v1.KindFunction, f.Name, f.Secrets); err != nil {
			return err
		}
	}
	for i, w := range s.Workflows {
		for j, st := range w.Steps {
			if st.Function == nil || st.Function.Image == "" {
				continue
			}
			if err := check(fmt.Sprintf("spec.workflows[%d].steps[%d].function", i, j), v1.KindWorkflow, w.Name, st.Function.Secrets); err != nil {
				return err
			}
		}
	}
	for i, c := range s.Catalogs {
		if err := check(fmt.Sprintf("spec.catalogs[%d]", i), v1.KindCatalogService, c.Name, c.Secrets); err != nil {
			return err
		}
	}
	return nil
}

// stored reads the namespace's objects of a kind once per Admit.
type stored struct {
	r     admission.StoreReader
	ns    v1.NamespaceName
	lists map[v1.Kind][]v1.Object
}

// get returns the stored object of kind named name, nil when absent.
func (s *stored) get(ctx context.Context, kind v1.Kind, name v1.ObjectName) (v1.Object, error) {
	items, ok := s.lists[kind]
	if !ok {
		var err error
		if items, err = s.r.List(ctx, kind.GVK(), s.ns); err != nil {
			return nil, fault.Wrapf(err, fault.Internal, admitOp, "list %s in %q", kind, s.ns)
		}
		s.lists[kind] = items
	}
	for _, o := range items {
		if o.GetObjectMeta().Name == name {
			return o, nil
		}
	}
	return nil, nil
}

// view is the store as the App's write would leave it for one part's admission: each of the App's other parts
// replaces the stored object of its kind and name, or is added.
type view struct {
	r    admission.StoreReader
	over []v1.Object
}

func (v view) List(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error) {
	items, err := v.r.List(ctx, gvk, ns)
	if err != nil {
		return nil, err
	}
	out := slices.Clone(items)
	for _, p := range v.over {
		k := keyOf(p)
		if p.GroupVersionKind() != gvk || ns != "" && k.Namespace != ns {
			continue
		}
		if i := slices.IndexFunc(out, func(o v1.Object) bool { return keyOf(o) == k }); i >= 0 {
			out[i] = p
		} else {
			out = append(out, p)
		}
	}
	return out, nil
}
