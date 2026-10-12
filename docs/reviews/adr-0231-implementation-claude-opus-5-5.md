## Verdict: changes-requested — 0 blockers, 1 major, 3 minors  (ADR-0231 implementation, model: claude-opus-5-5)

Scope: the uncommitted working tree of `feat/sql-provider` against `HEAD` (`e48ab715`). Production changes are in
`internal/provider/spec.go`, `internal/provider/runtime.go`, the new `internal/provider/storage.go`,
`internal/catalog/devengine/devengine.go`, `internal/runtime/containerd/containerd_linux.go`, `pkg/funcd/funcd.go`,
`pkg/funcd/options.go` and `cmd/funcd/main.go`. The tests are the new `internal/provider/storage_test.go` and one
test in `cmd/funcd/main_test.go`.

**Not stamped.** The ADR stays `Reviewing` and the FEAT-0011 F127 row stays `reviewing` until Major 1 is fixed.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...` | exit 0 |
| touched packages, race | `go test -race -count=1 ./internal/provider/ ./internal/services/catalog/` | 2 × `ok`, exit 0 |
| devengine, dev tag | `go test -race -count=1 -tags dev ./internal/catalog/devengine/` | `ok`, exit 0 (without the tag the package has no files) |
| cmd/funcd | `go test -race -count=1 -run 'TestScenarioMemoryModeStorageIsEphemeral\|Containerd\|CrashRecovery' ./cmd/funcd/` | `ok`, exit 0 |
| scenario tests | the 5 storage tests in `internal/provider`, `-v` | 5 × PASS, all 5 subtests of the refusal test PASS |
| vet | `go vet` on provider, cmd/funcd, pkg/funcd, services/catalog; `-tags dev` on devengine; `GOOS=linux go vet` on `internal/runtime/containerd`, provider, cmd/funcd | exit 0 (all runs) |
| lint | `go tool golangci-lint run` on the four packages; `--build-tags dev` on devengine | `0 issues.`, exit 0 × 2 |
| gofmt | `gofmt -l` on the changed packages | **`internal/provider/runtime.go`** (Major 1) |
| catalog tests unchanged | `git diff HEAD --stat -- internal/services/catalog` | empty |

### 🔴 Blocker
None.

### 🟡 Major

1. **`internal/provider/runtime.go` is not gofmt-clean** (`model`). Adding `storageRoot` re-aligned only the last three
   fields of `engineRuntime` (`runtime.go:58-63`) and of its literal in `NewRuntime` (`runtime.go:89-94`); `gofmt -d`
   re-aligns `rt`, `ownerKind` and `gateway`. golangci-lint does not catch it, but `just ci` runs `go fmt ./...` and
   then fails on any changed `.go` file (`justfile:456-457`), so the ADR's "the gate passes" does not hold once the work
   is committed. Fix: `gofmt -w internal/provider/runtime.go`.

### Minor

1. **The chown is never asserted** (`model`). Implementation plan step 3 says the chown is checked when the test runs as
   root; no test reads the leaf's owner, so `os.Lchown` (`storage.go:527`) is untested on a root CI runner too. A
   root-only subtest that compares `Stat_t.Uid/Gid` with a foreign uid would cover it.
2. **The memory-mode scenario tests the helper, not the exit path** (`model`). `TestScenarioMemoryModeStorageIsEphemeral`
   calls `providerStorageRoot` directly; that `containerdOptions` joins its closer into the closer `main` runs on exit
   (`main.go:1401-1408`) is shown only by reading. The plan asked for this scenario "with fakes for the containerd
   path", which `crashrecovery_test.go:308` already does for `containerdOptions`.
3. **The status bump rewrote the status line's history** (`model`). The `Accepted` line's summary of the two judge
   rounds was dropped in the `Accepted → Reviewing` edit. It is the status line, not substance, but the bump should
   only add to it.

### ✅ Verified correct (keep)

- Contracts match the ADR exactly: `StorageSpec`, `ProviderSpec.Storage`, `Deps.StorageRoot`, `provider.Volumes`
  with `Reclaim`/`List`, `WithProviderStorage`; `var _ Volumes = (*engineRuntime)(nil)` (`storage.go:472`).
- `prepareStorage` runs before `namedInstances` in `Converge` (`runtime.go:115-119`), so every refusal (no root,
  `Replicas != 1`, relative path, invalid names, symlink or non-directory leaf via `Lstat`, foreign uid on a non-root
  daemon) is `Invalid` before any container; the refusal test asserts `rt.created` stays empty for each.
- The path is `<root>/<lower(kind)>/<ns>/<name>` with both parts validated as DNS labels; mode 0700 and the chown on
  the leaf only (`Lchown`, no recursion).
- `Teardown` and `retire` never touch the directory; `Reclaim` refuses with `Conflict` while a container exists, is
  idempotent, and `List` is sorted — all asserted.
- A spec without storage gets `Mounts: nil`, the same `WorkerSpec` as before (`TestConverge_noStorageNoMount`).
- Read-write binds gain `nosuid,nodev` (`containerd_linux.go:1237`); `devengine` refuses `Storage` with `Invalid`; the
  process path sets no root; the memory-mode root is a temp dir removed after `mgr.Close()`.
- No scope creep: no owner wiring beyond the catalog's runtime constructor, consistent with Decision 4 belonging to the
  storage-using providers (ADR-0232).

### Recommendation

Back to `adr-impl` for Major 1 (one `gofmt -w`); Minors 1 and 2 are cheap to add in the same pass. Re-review is a
gofmt check plus the provider tests; then stamp ADR-0231 `Implemented` and F127 `implemented` (infra row, no card).
