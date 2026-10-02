## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #65 fix, model: claude-opus-5-5)

Change: branch `fix/i65`, one commit 2a41bdc `fix(contract): reject a JSON null "type" value instead of treating it as any`.
Files: `internal/contract/schema.go`, `internal/contract/contract_test.go`.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **`funcdctl types` still renders an unquoted `type: null` as the any form** · attribution: `issue` · evidence:
  `cmd/funcdctl/manifest.go` `typesCmd` calls `sdk.LoadManifest` and `sdk.GenerateTypes` and never calls
  `contract.Check`, so the DX-only generator keeps emitting `object` / `unknown` for `{"type":null}`. The
  issue's title and expected behavior target the push gate, which is fixed; the types command is
  ungated for every profile violation by design (DX-only, ADR-0122), so gating it is a separate change.
  Fix: if wanted, file a follow-up to gate `funcdctl types` with `contract.Check`; not required for this issue.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: reverting the `schema.go` hunk (keeping the new test) gives
  `FAIL` on all three subtests, e.g. `contract {"type":null} must be rejected, got nil`, and the same for
  `{"type":["string",null]}` and the nested `properties.x.type: null`.
- **Passes with the fix**: `go test -race -count=1 ./internal/contract/` → `ok`; the worktree was reset to
  2a41bdc and is clean.
- **Mutants (both killed)**:
  1. `return errNullType` → `continue` (drop null entries silently): 4 `--- FAIL` lines (parent + 3 subtests).
  2. Restore the pre-fix early `if string(b) == "null" { return nil }`: the root and nested subtests fail.
- **Root cause, not symptom**: the early return on the bytes `null` in `typeField.UnmarshalJSON` is removed; a null
  scalar or a null list entry now fails decoding. `Check` wraps decode errors as `fault.Invalid`
  (`internal/contract/contract.go:35-36`), which the test asserts. The quoted void side `{"type":"null"}` still passes
  (asserted in the test), so ADR-0090/ADR-0122's void form is preserved.
- **User-visible path covered**: all three push gates call `contract.Check` on each side —
  `cmd/funcdctl/manifest.go:73,76` (funcdctl.yaml push), `cmd/funcdctl/cli.go:299,302` (`--schema`),
  `internal/artifact/bundle.go:196,199` (bundle) — so an unquoted `type: null` is now refused at push.
- **Scope**: both hunks serve the issue; no test weakened or deleted.
- **Reuse**: the decoder still uses `encoding/json` with `*string` to detect null — no new helper or dependency; the
  only new identifier is the sentinel `errNullType`, which `Check` already wraps into a `fault.Invalid`. No existing
  helper in `internal/contract`, `api/fault` or `internal/platform` does this.
- **Conventions**: `api/fault` kind at the boundary, imports at the top of the file, short why-comments only, test
  named `TestIssue65_…` and `t.Parallel()` like its neighbours.
- **ADRs**: no ADR file touched; consistent with ADR-0090/ADR-0122 (a void side is `{"type":"null"}`) and the
  ADR-0058/0060 profile gate.
- **Checks (touched package)**: `go test -race` ok, `go vet` clean, `golangci-lint run ./internal/contract/` → `0 issues.`,
  `gofmt -l` empty. Repo-wide tests, Linux lint and e2e are left to the group gate.
- **Commit shape**: `fix(contract):` subject, `Fixes #65`, attribution trailer, one issue per commit.

Observation (not scored): `addlProps.UnmarshalJSON` (`internal/contract/schema.go`) has the same early return on
`null`, so `additionalProperties: null` decodes as absent. It is a different keyword from this issue; it may merit
its own issue if a chaos probe shows it changes the profile verdict.

### Definition of Done
11 / 11 items hold (touched-package scope for item 8; the group gate runs the rest).

### Model scorecard
Ledger fields: claude-opus-5-5 on issue #65 (fix) → pass, 0/0/1, 0 model-attributed, DoD 11/11.

### Recommendation
Ship. Optionally file a follow-up for gating `funcdctl types` and for `additionalProperties: null`.
