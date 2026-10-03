package sdk_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/pkg/sdk"
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

// Issue #498: a contract the profile gate accepts with the ADR-0058 nullable form `type: [T, "null"]`, on a
// field, an array item or a whole side, generates `T | None` (python) and `T | null` (node) types.
func TestIssue498_GenerateTypesMapsNullableTypeList(t *testing.T) {
	t.Parallel()
	input := []byte(`{"type":"object","properties":{"note":{"type":["string","null"]},` +
		`"tags":{"type":["array","null"],"items":{"type":["integer","null"]}}},` +
		`"required":["note"],"additionalProperties":false}`)
	output := []byte(`{"type":["object","null"],"properties":{"id":{"type":"string"}},` +
		`"required":["id"],"additionalProperties":false}`)
	require.NoError(t, contract.Check(input))
	require.NoError(t, contract.Check(output))

	cases := map[v1.RuntimeName]struct {
		file string
		want []string
	}{
		"python314": {file: "funcd_types.py", want: []string{
			"    note: str | None\n",
			"    tags: list[int | None] | None  # optional\n",
			"FuncOutput = dict[str, object] | None  # non-record contract side\n",
		}},
		"nodejs22": {file: "funcd.d.ts", want: []string{
			"  note: string | null;\n",
			"  tags?: (number | null)[] | null;\n",
			"export type FuncOutput = Record<string, unknown> | null;\n",
		}},
	}
	for runtime, tc := range cases {
		m := &sdk.Manifest{Runtime: runtime, Contract: sdk.Contract{Input: input, Output: output}}
		files, err := sdk.GenerateTypes(m)
		require.NoError(t, err, runtime)
		for _, w := range tc.want {
			require.Contains(t, string(files[tc.file]), w, runtime)
		}
	}
}
