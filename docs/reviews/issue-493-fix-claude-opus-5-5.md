# Fix review — issue #493 (containerd re-imports the embedded runtime tar on every Create)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #493 fix, model: claude-opus-5-5)

Change: branch `fix/i493`, one commit `b6a0c3a fix(runtime): find the imported curated image instead of re-importing it`.
Files: `internal/runtime/containerd/containerd_linux.go`, `internal/runtime/containerd/snapshotter_linux_test.go`, `go.mod`.

The fix makes `resolveImage` retry a NotFound lookup under the ref's normalized Docker name
(`reference.ParseDockerRef`), which is the name containerd's archive importer stores a curated tar under.
A second `Create` now takes the present-image branch, which checks the unpack, instead of running the
gunzip, `Import` and `Unpack` path again. The cause named in the issue (an exact-match lookup of the short
ref against a normalized stored name) is removed at its source.

### 🟡 Minor 1 — the regression test proves "no re-import" indirectly  ·  attribution: model

`TestIssue493_CreateFindsImportedCuratedImage` asserts only `require.NoError` on `Create`. Without the fix it
fails because the fallback reaches `Import` of the tracked placeholder tar (`internal/runtime/embedimg/nodejs22.tar`,
a few hundred bytes), which gives `archive/tar: invalid tar header`. The test therefore relies on that placeholder
not being a valid image archive. If a build first runs `just build-runtime-images` and embeds a real tar, the
pre-fix path could import successfully and the test would no longer tell the two paths apart. A direct assertion
(for example, an image store that counts `Create` calls, or a content-store check that no new blob was written)
would make the test independent of the embed contents. This does not block the fix: in the repository state and
in CI the test fails without the fix for the reported reason.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** With `origin/main`'s `containerd_linux.go` overlaid (Linux
  test binary, run in a Linux container), the test fails with
  `import embedded image for "funcd/runtime-nodejs22:latest": archive/tar: invalid tar header`: the lookup missed
  and the driver went to the embedded-tar import path, which is exactly the behavior the issue describes.
- **Passes with the fix**: `--- PASS: TestIssue493_CreateFindsImportedCuratedImage`; the whole
  `internal/runtime/containerd` Linux test binary passes (10 tests, none skipped).
- **Revert check**: `git revert --no-commit b6a0c3a` reverts the test together with the fix (one commit), so the
  reverted tree has no `TestIssue493_…` to run; the overlay run above is the meaningful "fails without the fix"
  evidence. `git reset --hard b6a0c3a` restored the worktree; it is clean at `b6a0c3a`.
- **Mutants** (overlay, Linux test binary), both killed by the regression test with the same import error:
  - M1: the guard `named.String() != ref` flipped to `==` (the retry never runs for a short ref).
  - M2: the retry looks up `ref` instead of `named.String()`.
- **Cause, not symptom**: no timeout, retry loop or swallowed error. A non-NotFound error from the first lookup
  still maps through `mapErr`; a ref that is already normalized (an `ImageOverride` such as `ghcr.io/...`) skips
  the second lookup because `named.String() == ref`; a parse error leaves the original NotFound in place, so the
  import and pull fallbacks behave as before.
- **Unpack check still applies** to the image found under the normalized name, so the #456 fix (unpack on the
  present-image branch) now covers curated refs, as the issue's reference to #456 expects.
- **Test harness tightened, not weakened**: `oneImage.Get` now matches names exactly and returns an `errdefs`
  NotFound otherwise, which is how containerd's image store behaves. The existing tests in the package
  (including `TestIssue456_CreateUnpacksPresentImage`) still pass with it.
- **Reuse**: the normalization uses `github.com/distribution/reference`'s `ParseDockerRef`, the same library and
  function containerd v2 uses to normalize refs. It was already in the module graph as an indirect dependency; the
  commit only promotes it to a direct requirement (`go mod tidy -diff` is clean, `go.sum` unchanged). No helper
  in the repository already normalizes image refs (`embedimg.TarForImageRef` strips prefixes for a tar lookup,
  which is a different job), so nothing is duplicated.
- **Conventions**: errors still go through `mapErr`/`fault`; imports at top level; the one added comment states the
  non-obvious why (exact-match store vs normalized import name); the stale comment at the import site was updated
  rather than left contradicting the new code.
- **Scope**: every hunk serves the issue. No Accepted or Implemented ADR file is touched; the change conforms to
  ADR-0054 (curated images come from the embed, never a registry pull) and makes it cheaper.
- **Checks** (touched packages): `go vet` (GOOS=linux) clean; `go build ./...` for GOOS=linux clean; host
  `golangci-lint run ./internal/runtime/...` 0 issues; host `go test -race ./internal/runtime/...` all ok.
- **Commit shape**: `fix(runtime):` subject, Cause/Fix/Test body, `Fixes #493`, attribution trailer, one issue.

### Not run here (the group gate runs them)

- The Linux test run used `CGO_ENABLED=0`, so the containerd package's `_linux.go` tests ran without `-race`
  (a cross-compiled race build is not possible from the macOS host); the host `-race` run covers the package's
  non-Linux files only. Linux lint, the e2e suite and the Lima lanes were not run. Attribution: env.

### Definition of Done

10 of 10 applicable items hold (item 8 counted on the touched-package checks above; Linux `-race`, Linux lint,
e2e and the lane are left to the group gate). Item 4 holds through the overlay and the two mutants; the commit's
own revert removes the test with the fix.

### Model scorecard

claude-opus-5-5 — pass; 0 blockers, 0 majors, 1 minor (model). A small root-cause fix that reuses the library
containerd itself uses, with a harness that now models the real store's exact-match semantics.

### Recommendation

Pass. Optionally strengthen the regression test with a direct "Import was not called" assertion so it does not
depend on the placeholder embed being an invalid archive. Hand back to `/fix` Step 8.
