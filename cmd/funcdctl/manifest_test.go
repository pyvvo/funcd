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

// pyFuncdctlYAML is a funcdctl.yaml (ADR-0122): a client push/dev config with an in-profile inline
// contract + a KV binding. Block-style YAML (no flow-style `{}` / `[]`), matching the examples.
const pyFuncdctlYAML = `
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

// writeFuncdctl writes a funcdctl.yaml into dir.
func writeFuncdctl(t *testing.T, dir, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "funcdctl.yaml"), []byte(body), 0o600))
}

// scenario: push-from-manifest (ADR-0122) — a colocated funcdctl.yaml is the PRIMARY contract source:
// `funcdctl push` gates each side (contract.Check), bakes the {dialect,input,output} blob, records the
// runtime annotation, and prints ref@digest — with NO --schema and NO language toolchain. `inspect`
// reads the baked schema-only contract straight back.
func TestScenario_push_from_manifest(t *testing.T) {
	dir := t.TempDir()
	handler := filepath.Join(dir, "handler.py")
	require.NoError(t, os.WriteFile(handler, []byte("def handle(event):\n    return event\n"), 0o600))
	writeFuncdctl(t, dir, pyFuncdctlYAML)
	ref := "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":v1"

	// No --schema flag: the contract comes from funcdctl.yaml.
	var out bytes.Buffer
	require.NoError(t, execCLI(&out, nil, "push", handler, ref))
	printed := strings.TrimSpace(out.String())
	require.True(t, strings.HasPrefix(printed, ref+"@sha256:"), "push prints <ref>@<digest>, got %q", printed)
	digest := strings.TrimPrefix(printed, ref+"@")

	// inspect reads the baked contract (schema-only) from OCI metadata — no bundle pull, no run.
	out.Reset()
	require.NoError(t, execCLI(&out, nil, "inspect", ref+"@"+digest))
	require.Contains(t, out.String(), `"name"`, "the input schema baked from funcdctl.yaml")
	require.Contains(t, out.String(), `"count"`, "the output schema baked from funcdctl.yaml")
	require.Contains(t, out.String(), "2020-12", "the JSON Schema dialect")
}

// scenario: out-of-profile-rejected (ADR-0122) — a funcdctl.yaml whose contract.input uses `anyOf`
// fails the funcd profile gate (contract.Check) → fault.Invalid, and nothing ships.
func TestScenario_out_of_profile_rejected(t *testing.T) {
	dir := t.TempDir()
	handler := filepath.Join(dir, "handler.py")
	require.NoError(t, os.WriteFile(handler, []byte("def handle(event):\n    return event\n"), 0o600))
	writeFuncdctl(t, dir, `
runtime: python314
handler: handle
contract:
  input:
    anyOf:
      - type: object
        additionalProperties: false
        properties:
          a:
            type: string
      - type: object
        additionalProperties: false
        properties:
          b:
            type: integer
  output:
    type: "null"
`)
	ref := "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":v1"

	var out bytes.Buffer
	err := execCLI(&out, nil, "push", handler, ref)
	require.Error(t, err, "an anyOf contract side is rejected at push")
	require.Equal(t, fault.Invalid, fault.KindOf(err), "out-of-profile → fault.Invalid")
	require.Contains(t, err.Error(), "profile", "the error names the profile violation")
}

// miniManifest is a minimal, structurally-valid funcdctl.yaml whose handler name is a marker so a test
// can assert which manifest resolveManifest picked. Block-style YAML (no flow `{}` / `[]`).
func miniManifest(handler string) string {
	return "runtime: nodejs22\nhandler: " + handler + "\ncontract:\n  input:\n    type: \"null\"\n  output:\n    type: \"null\"\n"
}

// scenario: per-function-manifest-resolved (ADR-0124) — for a single-file push, resolveManifest picks
// the <stem>.funcdctl.yaml matched by the file's basename, winning over BOTH the generic funcdctl.yaml
// AND a sibling function's <other>.funcdctl.yaml in the same directory.
func TestResolveManifest_per_function_manifest_resolved(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "front.funcdctl.yaml"), []byte(miniManifest("frontHandle")), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "greeter.funcdctl.yaml"), []byte(miniManifest("greeterHandle")), 0o600))
	writeFuncdctl(t, dir, miniManifest("genericHandle"))

	m, path, err := resolveManifest(filepath.Join(dir, "front.mjs"))
	require.NoError(t, err)
	require.NotNil(t, m)
	require.Equal(t, filepath.Join(dir, "front.funcdctl.yaml"), path, "the front stem manifest is resolved")
	require.Equal(t, "frontHandle", m.Handler, "front's manifest wins over greeter's and the generic")
}

// scenario: generic-fallback (ADR-0122 behavior preserved) — with only a generic funcdctl.yaml present
// (no <stem>.funcdctl.yaml), a single-file push falls back to funcdctl.yaml.
func TestResolveManifest_generic_fallback(t *testing.T) {
	dir := t.TempDir()
	writeFuncdctl(t, dir, miniManifest("genericHandle"))

	m, path, err := resolveManifest(filepath.Join(dir, "handler.py"))
	require.NoError(t, err)
	require.NotNil(t, m)
	require.Equal(t, filepath.Join(dir, "funcdctl.yaml"), path, "falls back to the generic manifest")
	require.Equal(t, "genericHandle", m.Handler)
}

// scenario: stem-vs-generic precedence — when both <stem>.funcdctl.yaml and the generic funcdctl.yaml
// exist, the stem manifest is chosen; and a stem with no matching file still falls back to the generic.
func TestResolveManifest_stem_precedence(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "front.funcdctl.yaml"), []byte(miniManifest("frontHandle")), 0o600))
	writeFuncdctl(t, dir, miniManifest("genericHandle"))

	// A file whose stem has a manifest → stem wins.
	m, _, err := resolveManifest(filepath.Join(dir, "front.mjs"))
	require.NoError(t, err)
	require.Equal(t, "frontHandle", m.Handler)

	// A file whose stem has no manifest → generic fallback.
	m, _, err = resolveManifest(filepath.Join(dir, "other.mjs"))
	require.NoError(t, err)
	require.Equal(t, "genericHandle", m.Handler)
}

// Issue #1049: without --entry, a bundle push governed by a funcdctl.yaml takes its entry from the manifest — its
// main, else handler.py for a python* runtime and handler.mjs otherwise — and an explicit --entry still wins. A
// bundle without a funcdctl.yaml keeps the ADR-0089 default. The pulled bundle resolves its handler from the pushed
// entry annotation.
func TestIssue1049_BundlePushTakesEntryFromManifest(t *testing.T) {
	cases := map[string]struct {
		runtime, protocol, main string // no runtime: no funcdctl.yaml, the bundle carries its own contract
		files, flags            []string
		want                    string
	}{
		"node runtime default":   {runtime: "nodejs22", files: []string{"handler.mjs"}, want: "handler.mjs"},
		"http function":          {runtime: "nodejs22", protocol: "http", files: []string{"handler.mjs"}, want: "handler.mjs"},
		"python runtime default": {runtime: "python314", files: []string{"handler.py"}, want: "handler.py"},
		"main":                   {runtime: "python314", main: "extract.py", files: []string{"extract.py"}, want: "extract.py"},
		"nested main":            {runtime: "nodejs22", main: "src/front.mjs", files: []string{"src/front.mjs"}, want: "src/front.mjs"},
		"explicit entry wins":    {runtime: "nodejs22", main: "front.mjs", files: []string{"front.mjs", "other.mjs"}, flags: []string{"--entry", "other.mjs"}, want: "other.mjs"},
		"no manifest":            {files: []string{"handler.py"}, want: "handler.py"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				p := filepath.Join(dir, filepath.FromSlash(f))
				require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
				require.NoError(t, os.WriteFile(p, []byte("// handler\n"), 0o600))
			}
			if tc.runtime == "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, bundleContractName), []byte(`{"input":{"type":"null"},"output":{"type":"null"}}`), 0o600))
			} else {
				manifest := "runtime: " + tc.runtime + "\nhandler: handle\n"
				if tc.main != "" {
					manifest += "main: " + tc.main + "\n"
				}
				if tc.protocol != "" {
					manifest += "protocol: " + tc.protocol + "\n"
				} else {
					manifest += "contract:\n  input:\n    type: \"null\"\n  output:\n    type: \"null\"\n"
				}
				writeFuncdctl(t, dir, manifest)
			}
			ref := "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":v1"

			var out bytes.Buffer
			require.NoError(t, execCLI(&out, nil, append([]string{"push", dir, ref}, tc.flags...)...))
			digest := strings.TrimPrefix(strings.TrimSpace(out.String()), ref+"@")

			pulled := t.TempDir()
			out.Reset()
			require.NoError(t, execCLI(&out, nil, "pull", ref, digest, pulled))
			require.Equal(t, filepath.Join(pulled, filepath.FromSlash(tc.want)), strings.TrimSpace(out.String()))
		})
	}
}

// scenario: types via the CLI — `funcdctl types -f funcdctl.yaml -o <dir>` writes a .py module.
func TestScenario_types_command(t *testing.T) {
	dir := t.TempDir()
	writeFuncdctl(t, dir, pyFuncdctlYAML)
	outDir := t.TempDir()

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, nil, "types", "-f", filepath.Join(dir, "funcdctl.yaml"), "-o", outDir))
	body, err := os.ReadFile(filepath.Join(outDir, "funcd_types.py")) //nolint:gosec // test-owned path
	require.NoError(t, err)
	require.Contains(t, string(body), "class FuncInput(TypedDict)")
	require.Contains(t, string(body), "class FuncOutput(TypedDict)")
}

// Issue #318: `funcdctl types` runs the same profile gate as `funcdctl push`, so an out-of-profile side
// fails fault.Invalid and no types file is written.
func TestIssue318_TypesRejectsOutOfProfileContract(t *testing.T) {
	cases := map[string]string{
		"anyOf input": `
runtime: nodejs22
handler: handle
contract:
  input:
    anyOf:
      - type: string
      - type: integer
  output:
    type: "null"
`,
		"unquoted null output": `
runtime: nodejs22
handler: handle
contract:
  input:
    type: "null"
  output:
    type: null
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFuncdctl(t, dir, body)
			outDir := t.TempDir()

			var out bytes.Buffer
			err := execCLI(&out, nil, "types", "-f", filepath.Join(dir, "funcdctl.yaml"), "-o", outDir)
			require.Error(t, err, "an out-of-profile contract side is rejected by types")
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.Contains(t, err.Error(), "outside the funcd profile")
			entries, rerr := os.ReadDir(outDir)
			require.NoError(t, rerr)
			require.Empty(t, entries, "no types file is written for a rejected contract")
		})
	}
}

// scenario: http-validate — `funcdctl push` of an http Function bakes no contract, from a single file and from a
// bundle directory, where it removes the contract file an earlier push left (ADR-0243 Decisions 2 and 7).
func TestScenarioPushHTTPFunction(t *testing.T) {
	const manifest = "runtime: nodejs22\nhandler: handle\nprotocol: http\n"
	layout := filepath.Join(t.TempDir(), "layout")
	file := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(file, "api.mjs"), []byte("export const handle = {};\n"), 0o600))
	writeFuncdctl(t, file, manifest)
	bundle := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "index.mjs"), []byte("export const handle = {};\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(bundle, bundleContractName), []byte(`{"input":{},"output":{}}`), 0o600))
	writeFuncdctl(t, bundle, manifest)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, nil, "push", filepath.Join(file, "api.mjs"), "oci-layout://"+layout+":file"))
	require.NoError(t, execCLI(&out, nil, "push", bundle, "oci-layout://"+layout+":bundle", "--entry", "index.mjs"))
	require.NoFileExists(t, filepath.Join(bundle, bundleContractName), "the stale contract is removed")
	for _, tag := range []string{"file", "bundle"} {
		_, _, err := artifact.InspectContract(context.Background(), "oci-layout://"+layout+":"+tag, "")
		require.Equal(t, fault.NotFound, fault.KindOf(err), "%s carries no contract: %v", tag, err)
	}
}
