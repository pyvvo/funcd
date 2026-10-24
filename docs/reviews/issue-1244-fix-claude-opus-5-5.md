## Verdict: pass — 0 blockers, 0 majors, 0 minors  (pyvvo/funcd issue #1244 fix, model: claude-opus-5-5)

Branch `fix/1244-durable-artifact-cache` on `origin/main` d9213bed, four commits: 459d2205 (#1244, the artifact cache),
baf5e18a (the platforms cache), 87d8fbbc (the node master secret) and fe8e1d57 (`funcd install`'s binaries), the
last three siblings found while fixing it. Issue: "A power loss soon after a pull empties a Function's cached artifact
for good". The review ran in the session that made the fix, as the decider asked on 2026-10-11 (no review agents);
every claim below comes from a command that ran.

### Routed (not this work's)
- **A file blob store acknowledges a Put before its object reaches the disk** · destination: #1247 (needs-adr, under
  #1245) · evidence: gocloud's fileblob writer closes its temp file without a sync and renames it into place
  (gocloud.dev v0.47.0 `blob/fileblob/fileblob.go:892`), and syncs no directory. A fix that syncs the object, its
  attributes and their directory passes a step-order test, but a Put went from 0.19 ms to 10 ms in a `go test` on
  macOS, and the s3gateway paging tests that seed 10,000 objects went past `go test`'s 10-minute timeout: a policy for
  the store's hot path, so an ADR decides it (`CLAUDE.md`: a fix that adds a durability mechanism needs one).

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.**
  `TestIssue1244_CacheFillReachesTheDiskBeforeItsMarker` traces every durable step of a bundle's cache fill. Over the
  pre-fix `fillCache` (with only the trace seam added) it fails at "the pulled tree is renamed into the cache: []":
  nothing is synced. With the fix it passes under `-race`.
- **Mutants.** Four overlays of `artifact.go`, each failing the test at its own assertion: no tree sync ("the pull dir
  is synced before the rename"), the tree synced after the rename (the same), the artifact dir synced after the marker
  ("the artifact dir is synced between the tree's rename and the marker's"), and the marker written with
  `os.WriteFile` ("the marker is published after the tree").
- **The fix addresses the cause.** `fillCache` now syncs every file and directory of the pulled tree before the rename
  that publishes it, syncs the artifact dir after it, and writes `.pulled` through a temp file that is synced, renamed
  and its dir synced, last. `cachedArtifact` trusts the marker alone, which is now safe: the marker can reach the disk
  only after the tree and its rename did.
- **Siblings, each its own commit and test, each failing on its seam-only baseline:**
  - `<digest>.platforms.json` (ADR-0145's offline restart): `TestIssue1244_PlatformsCacheIsWrittenDurably`.
  - The node master secret, written by `LoadOrCreateMaster`, `MigrateMaster` and a restore's install and rollback
    (ADR-0204): an empty file made `LoadOrCreateMaster` mint a new master, changing every derived key pair and catalog
    token. One function writes it now: `TestIssue1244_MasterSecretIsWrittenDurably` traces it, and
    `TestIssue1244_RestoreWritesTheMasterDurably` checks, on `run.go`'s syntax tree (embedded, so a revert overlay
    reaches it), that both of the restore's writes go through `s3gateway.WriteMaster` and no `os.WriteFile` is left.
  - `funcd install`'s binaries: `present` is `isFile`, so an empty binary was never laid down again:
    `TestIssue1244_LayDownWritesEachBinaryDurably`, with three mutants (a binary, an archive member or the CNI dir
    written plainly) each failing it.
- **Checked, not siblings.** The pool manifest: `NewReconciler` empties its dir at every start and `createPool`
  rewrites it before each pool starts. The lake and SQL embedded engines are extracted with `O_TRUNC` on every call.
  A self-signed TLS pair that does not load is regenerated. `hold`, `procreg` and the upgrade path already sync their
  file and directory. The CNI conflist, resolv.conf, port files and netns pins are rewritten per start or per worker.
  Volume images are best-effort by decision (2026-10-23). The App lock is a funcdctl file in the user's repository.
- **Reuse.** `hold.WriteFile` already wrote durably, but `internal/platform/**` may import no internal package (the
  platform-leaf rule), so the shared code is a new leaf, `internal/platform/durable` (`Disk`: `WriteFile` with a mode,
  `SyncTree`, `SyncDir`, `Rename`, `MkdirAll`), whose `Trace` hook gives each caller an order test; `hold` keeps its
  own two helpers. The trace is the seam the existing tests lacked: a power cut cannot run in `go test`.
- **ADRs.** ADR-0030's cache, ADR-0145's platforms file, ADR-0204's master and ADR-0056's install keep their decisions;
  only their writes became durable. No ADR file was edited.
- **Conventions.** Why-comments naming #1244; `fix(<scope>):` subjects, one commit per defect, `Fixes #1244` on the
  first; the attribution trailer.
- **Checks.** Each touched package's tests with `-race` (`internal/artifact`, `internal/platform/durable`,
  `internal/blob/s3gateway`, `internal/restore`, `internal/runtime/provision`, and `cmd/funcd`'s restore, master and
  backup tests); `go vet` and `golangci-lint` on darwin and linux: 0 issues. `scripts/agent/gate.sh` on fe8e1d57:
  PASS (audit with 0 hard flags, ci-full in 730 s, the Linux build, vet and lint, the Linux tests, stress with the 9
  new or changed tests 20 times each and 0 failed, a clean tree).

### Definition of Done
12 / 12 items hold. Item 13 does not apply: no design model covers these writes.

### Model scorecard
Recorded: claude-opus-5-5 on #1244 (fix) → pass, 0/0/0, 0 model-attributed, DoD 12/12.

### Recommendation
Ready for the PR. The blob store's durability is #1247, for an ADR.
