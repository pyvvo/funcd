package template

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

const baseApp = `name: shop
version: 1.0.0
registry: ${{ values.registry }}
images:
  api: shop-api:1.0.0
valuesSchema:
  type: object
  properties:
    registry:
      type: string
      default: reg.example
    replicas:
      type: integer
      default: 0
    debug:
      type: boolean
      default: false
    label:
      type: string
      default: "true"
    limits:
      type: object
      default: {}
      properties:
        cpu:
          type: string
          default: "1"
    loose:
      properties:
        n:
          type: integer
          default: 1
    mode:
      enum:
        - a
        - b
    optional:
      type: string
`

const apiFragment = `functions:
  - name: ${{ app.name + "-api" }}
    runtime: nodejs22
    handler: handle
    image: ${{ images.api }}
`

// writeTemplate writes a template directory: app.yaml plus files, each "resources/<f>.yaml" → body.
func writeTemplate(t *testing.T, app string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "resources"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.yaml"), []byte(app), 0o600))
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	return dir
}

func values(t *testing.T, docs ...string) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	for _, d := range docs {
		path := filepath.Join(t.TempDir(), "v.yaml")
		require.NoError(t, os.WriteFile(path, []byte(d), 0o600))
		raw, err := ReadValues(path)
		require.NoError(t, err, d)
		out = append(out, raw)
	}
	return out
}

func render(t *testing.T, app string, files map[string]string, vals ...string) (*v1.App, error) {
	t.Helper()
	tpl, err := Load(writeTemplate(t, app, files))
	if err != nil {
		return nil, err
	}
	pin(t, tpl)
	return Render(tpl, RenderInput{Namespace: "team-a", Values: values(t, vals...)})
}

// fakeDigest is a sha256 digest of s, a stand-in for the digest a registry would give.
func fakeDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// pin gives a template without app.lock a lock of its exact entries, each at fakeDigest(entry).
func pin(t *testing.T, tpl *Template) {
	t.Helper()
	if tpl.Lock != nil {
		return
	}
	tpl.Lock = map[string]LockedImage{}
	for name, entry := range tpl.Images {
		require.NotEmpty(t, tpl.Parsed[name].Exact, "pin locks exact entries only")
		tpl.Lock[name] = LockedImage{Requested: entry, Version: tpl.Parsed[name].Exact, Digest: fakeDigest(entry)}
	}
}

func mustRender(t *testing.T, app string, files map[string]string, vals ...string) *v1.App {
	t.Helper()
	a, err := render(t, app, files, vals...)
	require.NoError(t, err)
	return a
}

// refused asserts a render fails as fault.Invalid naming every want, and returns nothing.
func refused(t *testing.T, app string, files map[string]string, vals []string, want ...string) {
	t.Helper()
	a, err := render(t, app, files, vals...)
	require.Nil(t, a, "nothing is returned on error")
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), err.Error())
	for _, w := range want {
		require.ErrorContains(t, err, w)
	}
}

// ADR-0217 Decisions 1 and 2: Load refuses every other entry, an unknown key and a malformed value; ADR-0218's
// ParseImage refuses a bad images entry.
func TestLoadRefuses(t *testing.T) {
	t.Parallel()
	api := map[string]string{"resources/api.yaml": apiFragment}
	for name, tc := range map[string]struct {
		app   string
		files map[string]string
		want  []string
	}{
		"extra file":          {baseApp, map[string]string{"README.md": "x"}, []string{"README.md", "app.lock"}},
		"other extension":     {baseApp, map[string]string{"resources/api.yml": apiFragment}, []string{"resources/api.yml"}},
		"unknown key":         {baseApp + "deploy: true\n", api, []string{"deploy"}},
		"apiVersion":          {"apiVersion: funcd.io/v1alpha1\n" + baseApp, api, []string{"apiVersion"}},
		"no name":             {strings.Replace(baseApp, "name: shop\n", "", 1), api, []string{"name is required"}},
		"bad name":            {strings.Replace(baseApp, "name: shop", "name: Shop_1", 1), api, []string{"Shop_1"}},
		"v prefix":            {strings.Replace(baseApp, "version: 1.0.0", "version: v1.0.0", 1), api, []string{"v1.0.0"}},
		"two parts":           {strings.Replace(baseApp, "version: 1.0.0", "version: \"1.0\"", 1), api, []string{"1.0"}},
		"no registry":         {strings.Replace(baseApp, "registry: ${{ values.registry }}\n", "", 1), api, []string{"registry is required"}},
		"registry reads app":  {strings.Replace(baseApp, "${{ values.registry }}", "${{ app.name }}", 1), api, []string{"registry reads app"}},
		"registry interpol":   {strings.Replace(baseApp, "${{ values.registry }}", "r/${{ values.registry }}", 1), api, []string{"registry", "interpolation"}},
		"bad spec":            {strings.Replace(baseApp, "shop-api:1.0.0", "shop-api:latest", 1), api, []string{"images.api", "shop-api:latest"}},
		"digest":              {strings.Replace(baseApp, "shop-api:1.0.0", "shop-api@sha256:abc", 1), api, []string{"images.api", "shop-api@sha256:abc"}},
		"host":                {strings.Replace(baseApp, "shop-api:1.0.0", "localhost:5000/shop-api:1.0.0", 1), api, []string{"images.api"}},
		"bad image name":      {strings.Replace(baseApp, "  api: shop-api", "  a-pi: shop-api", 1), api, []string{"images.a-pi"}},
		"when unknown file":   {baseApp + "when:\n  resources/x.yaml: ${{ values.debug === true }}\n", api, []string{"when.resources/x.yaml"}},
		"when reads images":   {baseApp + "when:\n  resources/api.yaml: ${{ images.api === 'x' }}\n", api, []string{"when.resources/api.yaml reads images"}},
		"when not expression": {baseApp + "when:\n  resources/api.yaml: \"true\"\n", api, []string{"when.resources/api.yaml"}},
	} {
		_, err := Load(writeTemplate(t, tc.app, tc.files))
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%s: %v", name, err)
		for _, w := range tc.want {
			require.ErrorContains(t, err, w, name)
		}
	}

	dir := writeTemplate(t, baseApp, map[string]string{"resources/api.yaml": apiFragment})
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "resources", "nested"), 0o755))
	_, err := Load(dir)
	require.ErrorContains(t, err, "resources/nested")
	dir = writeTemplate(t, baseApp, nil)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "hooks"), 0o755))
	_, err = Load(dir)
	require.ErrorContains(t, err, "hooks")
	_, err = Load(filepath.Join(dir, "app.yaml"))
	require.ErrorContains(t, err, "is not a template directory")
	_, err = Load("registry.example/shop-app:1.0.0")
	require.ErrorContains(t, err, "is not a template directory")
	dir = writeTemplate(t, baseApp, nil)
	require.NoError(t, os.Remove(filepath.Join(dir, "app.yaml")))
	_, err = Load(dir)
	require.ErrorContains(t, err, "no app.yaml")

	tpl, err := Load(writeTemplate(t, "name: bare\nversion: 2.0.0-rc.1+build.5\n", nil))
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"object"}`, string(tpl.ValuesSchema), "an absent valuesSchema declares no value")
	require.Equal(t, "2.0.0-rc.1+build.5", tpl.Version, "pre-release and build are strict semver")
}

// ADR-0217 Decision 3: one mapping per file; maps merge key by key; a list, a scalar or null replaces.
func TestValuesReadAndMerge(t *testing.T) {
	t.Parallel()
	vals := values(t, "", "a:\n  b: 1\n  c: [1, 2]\n  d: x\non: yes\n", "a:\n  c: [3]\n  d: null\n  e: 2\n")
	require.JSONEq(t, `{}`, string(vals[0]), "an empty file is {}")
	merged, err := mergeValues(vals)
	require.NoError(t, err)
	require.JSONEq(t, `{"a":{"b":1,"c":[3],"d":null,"e":2},"on":"yes"}`, string(merged), "YAML 1.2 keeps on and yes strings")
	empty, err := mergeValues(nil)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(empty))

	for body, want := range map[string]string{
		"a: 1\n---\nb: 2\n": "2 YAML documents",
		"- a\n- b\n":        "not a mapping",
		"1: x\n":            "not a string",
	} {
		path := filepath.Join(t.TempDir(), "bad.yaml")
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		_, err := ReadValues(path)
		require.ErrorContains(t, err, want, body)
		require.ErrorContains(t, err, "bad.yaml", body)
	}
}

// ADR-0217 Decision 3: an undeclared value is refused at the root and nested; a key declared only under then or
// beside a $ref is accepted; another draft and a non-local $ref are refused.
func TestValuesSchemaClosed(t *testing.T) {
	t.Parallel()
	ok := func(schema, vals string) {
		t.Helper()
		require.NoError(t, validateValues(json.RawMessage(schema), json.RawMessage(vals)), schema, vals)
	}
	bad := func(schema, vals string, want ...string) {
		t.Helper()
		err := validateValues(json.RawMessage(schema), json.RawMessage(vals))
		require.Error(t, err, vals)
		for _, w := range want {
			require.ErrorContains(t, err, w)
		}
	}
	const nested = `{"type":"object","properties":{"a":{"type":"object","properties":{"b":{"type":"integer"}}},
		"list":{"type":"array","items":{"type":"object","properties":{"x":{"type":"string"}}}}}}`
	bad(nested, `{"c":1}`, "/c", "unevaluatedProperties")
	bad(nested, `{"a":{"bb":1}}`, "/a/bb", "unevaluatedProperties")
	bad(nested, `{"list":[{"y":"1"}]}`, "/list/0/y", "unevaluatedProperties")
	bad(nested, `{"a":{"b":"1"}}`, "/a/b", "type", "integer")
	ok(nested, `{"a":{"b":1},"list":[{"x":"1"}]}`)
	bad(`{"type":"object"}`, `{"a":1}`, "/a", "unevaluatedProperties")
	ok(`{"type":"object","additionalProperties":true}`, `{"a":1}`)

	const then = `{"type":"object","properties":{"on":{"type":"boolean"}},
		"if":{"properties":{"on":{"const":true}},"required":["on"]},"then":{"properties":{"extra":{"type":"string"}}}}`
	ok(then, `{"on":true,"extra":"x"}`)
	bad(then, `{"on":false,"extra":"x"}`, "/extra")

	const ref = `{"type":"object","$defs":{"base":{"type":"object","properties":{"a":{"type":"string"},
		"inner":{"type":"object","properties":{"z":{"type":"string"}}}}}},
		"properties":{"x":{"$ref":"#/$defs/base","properties":{"b":{"type":"string"}}}}}`
	ok(ref, `{"x":{"a":"1","b":"2","inner":{"z":"3"}}}`)
	bad(ref, `{"x":{"c":"1"}}`, "/x/c")
	bad(ref, `{"x":{"inner":{"w":"1"}}}`, "/x/inner/w")

	bad(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object"}`, `{}`, "draft 2020-12")
	ok(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`, `{}`)
	bad(`{"type":"object","properties":{"a":{"$ref":"https://schemas.example/a.json"}}}`, `{}`, "not local")
	bad(`{"type":"object","properties":{"a":{"$ref":"other.json"}}}`, `{}`, "not local")
}

// ADR-0217 Decisions 4 and 5: an expression over values, app and images is typed and replaced by its typed result;
// one over other roots, or none, passes through byte for byte.
func TestRenderRoutesScalars(t *testing.T) {
	t.Parallel()
	frag := `functions:
  - name: ${{ app.name + "-api" }}
    runtime: nodejs22
    handler: ${{ values.label }}
    image: ${{ images.api }}
    scaling:
      minReplicas: ${{ values.replicas + 1 }}
workflows:
  - name: flow
    contract:
      input:
        type: object
        properties:
          image:
            type: string
            default: none
    steps:
      - name: a
        builtin:
          pass: '${{ { cpu: "1" } }}'
      - name: b
        dependsOn:
          - a
        when:
          condition: ${{ step.a.output.cpu === "1" }}
        builtin:
          pass: ${{ true }}
sensors:
  - name: s
    "on":
      - name: tick
        source: t
        event: tick
    do:
      - name: go
        "on": tick
        workflow: flow
        input:
          image: ${{ event.data.image }}
          limits: ${{ values.limits }}
          debug: ${{ values.debug }}
          mode: "${{ values.debug ? 'on' : 'off' }}"
          ns: ${{ app.namespace }}
          version: ${{ app.version }}
          text: "plain ${{ event.data.x }} text"
`
	app := baseApp + "when:\n  resources/off.yaml: ${{ values.debug === true }}\n  resources/on.yaml: ${{ app.namespace === 'team-a' }}\n"
	a := mustRender(t, app, map[string]string{
		"resources/a.yaml":   frag,
		"resources/off.yaml": "routes:\n  - name: off\n",
		"resources/on.yaml":  "kv:\n  - name: on-store\n",
	}, "replicas: 2\n", "limits:\n  cpu: \"4\"\n")
	require.Equal(t, v1.ObjectName("shop"), a.Name)
	require.Equal(t, v1.NamespaceName("team-a"), a.Namespace)
	require.Equal(t, v1.ResourceGroupName("shop"), a.ResourceGroup)
	require.Equal(t, "1.0.0", a.Spec.Version)
	require.Empty(t, a.Spec.Routes, "when false excludes the file")
	require.Len(t, a.Spec.KV, 1, "when true includes the file")
	fn := a.Spec.Functions[0]
	require.Equal(t, v1.ObjectName("shop-api"), fn.Name)
	require.Equal(t, "reg.example/shop-api:1.0.0@"+fakeDigest("shop-api:1.0.0"), fn.Image, "the registry default stands in")
	require.Equal(t, 3, fn.Scaling.MinReplicas, "an integer stays an integer")
	require.Equal(t, "true", fn.Handler, "the string \"true\" stays a string")

	wf := a.Spec.Workflows[0]
	require.JSONEq(t, `{"type":"object","properties":{"image":{"type":"string","default":"none"}}}`, string(wf.Contract.Input),
		"a contract property named image is data")
	require.Equal(t, `${{ { cpu: "1" } }}`, wf.Steps[0].Builtin.Pass, "no root: passed through")
	require.Equal(t, `${{ step.a.output.cpu === "1" }}`, wf.Steps[1].When.Condition, "byte for byte")
	require.Equal(t, `${{ true }}`, wf.Steps[1].Builtin.Pass, "no root: passed through")
	require.JSONEq(t, `{"image":"${{ event.data.image }}","limits":{"cpu":"4"},"debug":false,"mode":"off","ns":"team-a",`+
		`"version":"1.0.0","text":"plain ${{ event.data.x }} text"}`, string(a.Spec.Sensors[0].Do[0].Input),
		"an object result is a node, a boolean a boolean; event expressions and text around them pass through")
}

// fnWith is a function fragment whose handler (line 4) and image (line 5) the case sets, plus extra lines.
func fnWith(handler, image, extra string) string {
	return "functions:\n  - name: f\n    runtime: nodejs22\n    handler: " + handler + "\n    image: " + image + "\n" + extra
}

// ADR-0217 Decisions 4-7: each refusal names the file, its line and the field path, or the file and the section
// entry, and Render returns nothing.
func TestRenderRefuses(t *testing.T) {
	t.Parallel()
	const img, lit = "${{ images.api }}", "registry.example/todo-api:1.0.0"
	step := "workflows:\n  - name: w\n    steps:\n      - name: s\n        function:\n          image: registry.example/shop-api:1.0.0\n"
	site := "sites:\n  - name: web\n    image: shop-web:1.0.0\n    bucket:\n      name: web\n"
	for name, tc := range map[string]struct {
		files map[string]string
		vals  []string
		want  []string
	}{
		"mixed roots":          {map[string]string{"resources/a.yaml": fnWith("${{ values.label + event.data.x }}", img, "")}, nil, []string{"resources/a.yaml:4: functions[0].handler", "values", "event"}},
		"unparsable":           {map[string]string{"resources/a.yaml": fnWith("\"${{ values.label + }}\"", img, "")}, nil, []string{"resources/a.yaml:4: functions[0].handler"}},
		"interpolation":        {map[string]string{"resources/a.yaml": fnWith("x-${{ values.label }}", img, "")}, nil, []string{"resources/a.yaml:4: functions[0].handler", "interpolation"}},
		"untyped enum":         {map[string]string{"resources/a.yaml": fnWith("${{ values.mode }}", img, "")}, nil, []string{"resources/a.yaml:4: functions[0].handler", "mode"}},
		"optional, no default": {map[string]string{"resources/a.yaml": fnWith("${{ values.optional }}", img, "")}, nil, []string{"resources/a.yaml:4: functions[0].handler", "optional"}},
		"undeclared":           {map[string]string{"resources/a.yaml": fnWith("${{ values.nope }}", img, "")}, nil, []string{"resources/a.yaml:4: functions[0].handler", "nope"}},
		"image literal":        {map[string]string{"resources/api.yaml": fnWith("h", "registry.example/todo-api:1.0.0", "")}, nil, []string{"resources/api.yaml:5: functions[0].image"}},
		"image from values":    {map[string]string{"resources/a.yaml": fnWith("h", "${{ values.label }}", "")}, nil, []string{"resources/a.yaml:5: functions[0].image"}},
		"image undeclared":     {map[string]string{"resources/a.yaml": fnWith("h", "${{ images.web }}", "")}, nil, []string{"resources/a.yaml:5: functions[0].image", "images.web"}},
		"step image":           {map[string]string{"resources/a.yaml": step}, nil, []string{"resources/a.yaml:6: workflows[0].steps[0].function.image"}},
		"site image":           {map[string]string{"resources/a.yaml": site}, nil, []string{"resources/a.yaml:3: sites[0].image"}},
		"digest literal":       {map[string]string{"resources/api.yaml": fnWith("h", img, "    imageDigest: sha256:abc\n")}, nil, []string{"resources/api.yaml:6: functions[0].imageDigest"}},
		"digest expression":    {map[string]string{"resources/a.yaml": fnWith("h", img, "    imageDigest: ${{ values.label }}\n")}, nil, []string{"resources/a.yaml:6: functions[0].imageDigest"}},
		"version":              {map[string]string{"resources/a.yaml": "version: 2.0.0\n"}, nil, []string{"resources/a.yaml:1: version"}},
		"paused":               {map[string]string{"resources/a.yaml": "paused: true\n"}, nil, []string{"resources/a.yaml:1: paused", "ADR-0212"}},
		"unknown key":          {map[string]string{"resources/api.yaml": fnWith("h", img, "    minReplica: 1\n")}, nil, []string{"resources/api.yaml: ", "minReplica"}},
		"unknown section":      {map[string]string{"resources/a.yaml": "volumes:\n  - name: c\n"}, nil, []string{"resources/a.yaml: ", "volumes"}},
		"two files, one name": {map[string]string{"resources/a.yaml": fnWith("h", img, ""), "resources/b.yaml": fnWith("h", img, "")}, nil,
			[]string{"resources/b.yaml: functions[0]", "resources/a.yaml: functions[0]"}},
		"untyped parent": {map[string]string{"resources/a.yaml": "kv:\n  - name: k\n    maxValueBytes: ${{ values.loose.n }}\n"}, []string{"loose: str\n"},
			[]string{"resources/a.yaml:3: kv[0].maxValueBytes", "loose"}},
		"alias":            {map[string]string{"resources/a.yaml": fnWith("&h h", "*h", "")}, nil, []string{"resources/a.yaml:5: functions[0].image", "alias"}},
		"two documents":    {map[string]string{"resources/a.yaml": "kv: []\n---\nkv: []\n"}, nil, []string{"resources/a.yaml"}},
		"empty registry":   {map[string]string{"resources/a.yaml": fnWith("h", img, "")}, []string{"registry: \"\"\n"}, []string{"registry"}},
		"registry slash":   {map[string]string{"resources/a.yaml": fnWith("h", img, "")}, []string{"registry: reg.example/\n"}, []string{"registry", "trailing /"}},
		"undeclared value": {map[string]string{"resources/a.yaml": fnWith("h", img, "")}, []string{"replica: 2\n"}, []string{"/replica", "unevaluatedProperties"}},
		"mistyped value":   {map[string]string{"resources/a.yaml": fnWith("h", img, "")}, []string{"replicas: \"2\"\n"}, []string{"/replicas", "integer"}},
		"image key case":   {map[string]string{"resources/a.yaml": strings.Replace(fnWith("h", lit, ""), "image:", "Image:", 1)}, nil, []string{"resources/a.yaml:5: functions[0].Image"}},
		"section key case": {map[string]string{"resources/a.yaml": strings.Replace(fnWith("h", lit, ""), "functions:", "Functions:", 1)}, nil, []string{"resources/a.yaml:5: Functions[0].image"}},
		"step key case": {map[string]string{"resources/a.yaml": strings.NewReplacer("steps:", "Steps:", "function:", "Function:", "image:", "IMAGE:").Replace(step)}, nil,
			[]string{"resources/a.yaml:6: workflows[0].Steps[0].Function.IMAGE"}},
		"site key case":    {map[string]string{"resources/a.yaml": strings.Replace(site, "image:", "Image:", 1)}, nil, []string{"resources/a.yaml:3: sites[0].Image"}},
		"site key fold":    {map[string]string{"resources/a.yaml": strings.Replace(site, "sites:", "\u017fites:", 1)}, nil, []string{"resources/a.yaml:3: \u017fites[0].image"}},
		"digest key case":  {map[string]string{"resources/a.yaml": fnWith("h", img, "    ImageDigest: sha256:abc\n")}, nil, []string{"resources/a.yaml:6: functions[0].ImageDigest"}},
		"version key case": {map[string]string{"resources/a.yaml": "Version: 9.9.9\n"}, nil, []string{"resources/a.yaml:1: Version"}},
		"paused key case":  {map[string]string{"resources/a.yaml": "PAUSED: true\n"}, nil, []string{"resources/a.yaml:1: PAUSED", "ADR-0212"}},
		"binary key":       {map[string]string{"resources/a.yaml": strings.Replace(fnWith("h", lit, ""), "image:", "!!binary aW1hZ2U=:", 1)}, nil, []string{"resources/a.yaml:5: functions[0]", "!!binary"}},
		"value is an expression": {map[string]string{"resources/a.yaml": fnWith("${{ values.label }}", img, "")}, []string{"label: \"${{ event.data.x }}\"\n"},
			[]string{"resources/a.yaml:4: functions[0].handler", "render never writes an expression"}},
		"value interpolates": {map[string]string{"resources/a.yaml": fnWith("${{ values.label }}", img, "")}, []string{"label: \"x-${{ values.registry }}\"\n"},
			[]string{"resources/a.yaml:4: functions[0].handler", "render never writes an expression"}},
		"nested value is an expression": {map[string]string{"resources/a.yaml": "sensors:\n  - name: s\n    do:\n      - name: go\n        input:\n          limits: ${{ values.limits }}\n"},
			[]string{"limits:\n  cpu: \"${{ event.data.x }}\"\n"}, []string{"resources/a.yaml:6: sensors[0].do[0].input.limits", "render never writes an expression"}},
		"registry is an expression": {map[string]string{"resources/a.yaml": fnWith("h", img, "")}, []string{"registry: \"${{ event.data.x }}\"\n"},
			[]string{"app.yaml: registry", "render never writes an expression"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			refused(t, baseApp, tc.files, tc.vals, tc.want...)
		})
	}
}

// ADR-0217 Decision 6: an object or a list substituted from a value cannot set an image field or imageDigest; a key
// image in a RawMessage field stays data.
func TestRenderRefusesSubstitutedImage(t *testing.T) {
	t.Parallel()
	app := baseApp + `    obj:
      type: object
      default: {}
      additionalProperties: true
      properties:
        name:
          type: string
    list:
      type: array
      default: []
`
	const evil = "evil.example/x:1.0.0"
	fn := "obj:\n  name: f\n  runtime: nodejs22\n  handler: h\n  image: " + evil + "\n"
	for name, tc := range map[string]struct {
		frag, vals string
		want       []string
	}{
		"function object": {"functions:\n  - ${{ values.obj }}\n", fn,
			[]string{"resources/a.yaml:2: functions[0]", "sets functions[0].image"}},
		"function digest": {"functions:\n  - ${{ values.obj }}\n", "obj:\n  name: f\n  imageDigest: sha256:" + strings.Repeat("0", 64) + "\n",
			[]string{"resources/a.yaml:2: functions[0]", "sets functions[0].imageDigest"}},
		"function list": {"functions: ${{ values.list }}\n", "list:\n  - name: f\n    image: " + evil + "\n",
			[]string{"resources/a.yaml:1: functions", "sets functions[0].image"}},
		"step function": {"workflows:\n  - name: w\n    steps:\n      - name: s\n        function: ${{ values.obj }}\n", "obj:\n  image: " + evil + "\n",
			[]string{"resources/a.yaml:5: workflows[0].steps[0].function", "sets workflows[0].steps[0].function.image"}},
		"site object": {"sites:\n  - ${{ values.obj }}\n", "obj:\n  name: web\n  image: " + evil + "\n  bucket:\n    name: web\n",
			[]string{"resources/a.yaml:2: sites[0]", "sets sites[0].image"}},
		"key case": {"functions:\n  - ${{ values.obj }}\n", strings.Replace(fn, "image:", "IMAGE:", 1),
			[]string{"resources/a.yaml:2: functions[0]", "sets functions[0].IMAGE"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			refused(t, app, map[string]string{"resources/a.yaml": tc.frag}, []string{tc.vals}, tc.want...)
		})
	}

	const sensor = `workflows:
  - name: flow
    steps:
      - name: a
        builtin:
          pass: ${{ true }}
sensors:
  - name: s
    "on":
      - name: tick
        source: t
        event: tick
    do:
      - name: go
        "on": tick
        workflow: flow
        input: ${{ values.obj }}
`
	a := mustRender(t, app, map[string]string{"resources/a.yaml": sensor}, "obj:\n  image: "+evil+"\n")
	require.JSONEq(t, `{"image":"`+evil+`"}`, string(a.Spec.Sensors[0].Do[0].Input), "a key image in a Sensor input is data")
}

// ADR-0217 Decision 6: the strict decode matches a key in any letter case, so routing does too.
func TestRenderRoutesKeyInAnyCase(t *testing.T) {
	t.Parallel()
	a := mustRender(t, baseApp, map[string]string{"resources/a.yaml": strings.Replace(apiFragment, "image:", "Image:", 1)})
	require.Equal(t, "reg.example/shop-api:1.0.0@"+fakeDigest("shop-api:1.0.0"), a.Spec.Functions[0].Image)
}

// ADR-0217 Decision 2: registry reads only values; a when reads values and app.
func TestLoadRefusesRegistryOverImages(t *testing.T) {
	t.Parallel()
	_, err := Load(writeTemplate(t, strings.Replace(baseApp, "${{ values.registry }}", "${{ images.api }}", 1), nil))
	require.ErrorContains(t, err, "registry reads images")
	tpl, err := Load(writeTemplate(t, baseApp+"when:\n  resources/a.yaml: ${{ app.name === 'shop' && values.debug === false }}\n",
		map[string]string{"resources/a.yaml": apiFragment}))
	require.NoError(t, err)
	pin(t, tpl)
	a, err := Render(tpl, RenderInput{Name: "other", ResourceGroup: "rg"})
	require.NoError(t, err)
	require.Empty(t, a.Spec.Functions, "when reads app.name, which --name sets")
	require.Equal(t, v1.NamespaceName("default"), a.Namespace)
	require.Equal(t, v1.ResourceGroupName("rg"), a.ResourceGroup)
}

// ADR-0217 Decision 7: render appends each file's hooks lists in file order, as it appends a section; a refusal of a
// hook entry names its file and its index there.
func TestRenderAppendsHooks(t *testing.T) {
	t.Parallel()
	fns := "functions:\n  - name: migrate\n    runtime: nodejs22\n    handler: h\n    image: ${{ images.api }}\n" +
		"  - name: warm\n    runtime: nodejs22\n    handler: h\n    image: ${{ images.api }}\n"
	app, err := render(t, baseApp, map[string]string{
		"resources/a.yaml": fns,
		"resources/b.yaml": "hooks:\n  preApply:\n    - function: migrate\n  postApply:\n    - function: warm\n",
		"resources/c.yaml": "hooks:\n  preApply:\n    - function: warm\n",
	})
	require.NoError(t, err)
	require.Equal(t, &v1.AppHooks{
		PreApply:  []v1.AppHook{{Function: "migrate"}, {Function: "warm"}},
		PostApply: []v1.AppHook{{Function: "warm"}},
	}, app.Spec.Hooks)

	app, err = render(t, baseApp, map[string]string{"resources/a.yaml": fns, "resources/b.yaml": "hooks:\n  preApply:\n"})
	require.NoError(t, err)
	require.Nil(t, app.Spec.Hooks, "an empty hooks section adds none")

	refused(t, baseApp, map[string]string{
		"resources/a.yaml": fns,
		"resources/b.yaml": "hooks:\n  preApply:\n    - function: migrate\n",
		"resources/c.yaml": "hooks:\n  preApply:\n    - function: nope\n",
	}, nil, "resources/c.yaml: hooks.preApply[0].function")
}
