## Verdict: pass — 0 blockers, 0 majors  (issue #516 fix, model: claude-opus-5-5)

Change: branch `fix/i516`, commit 0d026a3 `fix(provider): give internal/provider a single, accurate package doc`
(`internal/provider/provider.go`, `internal/provider/spec.go`, `internal/provider/doc_test.go`).

### 🟡 Major / Minor

- **Minor: the test lists the package's source files by hand** · attribution: model ·
  `internal/provider/doc_test.go` embeds `env.go`, `probe.go`, `provider.go`, `runtime.go` and `spec.go` one by one
  and iterates a literal map. The commit message says the test parses "every non-test source file". That is true
  today (the package has exactly these five), but a sixth file that adds a package doc would not be caught. A
  `//go:embed *.go` `embed.FS` that skips `_test.go` would cover future files and still work with `-overlay`.
  This does not block the fix.
- **Minor: ADR-0082's "leaf" invariant was overtaken without a supersede link** · attribution: adr ·
  `docs/adr/0082-provider-model-catalog.md` (Implemented) still calls `internal/provider` "a true leaf" (:51, :81,
  :96, :163). `docs/adr/0087-add-on-provider-runtime.md` put a runtime that imports `internal/gateway` and
  `internal/runtime` in the same package and never mentions ADR-0082. The fix is right to follow the code and the
  newer ADR-0087 in the package doc, and it does not edit either ADR. The leftover ADR inconsistency is a
  decision-layer matter for `/adr` (a note or a superseding ADR) and is not scored against the fix.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason**: with `go test -overlay` putting the `origin/main` `provider.go` and
  `spec.go` back in place, `TestIssue516_PackageHasOneDocComment` fails with `should have 1 item(s), but has 2`
  (two package doc comments). A plain `git revert --no-commit 0d026a3` also removes the test, so it reports
  "no tests to run". The overlay is the meaningful check, and the test embeds its sources (`//go:embed`) so the
  overlay reaches them. The worktree was reset to 0d026a3 and is clean.
- **Passes with the fix**: `go test -race -count=1 ./internal/provider/` → `ok`, nothing skipped.
- **User-visible behavior**: `go doc ./internal/provider` now prints one block that opens with "Package provider
  holds the platform provider catalog (ADR-0082) and the add-on-provider management runtime (ADR-0087, …)".
- **Mutants (3/3 killed)**: (M1) restoring only the old `provider.go` doc → FAIL (two docs); (M2) adding "It is a
  leaf." to the `spec.go` doc → FAIL; (M3) removing "(ADR-0082)" from the `spec.go` doc → FAIL.
- **Cause, not symptom**: the stale comment in `provider.go` is gone. The false "leaf" and "no runtime behavior"
  claims are dropped rather than reworded, and the accurate catalog text is merged into the single doc in `spec.go`.
  This meets the issue's "Done when" clause in full.
- **Scope**: three files, and every hunk serves #516. The existing `TestIssue458_…` test is unchanged.
- **Reuse**: the new test extends the embed and `go/parser` harness that `doc_test.go` already had for #458 and adds
  no helper or dependency.
- **Conventions**: imports stay at the top level; the comment change removes text rather than adding it; the doc style
  matches the existing `spec.go` prose. No ADR file was touched.
- **Checks (touched package)**: `go test -race` ok, `go vet` clean, `golangci-lint run ./internal/provider/...` → 0
  issues.
- **Shape**: `fix(provider):` subject, Cause/Fix/Test body, `Fixes #516`, attribution trailer, one issue in one commit.

### Recommendation

Pass. The handwritten file list (Minor, model) can be left as it is or changed to an `embed.FS` glob later. The
ADR-0082 leaf wording (Minor, adr) should go to `/adr`; it does not belong in this fix.

Checklist: 11/11 items hold. Item 8 was checked on the touched package only, by design; the group gate runs the
repo-wide, Linux-lint and e2e checks.
