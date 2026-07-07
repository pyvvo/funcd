# ADR-0090 Implementation Review — Mandatory single I/O schema (void = `{"type":"null"}`)

**Verdict**: **pass** — the implementation conforms to the ADR's Decision + Contracts, every non-deferred
scenario has a named passing test, and the four sub-checks are green (verified independently, not eyeballed).
**Producing model**: claude-opus-4-8 · **Reviewed against**: ADR-0090 Contracts/Scenarios/DoD · ADR-0058/0059/0060 · ADR-0002.

## Verification (run, with real exit codes)
- `go build ./...` → **0** · `go mod verify` → all modules verified · `go tool golangci-lint run` (artifact+funcdctl) → **0 issues** · `go test ./internal/artifact/... ./cmd/funcdctl/... ./tests/e2e/...` → **ok**.
- Python shim: `ruff` clean · `mypy` clean (17 files) · `pytest` → 48 passed.
- Full `go test ./...` → one failure only: `TestPythonPoolSmoke` (internal/testkit/bench). **Confirmed pre-existing** (env `env`-attributed): it touches no contract code (grep clean), fails on `py shim … did not become ready` (a Python-3.14 subinterpreter density smoke), and fails **identically on the clean tree** with the core changes stashed. Not a regression.

## Conformance (verified against the code)
- **`ContractBlob` mandatory** (`internal/artifact/artifact.go`): both `input`/`output` required → `fault.Invalid` else; `omitempty` dropped (both always serialized); `VoidSchema = {"type":"null"}` exported. ✅ matches Contracts.
- **Single `--schema` CLI** (`cmd/funcdctl/cli.go`): `--contract-input`/`--contract-output` removed; new `gateSchema` parses one `{input,output}` doc, requires both keys, runs `contract.Check` per side, mandatory (empty → `fault.Invalid`). ✅
- **`build.py` void** (`shim/python/src/funcd_shim/build.py`): `_VOID_VALIDATOR` **removed**; `_side_schema` raises on an undeclared side (unchecked path gone), maps `None → {"type":"null"}`, derives a declared type; void validator now **compiled from its schema** via the same fastjsonschema path — ADR-0060's validator≡schema invariant holds uniformly. `BuildResult` schemas non-optional. ✅ (the key strength: no place where advertised ≠ enforced).
- **Wire preserved**: no change to the shim's 204/422/500 mapping — the ADR-0058 wire is untouched. ✅
- **Blast radius handled honestly**: every broken test (`cmd/funcdctl`, `internal/artifact`, `tests/e2e/journey`) updated to *declare a contract* (void = `VoidSchema`), never weakened/skipped. `Push(...,nil)` at the OCI layer intact (the mandatory gate lives at the authoring surface `ContractBlob`/`gateSchema`, correct altitude).

## Scenario → test
`contract-mandatory-push-gate`, `both-keys-required`, `void-side-serialized` (artifact) · `single-schema-surface` (funcdctl) · `void-output-schema-explicit`, `void-input-schema-explicit`, `undeclared-io-is-error` (python) — all named + passing.

## Deferred (recorded, legitimate — inherently e2e)
- **Node void-schema emission** (`shim/nodejs`): the Node path derives schemas from TS types (no `= None` author marker) and its assertion is the node e2e lane. The Go/Python primary paths fully enforce mandatory + void. `env`/e2e-attributed, not a `model` gap.
- **`bundle-schema-satisfies-gate` + exhaustive `examples/**` migration + containerd/venom lanes**: `bundle-schema-satisfies-gate` belongs to ADR-0089 (no `bundle.go` yet). Unit-exercised examples already declare both sides. e2e-attributed.

## Findings
- 🔴 Blocker: **None.**
- 🟡 Major: **None.**
- Minor: the Node lane + exhaustive example migration land with ADR-0089 / the e2e lanes (tracked, not a defect here).

## ✅ Verified correct — keep it
The void-side **schema-vs-wire split** (advertise `{"type":"null"}`, still reply 204) with the validator
**compiled from the schema** (not hand-baked) is the crux and is implemented exactly as decided — a later edit
must not reintroduce a hand-baked void validator. The mandatory gate sits at the authoring surface, leaving the
low-level OCI packer's nil-contract path intact for the OCI-mechanism tests.
