//go:build e2e

package funcd_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// rootWorkflow is Workflow name with one image step running img, with mutate applied to its spec.
func rootWorkflow(name, step, img string, mutate func(*v1.WorkflowSpec)) *v1.Workflow {
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowSpec{Steps: []v1.WorkflowStep{{Name: v1.ObjectName(step), Function: &v1.FunctionStep{Image: img}}}},
	}
	if mutate != nil {
		mutate(&wf.Spec)
	}
	return wf
}

// waitWorkflow waits until Workflow name is Ready=False with reason.
func (e *gcEnv) waitWorkflow(t *testing.T, name, reason string) *v1.Workflow {
	t.Helper()
	var wf *v1.Workflow
	require.Eventually(t, func() bool {
		wf = e.object(t, v1.KindWorkflow, name).(*v1.Workflow)
		c, ok := wf.Status.Conditions.Get("Ready")
		return ok && c.Status == v1.ConditionFalse && c.Reason == reason
	}, 30*time.Second, 50*time.Millisecond, "Workflow/%s is Ready=False %s", name, reason)
	return wf
}

// scenario: workflow-owns-database
func TestScenarioWorkflowOwnsDatabase(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	e.apply(t, rootWorkflow("orders", "ingest", e.image(t), func(s *v1.WorkflowSpec) {
		s.SQLDatabases = []v1.SQLDatabaseEntry{{Name: "orders-db", SQLDatabaseSpec: v1.SQLDatabaseSpec{Owner: "ingest"}}}
		s.Steps[0].Function.Bindings.SQL = []v1.FunctionSQL{{Alias: "db", Database: "orders-db"}}
	}))
	e.waitExists(t, v1.KindSQLDatabase, "orders-db")
	wf := e.object(t, v1.KindWorkflow, "orders")
	db := e.object(t, v1.KindSQLDatabase, "orders-db").(*v1.SQLDatabase)
	require.True(t, ownedstore.Marked(db.OwnerReferences, wf), "SQLDatabase orders-db carries orders' marker")
	require.Equal(t, v1.ObjectName("orders-ingest"), db.Spec.Owner, "the owner names the step's Function, its writer")
	e.waitExists(t, v1.KindFunction, "orders-ingest")
	require.Equal(t, []v1.FunctionSQL{{Alias: "db", Database: "orders-db"}}, e.function(t, "orders-ingest").Spec.Bindings.SQL)
}

// scenario: workflow-config-hashed
func TestScenarioWorkflowConfigHashed(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	img := e.image(t)
	withSettings := func(v string) *v1.Workflow {
		return rootWorkflow("report", "build", img, func(s *v1.WorkflowSpec) {
			s.ConfigMaps = []v1.ConfigMapEntry{{Name: "settings", ConfigMapSpec: v1.ConfigMapSpec{Data: map[string]string{"TZ": v}}}}
			s.Steps[0].Function.Bindings.Config = []v1.ObjectName{"settings"}
		})
	}
	stored := func(v string) v1.ObjectName {
		return v1.AppConfigMapName("settings", v1.ConfigMapSpec{Data: map[string]string{"TZ": v}})
	}
	e.apply(t, withSettings("UTC"))
	e.waitExists(t, v1.KindConfigMap, string(stored("UTC")))
	wf := e.object(t, v1.KindWorkflow, "report")
	cm := e.object(t, v1.KindConfigMap, string(stored("UTC"))).(*v1.ConfigMap)
	require.True(t, v1.ControlledBy(cm.OwnerReferences, v1.KindWorkflow, wf.GetObjectMeta().UID), "the Workflow controls settings-<hash>")
	require.Eventually(t, func() bool {
		return e.exists(t, v1.KindFunction, "report-build") &&
			len(e.function(t, "report-build").Spec.Bindings.Config) == 1 && e.function(t, "report-build").Spec.Bindings.Config[0] == stored("UTC")
	}, 30*time.Second, 50*time.Millisecond, "the step Function binds settings-<hash>")

	e.apply(t, withSettings("Asia/Kathmandu"))
	require.Eventually(t, func() bool {
		return e.function(t, "report-build").Spec.Bindings.Config[0] == stored("Asia/Kathmandu")
	}, 30*time.Second, 50*time.Millisecond, "the step rolls to the new object")
	e.waitGone(t, v1.KindConfigMap, string(stored("UTC")))
}

// scenario: workflow-secret-check
func TestScenarioWorkflowSecretCheck(t *testing.T) {
	t.Parallel()
	e := startGC(t, funcd.WithPacing(funcd.Pacing{ArtifactPollInterval: 200 * time.Millisecond}))
	e.apply(t, rootWorkflow("billing", "charge", e.image(t), func(s *v1.WorkflowSpec) {
		s.SecretDeclarations = []v1.SecretDeclaration{{Name: "api-keys", Keys: []string{"STRIPE_KEY"}}}
		s.Steps[0].Function.Bindings.Secret = []v1.ObjectName{"api-keys"}
	}))
	e.waitWorkflow(t, "billing", ownedstore.ReasonSecretNotFound)
	require.False(t, e.exists(t, v1.KindFunction, "billing-charge"), "no step is materialized while a declared Secret is missing")

	sec := &v1.Secret{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret}}
	sec.Name, sec.Namespace, sec.ResourceGroup = "api-keys", "default", "rg1"
	sec.Spec.Type, sec.Spec.Data = "Opaque", map[string][]byte{"OTHER": []byte("sk-other")}
	e.apply(t, sec)
	wf := e.waitWorkflow(t, "billing", ownedstore.ReasonSecretKeyMissing)
	c, _ := wf.Status.Conditions.Get("Ready")
	require.Contains(t, c.Message, "STRIPE_KEY")
	require.NotContains(t, c.Message, "sk-other", "no Secret value enters the status")
	require.False(t, e.exists(t, v1.KindFunction, "billing-charge"))

	sec.Spec.Data = map[string][]byte{"STRIPE_KEY": []byte("sk-test")}
	e.apply(t, sec)
	e.waitExists(t, v1.KindFunction, "billing-charge")
}

// scenario: workflow-catalog
func TestScenarioWorkflowCatalog(t *testing.T) {
	t.Parallel()
	e := startGC(t, funcd.WithGCSweepInterval(time.Second))
	e.apply(t, rootWorkflow("marts", "query", e.image(t), func(s *v1.WorkflowSpec) {
		s.Buckets = []v1.BucketEntry{{Name: "marts-files", BucketSpec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "lake", Owner: "lake"}}}}}
		s.CatalogServices = []v1.CatalogEntry{{Name: "lake", CatalogServiceSpec: v1.CatalogServiceSpec{
			Catalog:  v1.CatalogRef{Bucket: "marts-files", Prefix: "lake"},
			Bindings: v1.CatalogServiceBindings{Blob: []v1.FunctionBlob{{Alias: "lake", Bucket: "marts-files", Prefix: "lake"}}},
		}}}
		s.ConfigMaps = []v1.ConfigMapEntry{{Name: "tuning", ConfigMapSpec: v1.ConfigMapSpec{Data: map[string]string{"THREADS": "1"}}}}
		s.Steps[0].Function.Bindings.Catalog = []v1.FunctionCatalog{{Alias: "lake", Catalog: "lake"}}
		s.Steps[0].Function.Bindings.Config = []v1.ObjectName{"tuning"}
	}))
	e.waitExists(t, v1.KindCatalogService, "lake")
	wf := e.object(t, v1.KindWorkflow, "marts")
	cs := e.object(t, v1.KindCatalogService, "lake").(*v1.CatalogService)
	require.True(t, ownedstore.Marked(cs.OwnerReferences, wf), "CatalogService lake carries the Workflow's marker")
	require.True(t, v1.ControlledBy(cs.OwnerReferences, v1.KindWorkflow, wf.GetObjectMeta().UID), "and goes with it")
	// The materializer writes the catalogs before the Workflow's buckets, so the bucket can lag the catalog.
	e.waitExists(t, v1.KindBucket, "marts-files")
	b := e.object(t, v1.KindBucket, "marts-files").(*v1.Bucket)
	require.Equal(t, []v1.BucketPrefix{{Name: "lake", Owner: "lake"}}, b.Spec.Prefixes, "a prefix owner naming a declared catalog is kept")
	e.waitExists(t, v1.KindFunction, "marts-query")
	require.Equal(t, []v1.FunctionCatalog{{Alias: "lake", Catalog: "lake"}}, e.function(t, "marts-query").Spec.Bindings.Catalog)

	tuning := v1.AppConfigMapName("tuning", v1.ConfigMapSpec{Data: map[string]string{"THREADS": "1"}})
	e.waitExists(t, v1.KindConfigMap, string(tuning))
	e.del(t, v1.KindWorkflow, "marts")
	e.waitGone(t, v1.KindCatalogService, "lake")
	e.waitGone(t, v1.KindConfigMap, string(tuning))
}

// scenario: workflow-ref-kept
func TestScenarioWorkflowRefKept(t *testing.T) {
	t.Parallel()
	e := startGC(t, funcd.WithGCSweepInterval(time.Second))
	db := &v1.SQLDatabase{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSQLDatabase.GVK().APIVersion(), Kind: v1.KindSQLDatabase}}
	db.Name, db.Namespace, db.ResourceGroup = "shared-db", "default", "rg1"
	cm := &v1.ConfigMap{TypeMeta: v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap}}
	cm.Name, cm.Namespace, cm.ResourceGroup = "shared-settings", "default", "rg1"
	cm.Spec.Data = map[string]string{"TZ": "UTC"}
	cat := &v1.CatalogService{TypeMeta: v1.TypeMeta{APIVersion: v1.KindCatalogService.GVK().APIVersion(), Kind: v1.KindCatalogService}}
	cat.Name, cat.Namespace, cat.ResourceGroup = "shared-lake", "default", "rg1"
	cat.Spec.Buckets = []v1.BucketEntry{{Name: "shared-files", BucketSpec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "lake"}}}}}
	cat.Spec.Catalog = v1.CatalogRef{Bucket: "shared-files", Prefix: "lake"}
	cat.Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "lake", Bucket: "shared-files", Prefix: "lake"}}
	for _, o := range []v1.Object{db, cm, cat} {
		e.apply(t, o)
	}
	before := map[v1.Kind]v1.Object{
		v1.KindSQLDatabase: e.object(t, v1.KindSQLDatabase, "shared-db"), v1.KindConfigMap: e.object(t, v1.KindConfigMap, "shared-settings"),
		v1.KindCatalogService: e.object(t, v1.KindCatalogService, "shared-lake"),
	}

	e.apply(t, rootWorkflow("users", "read", e.image(t), func(s *v1.WorkflowSpec) {
		s.SQLDatabases = []v1.SQLDatabaseEntry{{Ref: "shared-db"}}
		s.CatalogServices = []v1.CatalogEntry{{Ref: "shared-lake"}}
		s.ConfigMaps = []v1.ConfigMapEntry{{Ref: "shared-settings"}}
		s.Steps[0].Function.Bindings = v1.StepBindings{
			SQL:     []v1.FunctionSQL{{Alias: "db", Database: "shared-db"}},
			Catalog: []v1.FunctionCatalog{{Alias: "lake", Catalog: "shared-lake"}},
			Config:  []v1.ObjectName{"shared-settings"},
		}
	}))
	e.waitExists(t, v1.KindFunction, "users-read")
	step := e.function(t, "users-read")
	require.Equal(t, []v1.ObjectName{"shared-settings"}, step.Spec.Bindings.Config, "a ref ConfigMap is bound by its own name")
	require.Len(t, step.Spec.Bindings.SQL, 1)
	require.Len(t, step.Spec.Bindings.Catalog, 1)

	e.del(t, v1.KindWorkflow, "users")
	e.waitGone(t, v1.KindFunction, "users-read")
	require.Never(t, func() bool {
		return !e.exists(t, v1.KindSQLDatabase, "shared-db") || !e.exists(t, v1.KindConfigMap, "shared-settings") ||
			!e.exists(t, v1.KindCatalogService, "shared-lake")
	}, 2*time.Second, 100*time.Millisecond, "the ref objects outlive two GC sweeps")
	for kind, obj := range before {
		got := e.object(t, kind, string(obj.GetObjectMeta().Name))
		require.Empty(t, got.GetObjectMeta().OwnerReferences, "%s is never marked", kind)
		b1, _ := json.Marshal(obj)
		b2, _ := json.Marshal(got)
		var s1, s2 struct{ Spec json.RawMessage }
		require.NoError(t, json.Unmarshal(b1, &s1))
		require.NoError(t, json.Unmarshal(b2, &s2))
		require.JSONEq(t, string(s1.Spec), string(s2.Spec), "%s stays, unchanged", kind)
	}
}

// scenario: step-binding-needs-root-entry
func TestScenarioStepBindingNeedsRootEntry(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	body, err := json.Marshal(rootWorkflow("ghostly", "load", e.image(t), func(s *v1.WorkflowSpec) {
		s.Steps[0].Function.Bindings.SQL = []v1.FunctionSQL{{Alias: "db", Database: "ghost"}}
	}))
	require.NoError(t, err)
	code, out := e.call(t, http.MethodPost, "/apis/funcd.io/v1alpha1/namespaces/default/workflows", funcd.DevToken, body)
	require.Equal(t, http.StatusBadRequest, code, "%s", out)
	require.Equal(t, `admission.binding-declared: spec.steps[0].function.bindings.sql[0]: SQLDatabase "ghost" is not declared in spec.sql`, detail(t, out))
	require.False(t, e.exists(t, v1.KindWorkflow, "ghostly"))
}
