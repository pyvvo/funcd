# Fix review — issue #320 (model: claude-opus-5-5)

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #320 fix, model: claude-opus-5-5)

Change: branch `fix/i320`, commit f320594 `fix(funcdctl): hot-reload imported modules and funcdctl.yaml
edits in funcdctl dev` (2 files: `cmd/funcdctl/dev.go` +181/−67, `cmd/funcdctl/dev_test.go` +61).
Governing ADR: ADR-0125 (`funcdctl dev`; boot sequence "watch files, re-apply on change", `FUNCD_BUNDLE_DIR`
= working tree "(hot-reload on change)"; hot-reload granularity left to the implementation). ADR-0143
(revision rollout and drain) is what makes a new `spec.imageDigest` load new code.

The issue names the cause: `watchHandlers` → `devHandler.reload` digested only the entry file and re-applied
the same Function; nothing re-read the manifest or re-ran the synthesis. The fix removes that cause:

- `devHandler.fingerprint` (`cmd/funcdctl/dev.go:735`) digests the size and mtime of the manifest, the entry
  and, for an in-place bundle, every regular file under the bundle root. It skips dot-entries (the delivered
  contract, `.git`, `.venv`, the default `.funcd-dev` persist root), `node_modules`, `__pycache__` and the
  absolute durable state dirs (`stateDirs`, `cmd/funcdctl/dev.go:521`).
- `reloadChanged` (`cmd/funcdctl/dev.go:798`) re-reads each changed manifest, re-runs `synthesizeResources`
  over the whole set, re-applies the resources, re-delivers the contract (and the isolated copy of the entry)
  through the new `deliverBundle`, and re-applies the Function with the fingerprint as its digest. A
  `Conflict` on apply clears `seen`, so the next poll retries.
- Changes to `main`, `dev.backends`, `dev.node` and `dev.python` need a restart; the watcher logs a warning
  for them.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### 🟡 Minor 1 — the durable-state-dir exclusion has no test  ·  attribution: model

The `slices.Contains(stateDirs, p)` skip (`cmd/funcdctl/dev.go:758`) matters when `dev.backends.kv`/`.blob`
names a path inside an in-place bundle root (for example `./data`). Without the skip, every Badger/blob write
changes the fingerprint, and each 300 ms poll rolls out a new revision. No test in `cmd/funcdctl` sets a
`dev.backends` path together with a hot-reload session, so a mutant that removes the skip would survive.
A small unit test of `fingerprint` with a state dir under the root (write a file there and assert the
fingerprint does not change) would cover it. This finding does not block the fix.

### 🟡 Minor 2 — the in-place watch stats the whole bundle tree every poll  ·  attribution: model

`fingerprint` walks the full in-place root every `devReloadPoll` (300 ms). The skip list covers the common
heavy dirs, but a non-dot virtualenv (`venv/`, a common `python -m venv venv` layout), `dist/` or `target/`
under the bundle root is walked on every poll, which costs CPU for as long as the session runs. It is
correct, and ADR-0125 leaves the granularity open. An event-based watcher (fsnotify is already in the module
graph as an indirect dependency) or a skip for a detected virtualenv (`pyvenv.cfg`) could be a later
refinement. This finding does not block the fix.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit f320594` with
  the test file restored from f320594: all three subtests of `TestIssue320_DevHotReloadsImportsAndManifest`
  fail with `Condition never satisfied` after 20 s (`imported-module` keeps serving `"v":1`;
  `manifest-in-place` and `manifest-stem` keep the strict contract's 422 and `mode:one`). After
  `git reset --hard f320594`, the worktree is clean at that HEAD.
- **Passes with the fix under `-race`**, and so does `TestIssue135_DevHotReloadsEditedHandler` (the entry-file
  case the old code handled).
- **The user-visible behavior is fixed.** The test boots a real `funcdctl dev` session (gateway + process
  runtime + node) and edits files on disk. It covers the issue's module case (`handler.mjs` imports
  `./lib.mjs`, `v = 1 → 7`) and the manifest case for both an in-place generic manifest and an isolated stem
  manifest (contract strict → permissive, `dev.config` value `one → two`).
- **Mutants: 3 of 3 killed** (overlays, `-run TestIssue320`):
  1. Skip the in-place walk (`if false && !h.pf.isolate`) → `imported-module` fails.
  2. Keep the old manifest (`h.pf.m = m` → `_ = m`) → both manifest subtests fail.
  3. Drop the contract/bundle re-delivery in `reloadChanged` → both manifest subtests fail.
- **Cause, not symptom.** No timeout, retry, or skipped test hides the defect; the watcher now watches what
  the issue says it missed and re-runs the same synthesis `bootDev` runs.
- **Scope.** Every hunk serves the issue. `manifestDir` → `manifestPath` is needed to re-read the manifest
  (`filepath.Dir` keeps the old base for `dev.python`/`dev.node`). Moving `resolvePersistPlan` earlier is
  needed for the state dirs; `buildPersistDrivers` already fails boot on its error, so failing early changes
  no outcome. No test was weakened or deleted.
- **Reuse.** `deliverBundle` folds the boot-time contract/entry delivery into one helper that boot and reload
  share, instead of a second copy. `synthesizeResources`, `synthesizeFunction`, `loadManifestAt` and
  `artifact.ContractBlob` are reused unchanged. The only other `WalkDir` with a similar shape
  (`internal/artifact/bundle.go`, the OCI bundle packer) builds a tar node list and does not fit a watch
  fingerprint.
- **Conventions.** `api/fault` wrapping with kinds, `slog` only, ctx-first, imports at top level, no new
  dependency, no comment bloat. Test YAML is block style.
- **ADRs.** No Accepted/Implemented ADR file was touched. The change realizes ADR-0125's "watch files, re-apply
  on change" and relies on ADR-0143's rollout; it contradicts neither.
- **Checks (touched package).** `go build ./...` and `go build -tags dev ./cmd/funcdctl`; `go vet` with and
  without `-tags dev`; `golangci-lint run` with and without `--build-tags dev`: 0 issues;
  `go test -race ./cmd/funcdctl` with and without `-tags dev`: ok. The repo-wide gate, Linux lint and the
  lanes are left to the group gate.
- **Shape.** `fix(funcdctl):` subject, `Fixes #320`, the Co-Authored-By trailer, one issue in one commit.

### Fix checklist

11 of 11 items hold. Item 8 holds for the checks this review ran (the touched package on the host). Host
build, vet, lint and tests are green; the Linux lint, the repo-wide run and the lanes belong to the group gate.

### Recommendation

Pass. Hand back to `/fix` Step 8. The two Minors can be follow-ups: a unit test for the state-dir exclusion,
and an event-based or cheaper watch if large in-place trees show up.
