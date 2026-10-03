# Fix review — issue #416 (claude-opus-5-5)

- **Issue**: #416 — `funcdctl apply` rewrites number-like text in string fields (1.10 becomes 1.1).
- **Change**: branch `fix/i416`, commit `83558b0` `fix(sdk): keep number-like text in string fields of a decoded manifest`.
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Counts**: 0 Blocker · 0 Major · 1 Minor (1 model-attributed)
- **Fix checklist**: 10 / 11

## Summary

After `decodeDocument` learns the kind, it walks the yaml.v3 node tree beside the typed object
(`quoteTextScalars`, json tags, inline structs promoted, case-insensitive field match like
encoding/json) and double-quotes a plain `!!int`/`!!float`/`!!bool` scalar when it lands in a
string-kind target or is a mapping key. It then re-encodes and runs the unchanged strict decode
(ADR-0108). A value bound for a number, bool, `json.RawMessage` or `any` field keeps its parsed type.
This takes the issue's first expected outcome: the decode keeps the text the user wrote.

## Verification run

| Check | Result |
|---|---|
| `git revert --no-commit 83558b0`, test file kept at HEAD | **FAIL**: `spec.data` decodes to `VERSION:1.1 MODE:493 HEX:31 EXP:1000 FLAG:true` — the issue's reason |
| Reset to `83558b0`, `-race` | `TestIssue416_NumberLikeTextInStringFieldKeepsText` PASS; `./pkg/sdk/` ok under `-race` |
| Probe: inline-promoted field (`metadata.ownerReferences[0].name: 1.10`, `uid: 0755`) | kept as `1.10` / `0755` |
| Probe: case-folded keys (`Spec:` / `Data:` / `A: 1.10`) | kept as `1.10` |
| Mutant 1: drop the mapping-key quoting | FAIL (tags key `0755` becomes `493`) |
| Mutant 2: drop `!!float` from the quoted tags | FAIL (`1.10` becomes `1.1`) |
| Mutant 3: `jsonFieldType` ignores the case-insensitive match | **survives** |
| Mutant 4: `jsonFields` drops inline-struct promotion | **survives** |
| `go vet ./pkg/sdk/` | clean |
| `golangci-lint ./pkg/sdk/` | 0 issues |
| Worktree after the checks | at `83558b0`, clean |

Not run here by design: repo-wide tests, the e2e suite, Linux lint, Lima lanes (the group gate runs them).

## Findings

### Blocker
None.

### Major
None.

### Minor

1. **The regression test does not pin the inline-promotion or case-fold paths** (`model`). Mutants 3
   and 4 survive: `TestIssue416_…` never reaches a field through a `json:",inline"` embedded struct
   (`OwnerReference.ObjectRef`, `TypeMeta`, the `Status` inlines in WorkflowRun/Sensor/Service/Gateway)
   or through a key whose case differs from the json tag. The probes show both paths work today, but a
   later edit could break them silently. Evidence: mutant runs above; `pkg/sdk/sdk.go` `jsonFields` /
   `jsonFieldType`. Fix: add an `ownerReferences[].name: 1.10` case to the test.

## Verified correct (keep)

- **Cause, not symptom.** The issue says a fix needs the target type; the change uses it. It does not
  quote every number-like scalar (which would break int fields — the test checks `ports: [0x1BB]` still
  decodes to `443`) and does not swallow the decode.
- **Strict decode intact.** `yaml.UnmarshalStrict` still runs on the re-encoded document, so the ADR-0108
  unknown-key rejection is unchanged; the existing `#299` `quoteStrings` pass is untouched.
- **Scope.** Two files: `pkg/sdk/sdk.go` and its test. The re-encode moved into `decodeDocument` (its only
  caller is `DecodeManifestDocuments`); no test weakened.
- **Reuse.** No existing json-tag field walker to reuse: `internal/controlplane/inline_schema.go` flattens
  huma schemas (controlplane-internal, not importable from `pkg/sdk`), and `internal/platform/config`
  only maps a tag to a name for the validator. Standard library only (`reflect`, `slices`).
- **Conventions.** `api/fault` errors, top-level imports, block-style YAML in the test, a doc comment
  per helper that states the why (issue #416), no `any` in signatures.
- **Shape.** `fix(sdk):` subject, cause/fix/test body, `Fixes #416`, attribution trailer, one commit.

## Recommendation

Pass. The Minor is a test-gap follow-up; it may be folded into this PR or left as is.
