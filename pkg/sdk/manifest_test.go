package sdk_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

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
  artifact:
    uri: oci-layout:///mnt/funcd-deps/registry:front
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
