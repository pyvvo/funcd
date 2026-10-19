package admission

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// Declared is what a resource's root declares, by kind and name (ADR-0248 Decision 2): its name and ref entries, a
// defined ConfigMap under its name and its stored name, and its Secret declarations.
type Declared struct {
	names map[v1.Kind]map[v1.ObjectName]bool
}

// Add declares name of kind.
func (d *Declared) Add(kind v1.Kind, name v1.ObjectName) {
	if name == "" {
		return
	}
	if d.names == nil {
		d.names = map[v1.Kind]map[v1.ObjectName]bool{}
	}
	if d.names[kind] == nil {
		d.names[kind] = map[v1.ObjectName]bool{}
	}
	d.names[kind][name] = true
}

// Has reports whether name of kind is declared; a nil Declared declares nothing.
func (d *Declared) Has(kind v1.Kind, name v1.ObjectName) bool {
	return d != nil && d.names[kind][name]
}

// RootDeclared is what these root entries declare.
func RootDeclared(kv []v1.KVStoreEntry, buckets []v1.BucketEntry, sql []v1.SQLDatabaseEntry, catalogs []v1.CatalogEntry,
	configMaps []v1.ConfigMapEntry, secrets []v1.SecretDeclaration) *Declared {
	d := &Declared{}
	for _, e := range kv {
		d.Add(v1.KindKVStore, e.Name)
		d.Add(v1.KindKVStore, e.Ref)
	}
	for _, e := range buckets {
		d.Add(v1.KindBucket, e.Name)
		d.Add(v1.KindBucket, e.Ref)
	}
	for _, e := range sql {
		d.Add(v1.KindSQLDatabase, e.Name)
		d.Add(v1.KindSQLDatabase, e.Ref)
	}
	for _, e := range catalogs {
		d.Add(v1.KindCatalogService, e.Name)
		d.Add(v1.KindCatalogService, e.Ref)
	}
	for _, e := range configMaps {
		d.Add(v1.KindConfigMap, e.Ref)
		if e.Name != "" {
			d.Add(v1.KindConfigMap, e.Name)
			d.Add(v1.KindConfigMap, v1.AppConfigMapName(e.Name, e.ConfigMapSpec))
		}
	}
	for _, s := range secrets {
		d.Add(v1.KindSecret, s.Name)
	}
	return d
}

// --- binding-declared (ADR-0248 Decision 2, ADR-0251 Decision 2): Create/Update on Function, Workflow, CatalogService

type bindingDeclared struct{ r StoreReader }

// NewBindingDeclaredAdmission returns the Validating admission "binding-declared" (ADR-0248 Decision 2): every store,
// catalog, ConfigMap and Secret a Function binds, its catalog entries' bindings included, is declared at its root, and
// every binding of an image step is declared at its Workflow's (ADR-0250 Decision 2). A part of an App binds what the App
// declares too: Request.Declared under app-parts, else read from r for an object an App controls, so a direct write of
// a part passes as the App's own did. A step Function binds what its Workflow declares. A Site's one bucket is
// Site.Validate's.
// A standalone CatalogService's blob, config and secret bindings are declared at its root (ADR-0251 Decision 2); an
// App's catalog binds buckets the App declares. It replaces ADR-0229's workflow-step-stores.
func NewBindingDeclaredAdmission(r StoreReader) Admission { return bindingDeclared{r: r} }

func (bindingDeclared) Name() string { return "binding-declared" }
func (bindingDeclared) Phase() Phase { return Validating }

// ReadsNamespace: an App's part, or a step Function, is checked against its controller's spec (ADR-0147).
func (bindingDeclared) ReadsNamespace() bool { return true }

func (bindingDeclared) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return (gvk == v1.KindFunction.GVK() || gvk == v1.KindWorkflow.GVK() || gvk == v1.KindCatalogService.GVK()) &&
		(op == Create || op == Update)
}

func (a bindingDeclared) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.binding-declared"
	if req.Object == nil {
		return req.Object, nil
	}
	app, step, err := a.controllerDeclared(ctx, req)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "read the controller of %s", req.Object.GetObjectMeta().Name)
	}
	switch o := req.Object.(type) {
	case *v1.Function:
		if step != nil {
			err = stepFunctionUndeclared(&o.Spec, step, app)
		} else {
			err = functionUndeclared(&o.Spec, app)
		}
	case *v1.Workflow:
		err = workflowUndeclared(&o.Spec, app)
	case *v1.CatalogService:
		err = catalogUndeclared(&o.Spec, app)
	}
	if err != nil {
		return nil, fault.Invalidf(op, "%s", err.Error())
	}
	return req.Object, nil
}

// controllerDeclared returns what the App an object belongs to declares, req.Declared under app-parts, and for a step
// Function its Workflow's declarations. An absent or replaced controller declares nothing.
func (a bindingDeclared) controllerDeclared(ctx context.Context, req Request) (app, step *Declared, err error) {
	app = req.Declared
	c, ok := v1.ControllerOf(req.Object.GetObjectMeta().OwnerReferences)
	if !ok || a.r == nil {
		return app, nil, nil
	}
	ns := req.Object.GetObjectMeta().Namespace
	if c.Kind == v1.KindWorkflow {
		obj, err := a.controller(ctx, c, ns)
		if err != nil || obj == nil {
			return app, nil, err
		}
		wf := obj.(*v1.Workflow)
		step = workflowDeclared(&wf.Spec)
		if c, ok = v1.ControllerOf(wf.OwnerReferences); !ok {
			return app, step, nil
		}
	}
	if c.Kind != v1.KindApp || app != nil {
		return app, step, nil
	}
	obj, err := a.controller(ctx, c, ns)
	if err != nil || obj == nil {
		return nil, step, err
	}
	s := &obj.(*v1.App).Spec
	return RootDeclared(s.KV, s.Buckets, s.SQL, s.Catalogs, s.ConfigMaps, s.Secrets), step, nil
}

// controller is the object c names in ns at c's UID, or nil.
func (a bindingDeclared) controller(ctx context.Context, c v1.OwnerReference, ns v1.NamespaceName) (v1.Object, error) {
	objs, err := a.r.List(ctx, c.Kind.GVK(), ns)
	if err != nil {
		return nil, err
	}
	for _, o := range objs {
		if m := o.GetObjectMeta(); m.Name == c.Name && m.UID == c.UID {
			return o, nil
		}
	}
	return nil, nil
}

// stepFunctionUndeclared checks a step Function as its step: every binding against its Workflow's and its App's
// declarations (ADR-0250 Decision 2).
func stepFunctionUndeclared(s *v1.FunctionSpec, step, app *Declared) error {
	return stepUndeclared(undeclared{root: step, app: app, keys: workflowKeys("its Workflow's spec.")}, "spec.bindings", s.Bindings)
}

// undeclared checks a binding's target against the root's declarations and, for an App's part, the App's; keys names
// the root key that declares each kind.
type undeclared struct {
	root, app *Declared
	keys      map[v1.Kind]string
}

func (u undeclared) check(path string, kind v1.Kind, name v1.ObjectName) error {
	if u.root.Has(kind, name) || u.app.Has(kind, name) {
		return nil
	}
	return fmt.Errorf("%s: %s %q is not declared in %s", path, kind, name, u.keys[kind])
}

func functionUndeclared(s *v1.FunctionSpec, app *Declared) error {
	u := undeclared{
		root: RootDeclared(s.KVStores, s.Buckets, s.SQLDatabases, s.CatalogServices, s.ConfigMaps, s.SecretDeclarations),
		app:  app,
		keys: map[v1.Kind]string{v1.KindKVStore: "spec.kv", v1.KindBucket: "spec.buckets", v1.KindSQLDatabase: "spec.sql",
			v1.KindCatalogService: "spec.catalogs", v1.KindConfigMap: "spec.configMaps", v1.KindSecret: "spec.secrets"},
	}
	b := &s.Bindings
	for i, x := range b.KV {
		if err := u.check(fmt.Sprintf("spec.bindings.kv[%d]", i), v1.KindKVStore, x.Store); err != nil {
			return err
		}
	}
	for i, x := range b.Blob {
		if err := u.check(fmt.Sprintf("spec.bindings.blob[%d]", i), v1.KindBucket, x.Bucket); err != nil {
			return err
		}
	}
	for i, x := range b.SQL {
		if err := u.check(fmt.Sprintf("spec.bindings.sql[%d]", i), v1.KindSQLDatabase, x.Database); err != nil {
			return err
		}
	}
	for i, x := range b.Catalog {
		if err := u.check(fmt.Sprintf("spec.bindings.catalog[%d]", i), v1.KindCatalogService, x.Catalog); err != nil {
			return err
		}
	}
	if err := names(u, "spec.bindings", b.Config, b.Secret); err != nil {
		return err
	}
	for i, c := range s.CatalogServices {
		if c.Name == "" {
			continue
		}
		path := fmt.Sprintf("spec.catalogs[%d].bindings", i)
		for j, x := range c.Bindings.Blob {
			if err := u.check(fmt.Sprintf("%s.blob[%d]", path, j), v1.KindBucket, x.Bucket); err != nil {
				return err
			}
		}
		if err := names(u, path, c.Bindings.Config, c.Bindings.Secret); err != nil {
			return err
		}
	}
	return nil
}

func names(u undeclared, path string, config, secret []v1.ObjectName) error {
	for i, n := range config {
		if err := u.check(fmt.Sprintf("%s.config[%d]", path, i), v1.KindConfigMap, n); err != nil {
			return err
		}
	}
	for i, n := range secret {
		if err := u.check(fmt.Sprintf("%s.secret[%d]", path, i), v1.KindSecret, n); err != nil {
			return err
		}
	}
	return nil
}

// catalogUndeclared checks a CatalogService's bindings: a standalone one's blob, config and secret against its root
// (ADR-0251 Decision 2), an App's catalog's blob against the App's root.
func catalogUndeclared(s *v1.CatalogServiceSpec, app *Declared) error {
	u := undeclared{
		root: RootDeclared(nil, s.Buckets, nil, nil, s.ConfigMaps, s.SecretDeclarations),
		app:  app,
		keys: map[v1.Kind]string{v1.KindBucket: "spec.buckets", v1.KindConfigMap: "spec.configMaps", v1.KindSecret: "spec.secrets"},
	}
	for i, x := range s.Bindings.Blob {
		if err := u.check(fmt.Sprintf("spec.bindings.blob[%d]", i), v1.KindBucket, x.Bucket); err != nil {
			return err
		}
	}
	if app != nil {
		return nil // an App's catalog: only its stores are the App's to declare (ADR-0248 Decision 2)
	}
	return names(u, "spec.bindings", s.Bindings.Config, s.Bindings.Secret)
}

func workflowUndeclared(s *v1.WorkflowSpec, app *Declared) error {
	u := undeclared{root: workflowDeclared(s), app: app, keys: workflowKeys("spec.")}
	for i := range s.Steps {
		f := s.Steps[i].Function
		if f == nil || f.Image == "" {
			continue
		}
		if err := stepUndeclared(u, fmt.Sprintf("spec.steps[%d].function.bindings", i), f.Bindings.FunctionBindings()); err != nil {
			return err
		}
	}
	return nil
}

// workflowDeclared is what a Workflow's root declares (ADR-0248 Decision 1, ADR-0250 Decision 1).
func workflowDeclared(s *v1.WorkflowSpec) *Declared {
	return RootDeclared(s.KVStores, s.Buckets, s.SQLDatabases, s.CatalogServices, s.ConfigMaps, s.SecretDeclarations)
}

// workflowKeys names the Workflow root key that declares each kind, after prefix.
func workflowKeys(prefix string) map[v1.Kind]string {
	return map[v1.Kind]string{v1.KindKVStore: prefix + "kv", v1.KindBucket: prefix + "buckets", v1.KindSQLDatabase: prefix + "sql",
		v1.KindCatalogService: prefix + "catalogs", v1.KindConfigMap: prefix + "configMaps", v1.KindSecret: prefix + "secrets"}
}

// stepUndeclared checks every binding of a step against its Workflow's root, or the App's (ADR-0250 Decision 2).
func stepUndeclared(u undeclared, path string, b v1.FunctionBindings) error {
	for j, x := range b.KV {
		if err := u.check(fmt.Sprintf("%s.kv[%d]", path, j), v1.KindKVStore, x.Store); err != nil {
			return err
		}
	}
	for j, x := range b.Blob {
		if err := u.check(fmt.Sprintf("%s.blob[%d]", path, j), v1.KindBucket, x.Bucket); err != nil {
			return err
		}
	}
	for j, x := range b.SQL {
		if err := u.check(fmt.Sprintf("%s.sql[%d]", path, j), v1.KindSQLDatabase, x.Database); err != nil {
			return err
		}
	}
	for j, x := range b.Catalog {
		if err := u.check(fmt.Sprintf("%s.catalog[%d]", path, j), v1.KindCatalogService, x.Catalog); err != nil {
			return err
		}
	}
	return names(u, path, b.Config, b.Secret)
}

// --- function-store-owner (ADR-0248 Decisions 4 and 5) and catalog-store-owner (ADR-0251 Decision 1) ------------

type functionStoreOwner struct{}

// NewFunctionStoreOwnerAdmission returns the Mutating admission "function-store-owner": an omitted owner of a table, a
// prefix or a database the Function defines at its root becomes the Function (Decision 4), and a bindings.config entry
// naming a ConfigMap it defines becomes that ConfigMap's stored name (Decision 5), also when it names an earlier stored
// name of it, so the environment, the pool key and the GC read the name that holds the data.
func NewFunctionStoreOwnerAdmission() Admission { return functionStoreOwner{} }

func (functionStoreOwner) Name() string { return "function-store-owner" }
func (functionStoreOwner) Phase() Phase { return Mutating }

func (functionStoreOwner) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindFunction.GVK() && (op == Create || op == Update)
}

func (functionStoreOwner) Admit(_ context.Context, req Request) (v1.Object, error) {
	fn, ok := req.Object.(*v1.Function)
	if !ok {
		return req.Object, nil
	}
	s := &fn.Spec
	for i := range s.KVStores {
		if s.KVStores[i].Name == "" {
			continue
		}
		for j := range s.KVStores[i].Tables {
			if t := &s.KVStores[i].Tables[j]; t.Owner == "" {
				t.Owner = fn.Name
			}
		}
	}
	for i := range s.SQLDatabases {
		if d := &s.SQLDatabases[i]; d.Name != "" && d.Owner == "" {
			d.Owner = fn.Name
		}
	}
	ownDefined(fn.Name, s.Buckets, s.ConfigMaps, s.Bindings.Config)
	return fn, nil
}

type catalogStoreOwner struct{}

// NewCatalogStoreOwnerAdmission returns the Mutating admission "catalog-store-owner" (ADR-0251 Decisions 1 and 4), the
// twin of function-store-owner: an omitted owner of a prefix a standalone CatalogService defines at its root becomes
// the CatalogService, and a bindings.config entry naming a ConfigMap it defines becomes that ConfigMap's stored name.
func NewCatalogStoreOwnerAdmission() Admission { return catalogStoreOwner{} }

func (catalogStoreOwner) Name() string { return "catalog-store-owner" }
func (catalogStoreOwner) Phase() Phase { return Mutating }

func (catalogStoreOwner) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindCatalogService.GVK() && (op == Create || op == Update)
}

func (catalogStoreOwner) Admit(_ context.Context, req Request) (v1.Object, error) {
	cs, ok := req.Object.(*v1.CatalogService)
	if !ok {
		return req.Object, nil
	}
	ownDefined(cs.Name, cs.Spec.Buckets, cs.Spec.ConfigMaps, cs.Spec.Bindings.Config)
	return cs, nil
}

// ownDefined writes owner into each omitted owner of a prefix of a bucket defined by name, and repoints each config
// binding that names a defined ConfigMap, or an earlier stored name of it, at its current stored name.
func ownDefined(owner v1.ObjectName, buckets []v1.BucketEntry, configMaps []v1.ConfigMapEntry, config []v1.ObjectName) {
	for i := range buckets {
		if buckets[i].Name == "" {
			continue
		}
		for j := range buckets[i].Prefixes {
			if p := &buckets[i].Prefixes[j]; p.Owner == "" {
				p.Owner = owner
			}
		}
	}
	for i, c := range config {
		for _, e := range configMaps {
			if e.Name != "" && (c == e.Name || storedNameOf(c, e.Name)) {
				config[i] = v1.AppConfigMapName(e.Name, e.ConfigMapSpec)
				break
			}
		}
	}
}

// storedNameOf reports whether c has the shape of a stored name of the ConfigMap name: "<name>-" and 10 hex
// characters (v1.AppConfigMapName).
func storedNameOf(c, name v1.ObjectName) bool {
	suffix, ok := strings.CutPrefix(string(c), string(name)+"-")
	if !ok || len(suffix) != 10 {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}
