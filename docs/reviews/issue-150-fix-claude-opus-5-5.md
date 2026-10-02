# Fix review — issue #150 (fix, model: claude-opus-5-5)

**Issue:** A KV table removed and re-added before reconcile keeps the old data.
**Change:** branch `fix/i150`, commit bb6557a `fix(admission): reject re-adding a KV table before its old data is reclaimed`
(`internal/controlplane/admission/kvstore.go`, `internal/controlplane/admission/kvstore_test.go`, a comment in `pkg/funcd/funcd.go`).

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #150 fix, model: claude-opus-5-5)

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The regression test covers only the admission unit, not the platform path the issue reproduced** · attribution: `model` ·
  evidence: `TestIssue150_ReaddedTableDoesNotKeepRemovedData` drives `NewKVStoreDeletionProtectionAdmission` with a fake
  prober. Nothing tests that `pkg/funcd` passes the real `kvProber{c.kvStore}` on the Update path, or that the KVStore
  reconciler reclaims the table after the rejected re-add. The existing wiring is shared with the Delete path, so the risk is
  low · fix (optional): a platform-level test that removes a table holding data, re-adds it at once (expect Conflict),
  reconciles, and re-adds it (expect an empty table).

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** With the `origin/main` `kvstore.go` overlaid (test file kept), the test
  fails: `expected: "conflict" actual: ""` — the re-add of table `t` was admitted while `default/s/t/secret` still existed.
  (A plain `git revert --no-commit` also removes the test, so it reports "no tests to run"; the overlay is the real check.)
- **Passes with the fix**, un-skipped: `go test -race -count=1 ./internal/controlplane/admission/` → ok. The worktree was
  reset to bb6557a and left clean.
- **Mutants (3/3 killed):** (1) `if has` → `if false && has` — FAIL; (2) the probe prefix without the trailing `/` (so
  table `t` matches `tt/…`) — FAIL; (3) `if existing[tb.Name]` inverted (probe kept tables instead of added ones) — FAIL.
- **Cause, not symptom.** The issue's cause is that a level-triggered reconciler cannot see a removal that is undone before it
  runs. The fix makes the intermediate state impossible: a same-named table is admitted only after its old prefix is empty,
  so the removal is always reclaimed first. No timeout, retry or swallowed error. The probe uses the same
  `<ns>/<store>/<table>/` layout as `reclaimOrphanTables` (`internal/services/kv/reconcile.go`) and the facade's
  `tablePrefix` (`internal/services/kv/kv.go`). Tables already in the old spec are skipped, so status write-backs and
  in-place owner changes never probe (the test asserts the owner change is admitted).
- **Concurrency.** The removed table cannot gain keys (removal is blocked while it is bound), so the probe can only see data
  disappear; a probe racing the reclaim either rejects (retryable Conflict) or admits an empty table.
- **Scope.** Every hunk serves the issue; the `pkg/funcd/funcd.go` change only keeps the wiring comment true. No test was
  weakened or deleted.
- **Reuse.** Reuses `storePrefix`, the existing `KVProber` port and the `kvProber` adapter; errors use `fault.Conflictf` /
  `fault.Wrapf` as the Delete path does. The new `keyProber` test fake is needed because the existing `fakeProber` ignores the
  prefix and cannot tell `t` from `tt` or `keep`.
- **Conventions.** ADR-0002 holds (port declared in the admission package, `api/fault` kinds, ctx-first, no new imports in
  production code); top-level imports; no comment bloat beyond updating the existing doc comments.
- **ADRs.** No ADR file edited. Consistent with ADR-0073 Decision 7 and the table-removal-protected-and-reclaimed scenario
  (a re-added table starts empty). The ADR-0073 owner-transfer note ("delete+recreate the table entry") still works: the
  re-add succeeds once the reconciler reclaims the data. ADR-0121 moved *existence* gates to reconcile time; this is a data
  probe, the same kind the Delete path already does at admission.
- **Checks (touched packages).** `gofmt -l` clean; `go build ./...` ok; `go vet` on `internal/controlplane/admission` and
  `pkg/funcd` clean; `golangci-lint` on both: 0 issues; `go test -race ./pkg/funcd/` ok. E2E, Linux lint and lanes are left
  to the group gate.
- **Shape.** `fix(admission):` subject, `Fixes #150`, attribution trailer, one issue in one commit.

### Definition of Done

11/11 items hold (item 8 for the checks in this gate's scope; e2e, Linux lint and lanes run at the group gate).

### Model scorecard

claude-opus-5-5 · pass · 0 blockers · 0 majors · 1 minor (model) · DoD 11/11.

### Recommendation

Ship. Optionally add the platform-level remove/re-add/reconcile test from the minor finding in a follow-up.
