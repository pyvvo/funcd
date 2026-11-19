## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0334 implementation, round 2, model: claude-opus-5-5)

Reviewed at `f3d41c42` of `feat/addons`. That commit answers round 1 (`docs/reviews/adr-0334-implementation-claude-opus-5-5.md`). Every round-1 finding placed here is fixed, and each fix is shown by a run: the new dev-level scenarios fail under mutants of dev's wiring, and the round-1 mutant M2 and a mutant of the new lock both fail now. One new minor remains: the new race test leaks a volume when it fails.

### Round-1 findings

- **Major 1, dev-level deletion and backup: fixed.** `cmd/funcdctl/dev_addon_scenarios_test.go` adds `TestScenarioDevAddonDeletion` and `TestScenarioDevAddonBackup`, which run under `funcdctl dev` on Docker and are skipped with the reason without Docker. On colima's Docker they pass in 6.5 s and 8.0 s.
  - The deletion test runs `delete` and then `retain` under `--persist`, through the reload's prune.
  - The backup test runs a Backup to dev's S3 gateway and then an in-place Restore. It asserts that the member has the add-on's kind and some bytes, that the restore mark is cleared, that `restore.path` is empty, and that the engine runs in a new container.
  - Two mutants of the wiring that round 1 named (`pkg/funcd/addon_provider.go:101-102`) now fail these tests:
    - M5 hides the runtime's `workload.AddonFiles`: `TestScenarioDevAddonBackup` fails in 1.3 s with "member EchoService/echo: this server reaches no add-on engine's files".
    - M6 hides its `provider.Volumes`: `TestScenarioDevAddonDeletion` fails on "delete: the engine and its volume go".
  - The driver-level tests are renamed `TestDevAddonDeletionAtTheRuntime` and `TestDevAddonBackupAtTheRuntime`, so each scenario name now maps to one test.
- **Minor 1, the move against the sweep: fixed.** `docker.Runtime` has a quarantine mutex, which `Move` and `Expire` take (`internal/provider/docker/docker.go:848-849` and `:981-982`). `TestQuarantineMoveAndExpireTakeTurns` runs `Expire` from a hook right after the entry's `CreateVolume`. It passes in 4.2 s. Under mutant M4, which drops `Move`'s lock, it fails: "\"[]\" should have 1 item(s), but has 0".
- **Minor 2, the fake nerdctl: fixed.** The fake counts only a `type=volume` mount as a use of a volume, as nerdctl does, and the `RemoveVolume` doc says that nerdctl does not refuse. The round-1 mutant M2 (`Reclaim` without its container check) now fails `TestEngineThroughNerdctl` ("the engine's container exists").
- **Minor 3, containerd: done.**
  - `f3d41c42`'s message records `dev-addon-data` with `--persist` and `dev-addon-backup` with an in-place Restore, both run by hand under dev with `dev.addons.runtime: containerd` on nerdctl v2.3.1. `bede185b` recorded `dev-addon-docker`.
  - The lane issue is #1502 (`kind/task`, a sub-issue of #1496).
- **Minor 4: fixed.** The driver calls `v1.IsAddonFile`.
- **Minor 5: fixed.** The `security.md` row cites `TestScenarioDevAdminTokenLoopbackOnly`, and `just check-behavior` passes.
- **Routed notes: unchanged, still with the decider.** Decision 3's `volume-nocopy` and the Design model's reason make no Definition-of-done item impossible, so they block nothing.

### Minor

- **`TestQuarantineMoveAndExpireTakeTurns` leaks the entry's volume when it fails** (attribution: model).
  - When the race it guards against happens, the entry volume loses its labels. The test's cleanup (`cleanupKey`) removes only labelled volumes, so it leaves that volume behind.
  - The run of M4 in this review left `funcd-dev-addon-<key>-default-login-<since>` with `Labels: null` on colima's Docker. This review removed it by name.
  - An earlier failing run of the same test left another such volume, created at 10:27:07 and holding `state: kept` and `data/db/users`. It is still on colima's default Docker, and this review left it there.
  - Fix: record the volume name in the hook (`CreateVolume` receives it) and remove that volume in a `t.Cleanup`.

### ✅ Verified correct (keep it)

- **The lock**: `Move` holds it for its whole copy, and `Expire` for its whole scan. Neither path takes another lock, and `List` stays unlocked: it skips an entry with no state or data, as ADR-0288 Decision 2 says. The nerdctl driver shares the lock through the embedded `docker.Runtime`.
- **The tree**: `f3d41c42` changes five files, all test, documentation or the round-1 fixes. The ADR and the F260 row are unchanged since round 1 (`Reviewing` and `reviewing`).
- **Checks**, all exit 0 and run through `scripts/agent/d`:
  - `go build`, `go vet` and `go tool golangci-lint run` (0 issues) on `internal/provider/docker`, `internal/provider/nerdctl` and `internal/testkit/fakenerdctl`, and on `cmd/funcdctl` with the `dev` build tag;
  - `go test -race -count=1` of `internal/provider/docker` and `internal/provider/nerdctl`, every test run and none skipped;
  - `go test -race -count=1 -tags dev` of `cmd/funcdctl`: the add-on scenarios, all eight run and none skipped (37 s), and the whole package (272 s);
  - `just check-behavior` and `just check-hygiene`.
- **`just check-specs`**: not rerun, because no file under `docs/design` changed since round 1, when it passed (21 specs, 0 failing).
- No container or volume of these runs was left behind.

### Definition of Done

19 / 19 items hold. The three items that failed in round 1 now hold:

- each Scenario's test is where the plan puts it;
- the containerd runs are recorded;
- the containerd lane's issue is filed.

`just ci` is pending the final gate, as in round 1.

### Model scorecard

The integrator records: claude-opus-5-5 on ADR-0334 (implementation, round 2) → pass, 0/0/1, 1 model-attributed, DoD 19/19.

### Recommendation

Pass. The minor is a one-line cleanup in the test, and the leftover volume should be removed by name. Once the final gate is green, the integrator records the ledger row and stamps ADR-0334 `Implemented` and F260 `implemented`. The PR closes #1500.

### Scorecard arguments

```
python3 .claude/skills/adr-impl-review/scripts/scorecard.py record --ledger docs/reviews/model-ledger.json --adr 0334 --phase implementation --model claude-opus-5-5 --verdict pass --blockers 0 --majors 0 --minors 1 --model-attributed 1 --dod-passed 19 --dod-total 19 --report docs/reviews/adr-0334-implementation-claude-opus-5-5-2.md --notes "round 2: the round-1 Major and 5 minors fixed; the dev-level deletion and backup scenarios kill the AddonFiles and Volumes wiring mutants, the quarantine lock kills the race mutant, and the fake nerdctl now kills M2; minor (model): the take-turns test leaks an unlabelled volume when it fails; the routed adr notes are unchanged"
```
