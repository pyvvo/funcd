## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #135 fix, model: claude-opus-5-5)

Change: branch `fix/i135`, commit 147a219 `fix(funcdctl): hot-reload an edited handler in funcdctl dev`
(`cmd/funcdctl/dev.go`, `cmd/funcdctl/dev_test.go`). Governing ADRs: ADR-0125 (Decision 2, boot
sequence, Review checklist "hot-reloads on change"), ADR-0035 (digest pinned into the Revision),
ADR-0143 (revision rollout and drain).

### 🟡 Minor 1 — only the entry file is watched; an edit to an imported module is not reloaded  ·  attribution: model
Evidence: a throwaway probe test (written into the package, run, deleted) with an in-place bundle where
`handler.mjs` imports `./lib.mjs`. After editing `lib.mjs` from `v = 1` to `v = 7`, the dev session kept
serving `"v":1` for 10 s (`--- FAIL: TestProbe135Import`). `watchHandlers` hashes only
`filepath.Join(pf.srcDir, pf.entry)` (`devHandler.src`). ADR-0125 points `FUNCD_BUNDLE_DIR` at the
working tree and its boot sequence says "watch files, re-apply on change", so a multi-file in-place
bundle is a normal dev case. The issue's own scenario (an edit to the handler entry) is fixed, so this
does not block. Fix: digest the in-place bundle's tree (or at least the source files under `srcDir`)
instead of the entry alone, or file a follow-up issue.

### 🟡 Minor 2 — `spec.imageDigest` carries a source-file hash in dev  ·  attribution: model
Evidence: `api/types/v1alpha1/function.go:43-45` documents `ImageDigest` as the artifact content digest,
"system-set at materialization (ADR-0035)". The fix sets it client-side to `sha256` of the handler
file, so the stamped Revision pins a value that is not an OCI digest. It works in dev because the
embedded platform runs the file Materializer with no resolver and no `platformsOf`
(`internal/function/function.go:1138-1150`, `1527-1536`), and the field is the one spec field whose
change bumps the generation and rolls a new revision (ADR-0143) — a reasonable reuse instead of a new
field. The doc comment on `sourceDigest` states the intent. Fix (optional): note on the `devHandler`
type that the value is dev-only and never reaches an OCI materializer.

### Not scored — pre-existing flake in the touched package  ·  attribution: env
`TestScenarioDevPersistSurvivesRestart` (`cmd/funcdctl/dev_phase2_test.go:142`) fails intermittently with
`apply KVStore "cache-kv": … resourceVersion mismatch` on the second boot. It fails 8 of 20 runs with
`origin/main`'s `dev.go` overlaid and 13 of 20 with the fix (`-race -count=20`), and the failing apply is
the KVStore, which the change does not touch. No open issue was found for it; it should be filed.

### ✅ Verified correct (keep it)
- Revert check: `git revert --no-commit 147a219` with the new test kept → both subtests of
  `TestIssue135_DevHotReloadsEditedHandler` fail with "Condition never satisfied — the running dev session
  serves the edited handler" (20 s each), which is the issue's stale-handler symptom. After
  `git reset --hard 147a219` the test passes under `-race` (in-place and stem subtests, not skipped; node
  on PATH). The worktree was left at 147a219 and clean.
- Mutant M1 (drop `next.Spec.ImageDigest = sourceDigest(data)` in `reload`): both subtests fail — the
  digest bump is what rolls the new worker.
- Mutant M2 (drop the isolated-bundle re-copy in `reload`): the `stem` subtest fails, the `in-place`
  subtest passes — the two subtests cover the two bundle shapes.
- Repeated edits: a probe editing the handler three times in one session (v 2 → 3 → 4) served each value;
  the shallow copy of the applied Function does not trip resourceVersion checks.
- Cause, not symptom: the issue names the missing watcher plus the long-lived worker that caches the
  module. The fix adds the watcher and forces a new revision (a worker restart) through the existing
  reconcile path; no timeout, retry or skip hides the defect.
- Lifecycle: `watchDone` is awaited in `devInstance.stop()` before cleanup, so the watcher never reads a
  removed temp bundle; an unreadable source during an editor's atomic save is treated as unchanged and
  retried; a failed apply leaves `h.fn` unchanged so the next poll retries.
- Scope: every hunk serves the issue. `prepareBundle` now reads the handler once for both bundle shapes
  (returning the digest) instead of `Stat` plus a later read; no test was weakened or removed.
- Reuse: no file-watch library is a direct dependency (`fsnotify` is indirect only) and polling avoids
  adding one; no existing byte-digest helper exists in `internal/`, `pkg/` or `api/` (`go-digest` is
  indirect only); the test reuses `devProject`, `runDev`, `waitReady`, `post` and `permissiveContract`.
- Conventions: slog only, ctx-first, imports at top level, no YAML added, comments state the why
  (ADR references), naming follows the file.
- ADRs: no ADR file edited; the change realizes ADR-0125 Decision 2 and answers its hot-reload
  granularity question (worker restart) in the commit message, consistent with ADR-0143.
- Checks (touched package): `gofmt -l` clean; `go vet -tags dev ./cmd/funcdctl/` OK;
  `golangci-lint run` with and without `--build-tags dev` → 0 issues; `go build ./...` and the `-tags dev`
  build OK; `go test -race ./cmd/funcdctl/` (untagged) OK; `go test -tags dev -race ./cmd/funcdctl/`
  OK apart from the pre-existing flake above. Linux lint, e2e and lanes are left to the group gate.
- Commit shape: `fix(funcdctl):` subject, `Fixes #135`, attribution trailer, one issue in one commit.

### Definition of Done
11 / 11 items hold (fix checklist). Item 8 covers the touched package on the host only; Linux lint, e2e
and lanes run at the group gate. Item 11 covers the commit; the PR does not exist yet.

### Model scorecard
To record: claude-opus-5-5 on issue #135 (fix) → pass, 0/0/2, 2 model-attributed, DoD 11/11.

### Recommendation
Sign off. Optionally widen the watcher to the in-place bundle's source tree (Minor 1) and file an issue
for the `TestScenarioDevPersistSurvivesRestart` resourceVersion flake.
