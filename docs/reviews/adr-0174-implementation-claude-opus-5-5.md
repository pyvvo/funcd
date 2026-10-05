# ADR-0174 implementation review — claude-opus-5-5 (loop 1)

- **ADR**: [ADR-0174](../adr/0174-never-booted-revision-is-unknown.md), a revision that never booted is Unknown, not Ready
- **Work**: branch `feat/adr-0174-never-booted-unknown`, one commit `2c53c014` over `origin/main`
  (`internal/function/function.go` +59/-24 net, `function_test.go`, `pool_test.go`, `supervision_test.go`)
- **Model**: claude-opus-5-5
- **Verdict**: **pass**. No Blocker, no Major, one Minor (attributed to the ADR).

## Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go test -race -count=1 ./internal/function/...` | `ok github.com/pyvvo/funcd/internal/function 10.325s` |
| the eight scenario tests, `-race -v` | 8/8 `--- PASS` |
| `go vet ./internal/function/...` (host, Linux) | exit 0, exit 0 |
| `golangci-lint run ./internal/function/...` (host) | `0 issues.` |
| `golangci-lint run ./internal/function/...` (Linux target) | `0 issues.` (env: `GOOS=linux go tool golangci-lint` builds a Linux linter binary that cannot run on the host, `exec format error`; rerun with the host-built binary and `GOOS=linux`) |
| `gofmt -l internal/function` | empty |
| tree | clean; diff touches only `internal/function/` |

Not run here, by the process: `just ci` / `go test ./...` / e2e (the repo-wide gate runs once per PR). A grep of `e2e/`
and `pkg/funcd/` for `RevisionReady`/`ShapeValid` finds only failure-reason assertions (`ArtifactUnresolved`,
`ShapeInvalid`, `e2e/env-echo.venom.yml:336,367`; `pkg/funcd/redeploy_e2e_test.go:272-273`), which this change does not
alter. The only other reader in production code, `steadyState` (`function.go:750`), needs `RevisionReady` True on a
`Ready` Function; a `Ready` pass has `v.ready >= 1`, so it still reads True.

### Overlay mutants (`go test -overlay`, `-run 'TestScenario|TestIssue70_|TestIssue73_|TestIssue359_'`)

| Mutant | Change | Killed by |
|---|---|---|
| m1 | `loaded := true` (the pre-ADR behavior) | NeverBootedIdleIsUnknown, FirstReadyReplicaTurnsTrue, RedeployWhileIdleIsUnknown, SwitchInProgressShapeUnknown, PooledMemberNeverBootedIsUnknown, TestIssue70/73/359 |
| m2 | drop `!v.switching &&` from `loaded` | SwitchInProgressShapeUnknown |
| m3 | `served` ignores `ObservedGeneration` | RedeployWhileIdleIsUnknown, SwitchInProgressShapeUnknown |
| m4 | pool gate writes True without `served` | PooledMemberNeverBootedIsUnknown |
| m5 | `served` always false | ReclaimKeepsTrue, WakeOfServedGeneration, TestIssue70, TestScenarioFailedRestartRetriesWithBackoff |

5/5 killed. Every key line of the decision (the `loaded` predicate, its switching guard, the generation check in
`served`, the pool-gate branch, the served short-circuit) is held by a test.

## Contracts

- `served(fn)` (`function.go:662-666`) is the Contract's function verbatim: `ShapeValid` True with
  `ObservedGeneration == fn.Generation`. `Conditions.Set` replaces the whole condition (`api/types/v1alpha1/status.go:35-49`),
  so the stored `ObservedGeneration` moves with every write and `served` reads the latest one.
- The YAML shape: `notStarted` (`function.go:668-670`) writes `Unknown`, reason `NotStarted`, message
  "no replica of this generation has been ready yet", `observedGeneration: fn.Generation`, matching the Contract's YAML.
  `TestScenarioNeverBootedIdleIsUnknown` asserts the message too.
- Consumes/Exposes: `finish` reads `served` once, before either write (`:586`), as Decision 4 requires; no status field
  is added (no change under `api/`).

## Review checklist (5/5)

1. **No True for a generation without a ready replica — solo, pooled (pool gate), switch.** Held. `finish` writes True
   only on `loaded := (!v.switching && v.ready >= 1) || served(fn)` (`:586`, `:595`, `:644`), else `notStarted`
   (`:597-598`, `:646-647`); the pool gate (`gateFailed`, `:530-535`) writes True only when `served`. Killed mutants
   m1, m2, m4.
2. **True across reclaim and wake of the same generation; a new generation while Idle writes Unknown.** Held:
   `TestScenarioReclaimKeepsTrue`, `TestScenarioWakeOfServedGeneration` (ShapeValid True while the woken replica boots,
   RevisionReady False/Progressing, then both True), `TestScenarioRedeployWhileIdleIsUnknown` (`agent-2`, no create,
   Unknown/NotStarted, then True after a wake). Killed mutants m3, m5.
3. **Every `RevisionReady`/`ShapeValid` write sets `ObservedGeneration: fn.Generation`; no status field added.** Held: a
   grep of the package finds 13 writes (`:525-647`), every one with `gen` or through `notStarted`; no write exists
   outside `function.go`.
4. **Failure writes keep status and reason.** Held: the diff of the `ShapeInvalid`, `StartFailed`, `Progressing` and gate
   writes adds only `ObservedGeneration`; `TestScenarioFailedGenerationKeepsFalse` asserts False/`ShapeInvalid` at the
   generation, and the pool-gate subtest asserts `RevisionReady` keeps reason `PoolFull`.
5. **One named, passing test per scenario.** Held: eight `// scenario: <name>` tests, all passing under `-race`;
   `TestScenarioPooledMemberNeverBootedIsUnknown` covers both of its cases (`pool-wants-zero`, `pool-gate`).
   `just ci` is left to the PR gate, by the process; its per-package sub-checks are green above.

The three updated assertions (`TestIssue70`, `TestIssue73`, `TestIssue359`: ShapeValid Unknown for a Function whose
worker never started) are the "existing assertions that expect True on a never-booted Function" that Implementation plan
step 2 asks to update. They are not weakened: each still asserts the failure write, and m1 fails all three.

## Findings

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

1. **(adr) Decision 2 and scenario `switch-in-progress-shape-unknown` disagree when `replicas > 1`.** The scenario says
   ShapeValid is Unknown "until a replica of revision 2 is ready". Decision 2 defines a ready replica as
   `!v.switching && v.ready >= 1`, and `switchSolo` clears `switching` only when every desired replica of the new
   revision is ready (`function.go:879-882`). So with three replicas, ShapeValid stays Unknown while one or two replicas
   of revision 2 are ready, though on a first deploy (no serving revision) the first ready replica turns it True. The
   code follows Decision 2 and the Implementation plan exactly, the result is conservative (never a false True), and
   the test uses one replica. Recorded against the ADR; it does not count against the model.

## Verified correct — keep it

- The decision is one predicate (`loaded`) computed once, before both writes, and reused by both `switch` statements.
  The `ShapeValid` write stays a single write per pass, which keeps the issue #24 guard against `LastTransitionTime`
  churn.
- `notStarted` keeps the new reason and message in one place for both conditions and both write sites.
- The pool-gate branch changes only the value the ADR scopes in; the gate's own `RevisionReady`/`PoolFull` writes keep
  their reason.
- The tests drive the real reconciler through the shim harness (503 readiness for the issue's case, a held revision for
  the boot window, `withSwitch` for the switch, `PoolLimit: 1` for the gate) and assert `observedGeneration` on every
  condition through one helper (`requireRevisionStatus`).
- The commit message lists the scenario tests and the updated issue tests, and carries `Fixes #610`.

## Tracking

The ADR reads `Accepted` and the F13 row reads `never-booted Unknown: accepted`; both already name ADR-0174
(Implementation plan step 3). By the orchestrating process, status moves happen in the wave's docs PR, so this review
stamps nothing. On this pass, that PR moves the ADR to `Implemented` and the F13 entry to `implemented`. ADR-0174
realizes F13 as a status-truthfulness correction, not a new feature, so no board card is expected.

## Recommendation

Pass. Integrate the commit. Carry the Minor to the ADR owner: a later ADR that touches the switch can say whether
"a ready replica of the new revision" means the first one or the completed switch.

## Ledger row

```json
{
  "date": "2026-10-05",
  "adr": "0174",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 0,
  "dod_passed": 5,
  "dod_total": 5,
  "report": "docs/reviews/adr-0174-implementation-claude-opus-5-5.md",
  "notes": "served() matches the Contract verbatim; every RevisionReady/ShapeValid write carries observedGeneration; 8/8 scenario tests pass under -race; build/vet/lint green on host and Linux; 5/5 overlay mutants killed (loaded predicate, switching guard, generation check, pool gate, served short-circuit); Decision 2 vs switch scenario differ for replicas>1, Unknown until the switch completes (adr)"
}
```
