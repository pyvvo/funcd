# Fix review — issue #370 (containerd Create prepares the worker snapshot on the default snapshotter)

- **Producing model**: claude-opus-5-5
- **Change**: branch `fix/i370`, commit 4d5c56a `fix(runtime/containerd): create workers on the configured snapshotter`
- **Files**: `internal/runtime/containerd/containerd_linux.go` (+2/-1), `internal/runtime/containerd/snapshotter_linux_test.go` (new), `go.mod` (go-digest and grpc move from indirect to direct)
- **Governing ADRs**: ADR-0011 §3 (Create uses the configured snapshotter), ADR-0054, ADR-0002
- **Verdict**: **pass**

## Summary

The fix names the configured snapshotter on the worker container (`containerd.WithSnapshotter(d.snapshotter())`, placed before `WithNewSnapshot`) and removes a leftover `<id>-snap` from that same snapshotter in `reclaim`. Those are the two sites the issue names. The regression test fails on the pre-fix code for the reported reason, the three mutants each fail it, and the package checks are green.

## Verification run

The test file has the `linux` build tag and the host is darwin. I cross-compiled the package's test binary (`GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c`, with `-overlay` for the pre-fix and mutant variants) and ran each binary in an `alpine` container.

| Check | Result |
|---|---|
| `TestIssue370_CreateUsesConfiguredSnapshotter`, fixed code | PASS |
| Same test, `containerd_linux.go` overlaid from `origin/main` | **FAIL**: `create container "issue370-1.r0": parent snapshot sha256:… does not exist: not found`. The image layer exists only in the configured (`native`) snapshotter, and the pre-fix code prepared the snapshot on `overlayfs`. This is the issue's reason. |
| `git revert --no-commit 4d5c56a` | The revert also removes the test file, so the reverted tree has no `TestIssue370` to run. The overlay run above is the meaningful revert check. Afterwards, `git reset --hard 4d5c56a` left the worktree clean at that HEAD. |
| Mutant 1: drop the `WithSnapshotter` line | FAIL (parent snapshot not found) |
| Mutant 2: `reclaim` back to `SnapshotService("")` | FAIL (`snapshot issue370-1.r0-snap: already exists`). The test seeds a leftover snapshot in `native`, so the reclaim path is covered. |
| Mutant 3: move `WithSnapshotter` after `WithNewSnapshot` | FAIL (parent snapshot not found). The test enforces the option order. |
| Whole package, Linux binary | PASS |
| Host `go test -race ./internal/runtime/containerd/` | ok. The Linux-tagged test is not built on darwin. A Linux `-race` run needs a cgo cross toolchain, so it was not run here; the group gate and CI run it. |
| `go vet` (host and `GOOS=linux`) | clean |
| `golangci-lint run ./internal/runtime/containerd/` (host) | 0 issues |
| `go mod tidy` | no diff, so the go.mod direct/indirect changes are what tidy produces |

The issue's own steps need a Linux containerd host with the `native` snapshotter, so I did not rerun them (not cheap). The in-memory client test exercises the real `containerd/v2/client` `NewContainer` option chain, which is the defective path.

## Blockers

None.

## Majors

None.

## Minors

None.

## ✅ Verified correct

- **The root cause is fixed, not masked.** Both sites from the issue (the `NewContainer` options and the `reclaim` snapshot removal) now use `d.snapshotter()`, the same helper that the image unpack/pull path already uses. The `WithSnapshotCleanup` deletes follow the container record's `Snapshotter` field, which is now set, so cleanup stays on the right snapshotter without further edits.
- **Scope.** Every hunk serves the issue. The go.mod change is the tidy result of the test's direct imports. No test was weakened or deleted.
- **Reuse.** The fix reuses the existing `d.snapshotter()` helper. The package and `internal/testkit` have no in-memory containerd client fakes. The test builds on the library's own `local.NewStore`, `content.WriteBlob` and `containerd.WithServices` rather than reinventing them, and embeds the interfaces so that it implements only the methods `Create` calls.
- **Conventions.** Imports are at the top level, and the only comment is a short why-comment that cites the issue. No YAML is involved. The test does not assemble a platform with `funcd.New`, so `t.TempDir()` is acceptable.
- **ADRs.** The fix realizes ADR-0011 §3 ("configured snapshotter") and contradicts no Accepted or Implemented ADR. No ADR file was edited.
- **Shape.** The subject is `fix(runtime/containerd):`, the body has `Fixes #370` and the attribution trailer, and the commit covers one issue.

## Recommendation

Pass. Hand back to `/fix` Step 8. The group gate should confirm the Linux `-race` run and Linux lint for this package.
