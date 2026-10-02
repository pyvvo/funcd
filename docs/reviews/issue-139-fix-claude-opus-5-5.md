## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #139 fix, model: claude-opus-5-5)

Change: branch `fix/i139`, commit `5173685 fix(sdk): report the field errors of a 422 response`
(`pkg/sdk/sdk.go` +19/-2, `pkg/sdk/sdk_test.go` +30). Governing ADRs: ADR-0024 (SDK, `problemToFault`),
ADR-0048 (field constraints rejected 422 at the edge), ADR-0002 (conventions).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **Direct `huma` import in `pkg/sdk` versus the letter of ADR-0024's import discipline** · attribution: `adr`
  · evidence: ADR-0024 (Implemented) says the SDK and the CLI import "only `api/**` + stdlib"; the fix adds
  `github.com/danielgtaylor/huma/v2` to `pkg/sdk/sdk.go` imports to decode `errors[]` as `huma.ErrorDetail`.
  The rule's letter is already stale: `pkg/sdk` imported `sigs.k8s.io/yaml` before this change, and `huma` is
  already in the SDK's dependency graph through `api/types/v1alpha1` (`go list -deps` of `api/types/v1alpha1`
  reports 5 huma packages; `enums.go` and `ids.go` import it). No depguard rule enforces it (lint: 0 issues),
  the binary gains no new dependency, and reusing huma's own type and `Error()` formatting is what the reuse
  check asks for (a hand-rolled 3-field struct would duplicate it). Not a model defect. If the discipline
  should be restated ("`api/**`, stdlib, and the libraries `api/**` already depends on"), that is a
  superseding-ADR or blueprint note, not a change to this fix.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 5173685`, the
  test file restored from HEAD, `go test -run TestIssue139 ./pkg/sdk/`:
  `"sdk: validation failed" does not contain "body.spec.replicas"` → `FAIL` (exactly the issue's symptom).
  After `git reset --hard 5173685`: `go test -race -count=1 ./pkg/sdk/` → `ok` (1.59s). Worktree left at
  `5173685`, clean.
- **The test is real end to end**: `newClient` mounts the real `controlplane.NewServer` behind `httptest`,
  so the 422 comes from huma's own schema validation (the ADR-0048 edge), not a hand-written body. It covers
  both cases the issue reported (`spec.replicas: -1`, `spec.scaling.minReplicas: -1`) and asserts the kind
  stays `fault.Invalid`, the location, and the reason.
- **User-visible behavior**: `cmd/funcdctl/main.go:14` prints `"funcdctl: " + err.Error()`, so the CLI now
  prints `sdk: validation failed: expected number >= 0 (body.spec.replicas: -1)` — the field, the reason and
  the value the issue expected.
- **Root cause fixed, not masked**: the issue names `problemToFault` dropping `errors[]` because
  `fault.Problem` has no such field; the fix decodes `errors[]` alongside the embedded `fault.Problem` and
  appends each entry. The status→kind mapping (ADR-0024) is untouched; `api/fault` is not modified.
- **Mutants (3/3 killed)**, each run with `-run 'TestIssue139|TestScenarioSDK'` and restored:
  M1 JSON tag `errors`→`errs` → FAIL; M2 drop the `": " + joined` append → FAIL; M3 `Error()`→`.Message`
  (drops location and value) → FAIL.
- **Scope**: every hunk serves the issue; no test weakened or deleted; the doc comment on `problemToFault`
  gains one sentence of *why* (errors[] is the only place that names the field) — no comment bloat.
- **Reuse**: reuses `huma.ErrorDetail` and its `Error()` (`"%s (%s: %v)"`) instead of a new type or
  formatter; embeds `fault.Problem` rather than copying its fields; `strings.Join` from stdlib. No other
  `ErrorDetail` decoder exists in the repo to reuse instead.
- **Conventions (ADR-0002)**: errors stay typed `api/fault`; no `any` in a signature (`Value any` lives inside
  huma's type); imports at top level; gofmt clean.
- **ADRs**: no Accepted/Implemented ADR file edited; ADR-0024's mapping contract and ADR-0048's
  edge-validation decision hold.
- **Checks (touched packages)**: `gofmt -l pkg/sdk` empty; `go build ./pkg/... ./cmd/...` ok;
  `go vet ./pkg/sdk/ ./cmd/funcdctl/` ok; `golangci-lint run ./pkg/sdk/...` → `0 issues`;
  `go test -race ./pkg/sdk/ ./cmd/funcdctl/` ok. Linux lint, e2e and lanes are left to the group gate.
- **Shape**: `fix(sdk):` subject, `Fixes #139`, the attribution trailer, one issue in one commit.

Note (not a finding): the `msg == ""` branch (an `errors[]` with no `detail`) has no test; huma always sets
`detail` on its 422, so it is defensive only.

### Recommendation

Pass. Hand back to `/fix` Step 8 to open the PR. Optionally record the ADR-0024 import-discipline wording
as a follow-up for a future superseding ADR.
