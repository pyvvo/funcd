package sdk_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// writeManifest writes body to a funcdctl.yaml in a temp dir and returns its path.
func writeManifest(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "funcdctl.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

const manifestWithoutDev = `runtime: python314
handler: handle
bindings:
  kv:
    - alias: cache
      store: cache-kv
      table: entries
  config:
    - project-config
  secrets:
    - quack-token
contract:
  input:
    type: object
    additionalProperties: false
    properties:
      file:
        type: string
    required:
      - file
  output:
    type: object
    additionalProperties: false
    properties:
      rows:
        type: integer
    required:
      - rows
`

// dev: block appended to the same four sections above.
const manifestDevBlock = `
dev:
  backends:
    kv: memory
    blob: file://.funcd-dev/blob
    catalog: file://.funcd-dev/lake
  config:
    project-config:
      STOP_KEYWORDS: TOTALDESOPERATIONS,Solde
      BANK: bank
  secrets:
    quack-token:
      token: ${QUACK_TOKEN}
`

// scenario: the additive Dev block (ADR-0125) parses off funcdctl.yaml — backends (global per kind),
// inline config, and env-ref secrets — without disturbing the ADR-0122 four fields.
func TestLoadManifestParsesDevBlock(t *testing.T) {
	m, err := sdk.LoadManifest(writeManifest(t, manifestWithoutDev+manifestDevBlock))
	require.NoError(t, err)

	require.Equal(t, "memory", m.Dev.Backends.KV)
	require.Equal(t, "file://.funcd-dev/blob", m.Dev.Backends.Blob)
	require.Equal(t, "file://.funcd-dev/lake", m.Dev.Backends.Catalog)
	require.Equal(t, "bank", m.Dev.Config["project-config"]["BANK"])
	require.Equal(t, "TOTALDESOPERATIONS,Solde", m.Dev.Config["project-config"]["STOP_KEYWORDS"])
	require.Equal(t, "${QUACK_TOKEN}", m.Dev.Secrets["quack-token"]["token"],
		"the ${ENV} ref is parsed verbatim — resolution happens in funcdctl dev, never here")
}

// scenario (unit): funcdctl push/types IGNORE the dev: block — a manifest with and without it produces
// the IDENTICAL four ADR-0122 fields (the push/bake + GenerateTypes inputs), so `dev:` is inert to them.
func TestDevBlockIgnoredByPushAndTypes(t *testing.T) {
	base, err := sdk.LoadManifest(writeManifest(t, manifestWithoutDev))
	require.NoError(t, err)
	withDev, err := sdk.LoadManifest(writeManifest(t, manifestWithoutDev+manifestDevBlock))
	require.NoError(t, err)

	// The four fields push + types read are byte-identical regardless of the dev block.
	require.Equal(t, base.Runtime, withDev.Runtime)
	require.Equal(t, base.Handler, withDev.Handler)
	require.Equal(t, base.Bindings, withDev.Bindings)
	require.Equal(t, base.Contract, withDev.Contract)

	// The push-time contract sides are unchanged by dev.
	bi, bo, berr := base.ContractSides()
	require.NoError(t, berr)
	di, do, derr := withDev.ContractSides()
	require.NoError(t, derr)
	require.Equal(t, bi, di)
	require.Equal(t, bo, do)

	// The types codegen output is identical (dev never reaches GenerateTypes).
	baseTypes, err := sdk.GenerateTypes(base)
	require.NoError(t, err)
	devTypes, err := sdk.GenerateTypes(withDev)
	require.NoError(t, err)
	require.Equal(t, baseTypes, devTypes)
}

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
  links:
    - alias: greeter
      target: greeter
`)
	obj, err := sdk.DecodeManifest(yamlManifest)
	require.NoError(t, err)
	fn, ok := obj.(*v1.Function)
	require.True(t, ok)
	require.Equal(t, v1.ObjectName("front"), fn.Name)
	require.Len(t, fn.Spec.Links, 1)
	require.Equal(t, "greeter", fn.Spec.Links[0].Alias)
	require.Equal(t, v1.ObjectName("greeter"), fn.Spec.Links[0].Target)

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
        interval: 5000000000
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
        interval: 5000000000
`,
		},
		"nested unknown timer key": {
			key: "cron",
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
        interval: 5000000000
        cron: "*/5 * * * *"
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
