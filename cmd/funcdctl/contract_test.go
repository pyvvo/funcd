package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/artifact"
)

// counterContract is an in-profile contract file (ADR-0247 Decision 1). Block-style YAML.
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

const voidContract = "runtime: nodejs22\ninput:\n  type: \"null\"\noutput:\n  type: \"null\"\n"

// writeTree writes files, keyed by slash paths, under a fresh directory and returns it.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	}
	return dir
}

// pushOK pushes build to a fresh layout and returns the ref and digest it printed.
func pushOK(t *testing.T, build string, flags ...string) (ref, digest string) {
	t.Helper()
	ref = "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":v1"
	var out bytes.Buffer
	require.NoError(t, execCLI(&out, nil, append([]string{"push", build, ref}, flags...)...))
	printed := strings.TrimSpace(out.String())
	require.True(t, strings.HasPrefix(printed, ref+"@sha256:"), "push prints <ref>@<digest>, got %q", printed)
	return ref, strings.TrimPrefix(printed, ref+"@")
}

// pushErr pushes build and returns the error.
func pushErr(t *testing.T, build string) error {
	t.Helper()
	return execCLI(&bytes.Buffer{}, nil, "push", build, "oci-layout://"+filepath.Join(t.TempDir(), "layout")+":v1")
}

// scenario: contract-file-push — a file pushes with the contract and runtime of <stem>.contract.yaml or .json beside
// it; with no contract file, or both variants, push refuses, naming the paths.
func TestScenarioContractFilePush(t *testing.T) {
	t.Parallel()
	for _, ext := range []string{".contract.yaml", ".contract.json"} {
		t.Run(ext, func(t *testing.T) {
			t.Parallel()
			body := counterContract
			if ext == ".contract.json" {
				body = `{"runtime":"python314","input":{"type":"object","additionalProperties":false,"properties":{"name":{"type":"string"}}},` +
					`"output":{"type":"object","additionalProperties":false,"properties":{"count":{"type":"integer"}}}}`
			}
			dir := writeTree(t, map[string]string{"dist/counter.py": "def handle(ctx, e):\n    return e\n", "dist/counter" + ext: body})
			ref, digest := pushOK(t, filepath.Join(dir, "dist", "counter.py"))

			var out bytes.Buffer
			require.NoError(t, execCLI(&out, nil, "inspect", ref+"@"+digest))
			require.Contains(t, out.String(), `"name"`)
			require.Contains(t, out.String(), `"count"`)
			rt, err := artifact.InspectRuntime(context.Background(), ref, digest)
			require.NoError(t, err)
			require.Equal(t, "python314", rt)
		})
	}
	t.Run("none", func(t *testing.T) {
		t.Parallel()
		dir := writeTree(t, map[string]string{"counter.mjs": "export function handle() {}\n"})
		err := pushErr(t, filepath.Join(dir, "counter.mjs"))
		require.Equal(t, fault.Invalid, fault.KindOf(err))
		require.Contains(t, err.Error(), filepath.Join(dir, "counter.contract.yaml"))
		require.Contains(t, err.Error(), filepath.Join(dir, "counter.contract.json"))
	})
	t.Run("both", func(t *testing.T) {
		t.Parallel()
		dir := writeTree(t, map[string]string{"counter.mjs": "export function handle() {}\n", "counter.contract.yaml": voidContract, "counter.contract.json": "{}"})
		err := pushErr(t, filepath.Join(dir, "counter.mjs"))
		require.Equal(t, fault.Invalid, fault.KindOf(err))
		require.Contains(t, err.Error(), filepath.Join(dir, "counter.contract.yaml"))
		require.Contains(t, err.Error(), filepath.Join(dir, "counter.contract.json"))
	})
	t.Run("out-of-profile", func(t *testing.T) {
		t.Parallel()
		dir := writeTree(t, map[string]string{
			"counter.mjs":           "export function handle() {}\n",
			"counter.contract.yaml": "runtime: nodejs22\ninput:\n  anyOf:\n    - type: string\n    - type: integer\noutput:\n  type: \"null\"\n",
		})
		err := pushErr(t, filepath.Join(dir, "counter.mjs"))
		require.Equal(t, fault.Invalid, fault.KindOf(err))
		require.Contains(t, err.Error(), "outside the funcd profile")
	})
}

// scenario: http-contract — protocol http with no input bakes no contract, from a file and from a bundle, where it
// removes the contract file an earlier push left; with input, or on a python runtime, push refuses.
func TestScenarioHTTPContract(t *testing.T) {
	t.Parallel()
	const httpContract = "runtime: nodejs22\nprotocol: http\n"
	dir := writeTree(t, map[string]string{
		"api.mjs":                     "export const handle = {};\n",
		"api.contract.yaml":           httpContract,
		"web/main.mjs":                "export const handle = {};\n",
		"web/web.contract.yaml":       httpContract,
		"web/" + bundleContractName:   `{"input":{},"output":{}}`,
		"bad/withinput.mjs":           "export const handle = {};\n",
		"bad/withinput.contract.yaml": httpContract + "input:\n  type: \"null\"\n",
		"bad/python.py":               "app = None\n",
		"bad/python.contract.yaml":    "runtime: python314\nprotocol: http\n",
	})
	for _, build := range []string{filepath.Join(dir, "api.mjs"), filepath.Join(dir, "web")} {
		ref, digest := pushOK(t, build)
		_, _, err := artifact.InspectContract(context.Background(), ref, digest)
		require.Equal(t, fault.NotFound, fault.KindOf(err), "%s carries no contract: %v", build, err)
	}
	require.NoFileExists(t, filepath.Join(dir, "web", bundleContractName), "the stale contract is removed")
	for name, want := range map[string]string{"withinput.mjs": "no I/O contract", "python.py": "needs a node runtime"} {
		err := pushErr(t, filepath.Join(dir, "bad", name))
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
		require.Contains(t, err.Error(), want, name)
	}
}

// scenario: bundle-push — `funcdctl push dist/intake <ref>` records the runtime and contract of
// dist/intake/intake.contract.yaml, with entry main.py; a node bundle defaults to main.mjs, and --entry wins.
func TestScenarioBundlePush(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		files map[string]string
		flags []string
		want  string
	}{
		"python": {files: map[string]string{"main.py": "def handle(c, e):\n    return e\n", "intake.contract.yaml": counterContract}, want: "main.py"},
		"node":   {files: map[string]string{"main.mjs": "export function handle() {}\n", "intake.contract.yaml": voidContract}, want: "main.mjs"},
		"entry":  {files: map[string]string{"main.mjs": "// main\n", "other.mjs": "// other\n", "intake.contract.yaml": voidContract}, flags: []string{"--entry", "other.mjs"}, want: "other.mjs"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{}
			for f, body := range tc.files {
				files["dist/intake/"+f] = body
			}
			dir := writeTree(t, files)
			ref, digest := pushOK(t, filepath.Join(dir, "dist", "intake"), tc.flags...)

			in, _, err := artifact.InspectContract(context.Background(), ref, digest)
			require.NoError(t, err)
			require.NotEmpty(t, in)
			rt, err := artifact.InspectRuntime(context.Background(), ref, digest)
			require.NoError(t, err)
			require.Equal(t, strings.TrimPrefix(strings.SplitN(tc.files["intake.contract.yaml"], "\n", 2)[0], "runtime: "), rt)

			pulled := t.TempDir()
			var out bytes.Buffer
			require.NoError(t, execCLI(&out, nil, "pull", ref, digest, pulled))
			require.Equal(t, filepath.Join(pulled, tc.want), strings.TrimSpace(out.String()))
		})
	}
}

// scenario: old-manifest-refused — with a funcdctl.yaml or <name>.funcdctl.yaml where they look, push and types
// fail, naming the files that replace it.
func TestScenarioOldManifestRefused(t *testing.T) {
	t.Parallel()
	for _, old := range []string{"funcdctl.yaml", "counter.funcdctl.yaml"} {
		t.Run(old, func(t *testing.T) {
			t.Parallel()
			dir := writeTree(t, map[string]string{"counter.mjs": "export function handle() {}\n", "counter.contract.yaml": voidContract, old: "runtime: nodejs22\n"})
			err := pushErr(t, filepath.Join(dir, "counter.mjs"))
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.Contains(t, err.Error(), old+" is read nowhere")
			require.Contains(t, err.Error(), "counter.contract.yaml")

			err = execCLI(&bytes.Buffer{}, nil, "types", "-f", filepath.Join(dir, "counter.contract.yaml"), "-o", t.TempDir())
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.Contains(t, err.Error(), old+" is read nowhere")
		})
	}
}

// counterFunction is the Function document counter, on runtime, binding KV alias pycounters.
func counterFunction(runtime string) string {
	return "apiVersion: funcd.io/v1alpha1\nkind: Function\nmetadata:\n  name: counter\n  namespace: default\n  resourceGroup: rg1\n" +
		"spec:\n  runtime: " + runtime + "\n  handler: handle\n  image: oci-layout://unused:counter\n" +
		"  bindings:\n    kv:\n      - alias: pycounters\n        store: py-counters\n        table: table-counters\n"
}

// scenario: types-from-contract — `funcdctl types -f counter.contract.yaml -o src` writes the I/O types and the
// binding types of the Function document counter; with no Function document, the I/O types only; a runtime that
// differs from the contract file's stops it, naming both files.
func TestScenarioTypesFromContract(t *testing.T) {
	t.Parallel()
	types := func(t *testing.T, dir string) (string, error) {
		t.Helper()
		out := t.TempDir()
		if err := execCLI(&bytes.Buffer{}, nil, "types", "-f", filepath.Join(dir, "counter.contract.yaml"), "-o", out); err != nil {
			return "", err
		}
		body, err := os.ReadFile(filepath.Join(out, "funcd_types.py")) //nolint:gosec // test-owned path
		require.NoError(t, err)
		return string(body), nil
	}
	t.Run("with-function", func(t *testing.T) {
		t.Parallel()
		body, err := types(t, writeTree(t, map[string]string{"counter.contract.yaml": counterContract, "counter.yaml": counterFunction("python314")}))
		require.NoError(t, err)
		require.Contains(t, body, "class FuncInput(TypedDict)")
		require.Contains(t, body, "count: int")
		require.Contains(t, body, `kv: Literal["pycounters"]`)
	})
	t.Run("io-only", func(t *testing.T) {
		t.Parallel()
		body, err := types(t, writeTree(t, map[string]string{"counter.contract.yaml": counterContract}))
		require.NoError(t, err)
		require.Contains(t, body, "class FuncOutput(TypedDict)")
		require.Contains(t, body, "pass  # no bindings declared")
	})
	t.Run("mismatch", func(t *testing.T) {
		t.Parallel()
		dir := writeTree(t, map[string]string{"counter.contract.yaml": counterContract, "counter.yaml": counterFunction("nodejs22")})
		_, err := types(t, dir)
		require.Equal(t, fault.Invalid, fault.KindOf(err))
		require.Contains(t, err.Error(), filepath.Join(dir, "counter.yaml"))
		require.Contains(t, err.Error(), filepath.Join(dir, "counter.contract.yaml"))
	})
	t.Run("broken-function", func(t *testing.T) {
		t.Parallel()
		dir := writeTree(t, map[string]string{
			"counter.contract.yaml": counterContract,
			"counter.yaml":          strings.Replace(counterFunction("python314"), "spec:", "spec:\n  runtme: python314", 1),
			"other.yaml":            "kind: NotFuncd\nname: x\n",
		})
		_, err := types(t, dir)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "a Function document that does not decode fails types: %v", err)
		require.Contains(t, err.Error(), filepath.Join(dir, "counter.yaml"))
	})
}

// funcdctl types -o defaults to a python Function's source entry directory (ADR-0247 Decision 3): its
// [tool.funcd-bundle.functions] entry, else src when the folder is named after it, else the folder.
func TestTypesDefaultOutDir(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		folder string
		files  map[string]string
		want   string
	}{
		"pyproject": {folder: "proj", files: map[string]string{"pyproject.toml": "[tool.funcd-bundle.functions]\ncounter = \"src/counter/main.py\"\n", "src/counter/main.py": ""}, want: "src/counter"},
		"src-main":  {folder: "counter", files: map[string]string{"src/main.py": ""}, want: "src"},
		"flat":      {folder: "proj", files: map[string]string{"counter.py": ""}, want: "."},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{tc.folder + "/counter.contract.yaml": counterContract}
			for f, body := range tc.files {
				files[tc.folder+"/"+f] = body
			}
			dir := filepath.Join(writeTree(t, files), tc.folder)
			require.NoError(t, execCLI(&bytes.Buffer{}, nil, "types", "-f", filepath.Join(dir, "counter.contract.yaml")))
			require.FileExists(t, filepath.Join(dir, filepath.FromSlash(tc.want), "funcd_types.py"))
		})
	}
}

// Issue #318: `funcdctl types` runs the same profile gate as `funcdctl push`, so an out-of-profile side fails
// fault.Invalid and no types file is written.
func TestIssue318_TypesRejectsOutOfProfileContract(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"anyOf input":          "runtime: nodejs22\ninput:\n  anyOf:\n    - type: string\n    - type: integer\noutput:\n  type: \"null\"\n",
		"unquoted null output": "runtime: nodejs22\ninput:\n  type: \"null\"\noutput:\n  type: null\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := writeTree(t, map[string]string{"counter.contract.yaml": body})
			outDir := t.TempDir()
			err := execCLI(&bytes.Buffer{}, nil, "types", "-f", filepath.Join(dir, "counter.contract.yaml"), "-o", outDir)
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.Contains(t, err.Error(), "outside the funcd profile")
			entries, rerr := os.ReadDir(outDir)
			require.NoError(t, rerr)
			require.Empty(t, entries, "no types file is written for a rejected contract")
		})
	}
}
