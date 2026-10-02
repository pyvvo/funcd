## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #301 fix, model: claude-opus-5-5)

Change: branch `fix/i301`, commit `d6e44a8` — `fix(site): keep a Site's status in step with its Route's readiness`.
Files: `internal/site/reconcile.go`, `pkg/funcd/funcd.go`, `pkg/funcd/site_status_test.go` (new).

Checklist: 11 of 11 items apply and hold (item 8 scoped to the touched packages; Linux lint, e2e and the
lanes are left to the group gate).

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor 1 — ADR-0139 Decision §6 still says "no owner-watch"  ·  attribution: adr
ADR-0139 (Implemented) §6 reasons from "one reconciler per kind and no owner-watch" and says a later
`Route` regression "is observed at the next `Site` reconcile". The fix adds exactly such a watch
(`ctrl.Watches(v1.KindRoute.GVK(), site.MapRoute)`). This does not contradict the decision: §6's rule
(the `Site` is `NotReady` while its `Route` is, pending ⇒ requeue 2 s) is unchanged, and the watch only
schedules the "next `Site` reconcile" the ADR already relies on, which closes the false-green the ADR
exists to remove. The ADR is frozen and was not edited (correct). The sentence is now a stale description
of the mechanism; a later ADR that touches Site/Route ownership can note it. Recorded, not scored.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit d6e44a8` with the test file
  restored: `TestIssue301_SiteStatusFollowsRouteReadiness` FAILs after 8 s with "an earlier Route on the
  same host and path makes the Site NotReady; last {… Status:True … Reason:Materialized}" — the Site keeps
  its first verdict, as the issue describes. `git reset --hard` back to `d6e44a8`; tree clean.
- **Passes with the fix under `-race`**: `--- PASS (3.18s)`, `ok pkg/funcd`.
- **The test covers both directions** of the issue (Ready → NotReady on a `RouteConflict`, then back to
  Ready after the conflicting Route is deleted) with no write to the Site, through the real controller,
  store watch and Route reconciler (`funcd.New(InMemory())`, no data dir). The 3 s sleep deliberately
  outlasts the 2 s pending poll, so only the Route watch can re-run the Site — the test cannot pass on
  the poll.
- **Cause, not symptom.** The issue names the missing Route → Site mapping (`pkg/funcd/funcd.go` had only
  the KVStore `Watches`). The fix adds that mapping; the pending-Route poll and its interval are unchanged,
  no timeout or retry was lengthened.
- **Mutants** (each restored afterwards):
  1. `MapRoute` returns `nil` → test FAILs (8.07 s).
  2. `MapRoute` enqueues `v1.KindRoute.GVK()` instead of `v1.KindSite.GVK()` → test FAILs (8.07 s).
- **No loop or storm.** A Route event re-runs the same-named Site; `ensureRoute` skips an identical spec
  (`reflect.DeepEqual`) and `publish` writes a byte-identical status as a store no-op, so the Site's own
  Route write and the Route reconciler's status writes settle. A Route with no same-named Site hits the
  Site reconciler's `NotFound` early return, which reclaims nothing.
- **Mapping by name, not by owner reference, is right**: it mirrors `routeOf` (the Site reads its
  same-named Route), so a foreign same-named Route appearing or disappearing also refreshes the Site's
  `RouteNotOwned` verdict.
- **Reuse**: the fix uses the existing `controller.Watches` / `MapFunc` seam (the #147 precedent,
  `kvReconciler.MapFunction`); no generic owner mapper exists in `internal/controller` to reuse instead,
  and `MapRoute` is three lines with no duplicated logic.
- **Conventions**: ctx-first `MapFunc` signature, no `any`, no new dependency, top-level imports,
  comments state the why (the stale "no owner-watch" comment on `routeRequeue` was corrected), naming
  matches the surrounding code.
- **Scope**: every hunk serves the issue; no test weakened or deleted; no ADR file touched.
- **Checks**: `go test -race` on `internal/site`, `internal/controller` and `pkg/funcd` — ok; `go vet` on
  `internal/site` and `pkg/funcd` — clean; `golangci-lint run ./internal/site/... ./pkg/funcd/...` —
  0 issues.
- **Shape**: `fix(site):` subject, cause/fix/test body, `Fixes #301`, attribution trailer, one commit.

### Definition of Done
11 / 11: regression test present (1), fails pre-fix for the reported reason (2), passes under `-race` (3),
revert and both mutants fail it (4), root cause fixed (5), scope clean (6), no ADR contradicted or edited
(7), touched-package build/vet/lint/tests green (8), conventions (9), reuse (10), commit shape (11).

### Model scorecard
claude-opus-5-5: 0 blockers, 0 majors, 0 model-attributed findings; 1 minor attributed to `adr`.

### Recommendation
Pass. Hand back to `/fix` Step 8; the group gate runs the repo-wide checks, Linux lint and e2e.
