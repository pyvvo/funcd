# Fix review — issue #498 (`funcdctl types` fails on a nullable contract field) — claude-opus-5-5

- **Issue**: pyvvo/funcd#498 — `funcdctl types` rejects the ADR-0058 nullable form `type: [T, "null"]` that `contract.Check` (and so `funcdctl push`) accepts.
- **Change**: branch `fix/i498`, commit `605eca0` `fix(sdk): generate types for a nullable contract field that the profile gate accepts` (`git diff origin/main...HEAD`: `internal/contract/contract.go`, `internal/contract/schema.go`, `pkg/sdk/types_gen.go`, `pkg/sdk/manifest_ext_test.go`).
- **Producing model**: claude-opus-5-5
- **Governing ADRs**: ADR-0058 (contract profile; nullable row maps `{"type":[…,"null"]}` to `| null` / `Optional[T]`), ADR-0122 Decision 4 (`funcdctl types`), ADR-0002 (conventions).
- **Verdict**: **pass** — 0 Blocker, 0 Major, 1 Minor. Checklist 11/11.

## Verification run

| Check | Command (through `scripts/agent/d`) | Result |
|---|---|---|
| Fails without the fix | `git revert --no-commit 605eca0`, keep the branch's test file, `go test ./pkg/sdk/ -run TestIssue498` | FAIL with the issue's exact error: `sdk.GenerateTypes: input contract is not a usable JSON Schema: json: cannot unmarshal array into Go struct field typeSchema.properties.type of type string` |
| Passes with the fix | `git reset --hard 605eca0`, `go test -race -count=1 ./pkg/sdk/ ./internal/contract/ ./cmd/funcdctl/` | ok ×3; worktree clean at `605eca0` |
| User-visible behavior | built `funcdctl`, ran `funcdctl types -f funcdctl.yaml` on a python314 manifest with `note: type: [string, "null"]` (required) and a `"null"` output | `wrote funcd_types.py`; it contains `note: str \| None` and `FuncOutput = None` |
| Mutant 1 | `pyType`: drop the `" \| None"` suffix | killed (`TestIssue498_…` FAIL) |
| Mutant 2 | `TypeField.Nullable`: always false | killed (`TestIssue498_…` FAIL) |
| Mutant 3 | `tsBaseType`: drop the parentheses around a nullable item type | killed (`TestIssue498_…` FAIL) |
| vet | `go vet ./pkg/sdk/ ./internal/contract/` | clean |
| lint (host) | `go tool golangci-lint run ./pkg/sdk/... ./internal/contract/...` | 0 issues |

Linux lint, the repo-wide test set and e2e are left to the group gate, as this review's scope instructs.

## Findings

### Blocker

None.

### Major

None.

### Minor

1. **A nullable record side loses its field types** (`pkg/sdk/types_gen.go`, `isRecord`) — attribution: `model`.
   A side `{"type":["object","null"],"properties":{…}}` renders as `FuncOutput = dict[str, object] | None` /
   `export type FuncOutput = Record<string, unknown> | null`, so the declared properties are dropped. ADR-0058's
   nullable row reads as `T | null` with `T` the record. This is a sound fail-safe for a rare shape (the commit
   message states it, and the test pins it), and the issue's own repro is a nullable *field*, which is typed fully.
   A follow-up could emit the record under a helper name and alias `FuncOutput = _FuncOutputRecord | None`. Not
   blocking.

## Verified correct

- **Cause, not symptom.** The generator's `typeSchema.Type` was a plain `string`; it now decodes `type` with the gate's
  own `contract.TypeField` (exported, plus `Primary()` and `Nullable()`), so the generator reads every contract the
  gate accepts by construction — the push/types divergence the issue names is closed at its source, not patched per shape.
- **Reuse, no duplication.** No second string-or-list decoder was written; the existing `typeField` and
  `primaryType` logic in `internal/contract/schema.go` was promoted and reused, and the gate's two call sites
  (`walk`, `checkFormat`) moved to `s.Type.Primary()` with identical behavior (the `internal/contract` tests pass).
  `pkg/sdk` already imports `internal/*` packages (`internal/eventing/deadletter`, `internal/funclog/logread`), and no
  depguard rule forbids it (lint clean).
- **Mapping matches ADR-0058.** `T | None` (Python, valid at runtime on python314 even in the module-level alias) and
  `T | null` (TypeScript); TypeScript parenthesizes a nullable array item (`(number | null)[] | null`), which is required
  for correct precedence. Optionality (`?` / `# optional`) stays orthogonal to nullability.
- **Test quality.** `TestIssue498_GenerateTypesMapsNullableTypeList` first asserts `contract.Check` accepts both sides
  (pins the push/types agreement), then covers a nullable field, a nullable array of nullable items and a nullable
  whole side on both runtimes; all three mutants on the key lines fail it.
- **Scope.** Every hunk serves the issue; no test weakened or deleted; no ADR file touched.
- **Conventions.** Top-level imports, `api/fault` error path unchanged, comments state the why (ADR-0058 nullable
  form) without narration, surrounding naming kept (`pyType`/`pyBaseType`, `tsType`/`tsBaseType`).
- **Shape.** `fix(sdk):` subject, `Fixes #498`, the attribution trailer, one issue in one commit.

## Recommendation

Pass. Merge with the group; optionally file the nullable-record-side typing (Minor 1) as a small DX follow-up.
