## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #64 fix, model: claude-opus-5-5)

Change: branch `fix/i64`, commit `be6abe1 fix(sdk): reject unknown manifest keys in funcdctl apply`
(`pkg/sdk/sdk.go`, `pkg/sdk/manifest_test.go`).

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **A language-repo example is now rejected by `apply`** · attribution: `issue` (a defect that existed before
  this change and that the fix exposes; recorded, not scored). Every committed funcd resource manifest was
  decoded with the strict decoder: the 70 files in this repo and in the pinned `funcd-typescript` v0.4.0 and
  `funcd-python` v0.3.0 modules. All of them pass except `funcd-python` `examples/releve-lakehouse/resources/configmap.yaml`
  (two documents), which puts `data:` at the top level instead of under `spec.data`
  (`api/types/v1alpha1/configmap.go`). The error is `json: unknown field "data"`. Before the fix, `funcdctl apply -f resources/`
  silently stored two empty ConfigMaps, which is the issue's bug class. Now the apply fails loudly. That is the
  correct behavior, but the example needs a fix in `funcd-python` (`data:` → `spec.data:`). No Lima lane
  applies this file. The `FuncdConfig` files that also matched are not resource kinds. Follow-up: an issue/PR in
  `pyvvo/funcd-python`.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix.** `git revert --no-commit be6abe1` plus the HEAD test file restored:
  `TestIssue64_DecodeManifestRejectsUnknownKeys` fails in all three subtests with "An error is expected but got
  nil … an unknown manifest key must not be dropped silently". That is the issue's reason: the unknown key is
  dropped and decode succeeds. The tree was then reset to `be6abe1` and is clean.
- **Passes with the fix**, un-skipped, under `-race` (`ok github.com/pyvvo/funcd/pkg/sdk`, all 3 subtests PASS).
  The same holds for `TestDecodeManifestAcceptsYAMLAndJSON`.
- **User-visible behavior.** A freshly built `funcdctl apply -f` with the issue's legacy EventSource (`type`,
  `function`, `timer.events[].cron`) now exits 1 with `decode EventSource: … unknown field "function"` before
  any network call. It no longer prints "applied EventSource/legacy".
- **Cause, not symptom.** The non-strict `yaml.Unmarshal` into the typed object was the cause named in the issue.
  It is now `yaml.UnmarshalStrict` (sigs.k8s.io/yaml → `DisallowUnknownFields`). This is the single
  decode site for `apply` (`cmd/funcdctl/cli.go:141`), so every kind is covered, as the issue expected.
  The TypeMeta peek stays lenient, which is correct because it reads only `kind`.
- **Mutants (3/3 killed)** on the decode branch: swallowing the error (`_ = err`) → FAIL; `Invalidf`→`Internalf`
  → FAIL (the `fault.Invalid` assertion); dropping `err` from the message → FAIL (the `Contains(key)` assertion).
- **Scope.** Both hunks serve the issue. The one change to an existing test replaces the removed
  `spec.artifact` fixture (ADR-0097) with `spec.image`. Strict decoding requires that change, and the test is not weakened:
  it still asserts YAML and JSON decode.
- **Reuse.** No new helper. `yaml.UnmarshalStrict` from the existing `sigs.k8s.io/yaml` dependency mirrors the
  precedent in `internal/platform/config/config.go:310` (strict config-file decode).
- **Conventions.** The change returns `fault.Invalidf` with the existing op string and adds no `any`. It has no
  imports inside functions. The two-line comment explains why strict decoding is needed and names ADR-0108. The test cases
  are table-driven, with block-style YAML.
- **ADRs.** The change matches ADR-0108 scenario `no-binding-field-schema`: unknown keys are rejected on the
  primary caller path. No ADR file was touched.
- **Checks (touched packages).** gofmt is clean. `go build ./...` passes. `go vet ./pkg/sdk ./cmd/funcdctl` passes.
  golangci-lint on both packages reports 0 issues. `go test -race` on `./pkg/sdk/... ./cmd/funcdctl/...` passes. Repo-wide tests,
  Linux lint, e2e and the lanes are left to the group gate.
- **Shape.** The subject is `fix(sdk):`. The commit has `Fixes #64` and the attribution trailer, and covers one issue.

### Definition of Done
11 / 11 applicable items hold. Item 8 was verified for the touched packages on the host. Linux lint, e2e and
lanes are deferred to the group gate by design.

### Model scorecard
Ledger fields (not recorded here; the orchestrator records them): claude-opus-5-5 on issue #64 (fix) → pass,
0/0/1, 0 model-attributed, DoD 11/11.

### Recommendation
Ship. Separately, file a `funcd-python` follow-up so that `releve-lakehouse/resources/configmap.yaml` uses `spec.data`.
