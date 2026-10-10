## Verdict: pass — 0 blockers, 0 majors, 3 minors  (ADR-0204 implementation, round 2, model: claude-opus-5-5)

Scope: `88804658` ("fix(backup): address review of the ADR-0204 implementation"), which answers the two model Minors
of round 1. The three ADR-0204 commits under it were rebased onto `cf4bc0e9` (#890); `git range-diff` shows each
identical to the commit round 1 graded (`4f812692 = 62f298c0`, `be3d9c5b = 1472e7bb`, `b4673576 = f1ed3ec5`), so
round 1's findings on them stand.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| new tests | `go test -race -count=1 -v -run 'TestFindThroughSymlinks\|TestFindByContent' ./internal/backup/escrow/` | both PASS |
| new tests | `go test -race -count=1 -v -run TestBackupEncryptionCheckedAtStart ./cmd/funcd/` | 5/5 subtests PASS |
| revert check | `-overlay` with `git show 88804658~1:internal/backup/escrow/escrow.go` | `TestFindThroughSymlinks` FAIL (escrow_test.go:229, the symlinked key is not found) |
| loop probes | `-overlay` adding a probe test (not committed): nested loops (`x/y → secrets`, `z → x`), a link to the escrow root's parent, a mutual `a ↔ b` link, a self-linked `master/`, a link out of the escrow dir | all end in milliseconds; `a`, `b` and the self-link are named as `unresolved symlink (… too many levels of symbolic links)`; a file reached through two links is listed once |
| path containment | same probe | every path in the `found` list and the returned match is lexically under `<escrow>/<sub>`, also for a key reached through a link out of the escrow dir |
| error kind | same probe, a `0000` subdirectory | `fault.Invalid` "read escrow directory …", wraps `os.ErrPermission` (as before the change) |
| regression | `go test -race -count=1 ./internal/backup/... ./cmd/funcd/` | all `ok`, exit 0 |
| vet | `go vet ./internal/backup/... ./cmd/funcd/`, darwin and `GOOS=linux` | exit 0 both |
| lint | `golangci-lint run` on the same packages, darwin and `GOOS=linux` | `0 issues.`, exit 0 both |
| leaks | the commit's diff and message grepped for an absolute-path prefix and the local username | no hit |
| tree | `git status --short` | clean (overlays only, the work tree untouched) |

Mutants (`go test -overlay`, the committed tests plus the probe):
1. `os.Stat` → `os.Lstat` in `visit` → `TestFindThroughSymlinks` FAIL. Killed.
2. the `os.Lstat` dangling-link branch removed → `TestFindThroughSymlinks` FAIL ("a symlinked key is listed"). Killed.
3. the `s.seen[resolved]` check removed → the committed `TestFindThroughSymlinks` **passes**; the probe's nested
   loops run past the 60 s test timeout. Survives the committed test (Minor 1).

### Minor

- **The committed test does not pin the loop guard** · attribution: model · `internal/backup/escrow/escrow_test.go:218-254`.
  The test comment says "a symlink loop is read once", but nothing counts the reads. With the `s.seen[resolved]`
  check removed, the single `secrets/loop → secrets` link still ends, because the kernel's symlink limit stops
  `os.Stat` at about 15 nested `loop/` components, and the asserts (`NotFound`, the other key's fingerprint, the
  dangling link) all hold; the error then lists the other key 15 times. Two loop links make the walk exponential: the
  probe ran past 60 s. The guard itself is correct (probes above). Fix: assert
  `strings.Count(err.Error(), envelope.Fingerprint(other)) == 1`, or add a second loop link; the count assert was
  checked here, passes on the fix and fails on mutant 3 (count 15).
- **Contracts and dependency list drift from what was built** · attribution: adr · carried from round 1, unchanged:
  `envelope.Config.KeyPrefix`/`.Logger`, `funcd.WithMasterSecret`, `s3gateway.MasterPath`, the seal-mismatch refusal
  and `Sealer.Keys()` de-duplication are not in the Contracts; the go.mod list names modules age v1.3.2 does not pull
  and omits the MVS raises. Carry into ADR-0205/0206.
- **FEAT-0009 F109 cell still `encryption: accepted`** · attribution: env · carried from round 1 ·
  `docs/feat/0009-feat-disaster-recovery.md:59`. ADR-0204 reads `Reviewing`; the stamp of this pass must set the cell
  to `implemented` (rule 6).

### ✅ Verified correct (keep it)

- **Round 1 Minor 1 (symlinks) fixed**: `escrow.Find` stats every entry through symlinks, reads each directory once
  by its `filepath.EvalSymlinks` path, names a link that does not resolve in the `NotFound` message instead of
  dropping it, and keeps the error split: a missing root is `NotFound` "found: none", any other read error is one
  `fault.Wrapf(…, fault.Invalid, …)` at `Find`. Recursion is bounded by the number of distinct resolved directories;
  FIFOs, sockets and devices are skipped as before. Reported paths are built from the escrow root, so they never name
  a link's target. `examples/backup-escrow.md` says the lookup follows symlinks.
- **Round 1 Minor 2 (daemon test) fixed**: `TestBackupEncryptionCheckedAtStart` has 5 subtests; the two refusals
  assert `fault.Invalid`, and the `none` refusal names both `backup.encryption.none` and `secrets.encryptionKeyFile`;
  `none` with a secrets key starts and logs the `level=WARN` "backup encryption is off" line (scenario
  plaintext-secrets-refused; "stores no plaintext Secret" stays covered at the envelope level, round 1); the
  comma-separated `FUNCD_BACKUP_ENCRYPTION_RECIPIENTS` overlay loads 2 files, starts and logs both recipients'
  fingerprints. A started daemon's executor is closed in `t.Cleanup`.
- **Conventions**: two short doc comments (`search`, `visit`) state the why (the loop guard, unresolved links); no
  narration; the one `//nolint:gosec` keeps its reason; imports unchanged at top level.

### Definition of Done

7 / 7 items, unchanged from round 1 and re-checked where the fix touches them: the touched packages green under
`-race`, vet and lint clean on darwin and linux, no identity or path leak. `just ci` and the repo-wide runs are left to
the per-PR gate.

### Model scorecard

Not recorded by this gate (the DR session records the ledger rows). Row below.

### Recommendation

Pass. Both round 1 model Minors are fixed and proven by running. The one new Minor is test strength only (the loop
guard works; the test does not detect its removal) and can ride the next escrow change. On this pass, stamp ADR-0204
`Reviewing → Implemented`, set the F109 encryption cell to `implemented`, and move its board card to Done.

### Ledger row

```json
{
  "date": "2026-10-10",
  "adr": "0204",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 3,
  "model_attributed": 1,
  "dod_passed": 7,
  "dod_total": 7,
  "report": "docs/reviews/adr-0204-implementation-claude-opus-5-5-2.md",
  "notes": "round 2 pass; round 1 model minors fixed: escrow.Find follows symlinks (revert check fails TestFindThroughSymlinks; loop probes end, paths stay under the escrow dir), start-rule test split into 5 subtests with fault.Invalid and the env overlay; -race green on internal/backup/... and cmd/funcd, vet/lint darwin+linux clean; 2/3 mutants killed; Minor(model): the loop guard mutant survives the committed test (no read-once count); Minor(adr) and Minor(env) carried from round 1"
}
```
