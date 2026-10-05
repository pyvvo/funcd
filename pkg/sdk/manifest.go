package sdk

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"

	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// VoidSchema is the canonical void side (ADR-0090/0122): a contract side that carries no
// meaningful payload is the explicit JSON Schema {"type":"null"}, never an omission. Kept in
// pkg/sdk (not imported from internal/artifact) so funcdctl and the SDK share it without a
// package-boundary violation; the constant value matches internal/artifact.VoidSchema.
const VoidSchema = `{"type":"null"}`

// Manifest is the parsed funcdctl.yaml (ADR-0122): a colocated, per-function CLIENT push/dev config
// (the client-side analogue of wrangler.toml), NOT a deploy manifest. It carries only what the client
// tooling needs to push, run, and type a function — the runtime, the handler, the bindings, and the
// inline I/O contract. Deploy concerns (name, namespace, scaling, placement) live on the hand-written
// Function CRD, never here. funcdctl reads it to `push` (bake the schema-only contract + record the
// runtime annotation), `types` (contract → .pyi/.d.ts + a typed binding context), and — later — `dev`.
type Manifest struct {
	// Runtime is the runtime class (e.g. python314, nodejs22) recorded as the push runtime annotation.
	Runtime v1.RuntimeName `json:"runtime"`
	// Handler is the entrypoint the shim resolves.
	Handler string `json:"handler"`
	// Main is the handler SOURCE FILE, relative to the manifest dir — the wrangler `main` convention
	// (wrangler.toml `main = "src/entry.py"`). Empty ⇒ the default entry co-located with the manifest
	// (handler.py for python*, handler.mjs otherwise, or `<stem>.py`/`<stem>.mjs` for a stem manifest).
	// It lets a `<stem>.funcdctl.yaml` point at a NESTED handler (e.g. functions/extract/handler.py),
	// so an existing per-function layout runs under `funcdctl dev` without flattening.
	Main string `json:"main,omitempty"`
	// Bindings holds blob/kv/catalogs/links/config/secrets — needed to run the function and to type its
	// binding context (GenerateTypes). It is not a deploy field; it describes the capabilities the code uses.
	Bindings Bindings `json:"bindings,omitempty"`
	// Contract is the inline {input, output} JSON Schema; both sides always present (a void side is VoidSchema).
	Contract Contract `json:"contract"`
	// Dev is the funcdctl-dev-only block (ADR-0125): auto-provisioned/globally-overridable backends,
	// inline ConfigMap values, and env-sourced Secret values. It is ADDITIVE and dev-only — `funcdctl
	// push` and `funcdctl types` ignore it entirely (they read only the four fields above), so a
	// funcdctl.yaml carrying a `dev:` block pushes and types identically to one without it.
	Dev Dev `json:"dev,omitempty"`
}

// Dev is the funcdctl-dev-only manifest block (ADR-0125). `funcdctl dev` reads it to auto-provision a
// function's backing resources locally; `funcdctl push`/`types` ignore it. It is never a deploy field.
type Dev struct {
	// Python is the interpreter `funcdctl dev` launches a python* handler with — a path relative to the
	// manifest dir (e.g. `.venv/bin/python`) or absolute. Lets a project pin its virtualenv (which carries
	// the handler's deps, ADR-0125's local-deps boundary) in the committable manifest instead of the
	// FUNCD_PYTHON env. Precedence: FUNCD_PYTHON env > this > `python3` on PATH. GLOBAL per run (the first
	// function's block is representative, like Backends).
	Python string `json:"python,omitempty"`
	// Node is the interpreter for a nodejs* handler (path relative to the manifest dir, or absolute).
	// Precedence: FUNCD_NODE env > this > `node` on PATH.
	Node string `json:"node,omitempty"`
	// Catalog declares the PROVIDER side of a `catalogs:` binding so `funcdctl dev` can stand the
	// CatalogService up locally, keyed by catalog name (matching a `bindings.catalogs[].catalog`). The
	// consumer binding names WHICH catalog; this block gives dev the storage layout it cannot infer (which
	// buckets/prefixes the DuckLake reads + owns) — the provider analogue of dev.backends. GLOBAL per run
	// (first function that declares a given catalog wins). Empty ⇒ no catalog auto-provisioned.
	Catalog map[string]DevCatalog `json:"catalog,omitempty"`
	// Backends overrides the auto-provisioned backend per kind, GLOBALLY (not per binding). Empty ⇒ the
	// built-in default (memory).
	Backends Backends `json:"backends,omitempty"`
	// Config carries inline ConfigMap values, keyed by ConfigMap name → {key: value}. Non-sensitive and
	// committable (a plain env inject in dev — no ConfigMap CRD on disk).
	Config map[string]map[string]string `json:"config,omitempty"`
	// Secrets carries Secret values keyed by Secret name → key → "${ENV_VAR}". The value is NEVER read
	// from the file — `funcdctl dev` resolves each ${ENV_VAR} from the process environment (a missing var
	// fails fast), so funcdctl.yaml stays committable.
	Secrets map[string]map[string]string `json:"secrets,omitempty"`
}

// DevCatalog is the funcdctl-dev provider declaration for one catalog (the storage layout dev cannot infer
// from a consumer binding). Blob is the catalog engine's bucket bindings (read + own — the SAME shape as
// Function.spec.blob); Catalog is the (bucket, prefix) the DuckLake catalog syncs under (owned by the
// catalog). funcdctl dev synthesizes a CatalogService (name = the map key) + a Quack-token Secret from it.
type DevCatalog struct {
	Blob    []v1.FunctionBlob `json:"blob,omitempty"`
	Catalog v1.CatalogRef     `json:"catalog"`
}

// Backends selects the driver `funcdctl dev` auto-provisions for each backend kind (ADR-0125 Decision 4),
// GLOBAL per kind. The built-in default is memory (ephemeral); a `file://…` value selects a local-durable
// backend under `--persist`.
type Backends struct {
	// KV overrides the KVStore backend for ALL kv bindings ("memory" | a dsn/path override).
	KV string `json:"kv,omitempty"`
	// Blob overrides the Bucket backend for ALL blob bindings ("memory" | "file://…").
	Blob string `json:"blob,omitempty"`
	// Catalog overrides the CatalogService backend for ALL catalog bindings ("file://…", a local DuckLake).
	Catalog string `json:"catalog,omitempty"`
}

// Bindings reuses the v1alpha1 binding types 1:1 (ADR-0122 field map). The YAML shapes are those types'
// json shapes (alias/store/table, alias/bucket/prefix, …) — the capabilities the code uses at runtime,
// and the source GenerateTypes reads to emit the typed binding context.
type Bindings struct {
	Blob     []v1.FunctionBlob    `json:"blob,omitempty"`
	KV       []v1.FunctionKV      `json:"kv,omitempty"`
	Catalogs []v1.FunctionCatalog `json:"catalogs,omitempty"`
	Links    []v1.FunctionLink    `json:"links,omitempty"`
	Config   []v1.ObjectName      `json:"config,omitempty"`
	Secrets  []v1.ObjectName      `json:"secrets,omitempty"`
}

// Contract is the manifest's inline I/O JSON Schema (ADR-0090/0122): both sides always present, a void
// side spelled VoidSchema. The two raw messages are gated (contract.Check) and baked schema-only.
type Contract struct {
	Input  json.RawMessage `json:"input"`
	Output json.RawMessage `json:"output"`
}

// LoadManifest reads and parses a funcdctl.yaml at path, then structurally validates it. It does NOT
// gate the contract against the funcd profile (that is the CLI layer's contract.Check, which lives in
// internal/ and must not be imported here) — only shape rules a manifest cannot be useful without.
func LoadManifest(path string) (*Manifest, error) {
	const op = "sdk.LoadManifest"
	data, err := os.ReadFile(path) //nolint:gosec // path is a user-supplied CLI argument
	if err != nil {
		return nil, fault.Invalidf(op, "read manifest %q: %v", path, err)
	}
	return parseManifest(op, path, data)
}

// parseManifest decodes funcdctl.yaml bytes into a Manifest and structurally validates it. sigs.k8s.io/yaml
// accepts YAML and JSON (JSON is valid YAML) and round-trips through the json tags, so json.RawMessage
// contract sides receive their compact JSON bytes. Like DecodeManifests, keys and values keep their YAML 1.2 text
// (quoteStrings, #63), a number-like scalar bound for a string field keeps its text (quoteTextScalars, #416, #699)
// and an unknown key is rejected, not dropped (#64).
func parseManifest(op, path string, data []byte) (*Manifest, error) {
	var doc yamlv3.Node
	if err := yamlv3.Unmarshal(data, &doc); err != nil {
		return nil, fault.Invalidf(op, "parse manifest %q: %v", path, err)
	}
	if len(doc.Content) > 0 {
		quoteStrings(&doc)
		quoteTextScalars(&doc, reflect.TypeOf(Manifest{}))
		raw, err := yamlv3.Marshal(&doc)
		if err != nil {
			return nil, fault.Invalidf(op, "re-encode manifest %q: %v", path, err)
		}
		data = raw
	}
	var m Manifest
	if err := yaml.UnmarshalStrict(data, &m); err != nil {
		return nil, fault.Invalidf(op, "parse manifest %q: %v", path, err)
	}
	if err := m.structuralValidate(op, path); err != nil {
		return nil, err
	}
	return &m, nil
}

// structuralValidate enforces the manifest shape rules a push/dev config cannot be useful without: a
// runtime, a handler, and both contract sides present. There is no name/namespace — those are deploy
// concerns on the Function CRD, not this client config. The funcd-profile gate is the CLI's contract.Check.
func (m *Manifest) structuralValidate(op, path string) error {
	if m.Runtime == "" {
		return fault.Invalidf(op, "manifest %q is missing runtime", path)
	}
	if m.Handler == "" {
		return fault.Invalidf(op, "manifest %q is missing handler", path)
	}
	if len(bytes.TrimSpace(m.Contract.Input)) == 0 {
		return fault.Invalidf(op, "manifest %q contract is missing the input side (a void side is %s)", path, VoidSchema)
	}
	if len(bytes.TrimSpace(m.Contract.Output)) == 0 {
		return fault.Invalidf(op, "manifest %q contract is missing the output side (a void side is %s)", path, VoidSchema)
	}
	return nil
}

// ContractSides returns the two JSON Schema sides for the push-time gate + bake (contract.Check +
// artifact.ContractBlob). Both are mandatory (ADR-0090); a void side is VoidSchema, never nil.
func (m *Manifest) ContractSides() (input, output []byte, err error) {
	const op = "sdk.Manifest.ContractSides"
	if len(bytes.TrimSpace(m.Contract.Input)) == 0 {
		return nil, nil, fault.Invalidf(op, "manifest contract is missing the input side (a void side is %s)", VoidSchema)
	}
	if len(bytes.TrimSpace(m.Contract.Output)) == 0 {
		return nil, nil, fault.Invalidf(op, "manifest contract is missing the output side (a void side is %s)", VoidSchema)
	}
	return m.Contract.Input, m.Contract.Output, nil
}
