## Verdict: changes-requested — 0 blockers, 1 major, 1 minor  (ADR-0294 implementation, model: claude-opus-5-5)

This review covers ADR-0294's part of b7e6798e on `fix/limits-followup`, plus d15b6bd0 (nodiscard), 4e9baf12 (direct
I/O) and the duckdb lane commits (e238d0ed, 17d8b6e5, cd905b9b). The checks ran in a worktree detached at a7cccf39.
`scripts/agent/gate.sh` runs once on the branch, in the integration session, and did not run here.

### Checks run
- `go build ./...`, on darwin and with `GOOS=linux`: ok.
- `go test -race -count=1` on `internal/runtime/containerd`, `internal/provider`, `internal/services/lake`,
  `internal/platform/config`, `pkg/funcd` and the other packages the branch touches: ok.
- `images/runtime/duckdb`: `python3 -m unittest shim_test`, 59 tests ok.
- `just test-volumes-root` in funcd-linux-tests, as root: `TestLoopfsAsRoot`, `TestRemoveLeftoverScratchAsRoot` and
  `TestBootDirTmpfsAsRoot` pass. The first checks the image's blocks and the loop device's `dio` = 1.
- `go vet` on the touched packages, plain, `-tags dev`, `-tags e2e` and `GOOS=linux` (the sandbox probe too): ok.
  `golangci-lint run`, on darwin and with `GOOS=linux`: 0 issues.
- A mutant, with `go test -overlay`: `LakeScratchBytes` accepting a set size below twice the cap plus 512MiB fails
  `internal/platform/config`'s tests.
- A probe test on the replace path, added by overlay and not committed. It shows the Major below.
- The Lima lanes passed in the integration session: every lane on b7e6798e, then duckdb, volumes and app-todo on
  cd905b9b. That covers the volumes lane's V10 case, the duckdb lane's `lake-scratch-bounded` and the sql lane's
  read-only root.

### Major
- **A replaced Lake engine's scratch stays on disk while the floor refuses the new one** (`model`).
  - Decision 6 says the image is deleted after the engine's teardown. `Converge` retires a terminal, aged or changed
    engine (`internal/provider/runtime.go:181`, `:210`) and then calls `makeScratch` (`:223`). `makeScratch` asks the
    floor for the new scratch's size (`internal/provider/scratch.go:80`) before `loopScratch.Make`, the only step that
    removes the retired engine's image.
  - When the floor refuses, the retired image stays. Its blocks count as used in the floor's next reading, so each
    later pass is refused the same way.
  - The window is a data filesystem whose free space is between `storage.minFreeSize` and that plus
    `lake.scratchSize`: 2 GiB to 3 GiB by default. In it, a Lake whose engine ended, whose key pair aged (ADR-0270
    `MaxAge`) or whose spec changed stays `ScratchSpaceLacking` until an operator frees space. Yet the restart needs no
    more disk than the Lake already held.
  - The probe shows it with the fake scratch: after the engine fails and the floor refuses, three `Converge` passes
    leave `Removed` empty.
  - Direction: remove the retired engine's scratch before asking the floor (in `makeScratch`, or after each `retire` of
    an engine with a scratch). Add a test: the floor refuses a restart, the retired image is removed, and the next pass
    makes a new one once the floor admits it.

### Minor
- **The sql lane checks the spec, not a write** (`model`). The `sql-engine-read-only-root` scenario says a write to the
  engine's `/etc` fails with "read-only file system". The case "SR the engine's root is read-only"
  (`e2e/sql.venom.yml:120`) reads the container's spec instead: `root.readonly` and a `/tmp` tmpfs. It also does not
  carry the scenario's name, so a search for `sql-engine-read-only-root` finds no test. The kernel enforces a read-only
  spec, and V10 shows a real write failing in a Function's worker, so this is coverage and naming, not a defect. An
  `exec` that writes `/etc/x` in the engine would match the scenario.

### ✅ Verified correct (keep)
- **The sandbox** (Decisions 1-3, `sandboxOpts`):
  - A read-only root.
  - A `/tmp` tmpfs (`nosuid`, `nodev`, `mode=1777`) of the memory limit, 512 MiB without one.
  - `HOME=/tmp` unless the spec sets it, on a copy of the env.
  - The seccomp profile still applied last.
  - `TestScenarioWorkerRootReadOnly` and `TestScenarioTmpCountsInMemory` cover Function, Lake and SQL workers, and the
    pool case.
- **The boot directory** (Decision 4):
  - A 1 MiB tmpfs (`nosuid`, `nodev`, `noexec`, mode 0777) mounted before the container is created.
  - `removeBootDir` replaces every `os.RemoveAll` of a boot dir: the failed create, `stop` and `discard`.
  - `BootSweep` detaches what `/proc/self/mountinfo` lists under the boot root, deepest first, before it removes the
    namespaces' parents.
- **The log directory** (Decision 5) is mounted read-only. `TestLogDirReadOnly` dials its socket, and the funclog lane,
  which passed, carries a Function's logs through it on containerd.
- **The scratch** (Decision 6):
  - `ScratchSpec` and the floor in the provider runtime's deps.
  - One replica only.
  - A new image at each engine start, removed on a failed create and at teardown.
  - `RemoveLeftoverScratch` lists both roots, so an image without a mount point and a mount point without an image
    both go.
  - The Lake reconciler holds the Lake not Ready with `ScratchSpaceLacking` on the floor's refusal and polls again
    (`internal/services/lake/reconcile.go:154`).
  - `FUNCD_LAKE_CATALOG_MAX_BYTES` is set only with a scratch.
- **The spill limit**: the shim reads `statvfs` before it connects or recovers the catalog. It subtracts twice the cap,
  keeps a smaller `DUCKDB_MAX_TEMP_DIRECTORY_SIZE`, and exits 2 naming `lake.scratchSize` at zero or less. The duckdb
  lane shows a spilling query refused at the limit and the next commit answered.
- **The config**: `DefaultLakeCatalogMaxSize` is 256MiB. `LakeScratchBytes` returns the larger of 1GiB and 2 × cap +
  512MiB when empty, and refuses a set value below that sum. `CheckLake` runs it at load.
- **loopfs** (d15b6bd0, 4e9baf12): `-E nodiscard` keeps the preallocated blocks, and the `direct-io` loop option makes
  the device skip the second page cache. Both are tested as root. Direct I/O applies at every mount, and nodiscard to
  every image made from now on. No deployed node holds an older image.
- **Docs**: `docs/install.md` says where code may write after the upgrade and what the 256MiB default means for a
  larger catalog. `examples/funcdconfig.yaml` documents `scratchSize` and the new `catalogMaxSize` default. ADR-0293
  carries its "Superseded in part by" back-link. The text describes behavior only.
- **Tracking**: the ADR is at `Reviewing` and its substance is unchanged since acceptance. F210 is `reviewing`, and the
  blueprint names the read-only root and the scratch.

### Definition of done
7 of 8. That is the three Review checklist items, plus the done-when line's per-change checks: the tests with `-race`,
the root tests, build, vet and lint. The third checklist item fails on "removed after it stops" for the replace path
(the Major).

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0294 (implementation) → changes-requested, 0/1/1, 2 model-attributed, DoD 7/8. See
docs/reviews/model-scorecard.md.

### Recommendation
Fix the Major on the branch, with its test, and send it back for a second round. The Minor can ride along. Hold every
stamp of the batch until then, so ADR-0294 to ADR-0297 and F210 to F213 move to `Implemented` in one commit.
