## Verdict: pass — 0 blockers, 0 majors, 0 minors (ADR-0339 implementation, model: claude-opus-5-5)

This report reviews the commit `feat: implement ADR-0339` on the branch that ships FEAT-0019's five ADRs in one PR,
after ADR-0337's commit. By the decider's standing rule of no review agents, the session that wrote the code ran this
review, so it is not independent. Every check below was run, not read. The `Implemented` stamp waits for the lane case
(`addon-rauthy` updates rauthy's Addon, sees no engine replaced, then moves the App's part and rolls it back), which
runs once, before the PR, with ADR-0337's.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

None.

### Routed (not this work's)

- **A Backup of an object pinned to an older revision reads the current Addon's `snapshot.path`** (the member kind in
  `internal/backup/workload/addons.go` reads the Addon, as before). Not checked yet: ADR-0339's Scope names no backup
  rule, and a sound design could want either the pinned revision's path or the current one, so it is a question for
  the decider (`uncovered`), not a change here.
- **The lane case** waits for rauthy's template in funcd-addons, with ADR-0338's and ADR-0341's parts.
- **Two tests failed once in the full `pkg/funcd` run on a loaded host**: `TestShutdownReleasesTheLoopbackFeedPort`
  (also in ADR-0340's run) and `TestBackup_GoesOnWhenItsListingFails`; each passed alone (5 of 5, 3 of 3), and this
  change touches neither the SQL feed nor a backup listing. Not checked yet: for the flake tracker #1142, if the
  decider wants issues.

### ✅ Verified correct (keep it)

- **The checks**, through `scripts/agent/d`: `go test` on the touched packages (`api/types/v1alpha1`, `internal/app/...`,
  `internal/gc`, `internal/backup/workload`, `internal/controlplane/...`, `internal/services/addon/...`, `pkg/funcd`,
  `pkg/sdk`, `cmd/funcdctl`, `scripts`) and with `-tags dev` on `cmd/funcdctl`'s add-on tests; `go vet`; `go tool
  golangci-lint run` with and without the dev tag, 0 issues; `just check-behavior`; `just check-specs`, 23 specs, 0
  failing, `revisions.fizz` passing with its 11 mutants failing their assertions; the spec and the Spec pages from
  `just generate`. The repo-wide checks run once, in the PR's gate.
- **The scenarios**: all 12 have a test. `addon-revision-edit-moves` is the second half of
  `TestScenarioAddonRevisionNoMove`, and `addon-revision-dev` the end of `TestScenarioAddonTemplateDev` (dev tag); each
  comment names its scenario.
- **The design model's oracles**: each assertion of `docs/design/addon/revisions.fizz` has its marker on the test the
  ADR names. `scripts/specs.py` read a transition assertion's name only without its `(before, after)`, so a marker
  naming one was reported dangling; its pattern now takes the parameters, and
  `TestSpecOracleMarkerNamesATransitionAssertion` fails on the old pattern.
- **The mutants**, each run with `go test -overlay` against the test that should catch it:

  | Mutant | Caught by |
  |---|---|
  | the engine runs the Addon's spec, not the pin | `TestScenarioAddonRevisionNoMove` |
  | a standalone object follows the current revision | `TestScenarioAddonRevisionNoMove` |
  | a part moves before its revision is Applied | `TestScenarioAddonRevisionSuperseded` |
  | a part follows the current revision, not its record | `TestScenarioAddonRevisionSuperseded`, `TestScenarioAddonRevisionNoStepBack` |
  | removal ignores the pins | `TestScenarioAddonRevisionRetired` |
  | removal takes the current revision | `TestScenarioAddonRevisionRetired` |
  | nothing is ever removed | `TestScenarioAddonRevisionRetired` |
  | a rollback ignores the records it takes back | `TestScenarioAddonRevisionAppRollback` |
  | `from` is the target, not what the part runs | `TestScenarioAddonRevisionRollbackAfterFailure` |
  | the switch does not wait for the part's pin | `TestAddonRevisionSwitchWaitsForThePart` |
  | deploy does not wait for the Addon's revision | `TestAddonDeployWaitsForTheRevision` |
  | dev's objects do not follow the current revision | `TestScenarioAddonTemplateDev` |

  The removal test first let a record keep the revision that the part pins, so the pins mutant survived; the test now
  removes the record first, and the pin alone keeps it.
- **The contracts**: `AddonRevision` is cluster-scoped and read-only (`registerClusterRead`, 405 with `Allow: GET`),
  named `<addon>-<generation>` and shortened as a Revision's name; `status.currentRevision`, `status.addonRevision`,
  `status.appRevision`, `spec.addonRevisions` and `hookInput.addonRevisions` are the ADR's fields. The Addon's
  reconciler stamps, names the current revision and removes the unused ones, requeueing while one that is not current
  remains; the GC pair `(Addon, AddonRevision)` is the first cluster-scoped pair, collected by a pass over every
  namespace. The object's pass builds the engine from its pin before any other step reads the Addon.
- **ADR-0337's two open items** hold now: deploy waits for the Addon's `status.currentRevision` after its write,
  `--no-wait` too (`TestAddonDeployWaitsForTheRevision`), and an App's part counts as Ready only on its App's latest
  record. Its lane case is its last open item.
- **Two choices of the implementation, for the decider to confirm**: an App revision stamped while its Addon had no
  revision records nothing for that kind, and its part then pins the current revision; and ADR-0332's
  `addon-config-invalid` now runs under dev's rule, the one path left where a stored object meets a newer schema.
- **The behavior rows** the ADR lists are in `docs/behavior/addons.md`, `apps.md` and `funcdctl-dev.md`, and the rows
  an Addon's change touched say that it replaces no engine; the guide's add-ons page has "Upgrade it", and the App
  page names the hook input's `addonRevisions`.

### Definition of Done

16 of 17 items hold: the 6 Review-checklist items, the scenarios, the design model and its oracles, the touched
packages' checks (the gate runs `just ci-full`), and 7 generic items. Open: the lane case.

### Model scorecard

To record: claude-opus-5-5 on ADR-0339 (implementation) → pass, 0 blockers, 0 majors and 0 minors. DoD 16/17. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Nothing goes back to the builder. The lane run before the PR stamps ADR-0337 and ADR-0339 `Implemented`. The decider
rules on the backup question and the two choices. The PR that ships the batch names issue #1514.

### Lane case (added at the Implemented stamp, 2026-11-22)

`addon-rauthy` passed on containerd at d46b7c03, 99 s: an admin's change of rauthy's Addon stamped `rauthy-2` while `login` kept its engine (the same task) and its pin `rauthy-1`; the App's second revision moved `web-auth-login` to `rauthy-2`, and `funcdctl app rollback web-auth 1` took it back to `rauthy-1`. The stamp is now set.
