## Verdict: pass — 0 blockers, 0 majors, 0 minors (ADR-0337 implementation, model: claude-opus-5-5)

This report reviews the commit `feat: implement ADR-0337` on the branch that ships FEAT-0019's five ADRs in one PR,
after ADR-0340's commit. By the decider's standing rule of no review agents, the session that wrote the code ran this
review, so it is not independent. Every check below was run, not read. The `Implemented` stamp waits for ADR-0339's
commit: Review-checklist item 4 (deploy waits for the Addon's `status.currentRevision`) and the clause "and its record"
of item 5 read fields that ADR-0339 adds. The lane case waits for rauthy's template, which funcd-addons publishes once
ADR-0338's client exists.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

None.

### Routed (not this work's)

- **Deploy's wait for `status.currentRevision`** (Decision 2, checklist item 4) and a part's readiness at its record
  (Decision 4): the field and the record are ADR-0339's (its Decisions 1 and 4), so ADR-0339's commit adds the wait
  and the clause, then stamps both ADRs.
- **The lane case** (`addon-rauthy` deploys rauthy's template as an admin, then as a developer in a second namespace):
  the template goes to funcd-addons with ADR-0338's and ADR-0341's parts, in one release that funcd then pins; the lane
  runs once, before the PR.

### ✅ Verified correct (keep it)

- **The checks**, through `scripts/agent/d`: `go test` passes on `api/types/v1alpha1`, `internal/app/...`,
  `internal/gc`, `internal/backup/workload`, `internal/controlplane/...`, `internal/services/addon/...`, `pkg/funcd` and
  `cmd/funcdctl`, and with `-tags dev` on `cmd/funcdctl`'s add-on and template tests; `go tool golangci-lint run` on
  the touched packages, with and without the dev tag, reports 0 issues; `just check-behavior` passes; the OpenAPI spec
  and the App's Spec page come from `just generate`. The repo-wide checks run once, in the PR's gate.
- **The scenarios**: all 13 have a test named after them, un-skipped and passing. `addon-template-admin-install` is
  `TestScenarioAddonTemplateAdminInstall` (the deploy's lines and writes) with `TestScenarioAddonPartReady` (the part
  turns Ready on the platform); `addon-app-delete` is covered twice, the GC in `pkg/funcd` and the delete walk in
  `cmd/funcdctl`, the walk with the Addon removed first too.
- **The mutants**, each run with `go test -overlay` against the scenario that should catch it:

  | Mutant | Caught by |
  |---|---|
  | no `addon-object` admission | `TestScenarioAddonPartRefused` |
  | no `(App, AddonObject)` pair | `TestScenarioAddonPartPruned`, `TestScenarioAddonAppDelete` |
  | a line names the stored `AddonObject` | `TestScenarioAddonPartReady` |
  | deploy skips the Addon | `TestScenarioAddonTemplateAdminInstall` |
  | the delete walk skips the add-on kinds | `TestScenarioAddonAppDelete` (cmd/funcdctl) |
  | a developer's deploy skips the pre-check | `TestScenarioAddonTemplateDeveloperUnregistered` |
  | `scope.app` leaves the parts out | `TestScenarioAddonAppBackup` |

- **The contracts**: `AppSpec.Addons` sits after `sql`, `AppAddon` is `name`/`ref` over `AddonObjectSpec` with its own
  schema and `MarshalJSON`, and `Template.Addon` is nil without `addon.yaml`. A part is stored as an `AddonObject`
  under the App's controller reference; the App's keys stay on the stored kind, and every line, a stop's and a plan's
  included, names the add-on kind (`shownKind`). `app-parts` reaches the new `addon-object` admission, which repeats
  for an App's part the checks the API's routes make before any admission, and `binding-declared` checks an add-on
  object's Secrets only when an App controls it.
- **Two choices of the implementation, for the decider to confirm**: an `addons` entry's kind must be written as its
  Addon writes it (another case is refused, as the API's route refuses a body kind that is not the plural's), and
  under dev a template's Addon and an `--apply` document of the same name are refused.
- **The behavior rows** the ADR lists are in `docs/behavior/apps.md`, `funcdctl.md` and `funcdctl-dev.md`, split to
  fit the registry's row cap; the guide's App page has a "Carry an add-on" section, and the add-ons page links it.

### Definition of Done

14 of 17 items hold: 4 of the 6 Review-checklist items (item 4 and the record clause of item 5 wait for ADR-0339),
the touched packages' checks (the gate runs `just ci-full`), the scenarios, and 8 generic items (real behavior,
contracts, contract tests, tree, conventions, no new dependency, no scope creep, tracking). Open: checklist item 4,
the record clause, and the lane case.

### Model scorecard

To record: claude-opus-5-5 on ADR-0337 (implementation) → pass, 0 blockers, 0 majors and 0 minors. DoD 14/17. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Nothing goes back to the builder. ADR-0339's commit adds the deploy's wait and the record clause and stamps ADR-0337
`Implemented`; the lane case runs before the PR. The PR that ships the batch names issue #1504.

### Lane case (added at the Implemented stamp, 2026-11-22)

`addon-rauthy` passed on containerd at d46b7c03, 99 s: rauthy's template, pushed as one artifact, deployed by an admin's `funcdctl app deploy` as `web-auth`, whose IdentityProvider `web-auth-login` turned Ready with its own settings Function `web-auth-settings`, which got the calls for its policy. The stamp is now set.
