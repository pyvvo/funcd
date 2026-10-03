## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #456 fix, model: claude-opus-5-5)

Change: `9e33ddb fix(containerd): unpack an image already in the namespace before creating a worker`
(`internal/runtime/containerd/containerd_linux.go`, `internal/runtime/containerd/snapshotter_linux_test.go`).

The package is Linux-only (`//go:build linux`), so every test run below is a `GOOS=linux GOARCH=arm64
CGO_ENABLED=0` test binary built through the pinned dev shell and run in an Alpine container.

### 🟡 Major
None.

### Minor
- **Hand-rolled in-memory label store** · attribution: `model` · `snapshotter_linux_test.go` adds
  `memLabels` (a `local.LabelStore` over a bare map) so `local.NewLabeledStore` accepts the labels that
  `Unpack` writes. containerd v2.3.1, already a dependency, ships this:
  `core/images/imagetest.NewContentStore(ctx, t)` returns a labeled local content store backed by a
  mutex-guarded in-memory label store, plus `Blob`/`JSONObject`/`Manifest` helpers. The local copy has
  no mutex, so a future multi-layer test could race on it. Test-only and small, so Minor. Fix: build the
  content store with `imagetest.NewContentStore` and drop `memLabels`.
- **`-race` not run on the Linux test** · attribution: `env` · `go test -race` needs cgo, and this host
  has no Linux cgo toolchain or Go image (`go: -race requires cgo`). The Linux test passed without
  `-race`; the host `-race` run of the package passed, but it compiles none of the Linux-only files. The
  group gate or CI must cover `-race` on Linux.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** Overlaying the `origin/main` `containerd_linux.go`:
  `TestIssue456_CreateUnpacksPresentImage` FAILS with `create container "issue456-1.r0": parent
  snapshot sha256:… does not exist: not found`. This is the missing-parent `Prepare` that the issue
  describes. The harness's `git revert --no-commit 9e33ddb` reverted the test as well (one commit), so
  that run had no `TestIssue456` to run and passed vacuously. The overlay is the meaningful check. The
  worktree was reset to `9e33ddb` and is clean.
- **Passes with the fix**: `TestIssue456_CreateUnpacksPresentImage` and `TestIssue370_…` PASS, and the
  whole package passes on Linux. No test was skipped.
- **Mutants (3/3 killed)**: (1) inverting `!unpacked` fails TestIssue456 and TestIssue370. (2) Calling
  `IsUnpacked` against `"overlayfs"` instead of `d.snapshotter()` fails both. (3) Calling `Unpack` into
  `"overlayfs"` fails TestIssue456 with the same missing-parent error.
- **Cause, not symptom**: the present-image early return now checks `image.IsUnpacked(nctx,
  d.snapshotter())` and calls `Unpack` when needed, as the issue's Expected behavior asks. This matches
  the import path (`Unpack` after `Import`) and the pull path (`WithPullUnpack` +
  `WithPullSnapshotter`). Errors are not swallowed: both `IsUnpacked` and `Unpack` failures go through
  `mapErr(…, "unpack image %q", …)`. There is no retry, timeout or skip.
- **Scope**: two hunks in production code (the resolve logic and its doc comment). The test hunks are the
  regression test plus the fakes it needs. `memSnapshotter` gains `Stat` and `Commit`, and its `Prepare`
  accepts an empty parent for the base layer. The `TestIssue370` behavior is unchanged and that test still
  passes. No test was weakened.
- **Reuse**: the test extends the existing #370 harness (`memSnapshotter`, `oneImage`, `memContainers`,
  `writeBlob`, `createdTasks`, `attachedCNI`) instead of adding a second one. The only duplicate is the
  label store above.
- **Conventions**: ctx-first, errors through the package's `mapErr` and `api/fault`, imports at the top
  level. The new comment states the why (the image record outlives a failed unpack, and
  `WithNewSnapshot` does not unpack). No `any` was added to a signature.
- **ADRs**: consistent with ADR-0054 (no registry pull for curated images, since the present-image path
  only unpacks local content). No ADR file was touched.
- **Checks (touched package)**: host `go test -race` ok, host and `GOOS=linux` `go vet` clean,
  golangci-lint `0 issues` on the host and on Linux.
- **Shape**: subject `fix(containerd): …`, the body names the cause and the regression test, `Fixes #456`,
  and the attribution trailer is present. One issue, one commit.

### Definition of Done
9 / 11 items hold. Misses: #3 (`-race` on the Linux test was not runnable here · `env`) and #10 (reuse:
the hand-rolled label store duplicates containerd's `imagetest` · `model`, Minor). #8 holds for the touched
package (build, vet, lint on the host and Linux, and tests). The e2e suite and lanes are left to the group
gate. The user-visible repro (a VM with a failed unpack or a changed snapshotter) was not rerun. The unit
test reproduces the same code path end to end through `Create`.

### Model scorecard
claude-opus-5-5 on issue #456 (fix) → pass, 0/0/2, 1 model-attributed, DoD 9/11 (ledger row returned to
the orchestrator; not recorded here).

### Recommendation
Ship. Optionally swap `memLabels` for `imagetest.NewContentStore` in a follow-up. Make sure the group
gate or CI runs this package's tests under `-race` on Linux.
