# ADR-0141 Implementation Review — Repository split (pyvvo, pinned language modules, F01)

**Verdict**: **pass** — funcd is `github.com/pyvvo/funcd`, public from a rewritten history. It embeds and tests
the shims and examples of the pinned funcd-typescript v0.3.0 and funcd-python v0.2.0 modules, and merges only
through a squash merge queue with release-please. All 12 Scenarios hold. One Lima-lane case (duckdb consumer
auth) fails exactly as it does on the pre-move baseline.

**Producing model**: claude-opus-5-5
**Reviewed against**: ADR-0141 Contracts / Scenarios / Review checklist / DoD · ADR-0001 (superseded in part) ·
ADR-0025 (`tests/e2e` boundary) · ADR-0036/0032/0040 (refined, location only) · blueprint Repository structure

## Verdict: pass — 0 blockers, 0 majors  (ADR-0141 implementation, model: claude-opus-5-5)

Reviewed under `adr-batch` by the model that built it; every claim below cites a captured run.

### 🟡 Major / Minor
- **Minor · env — duckdb lane consumer case.** `catalog-reader` reaches Ready, but its SQL round-trip returns
  `Invalid Input Error: Authentication failed`. The unmodified pre-move main (a56650e) fails identically on
  the same lane (baseline run, 2026-09-28), so the failure predates ADR-0141. The ADR's own
  `duckdb-lane-builds-bundle` scenario holds: the bundle builds in a writable module copy (vendored
  `duckdb` for cp314/aarch64), the CatalogService reaches Ready and serves Quack, and the edge PEP case
  passes. Filed on the board: *Fix catalog consumer auth — spec.catalogs token rejected by the Quack engine
  (duckdb lane)*.
- **Minor · adr — the `copy` contract is narrower than the need.** The Contracts' lane table says
  "copy `dir` into `.modcopy/`". Copying only the dir breaks the catalog-quack build, whose uv path
  dependency points at `../../shim`. `scripts/lane.py` copies the whole module to `.modcopy/lane/<module>`,
  so `dir` is still copied under `.modcopy/` and the scenario holds; only the wording under-specifies it.
- **Minor · model — an unlisted CI addition.** `.github/workflows/ci.yml` also skips the fast `ci` job on
  docs-only changes (the language repos' pattern, their PR #7). The Implementation plan lists only
  `merge_group`, the filter change and paths-filter v4. It fits Decision 10.
- **Minor · model (fixed) — a brittle no-source check.** `TestScenarioShimFromPinnedModule` first asserted
  that `shim/` is absent, which fails in a pre-move checkout that keeps an ignored
  `shim/nodejs/node_modules`. pyvvo/funcd#2 checks the shim sources instead.

### ✅ Verified correct (keep it)
- **Build, lint, test.** `go build ./...` and `go vet` under every build tag in the repo (default, `e2e`,
  `dev`, `lintfixture`, `linux integration`): clean. `go tool golangci-lint run ./...`: 0 issues.
  `go test ./...`: all packages ok. `just test-e2e`: ok (pkg/funcd, 59.8s). `go mod verify`: all
  modules verified. `just ci` on the rewritten tip: exit 0, including `tests/lint-fixtures`, which proves
  the renamed depguard prefixes still fire.
- **CI on pyvvo/funcd main**: run 36360080764 (migrated tip ed523ef), 36361464266 (25f4e1f, #1) and
  36361545882 (a674422, #2) all succeeded. pyvvo/funcd#1 and #2 passed `ci`, `e2e` and `pr-title` on the PR
  and again in the merge group. release-please minted the App token and found the v0.1.0 baseline.
- **Scenario tests, un-skipped and passing.** `TestPinsAreReleaseTags`, `TestScenarioLaneStagesResolve`,
  `TestScenarioShimFromPinnedModule`, `TestScenarioBenchUsesEmbeddedShims`, and the updated
  `TestScenarioCuratedImageBuilds`.
- **Lima lanes** (`just lima-example-all`, colima, arm64): env-echo, fn-to-fn, s3, kv, workflow, funclog,
  egress and metastore pass. duckdb: see the first Minor.
- **local-override-go-work** (manual). A `go.work` pointing at a funcd-typescript clone whose `shim.mjs`
  carries a marker: `go list -m` resolves the clone, the built binary holds the marker (1 hit), and
  `TestScenarioShimFromPinnedModule` passes. Without the `go.work`, the marker is gone (0 hits).
- **bump-language-release.** The funcd-typescript v0.2.0 → v0.3.0 bump (the `Pool` export) was one
  `go get`. The `check-hygiene` version gate fires on a probe (a hard-coded `@v0.3.0` in a tracked file)
  and is clean on the tree.
- **module-path-moved.** An external module ran `go get github.com/pyvvo/funcd/pkg/sdk@main` through the
  public proxy (resolved v0.1.0), then built and ran. The old module path is in no live file; it remains
  only in frozen ADRs, `docs/reviews/`, `docs/legacy/` and ADR-0141.
- **history-scrubbed.** A mirror clone of pyvvo/funcd from GitHub (refs `main` and `v0.1.0`, 254 commits)
  scans to zero for PDF paths and PDF content, bank names, personal identifiers and home paths. Authors
  keep the noreply identity and their dates. The rewrite pruned no commit and changed one tip file,
  `docs/adr/0125-funcdctl-dev-local-run.md` (the sanctioned redaction).
- **merge-rules-enforced.** Ruleset 24088813 on `main`: deletion, non_fast_forward,
  required_linear_history, squash-only pull_request, required checks `ci`/`e2e`/`pr-title`, merge_queue
  (SQUASH). No bypass actors. `gh pr merge 1 --squash` was refused ("The merge strategy for main is set
  by the merge queue"); #1 and #2 landed through the queue.
- **old-repo-archived.** green-0-rabbit/funcd is archived and private and keeps its 22 PRs. The board copy
  ([pyvvo Project #1](https://github.com/orgs/pyvvo/projects/1)) holds all 72 items with identical titles,
  statuses and bodies, and `driver.py ids` reports MATCH against it.
- **Tracking.** Since acceptance ADR-0141 changed only its Status line and date note (a9beee3 → 333a3b8).
  The F01 row is at `reviewing`. ADR-0001 carries the partial-supersession back-link. The blueprint gained
  the companion-repositories table.
- **Conventions.** No new `any`. `tests/e2e` reads the shim through the public language module (no
  `internal/` import, ADR-0025). `internal/testkit/langmod` holds no globals. Imports are gofmt'd and YAML
  is block style.

### Definition of Done
13 / 14 hold (the 11 Review-checklist items, plus the DoD's build-and-test, Lima-lane and CI-on-main
items). Miss: *every Lima lane green*. duckdb fails, `env`-attributed: pre-existing, and reproduced on the
pre-move baseline.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0141 (implementation) → pass, 0/0/4, 2 model-attributed, DoD 13/14.
See docs/reviews/model-scorecard.md.

### Recommendation
Sign off: stamp ADR-0141 `Implemented` and move the F01 row to `repo split: implemented`. The duckdb
consumer-auth failure is a separate bug on the board.
