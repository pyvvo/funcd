## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #298 fix, model: claude-opus-5-5)

Change: `fix/i298`, commit 76caad1 `fix(sdk): keep funcdctl.yaml key text and reject unknown keys`
(`pkg/sdk/manifest.go`, `pkg/sdk/manifest_test.go`).

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **The YAML key-quoting pre-pass is copied, not shared** · attribution: model · evidence:
  `pkg/sdk/manifest.go` `parseManifest` (decode into a `yamlv3.Node`, `quoteKeys`, `yamlv3.Marshal`, wrap
  the error as `fault.Invalid`) repeats the per-document block of `DecodeManifests` in `pkg/sdk/sdk.go`
  (lines 301–315). The block is about eight lines and both copies are correct today, but a later change to
  the key handling (for example the value coercion that the issue reports separately) must now be made in
  two places. Fix: extract one helper in `pkg/sdk` that takes one document's bytes and returns the
  re-encoded bytes, and call it from both `DecodeManifests` and `parseManifest`.

### ✅ Verified correct (keep it)
- **The regression test fails on the pre-fix code for the reported reason.** With the `origin/main`
  `pkg/sdk/manifest.go` overlaid (`go test -overlay`), `TestIssue298_ManifestKeepsKeyTextAndRejectsUnknownKeys`
  fails at the first assertion: the input contract's properties are `{"true":…,"x":…}` instead of
  `{"x":…,"y":…}`, which is the coercion in the issue. (A plain `git revert --no-commit` also removes the
  test, so the run reports "no tests to run"; the overlay is the meaningful pre-fix check.)
- **It passes with the fix under `-race`**, both subtests included (`misspelled bindings`,
  `nested unknown key`). The worktree was restored to 76caad1 and is clean.
- **Mutants are killed.** `yaml.UnmarshalStrict` → `yaml.Unmarshal`: the test fails (the unknown keys are
  dropped again). Removing the `quoteKeys(&doc)` call: the test fails with `Not equal` on the contract.
- **The cause is fixed, not masked.** `parseManifest` now runs the same two fixes that `DecodeManifests`
  got for #63 and #64: the existing `quoteKeys` pass and the strict decode. Unknown keys return a
  `fault.Invalid` that names the key; the test checks both the kind and the key name.
- **Reuse.** The fix reuses the existing `quoteKeys`, the already-imported `go.yaml.in/yaml/v3`
  dependency (already in `go.mod`) and `sigs.k8s.io/yaml.UnmarshalStrict`. It adds no new dependency or
  type. The only duplication is the minor finding above.
- **No regression on real manifests.** All 11 `funcdctl.yaml` examples in the pinned funcd-typescript and
  funcd-python modules load without error under the strict decode (a scratch scan test, removed
  afterwards). The `pkg/sdk/...` and `cmd/funcdctl/...` tests pass under `-race`.
- **Edge cases.** An empty document skips the pre-pass and keeps the original bytes. JSON input still
  works, because yaml/v3 re-encodes it as YAML. Values keep the YAML 1.1 decode, the same as for resource
  manifests. The `json.RawMessage` contract sides are not checked for unknown fields, because they hold
  free-form JSON Schema.
- **Scope.** Both hunks serve the issue. No test was weakened or deleted. The `funcdctl dev` Workflow
  decode gap that the issue lists under "Related" is correctly left out of scope.
- **ADRs.** ADR-0122 (funcdctl.yaml) does not say that unknown keys are tolerated. The change aligns
  funcdctl.yaml with the strict resource-manifest decode (ADR-0108 rationale in `decodeDocument`). No ADR
  file was edited.
- **Conventions.** `fault.Invalidf` errors, imports at the top of the file, block-style YAML in the test
  fixture, and a doc comment that adds only the *why* and the issue numbers.
- **Checks (touched packages).** `go test -race ./pkg/sdk/... ./cmd/funcdctl/...` passes, `go vet ./pkg/sdk/`
  passes, and `golangci-lint run ./pkg/sdk/` reports 0 issues. Linux lint, e2e and the repo-wide gate are
  left to the group gate.
- **Commit shape.** `fix(sdk):` subject, `Fixes #298`, the attribution trailer, and one issue in the commit.

### Definition of Done
10 / 11 items hold. Miss: item 10 (reuse) holds only partly, because of the copied pre-pass block (minor,
model). Item 8 was checked on the host for the touched packages only; the Linux and repo-wide parts are
left to the group gate.

### Model scorecard
Ledger fields: claude-opus-5-5 on issue #298 (fix) → pass, 0/0/1, 1 model-attributed, DoD 10/11.
(Not recorded here; the orchestrator writes the ledger row.)

### Recommendation
Pass. It can ship as is. Folding the pre-pass into one shared helper is a small optional follow-up.
