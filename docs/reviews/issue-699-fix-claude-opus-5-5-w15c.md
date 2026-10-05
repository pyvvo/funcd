## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #699 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i699`, commit 294eb077 `fix(sdk): keep number-like text in funcdctl.yaml string fields`.
Files: `pkg/sdk/manifest.go` (+4/-1), `pkg/sdk/manifest_test.go` (+54).

The fix adds one call, `quoteTextScalars(&doc, reflect.TypeOf(Manifest{}))`, after `quoteStrings(&doc)` in
`parseManifest`. This is the type-directed quoting that the #416 fix added only to `decodeDocument` (the
resource-manifest path). With it, a plain scalar that YAML reads as a number keeps its text when its target
field in `Manifest` is a string. A `json.RawMessage` contract side keeps its numbers.

### Proof first (the decision for this issue)

`TestIssue699_ManifestNumberLikeTextInStringFieldKeepsText` was run against the current `origin/main` version
of `pkg/sdk/manifest.go` through `go test -overlay`, with the test file kept:

```
--- FAIL: .../dev.config            expected: {"EXP":"1e3","HEX":"0x1F","MODE":"0755","PORT":"8080","VERSION":"1.10"}
                                    actual  : {"EXP":"1000","HEX":"31","MODE":"493","PORT":"8080","VERSION":"1.1"}
--- FAIL: .../bindings.blob_prefix  expected: "0755"  actual: "493"
--- FAIL: .../dev.catalog_prefix    expected: "0x1F"  actual: "31"
FAIL  github.com/pyvvo/funcd/pkg/sdk
```

Each case of the issue (`dev.config`, a `bindings` string field, a `dev.catalog` string field) fails on current
main for the stated reason: the value is decoded as a YAML 1.1 number and formatted back. The `contract keeps
numbers` subtest passes on main as well, as expected, because it guards behavior that must not change.

### 🟡 Major / Minor

None.

### ✅ Verified correct (keep it)

- With the fix: `go test -race -count=1 ./pkg/sdk/` → `ok`. All four `TestIssue699` subtests pass, and none is skipped.
- Cause, not symptom: the change applies the existing #416 mechanism at the parser that lacked it. This is the
  cause the issue names (`parseManifest` ran only `quoteStrings`). Nothing is retried, swallowed or skipped.
- Mutants (overlay, `-run TestIssue699`):
  - `quoteTextScalars(&doc, reflect.Type(nil))` (keys only, no type walk) → all three string-field subtests fail.
  - `reflect.TypeOf(map[string]string{})` in place of `Manifest{}` (wrong target type) → all three string-field subtests fail.
  - Revert of the call (the proof above) → the same three subtests fail.
- Scope: two hunks. One is the call and the `reflect` import. The other is the `parseManifest` doc comment, which
  now names the #416 and #699 behavior. No test was weakened or removed.
- Reuse: the fix reuses `quoteTextScalars`, `jsonFieldType` and `jsonFields` from `pkg/sdk/sdk.go` and adds no
  helper. The test reuses the package's `writeManifest` helper.
- Siblings: all YAML decode sites in `pkg`, `cmd` and `internal` were checked. `LoadManifest` is the only
  funcdctl.yaml parser, and it serves `funcdctl dev`, `push` and `types`, so the one call covers all three.
  `decodeDocument` already has the fix (#416). `cmd/funcdctl/dev.go` probes only `kind`.
- ADRs: ADR-0122 (funcdctl.yaml) and ADR-0125 (`dev-config-inline`) are honored, and no ADR file was touched.
- Conventions: the YAML in the test is block style, the imports are at the top level, and the test comment is short
  and states why the test exists. The test name follows the `TestIssue<N>_…` form.
- Checks (touched package): `go vet ./pkg/sdk/` is clean, and `golangci-lint run ./pkg/sdk/...` reports 0 issues. The
  worktree is clean after the review.
- Shape: the subject is `fix(sdk): …`, the body has the Cause/Fix/Test paragraphs, then `Fixes #699`, then the
  attribution trailer. The commit fixes one issue.

### Observation (not scored)

The daemon config loader (`internal/platform/config/config.go`, `Load`) also strict-decodes YAML into string
fields (for example `shaping.headers.set`, a `map[string]string`) without type-directed quoting. It is a
different file with a different job (operator config, not funcdctl.yaml), so it is outside this issue's scope. A
separate probe could show whether it has the same rewrite.

### Recommendation

Pass. The fix goes to its group's gate (Linux build/vet/lint, `just ci-full`) and then to the PR.
