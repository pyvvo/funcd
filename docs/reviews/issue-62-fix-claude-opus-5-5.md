## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #62 fix, model: claude-opus-5-5)

Change: branch `fix/i62`, commit 543467b `fix(funcdctl): apply every document of a multi-document YAML manifest`
(`git diff origin/main...HEAD`: `cmd/funcdctl/cli.go`, `cmd/funcdctl/cli_test.go`, `pkg/sdk/sdk.go`,
`pkg/sdk/manifest_test.go`, `go.mod`).

Issue #62: `funcdctl apply -f` decoded a manifest with `sigs.k8s.io/yaml.Unmarshal`, which reads only the
first YAML document, and applied that one object with exit 0, so every document after `---` was dropped
silently.

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **The directory form `-f <dir>/` is still unsupported** · attribution: `issue` · The issue's expected
  behavior asks for multi-document apply *and* `-f <dir>/`, but also accepts "at minimum fail loudly".
  The directory form already fails loudly (`read <dir>: is a directory`, exit 1); the silent-loss defect
  that the issue title names is fixed. Directory apply is a separate capability that the funcd-python
  example README documents; it can be tracked as a follow-up issue. Not scored.
- **`decodeDocument` still names its op `sdk.DecodeManifest`** · attribution: `model` · `pkg/sdk/sdk.go`
  lines 309–319. After the split, a per-document error reads
  `sdk.DecodeManifests: manifest document 2: sdk.DecodeManifest: unknown kind …`: the inner op names a
  function that did not produce the error. Cosmetic; a `const op = "sdk.decodeDocument"` (or dropping the
  inner op) would read cleanly.

### Observation (not a finding of this change)

- Probing the pinned funcd-python example `examples/releve-lakehouse/resources/*.yaml` through
  `sdk.DecodeManifests` + `Validate` decoded all 16 documents (including the 5 Functions of
  `functions.yaml`, the 2 ConfigMaps and the 2 Routes). One document, `EgressPolicy/releve-confidential-deny`,
  fails the pre-existing validator (`spec.rules must list at least one rule`). That behavior exists on
  `origin/main` and is unrelated to multi-document decoding; it may deserve its own issue against the
  example or the validator.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 543467b`, test
  files restored from HEAD, then `go test -run TestIssue62 ./cmd/funcdctl/`:
  `"applied ConfigMap/md-first\n" does not contain "applied ConfigMap/md-second"` → FAIL — exactly the
  issue's symptom (only the first document applied, exit 0). The `pkg/sdk` test does not compile on the
  pre-fix code (`undefined: sdk.DecodeManifests`), as expected for a new API. Worktree then reset to
  543467b, clean.
- **Passes with the fix under `-race`.** `go test -race -count=1 ./cmd/funcdctl/ ./pkg/sdk/` → `ok` for both
  packages; no test skipped.
- **User-visible behavior.** The CLI test drives the real cobra root against the real control plane and
  `Get`s each ConfigMap after apply; all three exist. The issue's 5-Function `functions.yaml` now decodes to
  5 Functions (probe above, removed afterwards).
- **Root cause, not symptom.** `DecodeManifests` iterates documents with the `go.yaml.in/yaml/v3` decoder
  until `io.EOF`, re-encodes each node and decodes it through the unchanged `decodeDocument` (the old
  single-document path). `DecodeManifest` now refuses a multi-document input (`manifest holds 3 documents,
  want 1`) instead of keeping only the first, so no remaining caller can silently drop documents.
- **Mutants — all killed.**
  1. `cli.go` apply loop over `objs[:1]` → `TestIssue62_ApplyMultiDocumentAppliesEveryDocument` FAIL.
  2. `sdk.go` skip-null-document check disabled (`"!!null"` → `"!!null-x"`) → both `TestIssue62_*` FAIL.
  3. `sdk.go` `DecodeManifest` guard `len(objs) != 1` → `< 1` → `TestIssue62_DecodeManifestsDecodesEveryDocument` FAIL.
  4. `cli.go` pre-flight loop over `objs[:1]` → `TestIssue62_ApplyMultiDocumentAppliesEveryDocument` FAIL
     (the "no document applied when one fails pre-flight" assertion).
- **Test quality.** The sdk test covers a `---` inside a block scalar (not a separator), a comment-only
  document, a JSON document in a YAML stream, the `...` end marker, an all-empty manifest (Invalid) and a
  bad later document (error names `document 2`). The CLI test asserts all-or-nothing pre-flight: an invalid
  second document leaves the valid first one unapplied (`NotFound`).
- **Scope.** Every hunk serves the issue: the decoder split, the apply loop, the help text, the two tests,
  and `go.yaml.in/yaml/v3` moving from indirect to direct. No test weakened or deleted; the existing
  `TestDecodeManifestAcceptsYAMLAndJSON` still passes on the new `DecodeManifest`.
- **Reuse, no duplication.** No existing document splitter in the repo (searched for `yaml.NewDecoder`,
  `NewYAMLOrJSONDecoder`, `NewYAMLReader`, `SplitYAML`); `k8s.io/apimachinery` is not a direct dependency.
  The fix reuses a library already in the module graph (`go.yaml.in/yaml/v3`, MIT/Apache-2.0) instead of a
  hand-rolled `---` string splitter, and reuses the existing `decodeDocument` path and `v1.Object.Validate`.
  `go mod tidy -diff` is clean.
- **Conventions (ADR-0002, CLAUDE.md).** Errors are `api/fault` (`Invalidf`, `Wrapf` with `KindOf`
  preserved); no `any` in signatures; imports at top level; comments explain the why and stay short;
  embedded test YAML is block style (the one flow-style JSON line is deliberate test input for the JSON
  path).
- **ADRs.** No ADR file touched. The change matches the blueprint's kubectl-style apply and keeps the
  ADR-0042 offline pre-flight (now on every document before any network call).
- **Checks (touched packages).** `gofmt -l` empty; `go vet ./cmd/funcdctl/ ./pkg/sdk/` OK;
  `golangci-lint run ./cmd/funcdctl/... ./pkg/sdk/...` → `0 issues.`; race tests green. Repo-wide tests,
  Linux lint and e2e are left to the group gate.
- **Shape.** One commit, `fix(funcdctl):` subject, `Fixes #62`, attribution trailer.

### Recommendation

Pass. Optional polish: rename the inner op in `decodeDocument`. File a follow-up issue for `-f <dir>/`
support (and, separately, the example's EgressPolicy validation failure).
