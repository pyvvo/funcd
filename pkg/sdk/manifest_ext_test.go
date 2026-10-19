package sdk_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/pkg/sdk"
)

// pyManifest is a funcdctl.yaml (ADR-0122): a client push/dev config with a python314 runtime, a KV
// binding, and an inline {input, output} contract — the fixture for the LoadManifest/ContractSides
// tests. Block-style YAML throughout (no flow-style `{}` / `[]`), matching the examples.
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
