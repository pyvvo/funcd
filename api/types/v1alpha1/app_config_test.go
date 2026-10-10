package v1alpha1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	huma "github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
)

// configApp is ADR-0213's fixture: todoApp plus configMaps todo-settings (TZ: Europe/Paris) and secrets
// todo-stripe-key (STRIPE_API_KEY), both named by todo-api.
func configApp(mutate func(*App)) *App {
	return todoApp(func(a *App) {
		a.Spec.ConfigMaps = []AppConfigMap{{Name: "todo-settings", ConfigMapSpec: ConfigMapSpec{Data: map[string]string{"TZ": "Europe/Paris"}}}}
		a.Spec.Secrets = []AppSecret{{Name: "todo-stripe-key", Description: "Stripe API access", Keys: []string{"STRIPE_API_KEY"}}}
		a.Spec.Functions[0].Config = []ObjectName{"todo-settings"}
		a.Spec.Functions[0].Secrets = []ObjectName{"todo-stripe-key"}
		if mutate != nil {
			mutate(a)
		}
	})
}

// Decision 2: "<name>-" and the first 5 bytes of SHA-256 over json.Marshal(spec), in hex; equal data gives one name
// whatever the key order, other data another.
func TestAppConfigMapName(t *testing.T) {
	ab := map[string]string{}
	ab["A"], ab["B"] = "1", "2"
	ba := map[string]string{}
	ba["B"], ba["A"] = "2", "1"
	x := AppConfigMapName("todo-settings", ConfigMapSpec{Data: ab})
	if y := AppConfigMapName("todo-settings", ConfigMapSpec{Data: ba}); x != y {
		t.Fatalf("equal data: %s and %s", x, y)
	}
	sum := sha256.Sum256([]byte(`{"data":{"A":"1","B":"2"}}`))
	if want := ObjectName("todo-settings-" + hex.EncodeToString(sum[:5])); x != want {
		t.Fatalf("AppConfigMapName = %s, want %s", x, want)
	}
	for _, other := range []map[string]string{{"A": "1", "B": "3"}, {"A": "1"}, nil} {
		if y := AppConfigMapName("todo-settings", ConfigMapSpec{Data: other}); y == x || len(y) != len(x) {
			t.Fatalf("data %v: name %s, want a 10-hex name other than %s", other, y, x)
		}
	}
}

func TestAppConfigValidate(t *testing.T) {
	long := func(n int) func(*App) {
		return func(a *App) {
			name := ObjectName(strings.Repeat("c", n))
			a.Spec.ConfigMaps[0].Name = name
			a.Spec.Functions[0].Config = []ObjectName{name}
		}
	}
	for name, a := range map[string]*App{
		"the fixture":                    configApp(nil),
		"a name of 52 characters":        configApp(long(52)),
		"a ref entry":                    configApp(func(a *App) { a.Spec.ConfigMaps = append(a.Spec.ConfigMaps, AppConfigMap{Ref: "shared"}) }),
		"a declared Secret no part uses": configApp(func(a *App) { a.Spec.Functions[0].Secrets = nil }),
		"a part naming an undeclared Secret, which only the admission refuses": configApp(func(a *App) {
			a.Spec.Functions[0].Secrets = []ObjectName{"todo-billing"}
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if err := a.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
	stored := AppConfigMapName("todo-settings", ConfigMapSpec{Data: map[string]string{"TZ": "Europe/Paris"}})
	for _, tc := range []struct {
		name   string
		mutate func(*App)
		want   string
	}{
		{"a name of 53 characters", long(53), "spec.configMaps[0].name"},
		{"a ref entry on a defined entry's stored name", func(a *App) {
			a.Spec.ConfigMaps = append(a.Spec.ConfigMaps, AppConfigMap{Ref: stored})
		}, "spec.configMaps[0] and spec.configMaps[1] are both stored as ConfigMap/" + string(stored)},
		{"a repeated configMaps name", func(a *App) { a.Spec.ConfigMaps = append(a.Spec.ConfigMaps, a.Spec.ConfigMaps[0]) },
			`spec.configMaps[1] repeats the name "todo-settings"`},
		{"a data key that is not an env-var name", func(a *App) { a.Spec.ConfigMaps[0].Data["A=B"] = "x" },
			`spec.configMaps[0].data key "A=B" is not an env-var name`},
		{"a configMaps ref with data", func(a *App) {
			a.Spec.ConfigMaps[0] = AppConfigMap{Ref: "shared", ConfigMapSpec: ConfigMapSpec{Data: map[string]string{"A": "1"}}}
		}, "spec.configMaps[0] sets ref and another field"},
		{"a repeated Secret", func(a *App) { a.Spec.Secrets = append(a.Spec.Secrets, a.Spec.Secrets[0]) },
			`spec.secrets[1] repeats the name "todo-stripe-key" of spec.secrets[0]`},
		{"a repeated key", func(a *App) { a.Spec.Secrets[0].Keys = []string{"STRIPE_API_KEY", "STRIPE_API_KEY"} },
			`spec.secrets[0].keys[1] repeats the key "STRIPE_API_KEY" of spec.secrets[0].keys[0]`},
		{"no keys", func(a *App) { a.Spec.Secrets[0].Keys = nil }, "spec.secrets[0].keys is empty"},
		{"a key that is not an env-var name", func(a *App) { a.Spec.Secrets[0].Keys = []string{"STRIPE-KEY"} },
			`spec.secrets[0].keys[0] "STRIPE-KEY" is not an env-var name`},
		{"a Secret name that is not a DNS label", func(a *App) { a.Spec.Secrets[0].Name = "Stripe" }, "spec.secrets[0].name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := configApp(tc.mutate).Validate()
			if fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate: %v, want an Invalid fault naming %q", err, tc.want)
			}
		})
	}
}

// Decisions 3 and 4: the ConfigMap is the first part, under its stored name; a defined name in a Function's, a
// CatalogService's and an image step's config becomes the stored name, and the App's spec stays as declared.
func TestAppConfigRepointing(t *testing.T) {
	a := configApp(func(a *App) {
		a.Spec.ConfigMaps = append(a.Spec.ConfigMaps, AppConfigMap{Ref: "shared"})
		a.Spec.Functions[0].Config = []ObjectName{"todo-settings", "shared", "elsewhere"}
		a.Spec.Functions = append(a.Spec.Functions, AppFunction{Ref: "mailer"})
		wf := &a.Spec.Workflows[0]
		wf.Steps[0].Function.Config = []ObjectName{"todo-settings"}
		wf.Steps = append(wf.Steps, WorkflowStep{Name: "notify", Function: &FunctionStep{Ref: "todo-api", Config: []ObjectName{"todo-settings"}}})
		a.Spec.Catalogs = []AppCatalog{{Name: "todo-lake", CatalogServiceSpec: CatalogServiceSpec{
			Blob:    []FunctionBlob{{Alias: "lake", Bucket: "todo-files", Prefix: "lake"}},
			Catalog: CatalogRef{Bucket: "todo-files", Prefix: "lake"},
			Config:  []ObjectName{"todo-settings"},
		}}}
	})
	if err := a.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	before, err := json.Marshal(a.Spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	stored := AppConfigMapName("todo-settings", a.Spec.ConfigMaps[0].ConfigMapSpec)
	parts := a.Parts()
	cm, ok := parts[0].(*ConfigMap)
	if !ok || cm.Name != stored || !reflect.DeepEqual(cm.Spec, a.Spec.ConfigMaps[0].ConfigMapSpec) {
		t.Fatalf("Parts[0] = %#v, want ConfigMap/%s with the entry's data", parts[0], stored)
	}
	byName := map[string]Object{}
	for _, p := range parts {
		byName[string(p.GroupVersionKind().Kind)+"/"+string(p.GetName())] = p
	}
	if got := byName["Function/todo-api"].(*Function).Spec.Config; !slices.Equal(got, []ObjectName{stored, "shared", "elsewhere"}) {
		t.Fatalf("todo-api config = %v: the defined name is repointed, a ref entry and an undefined name are not", got)
	}
	if got := byName["CatalogService/todo-lake"].(*CatalogService).Spec.Config; !slices.Equal(got, []ObjectName{stored}) {
		t.Fatalf("todo-lake config = %v", got)
	}
	steps := byName["Workflow/todo-plan"].(*Workflow).Spec.Steps
	if got := steps[0].Function.Config; !slices.Equal(got, []ObjectName{stored}) {
		t.Fatalf("image step config = %v", got)
	}
	if got := steps[1].Function.Config; !slices.Equal(got, []ObjectName{"todo-settings"}) {
		t.Fatalf("ref step config = %v, want it as written", got)
	}
	if _, ok := byName["Function/mailer"]; ok {
		t.Fatal("a ref part is not a part")
	}
	after, err := json.Marshal(a.Spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("Parts changed the App's spec:\n%s\n%s", before, after)
	}
}

// Decision 1: a spec without the new sections marshals as before; the sections round-trip.
func TestAppConfigJSON(t *testing.T) {
	plain, err := json.Marshal(todoApp(nil).Spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(plain), "configMaps") || strings.Contains(string(plain), "secrets") {
		t.Fatalf("a spec without the sections carries them: %s", plain)
	}
	a := configApp(func(a *App) { a.Spec.ConfigMaps = append(a.Spec.ConfigMaps, AppConfigMap{Ref: "shared"}) })
	data, err := json.Marshal(a.Spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"configMaps":[{"name":"todo-settings","data":{"TZ":"Europe/Paris"}},{"ref":"shared"}]`,
		`"secrets":[{"name":"todo-stripe-key","description":"Stripe API access","keys":["STRIPE_API_KEY"]}]`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("spec JSON lacks %s: %s", want, data)
		}
	}
	var back AppSpec
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(back, a.Spec) {
		t.Fatalf("spec after a round trip:\n%+v\nwant\n%+v", back, a.Spec)
	}
	strict := json.NewDecoder(strings.NewReader(`{"secrets":[{"name":"s","keys":["K"],"data":{"K":"djE="}}]}`))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&AppSpec{}); err == nil || !strings.Contains(err.Error(), "data") {
		t.Fatalf("strict decoding of a secrets entry with data: %v, want an unknown field", err)
	}
}

// Decisions 2 and 6: a configMaps entry is ConfigMapSpec's schema plus name and ref; a secrets entry is closed, with
// name and at least one env-var key.
func TestAppConfigSchemas(t *testing.T) {
	r := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	spec := r.Schema(reflect.TypeFor[AppSpec](), false, "")
	cm := spec.Properties["configMaps"].Items
	if cm.Properties["data"] == nil || cm.Properties["name"] == nil || cm.Properties["ref"] == nil || cm.Properties["deletion"] != nil {
		t.Fatalf("configMaps[] properties: %v", cm.Properties)
	}
	sec := spec.Properties["secrets"].Items
	if !slices.Equal(sec.Required, []string{"name", "keys"}) || sec.AdditionalProperties != false || len(sec.Properties) != 3 {
		t.Fatalf("secrets[]: required %v, additionalProperties %v, properties %v", sec.Required, sec.AdditionalProperties, sec.Properties)
	}
	for _, tc := range []struct {
		name string
		body string
		ok   bool
	}{
		{"the fixture", `{"configMaps":[{"name":"todo-settings","data":{"TZ":"UTC"}},{"ref":"shared"}],"secrets":[{"name":"todo-stripe-key","description":"d","keys":["STRIPE_API_KEY"]}]}`, true},
		{"a secrets entry with data", `{"secrets":[{"name":"s","keys":["K"],"data":{"K":"djE="}}]}`, false},
		{"a secrets entry without keys", `{"secrets":[{"name":"s"}]}`, false},
		{"a secrets entry with no key", `{"secrets":[{"name":"s","keys":[]}]}`, false},
		{"a key that is not an env-var name", `{"secrets":[{"name":"s","keys":["STRIPE-KEY"]}]}`, false},
		{"a configMaps entry with deletion", `{"configMaps":[{"name":"c","deletion":"delete"}]}`, false},
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
