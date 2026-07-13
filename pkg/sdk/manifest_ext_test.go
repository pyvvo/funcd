package sdk_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// pyManifest is a funcdctl.yaml (ADR-0122): a client push/dev config with a python314 runtime, a KV
// binding, and an inline {input, output} contract — the fixture for the LoadManifest/ContractSides/
// GenerateTypes tests. Block-style YAML throughout (no flow-style `{}` / `[]`), matching the examples.
const pyManifest = `
runtime: python314
handler: handle
bindings:
  kv:
    - alias: pycounters
      store: py-counters
      table: table-counters
contract:
  input:
    type: object
    additionalProperties: false
    properties:
      name:
        type: string
    required:
      - name
  output:
    type: object
    additionalProperties: false
    properties:
      name:
        type: string
      count:
        type: integer
    required:
      - name
      - count
`

func writeManifestFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "funcdctl.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

// ContractSides returns both mandatory sides; a missing side is fault.Invalid (ADR-0090).
func TestContractSides(t *testing.T) {
	t.Parallel()
	m, err := sdk.LoadManifest(writeManifestFile(t, pyManifest))
	require.NoError(t, err)

	in, out, err := m.ContractSides()
	require.NoError(t, err)
	require.Contains(t, string(in), `"name"`)
	require.Contains(t, string(out), `"count"`)

	// a directly-constructed manifest with a missing output side is rejected.
	bad := &sdk.Manifest{Contract: sdk.Contract{Input: []byte(`{"type":"null"}`)}}
	_, _, berr := bad.ContractSides()
	require.Error(t, berr)
}

// LoadManifest structurally rejects a manifest missing a required field (runtime/handler/contract).
func TestLoadManifestStructuralErrors(t *testing.T) {
	t.Parallel()
	// missing runtime.
	_, err := sdk.LoadManifest(writeManifestFile(t, `
handler: handle
contract:
  input:
    type: "null"
  output:
    type: "null"
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "runtime")
}

// scenario: types-python — GenerateTypes on a python314 manifest emits a .py module declaring
// FuncInput/FuncOutput (from the schema) and a typed KV binding context (ADR-0122 Decision 4).
func TestScenario_types_python(t *testing.T) {
	t.Parallel()
	m, err := sdk.LoadManifest(writeManifestFile(t, pyManifest))
	require.NoError(t, err)

	files, err := sdk.GenerateTypes(m)
	require.NoError(t, err)
	body, ok := files["funcd_types.py"]
	require.True(t, ok, "a .py module is generated for python314")
	s := string(body)
	require.Contains(t, s, "class FuncInput(TypedDict)")
	require.Contains(t, s, "name: str")
	require.Contains(t, s, "class FuncOutput(TypedDict)")
	require.Contains(t, s, "count: int")
	require.Contains(t, s, `kv: Literal["pycounters"]`, "the binding context is typed by alias")
}

// scenario: types-node — the same manifest on a nodejs22 runtime emits a .d.ts with the two surfaces.
func TestScenario_types_node(t *testing.T) {
	t.Parallel()
	nodeManifest := pyManifest
	m, err := sdk.LoadManifest(writeManifestFile(t, nodeManifest))
	require.NoError(t, err)
	m.Runtime = "nodejs22" // reuse the fixture on the Node runtime

	files, err := sdk.GenerateTypes(m)
	require.NoError(t, err)
	body, ok := files["funcd.d.ts"]
	require.True(t, ok, "a .d.ts is generated for nodejs22")
	s := string(body)
	require.Contains(t, s, "export interface FuncInput")
	require.Contains(t, s, "name: string;")
	require.Contains(t, s, "export interface FuncOutput")
	require.Contains(t, s, "count: number;")
	require.Contains(t, s, `kv: "pycounters";`, "the binding context is typed by alias")
}
