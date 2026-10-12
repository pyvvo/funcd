## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0231 implementation re-review, model: claude-opus-5-5)

Re-review of the working tree of `feat/sql-provider` after the first review
([adr-0231-implementation-claude-opus-5-5.md](adr-0231-implementation-claude-opus-5-5.md), changes-requested). The
untracked ADR-0232 files in the same tree (`cmd/funcd-rqlite`, `internal/services/sql/engineargs`,
`images/runtime/rqlite`) are out of scope.

**Stamped**: ADR-0231 `Reviewing → Implemented`, FEAT-0011 F127 `reviewing → implemented` (infra row, no board card).

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| gofmt | `gofmt -l cmd internal pkg api` | empty, exit 0 |
| touched packages, race | `go test -race -count=1 ./internal/provider/ ./internal/services/catalog/`; `-tags dev ./internal/catalog/devengine/` | 3 × `ok`, exit 0 |
| cmd/funcd, race | `-run 'TestScenarioMemoryModeStorageIsEphemeral\|CrashRecovery\|Containerd' -v ./cmd/funcd/` | 5 × PASS, `ok` |
| chown as root | the `internal/provider` test binary cross-compiled for Linux, run as uid 0 in an alpine container | `TestConverge_chownsTheLeafAsRoot` PASS, and the 5 other storage tests PASS |
| vet | `go vet` on provider, cmd/funcd, pkg/funcd; `GOOS=linux go vet` on provider, cmd/funcd, `internal/runtime/containerd` | exit 0 |
| lint | `go tool golangci-lint run` on provider, cmd/funcd, pkg/funcd | `0 issues.` |

### What the first review's findings became

- Major 1 (gofmt): fixed; `runtime.go` is aligned.
- Minor 1 (chown unasserted): `TestConverge_chownsTheLeafAsRoot` checks the leaf's uid and gid (65532) and that the
  parent stays root-owned. It skips when the tests run as non-root, which CI's runner and `just test-linux` do, so the
  root run above is the check that was made.
- Minor 2 (exit path): the scenario now lives in `cmd/funcd/providerstorage_test.go` (`unix` tag). It drives
  `containerdOptions` with the fake manager and runtime under a scratch `TMPDIR`, finds exactly one
  `funcd-providers-*` root, and checks that `closeExec` removes it.
- Minor 3 (status history): the status line again carries the judge-round summary. The ADR diff touches only the status
  line.

### ✅ Verified correct (keep)

Everything listed in the first review still holds. The contracts match the ADR, every refusal returns `Invalid` before
any container is created, `Lstat` checks the leaf and only the leaf is chowned, `Reclaim` returns `Conflict` and is
idempotent, a spec without storage still gets no mount, read-write binds carry `nosuid,nodev`, `devengine` refuses
storage, and the process path sets no root.
