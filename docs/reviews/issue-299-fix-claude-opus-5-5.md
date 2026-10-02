# Fix review — issue #299 (`funcdctl apply` stores a bare no/on/y string value as "false"/"true")

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #299 fix, model: claude-opus-5-5)

Change: branch `fix/i299`, one commit `af14891 fix(sdk): keep the text of a bare no/on/y manifest value`
(`pkg/sdk/sdk.go`, `pkg/sdk/manifest_test.go`).

### 🔴 Blockers

None.

### 🟡 Major / Minor

None.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** With `origin/main`'s `pkg/sdk/sdk.go` overlaid
  (`go test -overlay`, test file kept), `TestIssue299_BareBoolWordValueKeepsText` fails with
  `actual: {"ENABLED":"false","MODE":"true","Y":"true",…}` against `expected: {"ENABLED":"no","MODE":"on","Y":"y",…}`,
  which is the exact rewrite the issue reports. (A plain `git revert` of the commit also drops the test, so the
  overlay is the meaningful revert check.)
- **Passes with the fix**: `go test -race ./pkg/sdk` → ok; the worktree is back at `af14891`, clean.
- **User-visible behavior**: the test drives `sdk.DecodeManifest`, the function `funcdctl apply` calls
  (`cmd/funcdctl/cli.go` → `sdk.DecodeManifests`), on the issue's own ConfigMap shape; `go test ./cmd/funcdctl` → ok.
- **Cause, not symptom.** The issue names `quoteKeys` handling keys only before the YAML 1.1 decode of
  `sigs.k8s.io/yaml`. The fix generalizes the same yaml.v3 pre-pass to every plain scalar that YAML 1.2 tags `!!str`,
  so YAML 1.1 never sees an unquoted `no`/`on`/`y`. Ints, bools (`true`), floats and timestamps keep their tags and
  are unchanged (`PORT: 8080`, `FLAG: true` and `count: 2` assert this).
- **Both value sinks covered**: a `map[string]string` (ConfigMap `spec.data`) and a `json.RawMessage`
  (WorkflowRun `spec.input`, including a sequence `[yes, no]` → `["yes","no"]`), plus a bool field: `paused: yes`
  now fails as `fault.Invalid` and names `paused`. This matches the issue's "or the decode fails and names the field".
- **Mutants** (overlay, `./pkg/sdk`):
  - values never quoted (`&& false` on the condition) → `TestIssue63_…` and `TestIssue299_…` fail;
  - drop the `ShortTag() == "!!str"` guard (quote ints/bools too) → `TestIssue299_…` and `TestIssue64_…` fail;
  - drop the `Style == 0` guard → survives, an equivalent mutant: re-quoting an already single-quoted or block
    scalar as double-quoted keeps its value, so no test can tell them apart. Not a test gap.
- **Scope**: two hunks, both for the issue: the rename `quoteKeys` → `quoteStrings` (one call site, no other
  reference outside the old review doc) and the new test. No test was weakened or deleted.
- **Reuse**: the change extends the existing yaml.v3 node pass rather than adding a second decoder or a hand-written
  boolean-word list; it is simpler than the code it replaces. No new dependency or helper.
- **Conventions**: `api/fault` error kind asserted; the test's embedded YAML is block style; imports at top level
  (`fmt` added for `fmt.Appendf`); the doc comment states the why (YAML 1.1 coercion, #63, #299) once.
- **ADRs**: no Accepted/Implemented ADR fixes the YAML 1.1 value coercion as a decision; no ADR file is touched.
- **Checks** (touched package): `go test -race ./pkg/sdk` ok, `go vet ./pkg/sdk` clean,
  `golangci-lint run ./pkg/sdk/...` → 0 issues. Linux lint, the repo-wide suite and e2e are left to the group gate.
- **Shape**: `fix(sdk):` subject, `Fixes #299`, the attribution trailer, one issue in one commit.

### Behavior note (not a finding)

A bool field written as `yes`/`no`/`on`/`off` used to decode as a boolean and now fails the decode with a
named field. That is the stricter YAML 1.2 reading the issue asks for; no tracked YAML in the repo uses a bare
bool word as a value, so nothing in-tree depends on the old reading. Release notes for the group should mention it.

### Definition of Done

11 of 11 items hold (item 8 at the host level for the touched packages; Linux lint and e2e are run by the group gate).

### Model scorecard

claude-opus-5-5 — fix phase, issue #299: pass, 0 / 0 / 0, 0 model-attributed findings.

### Recommendation

Pass. Merge with its group; mention the stricter bool-word handling in the group's release notes.
