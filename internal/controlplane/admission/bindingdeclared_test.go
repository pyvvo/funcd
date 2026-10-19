package admission_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
)

func rootFunction(mutate func(*v1.FunctionSpec)) *v1.Function {
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	fn.Name, fn.Namespace, fn.ResourceGroup = "counter", "default", "rg1"
	fn.Spec.KVStores = []v1.KVStoreEntry{{Name: "counters", KVStoreSpec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "counts"}}}}, {Ref: "sessions"}}
	fn.Spec.Bindings.KV = []v1.FunctionKV{{Alias: "c", Store: "counters", Table: "counts"}, {Alias: "s", Store: "sessions", Table: "t"}}
	if mutate != nil {
		mutate(&fn.Spec)
	}
	return fn
}

// ADR-0248 Decision 2: a Function binds only what its root declares, by name or ref, a part of an App also what the App
// declares; each refusal names the binding, the target and the root key.
func TestBindingDeclaredFunction(t *testing.T) {
	t.Parallel()
	a := admission.NewBindingDeclaredAdmission(nil)
	require.Equal(t, "binding-declared", a.Name())
	require.Equal(t, admission.Validating, a.Phase())
	for _, k := range []v1.Kind{v1.KindFunction, v1.KindWorkflow, v1.KindCatalogService} {
		require.True(t, a.Handles(k.GVK(), admission.Create))
		require.True(t, a.Handles(k.GVK(), admission.Update))
		require.False(t, a.Handles(k.GVK(), admission.Delete))
	}
	ctx := context.Background()
	admit := func(fn *v1.Function, d *admission.Declared) error {
		_, err := a.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindFunction.GVK(), Object: fn, Declared: d})
		return err
	}
	require.NoError(t, admit(rootFunction(nil), nil))

	for name, tc := range map[string]struct {
		mutate func(*v1.FunctionSpec)
		want   string
	}{
		"kv": {func(s *v1.FunctionSpec) { s.Bindings.KV[0].Store = "ghost" },
			`spec.bindings.kv[0]: KVStore "ghost" is not declared in spec.kv`},
		"blob": {func(s *v1.FunctionSpec) {
			s.Bindings.Blob = []v1.FunctionBlob{{Alias: "f", Bucket: "files", Prefix: "p"}}
		},
			`spec.bindings.blob[0]: Bucket "files" is not declared in spec.buckets`},
		"sql": {func(s *v1.FunctionSpec) { s.Bindings.SQL = []v1.FunctionSQL{{Alias: "db", Database: "notes"}} },
			`spec.bindings.sql[0]: SQLDatabase "notes" is not declared in spec.sql`},
		"catalog": {func(s *v1.FunctionSpec) { s.Bindings.Catalog = []v1.FunctionCatalog{{Alias: "lake", Catalog: "lake"}} },
			`spec.bindings.catalog[0]: CatalogService "lake" is not declared in spec.catalogs`},
		"config": {func(s *v1.FunctionSpec) { s.Bindings.Config = []v1.ObjectName{"settings"} },
			`spec.bindings.config[0]: ConfigMap "settings" is not declared in spec.configMaps`},
		"secret": {func(s *v1.FunctionSpec) { s.Bindings.Secret = []v1.ObjectName{"api-keys"} },
			`spec.bindings.secret[0]: Secret "api-keys" is not declared in spec.secrets`},
		"catalog entry": {func(s *v1.FunctionSpec) {
			s.CatalogServices = []v1.CatalogEntry{{Name: "lake", CatalogServiceSpec: v1.CatalogServiceSpec{
				Bindings: v1.CatalogServiceBindings{Blob: []v1.FunctionBlob{{Alias: "l", Bucket: "files", Prefix: "lake"}}}}}}
		}, `spec.catalogs[0].bindings.blob[0]: Bucket "files" is not declared in spec.buckets`},
	} {
		err := admit(rootFunction(tc.mutate), nil)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
		require.EqualError(t, err, "admission.binding-declared: "+tc.want, name)
	}

	declared := rootFunction(func(s *v1.FunctionSpec) {
		s.Buckets = []v1.BucketEntry{{Ref: "files"}}
		s.SQLDatabases = []v1.SQLDatabaseEntry{{Name: "notes"}}
		s.CatalogServices = []v1.CatalogEntry{{Ref: "lake"}}
		s.ConfigMaps = []v1.ConfigMapEntry{{Name: "settings", ConfigMapSpec: v1.ConfigMapSpec{Data: map[string]string{"TZ": "UTC"}}}}
		s.SecretDeclarations = []v1.SecretDeclaration{{Name: "api-keys", Keys: []string{"STRIPE_KEY"}}}
		s.Bindings.Blob = []v1.FunctionBlob{{Alias: "f", Bucket: "files", Prefix: "p"}}
		s.Bindings.SQL = []v1.FunctionSQL{{Alias: "db", Database: "notes"}}
		s.Bindings.Catalog = []v1.FunctionCatalog{{Alias: "lake", Catalog: "lake"}}
		s.Bindings.Config = []v1.ObjectName{v1.AppConfigMapName("settings", s.ConfigMaps[0].ConfigMapSpec)}
		s.Bindings.Secret = []v1.ObjectName{"api-keys"}
	})
	require.NoError(t, admit(declared, nil), "every binding declared by name or ref, a ConfigMap under its stored name")

	app := &admission.Declared{}
	app.Add(v1.KindKVStore, "todo-store")
	part := rootFunction(func(s *v1.FunctionSpec) {
		s.KVStores = nil
		s.Bindings.KV = []v1.FunctionKV{{Alias: "t", Store: "todo-store", Table: "todos"}}
	})
	require.NoError(t, admit(part, app), "an App's part binds the App's store")
	require.Error(t, admit(part, nil), "the same Function on its own does not")
}

// ADR-0248 Decision 2 and ADR-0250 Decision 2: an image step binds only what its Workflow declares, every kind of
// binding; a ref step is not checked.
func TestBindingDeclaredWorkflow(t *testing.T) {
	t.Parallel()
	a := admission.NewBindingDeclaredAdmission(nil)
	wf := func(mutate func(*v1.WorkflowSpec)) *v1.Workflow {
		w := &v1.Workflow{TypeMeta: v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow}}
		w.Name, w.Namespace, w.ResourceGroup = "w", "default", "rg1"
		w.Spec.KVStores = []v1.KVStoreEntry{{Name: "own"}, {Ref: "shared"}}
		w.Spec.Buckets = []v1.BucketEntry{{Name: "files"}}
		w.Spec.SQLDatabases = []v1.SQLDatabaseEntry{{Ref: "notes"}}
		w.Spec.CatalogServices = []v1.CatalogEntry{{Ref: "lake"}}
		w.Spec.ConfigMaps = []v1.ConfigMapEntry{{Name: "settings"}}
		w.Spec.SecretDeclarations = []v1.SecretDeclaration{{Name: "api-keys", Keys: []string{"K"}}}
		w.Spec.Steps = []v1.WorkflowStep{
			{Name: "r", Function: &v1.FunctionStep{Ref: "f", Bindings: v1.StepBindings{KV: []v1.FunctionKV{{Alias: "e", Store: "elsewhere", Table: "t"}}}}},
			{Name: "s", Function: &v1.FunctionStep{Image: "oci:s", Bindings: v1.StepBindings{
				KV:      []v1.FunctionKV{{Alias: "o", Store: "own", Table: "t"}, {Alias: "sh", Store: "shared", Table: "t"}},
				Blob:    []v1.FunctionBlob{{Alias: "f", Bucket: "files", Prefix: "p"}},
				SQL:     []v1.FunctionSQL{{Alias: "db", Database: "notes"}},
				Catalog: []v1.FunctionCatalog{{Alias: "lake", Catalog: "lake"}},
				Config:  []v1.ObjectName{"settings"},
				Secret:  []v1.ObjectName{"api-keys"},
			}}},
		}
		if mutate != nil {
			mutate(&w.Spec)
		}
		return w
	}
	ctx := context.Background()
	admit := func(w *v1.Workflow) error {
		_, err := a.Admit(ctx, admission.Request{Operation: admission.Update, GVK: v1.KindWorkflow.GVK(), Object: w, Old: w})
		return err
	}
	require.NoError(t, admit(wf(nil)))
	require.EqualError(t, admit(wf(func(s *v1.WorkflowSpec) { s.KVStores = s.KVStores[:1] })),
		`admission.binding-declared: spec.steps[1].function.bindings.kv[1]: KVStore "shared" is not declared in spec.kv`)
	require.EqualError(t, admit(wf(func(s *v1.WorkflowSpec) { s.Buckets = nil })),
		`admission.binding-declared: spec.steps[1].function.bindings.blob[0]: Bucket "files" is not declared in spec.buckets`)
	for name, tc := range map[string]struct {
		mutate func(*v1.WorkflowSpec)
		want   string
	}{
		"sql":     {func(s *v1.WorkflowSpec) { s.SQLDatabases = nil }, `spec.steps[1].function.bindings.sql[0]: SQLDatabase "notes" is not declared in spec.sql`},
		"catalog": {func(s *v1.WorkflowSpec) { s.CatalogServices = nil }, `spec.steps[1].function.bindings.catalog[0]: CatalogService "lake" is not declared in spec.catalogs`},
		"config":  {func(s *v1.WorkflowSpec) { s.ConfigMaps = nil }, `spec.steps[1].function.bindings.config[0]: ConfigMap "settings" is not declared in spec.configMaps`},
		"secret":  {func(s *v1.WorkflowSpec) { s.SecretDeclarations = nil }, `spec.steps[1].function.bindings.secret[0]: Secret "api-keys" is not declared in spec.secrets`},
	} {
		require.EqualError(t, admit(wf(tc.mutate)), "admission.binding-declared: "+tc.want, name)
	}
}

// ADR-0248 Decisions 4 and 5: function-store-owner writes the Function in as the omitted owner of what it defines, and
// points a config binding at the stored name of the ConfigMap it defines, also from an earlier stored name.
func TestFunctionStoreOwner(t *testing.T) {
	t.Parallel()
	a := admission.NewFunctionStoreOwnerAdmission()
	require.Equal(t, "function-store-owner", a.Name())
	require.Equal(t, admission.Mutating, a.Phase())
	require.True(t, a.Handles(v1.KindFunction.GVK(), admission.Create))
	require.False(t, a.Handles(v1.KindWorkflow.GVK(), admission.Create))

	data := v1.ConfigMapSpec{Data: map[string]string{"TZ": "UTC"}}
	stored := v1.AppConfigMapName("settings", data)
	fn := rootFunction(func(s *v1.FunctionSpec) {
		s.KVStores[0].Tables = append(s.KVStores[0].Tables, v1.KVTable{Name: "audit", Owner: "auditor"})
		s.Buckets = []v1.BucketEntry{{Name: "files", BucketSpec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "raw"}}}}, {Ref: "shared"}}
		s.SQLDatabases = []v1.SQLDatabaseEntry{{Name: "notes"}}
		s.ConfigMaps = []v1.ConfigMapEntry{{Name: "settings", ConfigMapSpec: data}, {Ref: "flags"}}
		s.Bindings.Config = []v1.ObjectName{"settings", "flags", "settings-0123456789", "settings-other"}
	})
	out, err := a.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: v1.KindFunction.GVK(), Object: fn})
	require.NoError(t, err)
	s := out.(*v1.Function).Spec
	require.Equal(t, []v1.KVTable{{Name: "counts", Owner: "counter"}, {Name: "audit", Owner: "auditor"}}, s.KVStores[0].Tables, "an explicit owner is kept")
	require.Equal(t, v1.ObjectName("counter"), s.Buckets[0].Prefixes[0].Owner)
	require.Equal(t, v1.ObjectName("counter"), s.SQLDatabases[0].Owner)
	require.Equal(t, []v1.ObjectName{stored, "flags", stored, "settings-other"}, s.Bindings.Config)
}

// listed is a StoreReader over fixed objects.
type listed []v1.Object

func (l listed) List(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error) {
	var out []v1.Object
	for _, o := range l {
		if o.GroupVersionKind() == gvk && o.GetObjectMeta().Namespace == ns {
			out = append(out, o)
		}
	}
	return out, nil
}

// ADR-0248 Decision 2: a direct write of an App's part binds what the App declares, and a step Function what its
// Workflow and that Workflow's App declare, with the step's other bindings the first exception; a controller of
// another UID declares nothing.
func TestBindingDeclaredReadsTheController(t *testing.T) {
	t.Parallel()
	app := &v1.App{TypeMeta: v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp}}
	app.Name, app.Namespace, app.UID = "todo", "default", "app-uid"
	app.Spec.KV = []v1.KVStoreEntry{{Name: "todo-store"}}
	wf := &v1.Workflow{TypeMeta: v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow}}
	wf.Name, wf.Namespace, wf.UID = "plan", "default", "wf-uid"
	wf.Spec.KVStores = []v1.KVStoreEntry{{Name: "plan-kv"}}
	wf.Spec.SQLDatabases = []v1.SQLDatabaseEntry{{Ref: "notes"}}
	wf.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Name: "todo"}, UID: "app-uid", Controller: true}}
	a := admission.NewBindingDeclaredAdmission(listed{app, wf})
	controlled := func(kind v1.Kind, name v1.ObjectName, uid v1.UID, b v1.FunctionBindings) *v1.Function {
		fn := rootFunction(func(s *v1.FunctionSpec) { s.KVStores, s.Bindings = nil, b })
		fn.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: kind, Name: name}, UID: uid, Controller: true}}
		return fn
	}
	admit := func(fn *v1.Function) error {
		_, err := a.Admit(context.Background(), admission.Request{Operation: admission.Update, GVK: v1.KindFunction.GVK(), Object: fn, Old: fn})
		return err
	}
	kv := func(store v1.ObjectName) []v1.FunctionKV {
		return []v1.FunctionKV{{Alias: "k", Store: store, Table: "t"}}
	}
	require.NoError(t, admit(controlled(v1.KindApp, "todo", "app-uid", v1.FunctionBindings{KV: kv("todo-store")})), "a part binds the App's store")
	require.Error(t, admit(controlled(v1.KindApp, "todo", "old-uid", v1.FunctionBindings{KV: kv("todo-store")})), "another incarnation declares nothing")
	step := v1.FunctionBindings{KV: append(kv("plan-kv"), kv("todo-store")...), SQL: []v1.FunctionSQL{{Alias: "db", Database: "notes"}}}
	require.NoError(t, admit(controlled(v1.KindWorkflow, "plan", "wf-uid", step)), "a step binds its Workflow's and its App's stores")
	require.EqualError(t, admit(controlled(v1.KindWorkflow, "plan", "wf-uid", v1.FunctionBindings{KV: kv("ghost")})),
		`admission.binding-declared: spec.bindings.kv[0]: KVStore "ghost" is not declared in its Workflow's spec.kv`)
	require.EqualError(t, admit(controlled(v1.KindWorkflow, "plan", "wf-uid", v1.FunctionBindings{SQL: []v1.FunctionSQL{{Alias: "db", Database: "ghost"}}})),
		`admission.binding-declared: spec.bindings.sql[0]: SQLDatabase "ghost" is not declared in its Workflow's spec.sql`)
}

// ADR-0251 Decisions 1 and 4: catalog-store-owner, function-store-owner's twin, writes the CatalogService into an
// omitted prefix owner of a bucket it defines and points a config binding at the stored ConfigMap; a ref is untouched.
func TestCatalogStoreOwner(t *testing.T) {
	t.Parallel()
	a := admission.NewCatalogStoreOwnerAdmission()
	require.Equal(t, "catalog-store-owner", a.Name())
	require.Equal(t, admission.Mutating, a.Phase())
	require.True(t, a.Handles(v1.KindCatalogService.GVK(), admission.Update))
	require.False(t, a.Handles(v1.KindFunction.GVK(), admission.Create))

	data := v1.ConfigMapSpec{Data: map[string]string{"DUCKDB_THREADS": "2"}}
	cs := &v1.CatalogService{TypeMeta: v1.TypeMeta{APIVersion: v1.KindCatalogService.GVK().APIVersion(), Kind: v1.KindCatalogService}}
	cs.Name, cs.Namespace = "lake", "default"
	cs.Spec.Buckets = []v1.BucketEntry{
		{Name: "lakehouse", BucketSpec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "lake"}, {Name: "raw", Owner: "ingest"}}}},
		{Ref: "landing"},
	}
	cs.Spec.ConfigMaps = []v1.ConfigMapEntry{{Name: "tuning", ConfigMapSpec: data}}
	cs.Spec.Bindings.Config = []v1.ObjectName{"tuning"}
	out, err := a.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: v1.KindCatalogService.GVK(), Object: cs})
	require.NoError(t, err)
	s := out.(*v1.CatalogService).Spec
	require.Equal(t, []v1.BucketPrefix{{Name: "lake", Owner: "lake"}, {Name: "raw", Owner: "ingest"}}, s.Buckets[0].Prefixes)
	require.Equal(t, []v1.ObjectName{v1.AppConfigMapName("tuning", data)}, s.Bindings.Config)
}

// ADR-0251 Decision 2: a standalone CatalogService binds only what its root declares; an App's catalog checks only its
// buckets against the App's root, since an App's ConfigMaps and Secrets are declared for its Functions (ADR-0248).
func TestBindingDeclaredCatalogService(t *testing.T) {
	t.Parallel()
	a := admission.NewBindingDeclaredAdmission(nil)
	admit := func(s v1.CatalogServiceSpec, d *admission.Declared) error {
		cs := &v1.CatalogService{Spec: s}
		_, err := a.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: v1.KindCatalogService.GVK(), Object: cs, Declared: d})
		return err
	}
	bound := v1.CatalogServiceSpec{Bindings: v1.CatalogServiceBindings{
		Blob: []v1.FunctionBlob{{Alias: "lake", Bucket: "lakehouse", Prefix: "lake"}}, Config: []v1.ObjectName{"tuning"}, Secret: []v1.ObjectName{"quack"}}}
	declared := bound
	declared.Buckets = []v1.BucketEntry{{Ref: "lakehouse"}}
	declared.ConfigMaps = []v1.ConfigMapEntry{{Ref: "tuning"}}
	declared.SecretDeclarations = []v1.SecretDeclaration{{Name: "quack", Keys: []string{"QUACK_TOKEN"}}}
	require.NoError(t, admit(declared, nil))
	require.ErrorContains(t, admit(bound, nil), `spec.bindings.blob[0]: Bucket "lakehouse" is not declared in spec.buckets`)
	noConfig := declared
	noConfig.ConfigMaps = nil
	require.ErrorContains(t, admit(noConfig, nil), `spec.bindings.config[0]: ConfigMap "tuning" is not declared in spec.configMaps`)
	noSecret := declared
	noSecret.SecretDeclarations = nil
	require.ErrorContains(t, admit(noSecret, nil), `spec.bindings.secret[0]: Secret "quack" is not declared in spec.secrets`)

	app := admission.RootDeclared(nil, []v1.BucketEntry{{Name: "lakehouse"}}, nil, nil, nil, nil)
	require.NoError(t, admit(bound, app), "an App's catalog: its config and secret are not the App's root's to declare")
	require.ErrorContains(t, admit(bound, admission.RootDeclared(nil, nil, nil, nil, nil, nil)), `Bucket "lakehouse" is not declared`)
}
