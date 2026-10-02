## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #41 fix, model: claude-opus-5-5)

Change: branch `fix/i41`, commit 438bc7d `fix(workernode): give every function a local API socket that fits the
Unix path limit` (`internal/workernode/local/manager.go`, `internal/workernode/local/manager_test.go`,
`pkg/funcd/funcd.go`, `pkg/funcd/options.go`, `pkg/funcd/funcd_test.go`, `cmd/funcd/main_test.go`; 76 insertions,
4 deletions).

The issue: `Manager.SocketFor` named the socket `<dataDir>/invoke/<namespace>-<name>.sock` with no length check.
Valid names (63 characters each) or a long dataDir pushed the path over the AF_UNIX limit, `net.Listen` failed
with `bind: invalid argument`, the reconciler only logged a WARN, and the Function went Ready without
`FUNCD_INVOKE_SOCKET`, so every `context.kv` and `context.invoke` call returned HTTP 500.

The fix has two parts. `sockName` now returns a fixed-length name (the hex of the first 10 bytes of the SHA-256 of
the caller key, plus `.sock`, 25 bytes), so the socket path length depends on the directory alone. A new
`local.CheckDir` checks that length once against `len(syscall.RawSockaddrUnix{}.Path) - 1`, and
`Platform.buildControlPlane` calls it, so a directory that is too long fails `New` with `fault.Invalid` at startup.
This is the first option in the issue's expected behavior: the daemon validates at startup.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **The boundary of `CheckDir` is not tested** (attribution: `model`). The mutant `n > maxSocketPath+1` (it accepts
  a path one byte too long) survives every test in `internal/workernode/local` and `pkg/funcd`. The limit itself is
  correct: on macOS, Go's `net` rejects a path of 104 bytes or more, so 103 is the longest that binds. No test
  checks a directory exactly at the limit and one byte over it. A table test with a directory of length
  `maxSocketPath - 25` (it passes) and `maxSocketPath - 24` (it fails) would close the gap. The mutants on the
  key lines (the hash name, the startup check) are killed.

### ✅ Verified correct (keep it)

- **The regression tests fail without the fix, for the issue's reason.** With `git revert --no-commit 438bc7d` and
  the two new tests restored, `go test -race -run TestIssue41` fails both:
  `TestIssue41_MaxLengthNamesGetALocalAPISocket` fails in `SocketFor` with `listen unix …/nnn…-fff….sock: bind:
  invalid argument`, which is the issue's error. `TestIssue41_RejectsInvokeSocketDirOverUnixLimit` fails with
  `An error is expected but got nil`. After `git reset --hard 438bc7d` the worktree is clean at that HEAD.
- **They pass with the fix under `-race`.** `go test -race -count=1` passes for `internal/workernode/...`,
  `pkg/funcd`, `cmd/funcd` and `internal/function`. The first test dials the socket for a 63-character namespace
  and a 63-character name, which is the exact trigger from the issue.
- **Mutants: 3 of 3 on the key lines killed.**
  - M1: `sockName` returns the old `<ns>-<name>.sock` name. This fails `TestIssue41_MaxLengthNamesGetALocalAPISocket`.
  - M2: the `CheckDir` limit is raised by 200 bytes. This fails `TestIssue41_RejectsInvokeSocketDirOverUnixLimit`.
  - M3: the `CheckDir` result in `buildControlPlane` is ignored. This fails `TestIssue41_RejectsInvokeSocketDirOverUnixLimit`.
  - M4 (a boundary probe, not a key line) survived. See the Minor.
- **The root cause is fixed, not masked.** The fix adds no retry and no longer timeout, and it hides no error.
  After the fix, the name part of the path has a constant length, so no namespace and name pair can push it over
  the limit. A directory that is too long is now a startup error, not a WARN for each function.
  `CheckDir(dir)` measures `filepath.Join(dir, sockName(""))`, which is the same join that `SocketFor` uses, so the
  check and the bind cannot disagree. The 80-bit hash makes a name collision between two functions negligible.
- **The production path is covered.** Both the process mode (`addInvokeSocket`) and the containerd mode
  (`internal/function/function.go:1442`) get the host path from `SocketFor`. Containerd bind-mounts the socket at
  the fixed in-container path `containerInvokeSocket`, so the new host file name does not reach the shim. No code,
  test, ADR or blueprint text depends on the old `<ns>-<name>.sock` name.
- **The limit is portable.** `syscall.RawSockaddrUnix{}.Path` is 104 bytes on darwin and 108 on Linux. The package
  also builds with `GOOS=windows`. Reserving the NUL byte on Linux as well is the conservative choice for the
  Node and Python shims that dial the socket.
- **Scope.** Every hunk serves the issue. The `cmd/funcd/main_test.go` change moves
  `TestScenarioFileSetsAddresses` to a short `os.MkdirTemp` dataDir, because its `t.TempDir()` path is over the
  macOS limit. The new startup check found this. The change does not weaken the test: it still asserts the same
  addresses. The `WithInvokeSocketDir` doc states the new `fault.Invalid` result.
- **Reuse.** No socket path length check existed in the repository before this change (no other use of
  `RawSockaddrUnix` or `sun_path`). The fix takes the limit from the standard library's `syscall` type instead of
  hard-coding 104 or 108. It uses `crypto/sha256` and `encoding/hex` from the standard library and adds no
  dependency. The test reuses the package's existing `fakeStore` and `fakeInvoker`.
- **Conventions.** The error is `fault.Invalidf` with an operation name and is wrapped with `fault.Wrapf`, which
  keeps its kind (ADR-0002). Imports are at the top level. The comments state the why (the NUL byte, the fixed
  length, issue #41) and do not narrate the code. Naming fits the package.
- **ADRs.** ADR-0064, ADR-0069 and ADR-0121 do not fix the socket file name, and the fix does not contradict them.
  No ADR file was edited.
- **Checks (touched packages).** `gofmt -l` reports no files. `go vet` passes. `golangci-lint run` reports
  `0 issues`. The tests pass under `-race`, as listed above. The group gate runs the Linux lint, the e2e suite
  and the lanes.
- **Shape.** The subject is `fix(workernode): …`, the body has `Fixes #41` and the attribution trailer, and the
  commit fixes one issue.

### Recommendation

Pass. The boundary test in the Minor is a small follow-up. It is not needed for this fix. One related point is
outside the scope of the issue: the reconciler still logs a WARN and continues for any other `SocketFor` failure
(for example, a directory it cannot create). The length cause is gone, but a Function condition for a failed local
API socket remains a possible separate issue.
