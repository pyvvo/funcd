# ADR-0140 Implementation Review — Path-mounted `Site` (`ingress.path`, F104)

**Verdict**: **pass** — `ingress.path`, the `EffectivePath` resolver, the resolved-path validation and the
digest-keyed bundle-rule lookup match the Contracts; all 11 Scenarios have named, un-skipped, passing
tests; the four sub-checks are green; the `s3` containerd lane is 8/8 with two sites on one listener.

**Producing model**: claude-opus-5
**Reviewed against**: ADR-0140 Contracts / Scenarios / Review checklist / DoD · ADR-0139 (superseded in
part — its other decisions must still hold) · ADR-0110 (matcher + strip) · ADR-0120 (static handler) ·
blueprint (ingress bullet) · ADR-0002 conventions.

## Verification run (evidence)

- `go build ./...` → exit 0. `go tool golangci-lint run ./...` → exit 0, **0 issues**;
  `--build-tags e2e ./pkg/funcd/...` → exit 0, 0 issues. `go test ./...` → exit 0 (79 packages ok).
  `go mod verify` → exit 0. `gofmt -l` → empty.
- `just ci` → exit 1 **only** on its git-diff gate ("Run just fmt and commit the result" — 
  uncommitted tracked files, the documented CLAUDE.md pitfall). Every sub-check it runs is green above;
  it passes once the work is committed.
- OpenAPI regenerated (`just generate`); `TestSpecGeneratedFromGo` green with the additive
  `SiteIngress.path`.
- `go test -tags e2e ./pkg/funcd/ -run 'TestScenarioE2ESite$|TestScenarioE2EPathMountedSites|TestScenarioE2EStaticServing'`
  → all three PASS. The ADR-0139 and ADR-0120 e2e suites still pass unchanged — no regression from the
  mount change.
- **`just lima-example s3` on real containerd → final status PASS, exit 0, 8/8 testcases**, the eighth
  being the new `a-host-less-Site-is-served-by-path-on-the-same-listener`: `/site/docs/` served with **no
  `Host` header**, and the coexistence sub-step asserting `bi=200` (host-qualified, still at `/`),
  `rootNoHost=404` and `sibling=404` — segment boundaries and non-interference proven live.

## Scenario → test traceability (all passing, none skipped)

| Scenario | Test |
|---|---|
| `path-collision-rejected` · `derived-path-collision-rejected` | `TestSiteValidatePath` (api/types), both the written and the derived mount |
| (resolver table) | `TestSiteEffectivePath` — explicit · host ⇒ `/` · host-less ⇒ `/site/<name>` |
| `two-hostless-sites-coexist` | `TestScenarioTwoHostlessSitesCoexist` (internal/site) **+** `TestScenarioE2EPathMountedSites` (both Sites reach `Ready`, so neither Route conflicted) |
| `hosted-site-defaults-to-root` | `TestScenarioHostedSiteDefaultsToRoot` |
| `existing-hostless-site-moves-on-upgrade` | `TestScenarioExistingHostlessSiteMovesOnUpgrade` (and `ingress.path: /` pins the old URL) |
| `path-change-reprograms-without-reupload` | `TestScenarioPathChangeReprogramsWithoutReupload` (recorder asserts zero `Put`s; `status.digest` intact) |
| `explicit-namespace-still-requires-host` | `TestScenarioExplicitNamespaceStillRequiresHost` |
| `path-mounted-site-serves-bundle` · `path-mount-respects-segment-boundary` · `data-mount-nested-under-bundle-path` · `spa-fallback-scoped-to-the-mount` | `TestScenarioE2EPathMountedSites` (real edge) + the `s3` lane testcase |

## ✅ Verified correct — keep

- **The ADR's central claim held under implementation**: no change to `internal/edge/router`,
  `internal/dataplane` or `internal/edge/static` (`git status` on those trees: untouched), yet a path
  mount serves correctly end to end. The reuse argument was real, not aspirational.
- **`EffectivePath` is defined exactly once**, in `api/types/v1alpha1/site.go:100` — the judge's M1 trap
  (a duplicate resolver drifting between `Validate` and the compiler) is closed. `Validate` uses it at
  `site.go:181`, the compiler at `materialize.go:118`. Do not "move it closer to its user".
- **`servingDigest` no longer looks up by path** (`grep 'rule.Path != "/"'` → no match): it scans for the
  digest-scoped key prefix, which is what makes `path-change-reprograms-without-reupload` pass with zero
  `Put`s. This is the property that keeps ADR-0139 §3's durable record intact across an exposure change.
- **The derived-collision gap the judge blocked on is genuinely covered**, not just declared:
  `TestSiteValidatePath` drives the host-less default (`/site/bi` vs a rule at `/site/bi`) and asserts
  `fault.Invalid` with the mount named in the message — so the operator sees the cause at apply, never a
  stuck Site.
- **Back-compat proven, not assumed**: the pre-existing `internal/site` and `pkg/funcd` Site suites pass
  unchanged, and `TestScenarioHostedSiteDefaultsToRoot` pins the hosted mount at `/`.
- **The upgrade break is test-visible**, not only prose — `existing-hostless-site-moves-on-upgrade`
  asserts both the move and the `ingress.path: /` remedy, so a future reader cannot miss it.
- **Tracking**: ADR-0140 at `Reviewing` with its Accepted note and Contracts intact; F104 at `reviewing`;
  ADR-0139 carries the `Superseded in part by` back-link and is otherwise unchanged, still `Implemented`.

## Findings

None. (No Blockers, Majors, or Minors survived verification.)

## Definition of Done

**21 / 21** hold (11 ADR Review-checklist items + 10 generic). `just ci`'s git-diff gate is the only
non-green line and is satisfied by the commit.

## Model scorecard

Recorded: claude-opus-5 on ADR-0140 (implementation) → pass, 0 blockers / 0 majors / 0 minors, 0
model-attributed, DoD 21/21. See docs/reviews/model-scorecard.md.

## Recommendation

Sign off. Advance ADR-0140 `Reviewing → Implemented` and F104 `→ implemented`; move the board card to
Done. No follow-up work is owed by this ADR; the separately-carded edge-router `Host`-vs-`Host:port`
defect remains open and is orthogonal (it affects hosted sites, which this ADR makes optional).
