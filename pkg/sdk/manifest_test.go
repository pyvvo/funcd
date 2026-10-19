package sdk_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// DecodeManifest accepts YAML (the kubectl-style `funcdctl apply -f fn.yaml`) AND JSON.
func TestDecodeManifestAcceptsYAMLAndJSON(t *testing.T) {
	yamlManifest := []byte(`
apiVersion: funcd.io/v1alpha1
kind: Function
metadata:
  name: front
  namespace: default
  resourceGroup: rg1
spec:
  runtime: nodejs22
  handler: handle
  image: oci-layout:///mnt/funcd-deps/registry:front
  bindings:
    links:
      - alias: greeter
        target: greeter
`)
	obj, err := sdk.DecodeManifest(yamlManifest)
	require.NoError(t, err)
	fn, ok := obj.(*v1.Function)
	require.True(t, ok)
	require.Equal(t, v1.ObjectName("front"), fn.Name)
	require.Len(t, fn.Spec.Bindings.Links, 1)
	require.Equal(t, "greeter", fn.Spec.Bindings.Links[0].Alias)
	require.Equal(t, v1.ObjectName("greeter"), fn.Spec.Bindings.Links[0].Target)

	// JSON still decodes (JSON is valid YAML).
	jsonManifest := []byte(`{"apiVersion":"funcd.io/v1alpha1","kind":"Function","metadata":{"name":"g"}}`)
	o2, err := sdk.DecodeManifest(jsonManifest)
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("g"), o2.GetName())
}

// Issue #62: every document of a multi-document manifest is decoded, in order; DecodeManifest refuses
// a multi-document manifest instead of silently keeping only the first.
func TestIssue62_DecodeManifestsDecodesEveryDocument(t *testing.T) {
	multi := []byte(`---
apiVersion: funcd.io/v1alpha1
kind: ConfigMap
metadata:
  name: first
spec:
  data:
    script: |
      ---
      not a separator
---
# comment-only document
---
{"apiVersion":"funcd.io/v1alpha1","kind":"Function","metadata":{"name":"second"}}
---
apiVersion: funcd.io/v1alpha1
kind: ConfigMap
metadata:
  name: third
...
`)
	objs, err := sdk.DecodeManifests(multi)
	require.NoError(t, err)
	require.Len(t, objs, 3)
	require.Equal(t, v1.ObjectName("first"), objs[0].GetName())
	require.Equal(t, "---\nnot a separator\n", objs[0].(*v1.ConfigMap).Spec.Data["script"])
	require.Equal(t, v1.KindFunction, objs[1].GroupVersionKind().Kind)
	require.Equal(t, v1.ObjectName("second"), objs[1].GetName())
	require.Equal(t, v1.ObjectName("third"), objs[2].GetName())

	_, err = sdk.DecodeManifest(multi)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), "3 documents")

	_, err = sdk.DecodeManifests([]byte("---\n# nothing\n"))
	require.Equal(t, fault.Invalid, fault.KindOf(err))

	_, err = sdk.DecodeManifests([]byte("apiVersion: funcd.io/v1alpha1\nkind: ConfigMap\n---\nkind: Frobnicate\n"))
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), "document 2")
}

// A bare `on:` key (the documented Sensor and blob EventSource shape) decodes as the string key "on",
// never as YAML 1.1's boolean true — which the typed decode would drop without a word.
func TestIssue63_BareOnKeyDecodesAsString(t *testing.T) {
	sensor, err := sdk.DecodeManifest([]byte(`
apiVersion: funcd.io/v1alpha1
kind: Sensor
metadata:
  name: orders-pipelines
  namespace: default
  resourceGroup: rg1
spec:
  on:
    - name: hook
      source: team-hooks
      event: new-orders
  do:
    - name: on-demand
      on: hook
      workflow: orders-report
`))
	require.NoError(t, err)
	s, ok := sensor.(*v1.Sensor)
	require.True(t, ok)
	require.Len(t, s.Spec.On, 1)
	require.Equal(t, v1.ObjectName("hook"), s.Spec.On[0].Name)
	require.Len(t, s.Spec.Do, 1)
	require.Equal(t, v1.ObjectName("hook"), s.Spec.Do[0].On)
	require.NoError(t, s.Validate())

	source, err := sdk.DecodeManifest([]byte(`
apiVersion: funcd.io/v1alpha1
kind: EventSource
metadata:
  name: removals
  namespace: default
  resourceGroup: rg1
spec:
  blob:
    bucket: raw
    events:
      - name: gone
        prefix: drop/
        on:
          - Removed
`))
	require.NoError(t, err)
	es, ok := source.(*v1.EventSource)
	require.True(t, ok)
	require.Equal(t, []v1.BlobEventType{"Removed"}, es.Spec.Blob.Events[0].On)
	require.ErrorContains(t, es.Validate(), `"Removed" is unsupported`)
}

// A plain value that YAML 1.2 reads as a string (no, on, y) keeps its text: a string field never gets
// YAML 1.1's "false"/"true", a json.RawMessage field gets a JSON string, and a bool field written that
// way fails the decode and names the field.
func TestIssue299_BareBoolWordValueKeepsText(t *testing.T) {
	obj, err := sdk.DecodeManifest([]byte(`
apiVersion: funcd.io/v1alpha1
kind: ConfigMap
metadata:
  name: c
  namespace: default
spec:
  data:
    ENABLED: no
    MODE: on
    Y: y
    QUOTED: "yes"
    FLAG: true
    PORT: 8080
`))
	require.NoError(t, err)
	cm, ok := obj.(*v1.ConfigMap)
	require.True(t, ok)
	require.Equal(t, map[string]string{
		"ENABLED": "no",
		"MODE":    "on",
		"Y":       "y",
		"QUOTED":  "yes",
		"FLAG":    "true",
		"PORT":    "8080",
	}, cm.Spec.Data)

	run := `
apiVersion: funcd.io/v1alpha1
kind: WorkflowRun
metadata:
  name: r
  namespace: default
spec:
  workflow: w
  input:
    answer: no
    choices:
      - yes
      - no
    count: 2
  paused: %s
`
	obj, err = sdk.DecodeManifest(fmt.Appendf(nil, run, "true"))
	require.NoError(t, err)
	wr, ok := obj.(*v1.WorkflowRun)
	require.True(t, ok)
	require.JSONEq(t, `{"answer":"no","choices":["yes","no"],"count":2}`, string(wr.Spec.Input))
	require.True(t, wr.Spec.Paused)

	_, err = sdk.DecodeManifest(fmt.Appendf(nil, run, "yes"))
	require.Error(t, err, "a YAML 1.2 string in a bool field must not decode as true")
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), "paused")
}

// A plain scalar that YAML reads as a number or a boolean keeps its text when it lands in a string — a
// string field, a []string element or a map key: 1.10, 0755, 0x1F and 1e3 never become 1.1, 493, 31 and
// 1000. A number field still gets the number.
func TestIssue416_NumberLikeTextInStringFieldKeepsText(t *testing.T) {
	obj, err := sdk.DecodeManifest([]byte(`
apiVersion: funcd.io/v1alpha1
kind: ConfigMap
metadata:
  name: c
  namespace: default
  tags:
    0755: 1.10
spec:
  data:
    VERSION: 1.10
    MODE: 0755
    HEX: 0x1F
    EXP: 1e3
    FLAG: True
    PORT: 8080
`))
	require.NoError(t, err)
	cm, ok := obj.(*v1.ConfigMap)
	require.True(t, ok)
	require.Equal(t, map[string]string{
		"VERSION": "1.10",
		"MODE":    "0755",
		"HEX":     "0x1F",
		"EXP":     "1e3",
		"FLAG":    "True",
		"PORT":    "8080",
	}, cm.Spec.Data)
	require.Equal(t, v1.Tags{"0755": "1.10"}, cm.Tags)

	obj, err = sdk.DecodeManifest([]byte(`
apiVersion: funcd.io/v1alpha1
kind: EgressPolicy
metadata:
  name: e
  namespace: default
spec:
  rules:
    - to:
        domains:
          - 1.10
      ports:
        - 0x1BB
`))
	require.NoError(t, err)
	ep, ok := obj.(*v1.EgressPolicy)
	require.True(t, ok)
	require.Equal(t, []string{"1.10"}, ep.Spec.Rules[0].To.Domains)
	require.Equal(t, []int{443}, ep.Spec.Rules[0].Ports)
}

// A manifest key the typed object does not know is rejected at decode, not silently dropped before the
// request is built: the server's additionalProperties:false edge (ADR-0108 no-binding-field-schema) never
// sees a key the client discarded, so `funcdctl apply` would report a manifest applied that was not.
func TestIssue64_DecodeManifestRejectsUnknownKeys(t *testing.T) {
	valid := []byte(`
apiVersion: funcd.io/v1alpha1
kind: EventSource
metadata:
  name: legacy
  namespace: default
spec:
  timer:
    events:
      - name: tick
        interval: 5s
`)
	obj, err := sdk.DecodeManifest(valid)
	require.NoError(t, err)
	es, ok := obj.(*v1.EventSource)
	require.True(t, ok)
	require.NotNil(t, es.Spec.Timer)
	require.Len(t, es.Spec.Timer.Events, 1)

	cases := map[string]struct {
		manifest string
		key      string
	}{
		"removed v1 eventsource keys": {
			key: "function",
			manifest: `
apiVersion: funcd.io/v1alpha1
kind: EventSource
metadata:
  name: legacy
  namespace: default
spec:
  function: echo
  timer:
    events:
      - name: tick
        interval: 5s
`,
		},
		"nested unknown timer key": {
			key: "jitter",
			manifest: `
apiVersion: funcd.io/v1alpha1
kind: EventSource
metadata:
  name: legacy
  namespace: default
spec:
  timer:
    events:
      - name: tick
        interval: 5s
        jitter: 1s
`,
		},
		"unknown function spec key": {
			key: "imagePullPolicy",
			manifest: `
apiVersion: funcd.io/v1alpha1
kind: Function
metadata:
  name: front
  namespace: default
spec:
  runtime: nodejs22
  handler: handle
  imagePullPolicy: Always
`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := sdk.DecodeManifest([]byte(tc.manifest))
			require.Error(t, err, "an unknown manifest key must not be dropped silently")
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.Contains(t, err.Error(), tc.key)
		})
	}
}

// scenario: app-admission-refuses (the strict-decode half, ADR-0199 Decision 2): an App's sections decode into their
// typed entries, a ref entry included, and an unknown section or entry key fails the decode naming it.
func TestDecodeManifestApp(t *testing.T) {
	const head = `
apiVersion: funcd.io/v1alpha1
kind: App
metadata:
  name: todo
  namespace: default
  resourceGroup: todo-rg
spec:
`
	obj, err := sdk.DecodeManifest([]byte(head + `
  kv:
    - name: todo-store
      deletion: delete
      tables:
        - name: todos
          owner: todo-api
  functions:
    - ref: mailer
`))
	require.NoError(t, err)
	app, ok := obj.(*v1.App)
	require.True(t, ok)
	require.Equal(t, v1.DeletionDelete, app.Spec.KV[0].Deletion)
	require.Equal(t, []v1.KVTable{{Name: "todos", Owner: "todo-api"}}, app.Spec.KV[0].Tables)
	require.Equal(t, v1.ObjectName("mailer"), app.Spec.Functions[0].Ref)
	require.NoError(t, app.Validate())

	for key, body := range map[string]string{
		"deployments": "\n  deployments:\n    - name: web\n",
		"replicaz":    "\n  functions:\n    - name: web\n      replicaz: 2\n",
	} {
		_, err := sdk.DecodeManifest([]byte(head + body))
		require.Error(t, err, key)
		require.Contains(t, err.Error(), key)
	}
}
