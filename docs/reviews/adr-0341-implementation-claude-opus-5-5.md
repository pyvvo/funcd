## Verdict: pass — 0 blockers, 0 majors, 0 minors (ADR-0341 implementation, model: claude-opus-5-5)

This report reviews the commit `feat: implement ADR-0341` on the branch that ships FEAT-0019's five ADRs in one PR,
after ADR-0338's commit. By the decider's standing rule of no review agents, the session that wrote the code ran this
review, so it is not independent. Every check below was run, not read. The commit also stamps ADR-0340 `Implemented`:
its review left one clause open, a settings object's `spec.functions`, which this commit refuses. ADR-0341's own stamp
waits for the lane case (`addon-rauthy`, with the settings Function of pyvvo/funcd-addons: it applies a policy,
repairs a drift at rauthy's API and resends after funcd's restart) and for the add-ons page's section on settings
kinds.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

None.

### Routed (not this work's)

- **The lane case** waits for rauthy's settings Function and template in pyvvo/funcd-addons, released and pinned.
- **The add-ons page's section** on a settings kind and its Function, with rauthy's password policy, comes with the
  docs pass that the decider asked for, from that Function's code.
- **`TestBackup_GoesOnWhenItsListingFails` failed again** in a full `pkg/funcd` run on a loaded host, as in ADR-0339's,
  and passed 5 of 5 alone; this change touches no backup listing. Not checked yet: for the flake tracker #1142, if the
  decider wants an issue.

### ✅ Verified correct (keep it)

- **The checks**, through `scripts/agent/d`: `go test` on the touched packages (`api/types/v1alpha1`,
  `internal/controlplane/...`, `internal/app/...`, `internal/sensor`, `internal/invoke`, `internal/services/addon`,
  `internal/platform/...`, `cmd/funcd`, `scripts`, and all of `pkg/funcd`, whose TestMain checks every declared answer);
  the settings tests 5 times under `-race`; `go vet`; `go tool golangci-lint run`, 0 issues; `just check-behavior`;
  `just check-specs`, 23 specs, 0 failing, `settings.fizz` passing with its 7 mutants failing; `just generate`.
- **The scenarios**: all 12 have a test, 10 in `internal/services/addon` over a fake caller and a runtime whose engines
  are Ready, and `settings-functions-refused` and the caller's CloudEvent in `pkg/funcd`.
- **The design model's oracles**: `OneCallAtATime` on `TestScenarioSettingsHungFunction`, `ReadyMeansApplied` on
  `TestScenarioSettingsRecreate`, `Converges` on `TestScenarioSettingsDrift`.
- **The mutants**, each run against the tests that should catch it:

  | Mutant | Caught by |
  |---|---|
  | no in-flight guard | `TestScenarioSettingsHungFunction` |
  | a failed call marks the objects Ready | `TestScenarioSettingsHungFunction`, `TestSettingsBackoff` |
  | an answer ignores the uid | `TestScenarioSettingsRecreate` |
  | an answer ignores the generation | `TestScenarioSettingsEdit` |
  | no retry | `TestScenarioSettingsHungFunction`, `TestSettingsBackoff` |
  | no resync | `TestScenarioSettingsDrift` |
  | the digest leaves out the uid | `TestScenarioSettingsRecreate` |
  | the digest leaves out the Function's revision | `TestScenarioSettingsFunctionChange` |
  | no `FunctionNotBound` wait | `TestScenarioSettingsWaits` |
  | the target's own Function is ignored | `TestScenarioSettingsFunctionPerTarget`, `TestMapSettingsAndFunction` |
  | the backoff ignores the resync | `TestSettingsBackoff` |
  | a new object is never `Progressing` | `TestScenarioSettingsApply`, `TestScenarioSettingsEdit`, `TestScenarioSettingsRestart` |
  | a kind named twice in `spec.functions` passes | `TestScenarioSettingsFunctionsRefused` |
  | an App's part names an undeclared Function | `TestScenarioSettingsFunctionsRefused` |

  The generation mutant first survived: no test edited an object while its call was in flight. `TestScenarioSettingsEdit`
  now does, and the answer for the old generation marks the new one nothing.
- **The contracts**: `SettingsCaller`, `SettingsCall`, `SettingsTarget`, `SettingsObject`, `SettingsReply` and
  `SettingsResult` as written; `AddonObjectSpec.Functions` and `AddonObjectFunction`; `HTTPInvoker.InvokeReply`;
  `invoke.AddonSettings`; the CloudEvent `funcd.addon.settings` from `funcd://<namespace>/addon/<target>`; the answer
  read under 1 MiB; `addons.settingsTimeout` (30 s); the eight reasons. The step runs after a Ready pass on a goroutine
  of its own, bounded by the timeout, and funcd's stop cuts it and writes nothing.
- **ADR-0340's open clause**: `spec.functions` is the fifth field a settings object may not set, in
  `TestScenarioSettingsObjectNoEngine`; ADR-0340 is stamped `Implemented` in this commit. A settings object now has a
  status that funcd writes, so the settings tests' replace reads again after a 409.
- **Choices of the implementation, for the decider to confirm**: the digest holds the Function's uid too, so a
  Function created again sends the list again; the step reads the Addon as it is now, as admission and a settings
  object's pass do, not the revision the target pins; a Function's change requeues its targets as well as its
  objects; a call's end requeues the objects it wrote, whose passes check their waits again; a status write retries a
  conflict three times; the answer's 1 MiB bound is `settingsReplyLimit` in `pkg/funcd`, the value of the local API's
  `maxInvokeBytes`.
- **The behavior rows** the ADR lists are in `docs/behavior/addons.md` (seven rows) and `limits.md`.

### Definition of Done

18 of 20 items hold: the 7 Review-checklist items, the scenarios, the design model and its oracles, the touched
packages' checks (the gate runs `just ci-full`), the behavior rows, and 7 generic items. Open: the lane case, and the
add-ons page's section.

### Model scorecard

To record: claude-opus-5-5 on ADR-0341 (implementation) → pass, 0 blockers, 0 majors and 0 minors. DoD 18/20. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Nothing goes back to the builder. The lane run before the PR, with the docs section, stamps ADR-0341 `Implemented`,
and F266 then turns `implemented`. The decider confirms the choices. The PR that ships the batch names issue #1515.

### Lane case (added at the Implemented stamp, 2026-11-22)

`addon-rauthy` passed on containerd at d46b7c03, 99 s: rauthy's settings Function of pyvvo/funcd-addons v0.2.0 put login's policy into rauthy, which then refused a password under 16 characters; a looser policy put at rauthy's own API was put back at the next resync; a second policy was refused; after funcd's restart, login's first pass sent its list with the cause `start`; and the delete set rauthy's default of 14 back. The guide's Add-ons page gained "Apply its settings". The stamp is now set.
