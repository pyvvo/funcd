## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #156 fix, model: claude-opus-5-5)

Change: commit 5c61b7b on `fix/i156`, `fix(artifact): refuse a digest-form index source with "needs a tag" on both targets`.
It touches `internal/artifact/artifact.go`, `internal/artifact/platform.go` and `internal/artifact/platform_test.go`.

### Minor 1 — the index-ref digest check is not covered by a test  ·  attribution: model
- Evidence: a mutant that reverts `PushIndex`'s index-ref guard to `if tag == ""` (dropping `|| isDigest(tag)`)
  survives. `go test ./internal/artifact -count=1` and `go test ./cmd/funcdctl -run Index` both report `ok`.
- Impact: low. The issue is about sources, and the source guard is covered. The old guard was also untested.
  The fix changed this line (from `strings.HasPrefix(tag, "sha256:")` to `isDigest(tag)`), and its layout
  behavior changed with the new `parseLocalRef`. For example, `oci-layout://<dir>@sha256:…` now fails with
  "needs a tag" instead of the misleading repository error.
- Fix (optional): add an index-ref case, such as `oci-layout://<dir>@<digest>`, to `TestIssue156_…`.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** I ran `git revert --no-commit 5c61b7b`
  and kept the new test. All 3 subtests failed:
  - `layout_digest` and `layout_tag_and_digest` failed with "… is not in the index's repository …".
    This is the misleading message from the issue, caused by the `<dir>@sha256` directory split.
  - `registry_digest` passed the tag check and reached a manifest fetch ("connection refused").
    This shows that a registry digest source was accepted.
- **Passes with the fix under `-race`.** `go test -race -run TestIssue156 -v` passed all 3 subtests.
  The worktree was reset to the starting HEAD and is clean.
- **Root cause fixed, not masked.** `parseLocalRef` now recognises a trailing `@<digest>`, so the layout
  directory is correct. It also handles `<dir>:<tag>@<digest>`, the exact form `funcdctl push` prints.
  `readIndexSource` now refuses a digest reference on both backends with the dedicated "needs a tag"
  error. This meets the issue's expected behavior and ADR-0145's Scope Out ("digest source refs for
  index (tags only)").
- **Mutants on key lines are killed:**
  - M1: the source guard reverted to `tag == ""` → `TestIssue156_…` fails (registry case).
  - M2: the recursive dir parse replaced with `dir = rest[:i]` → `TestIssue156_…` fails (tag+digest case).
- **Scope.** Every hunk serves the issue. No test was weakened or deleted, and no ADR file was touched.
- **Wider effect checked.** `parseLocalRef` also feeds `resolveTarget`, which pull, resolve, site and
  bundle use. A layout `@digest` ref used to produce a non-existent `<dir>@sha256` directory, so it never
  worked. It now resolves by digest in the right layout. No caller depended on the old split, and the
  full `internal/artifact` and `cmd/funcdctl` suites pass under `-race`.
- **Reuse.** `isDigest` delegates to oras-go's `registry.Reference.ValidateReferenceAsDigest`. This is an
  existing dependency, so the code does not hand-roll a digest parser. One helper replaces the ad-hoc
  `HasPrefix(tag, "sha256:")`. `cmd/funcdctl`'s `splitRefDigest` sits in another layer, and
  `internal/artifact` cannot import it, so this is not duplication.
- **Conventions.** The code uses `fault.Invalidf` errors, keeps imports at the top level, and adds short
  doc comments only. Naming matches the surrounding code.
- **Checks (touched packages):**
  - `go test -race -count=1 ./internal/artifact ./cmd/funcdctl` → ok, ok.
  - `go vet` → clean.
  - `golangci-lint run ./internal/artifact/...` → 0 issues.
  - `gofmt -l` → clean.
  - The group gate runs e2e and Linux lint.
- **Shape.** The subject is `fix(artifact):`, the body has `Fixes #156` and the attribution trailer, and
  the commit covers one issue.

### Recommendation
Pass. The Minor is optional hardening for a follow-up or for this branch; it does not block the PR.
