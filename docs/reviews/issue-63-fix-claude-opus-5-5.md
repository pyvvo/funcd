# Fix review — issue #63 (fix, model: claude-opus-5-5)

**Issue:** YAML 1.1 key coercion turns on/y/n/yes/no keys into true/false in manifests.
**Change:** branch `fix/i63`, commit 01efaa0 `fix(sdk): decode bare on/yes/no manifest keys as strings, not booleans`
(`pkg/sdk/sdk.go`, `pkg/sdk/manifest_test.go`, `go.mod`).

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #63 fix, model: claude-opus-5-5)

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **No test pins that only keys are quoted, not values** · attribution: `model` · evidence: mutant 3 (`i += 2` → `i++` in
  `quoteKeys`, so plain string *values* are double-quoted too) survives: `go test ./pkg/sdk/` → ok. The doc comment promises
  that "values keep the YAML 1.1 decode", but nothing asserts it. A regression of this kind would turn a bool field written
  as `yes` into a decode error. · fix (optional): add a case to the regression test with a plain `on`/`yes` value in a string
  field and a `yes` value in a bool field, and assert both decode as before.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 01efaa0`, with the test file restored from HEAD,
  then `go test ./pkg/sdk/ -run TestIssue63` → FAIL: `"[]" should have 1 item(s), but has 0`. This is the dropped `spec.on`
  list from the issue. Then `git reset --hard 01efaa0`; the worktree was left clean at that HEAD.
- **Passes with the fix**, un-skipped: `go test -race -count=1 ./pkg/sdk/` → ok, and `go test -race -count=1 ./cmd/funcdctl/`
  → ok.
- **User-visible behavior.** The test drives `sdk.DecodeManifest` and then `Validate`, which is the exact offline path of
  `funcdctl apply` (`cmd/funcdctl/cli.go` applyCmd) before any network call. Both issue cases are covered. The documented
  Sensor shape decodes with `spec.on` and `do[].on` intact and validates. The bare `on: [Removed]` EventSource keeps
  `Removed` and is now rejected by Validate (`"Removed" is unsupported`), so it is no longer silently turned into Created.
- **Mutants (2/3 killed on the key lines):** (1) the `k.Style = yamlv3.DoubleQuotedStyle` assignment removed → FAIL;
  (2) recursion limited to the document node, so nested keys are not quoted → FAIL; (3) the loop stride changed (see Minor)
  → survives.
- **Cause, not symptom.** The cause named in the issue is the YAML 1.1 resolution of a bare `on` key into the boolean
  `true`. The fix re-resolves keys under YAML 1.2 (a `yaml.v3` node walk) and quotes every plain key that resolves to
  `!!str`, before the existing `sigs.k8s.io/yaml` decode runs. Keys that YAML 1.2 types as non-strings (`!!int`, `!!bool`
  for `true`/`false`, `!!merge` for `<<`) are left as they are. Values are untouched, so the current string-target coercion
  is kept. No error is swallowed: a parse failure is returned as `fault.Invalid`.
- **No regressions on edge inputs** (a scratch probe test, deleted afterwards): JSON input decodes unchanged; empty and
  comment-only input still give "manifest is missing 'kind'" (the `doc.Kind == 0` guard); multi-document input still decodes
  only the first document, as before; anchors and duplicate keys decode as before; tab indentation is still a parse error.
- **Reuse.** `go.yaml.in/yaml/v3` was already in the module graph (an indirect dependency of `sigs.k8s.io/yaml` v1.6.0).
  The change only promotes it to a direct requirement, and `go mod tidy -diff` is clean. No YAML 1.2 key handling existed
  elsewhere in the repo to reuse: `DecodeManifest` is the only manifest decoder on the apply path. `sigs.k8s.io/yaml`
  offers no option to switch key resolution, and its `UnmarshalStrict` would reject the bare `on` key, not accept it.
- **ADRs.** No ADR file was edited. ADR-0024 names `yaml.v3` as the planned YAML follow-up for `apply`. ADR-0061 rejects
  decoding the daemon config into structs with `yaml/v3` because it needs a second set of `yaml:` tags. That reason does not
  apply here: v3 only rewrites the node tree, and the typed decode still uses the json tags through `sigs.k8s.io/yaml`.
- **Conventions.** Imports are at the top level and the alias `yamlv3` is clear next to `sigs.k8s.io/yaml`. Errors use
  `api/fault`. There is no `any` in a signature. The comment explains the reason and cites the issue, without narration.
  The test YAML is block style.
- **Checks (touched packages):** `gofmt -l pkg/sdk` empty; `go build ./...` ok; `go vet ./pkg/sdk/ ./cmd/funcdctl/` ok;
  `golangci-lint run ./pkg/sdk/...` → 0 issues. The e2e suite, Linux lint and lanes are left to the group gate, by scope.
- **Shape.** The subject is `fix(sdk): …`. The body states the cause, the fix and the regression test, and ends with
  `Fixes #63` and the attribution trailer. There is one issue per commit.

### Definition of Done

11 / 11 items hold (fix checklist). Item 4 holds through the revert check and the two core mutants. The surviving stride
mutant is recorded as the Minor above. Item 8 was run for the touched packages only; the group gate runs the rest.

### Model scorecard

Not recorded here (the orchestrator records it). Ledger fields: issue 63, phase fix, model claude-opus-5-5 → pass, 0/0/1,
1 model-attributed, DoD 11/11.

### Recommendation

Ship it. Optional: add a value-side assertion so that the "keys only" rule is pinned. A note outside this fix's scope:
`cmd/funcdctl/dev.go` decodes Workflow files with `sigs.k8s.io/yaml` directly instead of through `DecodeManifest`. No
Workflow json tag is affected today, but that path does not get this protection.
