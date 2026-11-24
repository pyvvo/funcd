package sdk_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const counterContract = `runtime: python314
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

// writeFiles writes files into a fresh directory and returns it.
func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	return dir
}

// ADR-0247 Decision 1: the contract file is .yaml or .json, strict and required, with the http rules.
func TestLoadContract(t *testing.T) {
	t.Parallel()
	t.Run("yaml", func(t *testing.T) {
		t.Parallel()
		dir := writeFiles(t, map[string]string{"counter.contract.yaml": counterContract})
		c, path, err := sdk.LoadContract(dir, "counter")
		require.NoError(t, err)
		require.Equal(t, filepath.Join(dir, "counter.contract.yaml"), path)
		require.Equal(t, v1.RuntimeName("python314"), c.Runtime)
		in, out := c.Sides()
		require.Contains(t, string(in), `"name"`)
		require.Contains(t, string(out), `"count"`)
	})
	t.Run("json", func(t *testing.T) {
		t.Parallel()
		dir := writeFiles(t, map[string]string{"counter.contract.json": `{"runtime":"nodejs22","input":{"type":"null"},"output":{"type":"null"}}`})
		c, _, err := sdk.LoadContract(dir, "counter")
		require.NoError(t, err)
		require.Equal(t, v1.RuntimeName("nodejs22"), c.Runtime)
	})
	t.Run("http", func(t *testing.T) {
		t.Parallel()
		dir := writeFiles(t, map[string]string{"api.contract.yaml": "runtime: nodejs22\nprotocol: http\n"})
		c, _, err := sdk.LoadContract(dir, "api")
		require.NoError(t, err)
		in, out := c.Sides()
		require.Nil(t, in)
		require.Nil(t, out)
	})
	for name, tc := range map[string]struct {
		files map[string]string
		want  []string
	}{
		"none":            {want: []string{"counter.contract.yaml", "counter.contract.json"}},
		"both":            {files: map[string]string{"counter.contract.yaml": counterContract, "counter.contract.json": "{}"}, want: []string{"two contract files", "counter.contract.yaml", "counter.contract.json"}},
		"no-runtime":      {files: map[string]string{"counter.contract.yaml": "input:\n  type: \"null\"\noutput:\n  type: \"null\"\n"}, want: []string{"missing runtime"}},
		"no-output":       {files: map[string]string{"counter.contract.yaml": "runtime: nodejs22\ninput:\n  type: \"null\"\n"}, want: []string{"missing output"}},
		"unknown-key":     {files: map[string]string{"counter.contract.yaml": counterContract + "handler: handle\n"}, want: []string{"handler"}},
		"http-with-input": {files: map[string]string{"counter.contract.yaml": "runtime: nodejs22\nprotocol: http\ninput:\n  type: \"null\"\n"}, want: []string{"no I/O contract"}},
		"http-python":     {files: map[string]string{"counter.contract.yaml": "runtime: python314\nprotocol: http\n"}, want: []string{"needs a node runtime"}},
		"old-manifest":    {files: map[string]string{"counter.contract.yaml": counterContract, "funcdctl.yaml": "runtime: nodejs22\n"}, want: []string{"funcdctl.yaml is read nowhere", "counter.contract.yaml"}},
		"old-stem":        {files: map[string]string{"counter.funcdctl.yaml": "runtime: nodejs22\n"}, want: []string{"counter.funcdctl.yaml is read nowhere"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, _, err := sdk.LoadContract(writeFiles(t, tc.files), "counter")
			require.Error(t, err)
			for _, w := range tc.want {
				require.Contains(t, err.Error(), w)
			}
		})
	}
}

func counterFile(t *testing.T, runtime v1.RuntimeName) *sdk.ContractFile {
	t.Helper()
	c, _, err := sdk.LoadContract(writeFiles(t, map[string]string{"counter.contract.yaml": counterContract}), "counter")
	require.NoError(t, err)
	c.Runtime = runtime
	return c
}

func counterBindings() v1.FunctionBindings {
	return v1.FunctionBindings{KV: []v1.FunctionKV{{Alias: "pycounters", Store: "py-counters", Table: "table-counters"}}}
}

// GenerateTypes on a python314 contract file emits a .py module declaring FuncInput/FuncOutput and the Function's
// typed KV binding context (ADR-0247 Decision 3).
func TestGenerateTypesPython(t *testing.T) {
	t.Parallel()
	files, err := sdk.GenerateTypes(counterFile(t, "python314"), counterBindings())
	require.NoError(t, err)
	s := string(files["funcd_types.py"])
	require.Contains(t, s, "class FuncInput(TypedDict)")
	require.Contains(t, s, "name: str")
	require.Contains(t, s, "class FuncOutput(TypedDict)")
	require.Contains(t, s, "count: int")
	require.Contains(t, s, `kv: Literal["pycounters"]`, "the binding context is typed by alias")
}

// The same contract file on nodejs22 emits a .d.ts; with no Function document the bindings are empty.
func TestGenerateTypesNode(t *testing.T) {
	t.Parallel()
	files, err := sdk.GenerateTypes(counterFile(t, "nodejs22"), counterBindings())
	require.NoError(t, err)
	s := string(files["funcd.d.ts"])
	require.Contains(t, s, "export interface FuncInput")
	require.Contains(t, s, "count: number;")
	require.Contains(t, s, `kv: "pycounters";`)

	files, err = sdk.GenerateTypes(counterFile(t, "nodejs22"), v1.FunctionBindings{})
	require.NoError(t, err)
	require.Contains(t, string(files["funcd.d.ts"]), "export interface FuncBindings {\n}\n", "the I/O types only")
}

// ADR-0347 Decision 9 and ADR-0348 Decision 6: a Job's contract file emits JobInput and JobOutput, its binding context
// and a JobContext whose run is the RunInfo of its call, in phase run or preview, and whose approval is the Approval it
// runs under, with each shim's key names.
func TestGenerateJobTypes(t *testing.T) {
	t.Parallel()
	files, err := sdk.GenerateJobTypes(counterFile(t, "nodejs22"), counterBindings())
	require.NoError(t, err)
	ts := string(files["funcd.d.ts"])
	for _, want := range []string{"export interface JobInput", "export interface JobOutput", `export interface JobBindings {
  kv: "pycounters";`, `phase: "run" | "preview";`, "startedBy: string;", "export interface Approval {\n  approvedBy: string;",
		"export interface JobContext {\n  run: RunInfo;\n  approval: Approval | null;\n}"} {
		require.Contains(t, ts, want)
	}
	require.NotContains(t, ts, "FuncInput")
	files, err = sdk.GenerateJobTypes(counterFile(t, "python314"), counterBindings())
	require.NoError(t, err)
	py := string(files["funcd_types.py"])
	for _, want := range []string{"class JobInput(TypedDict)", "class JobOutput(TypedDict)", `kv: Literal["pycounters"]`,
		"class RunInfo(TypedDict):", `phase: Literal["run", "preview"]`, "started_by: str", "class Approval(TypedDict):\n    approved_by: str",
		"class JobContext:\n    run: RunInfo\n    approval: Approval | None"} {
		require.Contains(t, py, want)
	}
}

// ADR-0347 Decision 9: the RunInfo that a Job's types hold has its shim's shape, read from the pinned shims' sources: a
// TypedDict keyed name, job, revision, phase and started_by in Python, an interface with startedBy in TypeScript, and
// phase "run" or "preview" in both.
func TestJobRunInfoIsTheShims(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		runtime, file, mod, shim, open, field string
	}{
		{"nodejs22", "funcd.d.ts", langmod.TypeScript, "shim/src/types.ts", "export interface RunInfo {", `^\s+(\w+): ([^;]+);$`},
		{"python314", "funcd_types.py", langmod.Python, "shim/src/funcd_shim/types.py", "class RunInfo(TypedDict):", `^ {4}(\w+): (.+)$`},
	} {
		shim, err := os.ReadFile(filepath.Join(langmod.Dir(t, tc.mod), filepath.FromSlash(tc.shim)))
		require.NoError(t, err)
		files, err := sdk.GenerateJobTypes(counterFile(t, v1.RuntimeName(tc.runtime)), counterBindings())
		require.NoError(t, err)
		want := blockFields(t, string(shim), tc.open, tc.field)
		require.NotEmpty(t, want, "%s holds RunInfo's fields", tc.shim)
		require.Equal(t, want, blockFields(t, string(files[tc.file]), tc.open, tc.field), "%s's RunInfo", tc.file)
	}
}

// blockFields are the fields of the block that starts with open, up to the next line that is not indented: each
// line that field matches gives a name and its type, with TypeScript's single quotes as double ones.
func blockFields(t *testing.T, src, open, field string) map[string]string {
	t.Helper()
	i := strings.Index(src, open)
	require.GreaterOrEqual(t, i, 0, "no %q in:\n%s", open, src)
	re := regexp.MustCompile(field)
	out := map[string]string{}
	for _, line := range strings.Split(src[i+len(open):], "\n")[1:] {
		if line != "" && !strings.HasPrefix(line, " ") {
			break
		}
		if m := re.FindStringSubmatch(line); m != nil {
			out[m[1]] = strings.ReplaceAll(m[2], "'", `"`)
		}
	}
	return out
}

// An http Function has no I/O contract to type.
func TestGenerateTypesRefusesHTTP(t *testing.T) {
	t.Parallel()
	_, err := sdk.GenerateTypes(&sdk.ContractFile{Runtime: "nodejs22", Protocol: v1.ProtocolHTTP}, v1.FunctionBindings{})
	require.Error(t, err)
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
		files, err := sdk.GenerateTypes(&sdk.ContractFile{Runtime: runtime, Input: input, Output: output}, v1.FunctionBindings{})
		require.NoError(t, err, runtime)
		for _, w := range tc.want {
			require.Contains(t, string(files[tc.file]), w, runtime)
		}
	}
}

// Issues #298, #416 and #699 for the contract file: it decodes as funcdctl.yaml did, so a plain y/on/no key keeps its
// text (a contract property is user-named), a contract side keeps its numbers, and an unknown key is a fault.Invalid
// naming it rather than a silent drop.
func TestContractFileKeepsKeyTextAndRejectsUnknownKeys(t *testing.T) {
	t.Parallel()
	const body = "runtime: python314\ninput:\n  type: object\n  properties:\n    y:\n      type: number\n      maximum: 1.10\n" +
		"output:\n  type: \"null\"\n"
	c, _, err := sdk.LoadContract(writeFiles(t, map[string]string{"f.contract.yaml": body}), "f")
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"object","properties":{"y":{"type":"number","maximum":1.1}}}`, string(c.Input))

	_, _, err = sdk.LoadContract(writeFiles(t, map[string]string{"f.contract.yaml": strings.Replace(body, "runtime:", "runtme:", 1)}), "f")
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), "runtme")
}
