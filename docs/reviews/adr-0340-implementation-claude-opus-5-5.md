## Verdict: pass — 0 blockers, 0 majors, 0 minors (ADR-0340 implementation, model: claude-opus-5-5)

This report reviews the commit `feat: implement ADR-0340` on the branch that ships FEAT-0019's five ADRs in one PR,
on `origin/main` d5bef540. By the decider's standing rule of no review agents, the session that wrote the code ran
this review, so it is not independent. Every check below was run, not read. The `Implemented` stamp waits for
ADR-0341's commit: the scenario `settings-object-no-engine` names `spec.functions`, a field that ADR-0341 adds.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

None.

### Routed (not this work's)

- **`spec.functions` on a settings object** (the fifth field of `settings-object-no-engine`): the field is ADR-0341's
  (its Decision 2), so its refusal case joins `TestScenarioSettingsObjectNoEngine` in ADR-0341's commit, which then
  stamps both ADRs and F266, the row they share.
- **`TestShutdownReleasesTheLoopbackFeedPort` failed once** in the full `pkg/funcd` run on a loaded host and passed
  5 of 5 runs alone. Not checked yet: this change touches no SQL feed code, and the test rebinds an ephemeral port
  that a parallel test can take. For the flake tracker #1142, if the decider wants an issue.

### ✅ Verified correct (keep it)

- **The checks**, through `scripts/agent/d`: `go test -race` on the settings scenarios passes; `go test` on
  `pkg/funcd`, `cmd/funcd`, `internal/controlplane/...`, `internal/services/addon/...`, `internal/platform/config`
  and `api/types/v1alpha1` passes except the flake above; `go vet` and `go tool golangci-lint run` on the touched
  packages are clean; `just check-behavior` and `just check-hygiene` pass. The OpenAPI spec and the Spec page come
  from `just generate`. The repo-wide checks run once, in the PR's gate.
- **The scenarios**: all 11 have a test named after them, un-skipped and passing
  (`pkg/funcd/addon_settings_internal_test.go`). `settings-kind-refused` covers the 409 on an Addon update too, and
  checks that the refused update is not stored.
- **The mutants**, each run with `go test -overlay` against the scenario that should catch it:

  | Mutant | Caught by |
  |---|---|
  | `count.perTarget` off by one | `TestScenarioSettingsCountPerTarget`, `TestScenarioSettingsConcurrentCreates` |
  | the quota counts every add-on object | `TestScenarioSettingsObjectCap` |
  | `addon-unique` on a create only | `TestScenarioSettingsKindRefused` (the update case) |
  | `addon-settings` reads the namespace outside the lock | `TestAddonAdmissionsTakeTheLock` |
  | the engine's env carries `spec.settings` | `TestAddonSettingsChangeReplacesNoEngine` |

  The lock mutant survived `TestScenarioSettingsConcurrentCreates` 20 of 20 runs: on the in-memory store, two HTTP
  creates rarely overlap inside the admission. So `TestAddonAdmissionsTakeTheLock` asserts what ADR-0147's lock
  needs, `ReadsNamespace` and both operations, for `addon-settings` and `addon-unique`.
- **The contracts**: `AddonSettings` and its constraints match the ADR's types; a settings object is an
  `AddonObject` served at its kind's plural; the reconciler returns before `remember` for one, so it runs no engine
  and holds no entry; `addonobject-quota` counts the class of the object it admits, against
  `addons.maxObjectsPerNamespace` or the new `addons.maxSettingsPerNamespace` (0 means 200, a negative value no cap).
- **The behavior rows** the ADR lists are in `docs/behavior/addons.md` and `docs/behavior/limits.md`, and the generated
  value tables are current.

### Definition of Done

13 of 14 items hold: the 5 Review-checklist items, the touched packages' checks (the gate runs `just ci-full`), and 7
generic items (real behavior, contracts, tree, conventions, no new dependency, no scope creep, tracking). The item
still open is the `spec.functions` clause of one scenario, which ADR-0341's commit closes.

### Model scorecard

To record: claude-opus-5-5 on ADR-0340 (implementation) → pass, 0 blockers, 0 majors and 0 minors. DoD 13/14. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Nothing goes back to the builder. ADR-0341's commit adds the last refusal case and stamps ADR-0340 `Implemented`.
The PR that ships the batch names issue #1515.
