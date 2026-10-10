package v1alpha1

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	huma "github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
)

// todoApp is the ADR-0199 Scenarios fixture: two stores and two buckets (one of each deletion: delete), the API
// Function bound to them, its Route and the todo-plan Workflow whose step due makes Function todo-plan-due.
func todoApp(mutate func(*App)) *App {
	a := &App{
		TypeMeta:   TypeMeta{APIVersion: KindApp.GVK().APIVersion(), Kind: KindApp},
		ObjectMeta: ObjectMeta{Name: "todo", Namespace: "default", ResourceGroup: "todo-rg"},
		Spec: AppSpec{
			Version: "1.0.0",
			KV: []AppKVStore{
				{Name: "todo-store", KVStoreSpec: KVStoreSpec{Tables: []KVTable{{Name: "todos", Owner: "todo-api"}}}},
				{Name: "todo-cache", Deletion: DeletionDelete, KVStoreSpec: KVStoreSpec{Tables: []KVTable{{Name: "entries", Owner: "todo-api"}}}},
			},
			Buckets: []AppBucket{
				{Name: "todo-files", BucketSpec: BucketSpec{Prefixes: []BucketPrefix{{Name: "attachments", Owner: "todo-api"}}}},
				{Name: "todo-tmp", Deletion: DeletionDelete},
			},
			Functions: []AppFunction{{Name: "todo-api", FunctionSpec: FunctionSpec{
				Runtime: "nodejs22",
				Handler: "index.handler",
				Image:   "oci-layout://todo-api:1",
				Scaling: Scaling{MinReplicas: 1},
				KV: []FunctionKV{
					{Alias: "store", Store: "todo-store", Table: "todos"},
					{Alias: "cache", Store: "todo-cache", Table: "entries"},
				},
				Blob: []FunctionBlob{{Alias: "files", Bucket: "todo-files", Prefix: "attachments"}},
			}}},
			Workflows: []AppWorkflow{{Name: "todo-plan", WorkflowSpec: WorkflowSpec{
				Steps: []WorkflowStep{{Name: "due", Function: &FunctionStep{Image: "oci-layout://todo-due:1"}}},
			}}},
			Routes: []AppRoute{{Name: "todo-api", RouteSpec: RouteSpec{
				Rules: []RouteRule{{Path: "/api", Backend: RouteBackend{Function: "todo-api"}}},
			}}},
		},
	}
	if mutate != nil {
		mutate(a)
	}
	return a
}

// fullApp extends todoApp with a part in every other section and a ref entry in four sections.
func fullApp() *App {
	return todoApp(func(a *App) {
		a.Spec.KV = append(a.Spec.KV, AppKVStore{Ref: "shared-store"})
		a.Spec.Functions = append(a.Spec.Functions, AppFunction{Ref: "mailer"})
		a.Spec.EventSources = []AppEventSource{{Name: "todo-tick", EventSourceSpec: EventSourceSpec{
			Timer: &TimerSource{Events: []TimerEvent{{Name: "hourly", Interval: Duration(time.Hour)}}},
		}}}
		a.Spec.Sensors = []AppSensor{{Name: "todo-on-tick", SensorSpec: SensorSpec{
			On: []Dependency{{Name: "tick", Source: "todo-tick", Event: "hourly"}},
			Do: []Action{{Name: "plan", On: "tick", Workflow: "todo-plan"}},
		}}}
		a.Spec.Sites = []AppSite{
			{Name: "todo-web", SiteSpec: SiteSpec{
				Image:   "oci-layout://todo-web:1",
				Bucket:  SiteBucket{Name: "todo-web"},
				Prefix:  "web",
				Ingress: SiteIngress{Path: "/todo"},
			}},
			{Ref: "docs"},
		}
		a.Spec.Catalogs = []AppCatalog{
			{Name: "todo-lake", CatalogServiceSpec: CatalogServiceSpec{
				Blob:    []FunctionBlob{{Alias: "lake", Bucket: "todo-files", Prefix: "lake"}},
				Catalog: CatalogRef{Bucket: "todo-files", Prefix: "lake"},
			}},
			{Ref: "lake"},
		}
	})
}

func TestAppValidateAcceptsFixtures(t *testing.T) {
	for name, a := range map[string]*App{
		"todo":                  todoApp(nil),
		"full":                  fullApp(),
		"name of 52 characters": todoApp(func(a *App) { a.Name = ObjectName(strings.Repeat("a", 52)) }),
		"a workflow step with a function ref to a declared function": todoApp(func(a *App) {
			a.Spec.Workflows[0].Steps = append(a.Spec.Workflows[0].Steps, WorkflowStep{Name: "notify", Function: &FunctionStep{Ref: "todo-api"}})
		}),
		"a ref to the function a workflow step makes": todoApp(func(a *App) {
			a.Spec.Functions = append(a.Spec.Functions, AppFunction{Ref: "todo-plan-due"})
		}),
		"a ref site named like a route entry": todoApp(func(a *App) {
			a.Spec.Sites = []AppSite{{Ref: "todo-api"}}
		}),
		"one name in two sections": todoApp(func(a *App) {
			a.Spec.Buckets = append(a.Spec.Buckets, AppBucket{Name: "todo-api"})
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if err := a.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

// scenario: app-admission-refuses and app-shared-writer-refused (the store-free half, Decision 3): each refusal names
// its field path.
func TestAppValidateRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*App)
		want   string
	}{
		{"name over 52 characters", func(a *App) { a.Name = ObjectName(strings.Repeat("a", 53)) }, "metadata.name"},
		{"wrong kind", func(a *App) { a.Kind = KindWorkflow }, "does not match"},
		{"entry with neither name nor ref", func(a *App) { a.Spec.Routes = append(a.Spec.Routes, AppRoute{}) },
			"spec.routes[1] sets neither name nor ref"},
		{"ref entry with an image", func(a *App) {
			a.Spec.Functions[0] = AppFunction{Ref: "todo-api", FunctionSpec: FunctionSpec{Image: "oci-layout://x:1"}}
		}, "spec.functions[0] sets ref and another field"},
		{"ref entry with a name", func(a *App) { a.Spec.Buckets = append(a.Spec.Buckets, AppBucket{Name: "a", Ref: "b"}) },
			"spec.buckets[2] sets ref and another field"},
		{"ref entry with deletion", func(a *App) { a.Spec.KV[1] = AppKVStore{Ref: "todo-cache", Deletion: DeletionRetain} },
			"spec.kv[1] sets ref and another field"},
		{"ref entry with an empty list", func(a *App) {
			a.Spec.KV[1] = AppKVStore{Ref: "todo-cache", KVStoreSpec: KVStoreSpec{Tables: []KVTable{}}}
		},
			"spec.kv[1] sets ref and another field"},
		{"ref not a DNS label", func(a *App) { a.Spec.Routes[0] = AppRoute{Ref: "Todo"} }, "spec.routes[0].ref"},
		{"name not a DNS label", func(a *App) { a.Spec.Functions[0].Name = "Todo_API" }, "spec.functions[0].name"},
		{"unknown deletion", func(a *App) { a.Spec.KV[0].Deletion = "keep" }, "spec.kv[0].deletion"},
		{"name repeated within a section", func(a *App) { a.Spec.Functions = append(a.Spec.Functions, a.Spec.Functions[0]) },
			`spec.functions[1] repeats the name "todo-api" of spec.functions[0]`},
		{"ref repeating a declared name", func(a *App) { a.Spec.Buckets = append(a.Spec.Buckets, AppBucket{Ref: "todo-files"}) },
			`spec.buckets[2] repeats the name "todo-files" of spec.buckets[0]`},
		{"table name not a DNS label", func(a *App) { a.Spec.KV[0].Tables[0].Name = "Bad_Name" },
			`spec.kv[0].tables[0].name "Bad_Name"`},
		{"route path without a slash", func(a *App) { a.Spec.Routes[0].Rules[0].Path = "api" }, "spec.routes[0].rules[0].path"},
		{"part refusal without a path", func(a *App) {
			a.Spec.Workflows[0].Steps = append(a.Spec.Workflows[0].Steps, a.Spec.Workflows[0].Steps[0])
		}, `spec.workflows[0]: duplicate step name "due"`},
		{"site bucket is a buckets entry", func(a *App) {
			a.Spec.Sites = []AppSite{{Name: "todo-web", SiteSpec: SiteSpec{
				Image: "oci-layout://todo-web:1", Bucket: SiteBucket{Name: "todo-files"}, Prefix: "web",
			}}}
		}, "spec.sites[0].bucket.name and spec.buckets[0].name both write Bucket/todo-files"},
		{"site named like a route entry", func(a *App) {
			a.Spec.Sites = []AppSite{{Name: "todo-api", SiteSpec: SiteSpec{
				Image: "oci-layout://todo-web:1", Bucket: SiteBucket{Name: "todo-web"}, Prefix: "web",
			}}}
		}, "spec.sites[0].name and spec.routes[0].name both write Route/todo-api"},
		{"function named like a step function", func(a *App) {
			a.Spec.Functions = append(a.Spec.Functions, AppFunction{Name: "todo-plan-due"})
		}, "spec.workflows[0].steps[0].name and spec.functions[1].name both write Function/todo-plan-due"},
		{"workflow store is a kv entry", func(a *App) { a.Spec.Workflows[0].KV = []WorkflowKVStore{{Name: "todo-store"}} },
			"spec.workflows[0].kv[0].name and spec.kv[0].name both write KVStore/todo-store"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := todoApp(tc.mutate).Validate()
			if err == nil {
				t.Fatal("Validate accepted the App")
			}
			if fault.KindOf(err) != fault.Invalid {
				t.Fatalf("Validate: %v, want an Invalid fault", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate: %v, want %q", err, tc.want)
			}
		})
	}
}

func TestAppParts(t *testing.T) {
	a := fullApp()
	parts := a.Parts()
	var got []string
	for _, p := range parts {
		got = append(got, string(p.GroupVersionKind().Kind)+"/"+string(p.GetName()))
		m := p.GetObjectMeta()
		if m.Namespace != "default" || m.ResourceGroup != "todo-rg" || len(m.OwnerReferences) != 0 {
			t.Errorf("%s/%s: metadata %+v, want the App's namespace and resource group and no owner references",
				p.GroupVersionKind().Kind, p.GetName(), m)
		}
		if err := p.Validate(); err != nil {
			t.Errorf("part %s/%s: %v", p.GroupVersionKind().Kind, p.GetName(), err)
		}
	}
	want := []string{
		"KVStore/todo-store", "KVStore/todo-cache", "Bucket/todo-files", "Bucket/todo-tmp", "Function/todo-api",
		"Workflow/todo-plan", "EventSource/todo-tick", "Sensor/todo-on-tick", "Route/todo-api", "Site/todo-web",
		"CatalogService/todo-lake",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Parts = %v, want %v", got, want)
	}
	fn, ok := parts[4].(*Function)
	if !ok || !reflect.DeepEqual(fn.Spec, a.Spec.Functions[0].FunctionSpec) {
		t.Fatalf("Parts[4] = %#v, want the Function with the entry's spec", parts[4])
	}
	if st, ok := parts[1].(*KVStore); !ok || !reflect.DeepEqual(st.Spec, a.Spec.KV[1].KVStoreSpec) {
		t.Fatalf("Parts[1] = %#v, want the KVStore with the entry's spec", parts[1])
	}
}

func TestAppRefs(t *testing.T) {
	want := []ObjectRef{
		{Kind: KindKVStore, Namespace: "default", Name: "shared-store"},
		{Kind: KindFunction, Namespace: "default", Name: "mailer"},
		{Kind: KindSite, Namespace: "default", Name: "docs"},
		{Kind: KindCatalogService, Namespace: "default", Name: "lake"},
	}
	if got := fullApp().Refs(); !slices.Equal(got, want) {
		t.Fatalf("Refs = %v, want %v", got, want)
	}
	if got := todoApp(nil).Refs(); len(got) != 0 {
		t.Fatalf("Refs of an App without ref entries = %v", got)
	}
}

// A ref entry goes on the wire as its ref alone, and the spec survives a round trip.
func TestAppJSONRoundtrip(t *testing.T) {
	a := fullApp()
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal App: %v", err)
	}
	for _, want := range []string{
		`"sites":[{"name":"todo-web",`, `{"ref":"docs"}]`, `{"ref":"lake"}]`, `{"ref":"shared-store"}]`,
		`{"name":"todo-cache","deletion":"delete","tables":[`, `{"name":"todo-tmp","deletion":"delete"}`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("App JSON lacks %s: %s", want, data)
		}
	}
	var back App
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal App: %v", err)
	}
	if !reflect.DeepEqual(back.Spec, a.Spec) {
		t.Fatalf("spec after a round trip:\n%+v\nwant\n%+v", back.Spec, a.Spec)
	}
}

// Each entry schema is its kind's spec schema plus name, ref and, for a store, deletion, with no required list.
func TestAppEntrySchemas(t *testing.T) {
	r := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	spec := r.Schema(reflect.TypeFor[AppSpec](), false, "")
	for section, field := range map[string]string{
		"kv": "tables", "buckets": "prefixes", "functions": "image", "workflows": "steps", "eventSources": "timer",
		"sensors": "do", "routes": "rules", "sites": "ingress", "catalogs": "catalog",
	} {
		sec := spec.Properties[section]
		if sec == nil || sec.Items == nil {
			t.Fatalf("spec.%s: no array schema", section)
		}
		e := sec.Items
		store := section == "kv" || section == "buckets"
		if e.Properties["name"] == nil || e.Properties["ref"] == nil || e.Properties[field] == nil ||
			(e.Properties["deletion"] != nil) != store {
			t.Errorf("spec.%s[] properties %v: want name, ref, %s and deletion only for a store", section, slices.Sorted(maps.Keys(e.Properties)), field)
		}
		if len(e.Required) != 0 || e.AdditionalProperties != false {
			t.Errorf("spec.%s[]: required %v, additionalProperties %v; want none and false", section, e.Required, e.AdditionalProperties)
		}
	}
	refs, err := json.Marshal(AppSpec{
		KV: []AppKVStore{{Ref: "a"}}, Buckets: []AppBucket{{Ref: "a"}}, Functions: []AppFunction{{Ref: "a"}},
		Workflows: []AppWorkflow{{Ref: "a"}}, EventSources: []AppEventSource{{Ref: "a"}}, Sensors: []AppSensor{{Ref: "a"}},
		Routes: []AppRoute{{Ref: "a"}}, Sites: []AppSite{{Ref: "a"}}, Catalogs: []AppCatalog{{Ref: "a"}},
	})
	if err != nil {
		t.Fatalf("marshal ref entries: %v", err)
	}
	full, err := json.Marshal(fullApp().Spec)
	if err != nil {
		t.Fatalf("marshal the full spec: %v", err)
	}
	for _, tc := range []struct {
		name string
		body string
		ok   bool
	}{
		{"a ref entry in every section", string(refs), true},
		{"every section declared", string(full), true},
		{"an unknown section", `{"deployments":[{"name":"a"}]}`, false},
		{"an unknown entry field", `{"functions":[{"name":"a","replicaz":1}]}`, false},
		{"a deletion on a function", `{"functions":[{"name":"a","deletion":"delete"}]}`, false},
		{"an unknown deletion", `{"kv":[{"name":"a","deletion":"keep"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v interface{}
			if err := json.Unmarshal([]byte(tc.body), &v); err != nil {
				t.Fatalf("decode %s: %v", tc.body, err)
			}
			res := &huma.ValidateResult{}
			huma.Validate(r, spec, huma.NewPathBuffer([]byte{}, 0), huma.ModeWriteToServer, v, res)
			if ok := len(res.Errors) == 0; ok != tc.ok {
				t.Fatalf("schema errors for %s: %v; want ok=%v", tc.body, res.Errors, tc.ok)
			}
		})
	}
}

func TestAppChildStateSchema(t *testing.T) {
	got := AppChildState("").Schema(nil).Enum
	if len(got) != 4 || got[0] != "Ready" || got[1] != "NotStarted" || got[2] != "Pending" || got[3] != "Pruning" {
		t.Fatalf("AppChildState enum = %v", got)
	}
}

// ADR-0212 Decision 3: WithoutPause clears Paused alone and leaves the receiver unchanged; paused: false is absent
// from the JSON, so a frozen spec never holds the key.
func TestAppSpecWithoutPause(t *testing.T) {
	a := fullApp()
	a.Spec.Paused = true
	got := a.Spec.WithoutPause()
	if got.Paused || !a.Spec.Paused {
		t.Fatalf("WithoutPause: Paused = %v, receiver Paused = %v", got.Paused, a.Spec.Paused)
	}
	want := a.Spec
	want.Paused = false
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WithoutPause changed another field:\n%+v\nwant\n%+v", got, want)
	}
	paused, err := json.Marshal(a.Spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	if !strings.Contains(string(paused), `"paused":true`) {
		t.Errorf("a paused spec lacks paused: %s", paused)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	if strings.Contains(string(data), "paused") {
		t.Errorf("paused: false is on the wire: %s", data)
	}
}

// ADR-0212 Decision 2: lastSelfHeal names the part and marshals at as UTC milliseconds (ADR-0196).
func TestAppSelfHealJSON(t *testing.T) {
	at := time.Date(2026, 10, 8, 11, 12, 3, 123456789, time.FixedZone("CEST", 2*60*60))
	s := AppStatus{LastSelfHeal: &AppSelfHeal{Kind: KindFunction, Name: "todo-api", At: NewTimestamp(at)}}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if want := `"lastSelfHeal":{"kind":"Function","name":"todo-api","at":"2026-10-08T09:12:03.123Z"}`; !strings.Contains(string(data), want) {
		t.Fatalf("status JSON = %s, want it to hold %s", data, want)
	}
	empty, err := json.Marshal(AppStatus{})
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if strings.Contains(string(empty), "lastSelfHeal") {
		t.Errorf("an App never healed has lastSelfHeal: %s", empty)
	}
}

// ADR-0220 Contracts: a plan travels in status.plan with its empty fields left out; an unchanged spec's plan is {}.
func TestAppPlanJSON(t *testing.T) {
	s := AppStatus{Plan: &AppPlan{
		Revision: "todo-5",
		Parts: []PlanPart{
			{Kind: KindFunction, Name: "todo-api", Action: PlanUpdate},
			{Kind: KindSecret, Name: "todo-key", Reason: "SecretNotFound"},
		},
		Hooks: []string{"todo-migrate"},
	}}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	want := `"plan":{"revision":"todo-5","parts":[{"kind":"Function","name":"todo-api","action":"update"},` +
		`{"kind":"Secret","name":"todo-key","reason":"SecretNotFound"}],"hooks":["todo-migrate"]}`
	if !strings.Contains(string(data), want) {
		t.Fatalf("status JSON = %s, want it to hold %s", data, want)
	}
	var back AppStatus
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if !reflect.DeepEqual(back.Plan, s.Plan) {
		t.Fatalf("plan after a round trip = %+v, want %+v", back.Plan, s.Plan)
	}
	for _, tc := range []struct {
		status AppStatus
		want   string
	}{
		{AppStatus{}, `{}`},
		{AppStatus{Plan: &AppPlan{}}, `{"plan":{}}`},
	} {
		data, err := json.Marshal(tc.status)
		if err != nil {
			t.Fatalf("marshal status: %v", err)
		}
		if string(data) != tc.want {
			t.Errorf("status JSON = %s, want %s", data, tc.want)
		}
	}
	if got := PlanAction("").Schema(nil).Enum; len(got) != 3 || got[0] != "create" || got[1] != "update" || got[2] != "prune" {
		t.Errorf("PlanAction enum = %v", got)
	}
}
