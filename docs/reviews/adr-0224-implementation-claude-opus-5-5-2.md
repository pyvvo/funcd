## Verdict: pass, with 0 blockers, 0 majors and 2 minors (ADR-0224 implementation, re-review round 2, model: claude-opus-5-5)

Scope: `git log d03ecd5c..HEAD` on `feat/adr-0224-0225-pool`: 99e1136c (feat), 49856062 (status bump) and the new
top commit 8185383f ("address review of ADR-0224 implementation"). This round checks that 8185383f resolves the two
model Minors of round 1 (`review-0224-loop1.md`) and that nothing regressed, including the ADR-0225 tests on the base.

### Blockers

None.

### Majors

None.

### Minor

- **A failed member holds the switch, so the #70 shape-failure test and its e2e had to change, and the ADR's plan
  names neither** · attribution: adr (Q1, decided) · carried over from round 1 unchanged; recorded, not scored.
- **After funcd restarts, `keep` is empty, so the restart record waits for no member** · attribution: adr (Q2,
  decided) · carried over from round 1 unchanged; recorded, not scored.

### Round-1 model findings: resolved

- **`from`'s drain clock is now pinned to the switch.** `TestPoolSwitchDrainsFromTheSwitch`
  (`internal/function/pool_switch_test.go`) sets `BootTimeout` 1 min and `DrainGrace` 30 s, holds a call in flight on
  O through the activator's `CallTracker`, switches at the load timeout, and requires O to be kept at the switch.
  Subtest "drainGrace from the switch" then requires O kept at `drainGrace - 1ms` and retired at `drainGrace`. Subtest
  "idle after the call" requires O retired once the call ends and `HandOutSettle` passes. Mutant M5, which deletes the
  `if in.ID == sw.from { start = sw.switched }` override in `drainPool`, now FAILS `TestPoolSwitchDrainsFromTheSwitch`.
  In round 1 it survived.
- **The doc comments are re-wrapped.** The `convergePooled` and `ensurePool` docs in `internal/function/pool.go` are
  reflowed with the wording unchanged (checked in the diff), and every re-wrapped line is at most 120 columns. Five
  comment lines of 121 to 122 columns elsewhere in `pool.go` predate this branch (5 at the base d03ecd5c). They are not
  in scope.
- The commit touches only `pool.go` (comments only, no code change) and `pool_switch_test.go` (the test plus the
  `heldCall` transport and the `net/http` and `sync` imports). There is no unrelated change.

### Verified (checks run this round)

- **gofmt**: `gofmt -l internal/ pkg/` prints nothing.
- **Build**: `go build ./...` exit 0, and `GOOS=linux go build ./...` exit 0.
- **Vet**: `go vet ./internal/function/ ./pkg/funcd/`, `GOOS=linux go vet ./internal/function/` and
  `go vet -tags e2e ./pkg/funcd/` exit 0.
- **Lint**: golangci-lint on `./internal/function/... ./pkg/funcd/...` prints "0 issues." on darwin. It also prints
  "0 issues." for `GOOS=linux`, run as a host-built binary, because `go tool` under `GOOS=linux` execs a linux binary.
- **Hygiene**: `just check-hygiene` reports "hygiene: clean".
- **Package tests**: `go test -race -count=1 ./internal/function/` reports `ok` in 11.0 s. That run includes ADR-0225's
  boot-rule tests on the base, so they did not regress.
- **New test, run 30 times**: `go test -race -count=30 -run TestPoolSwitchDrainsFromTheSwitch` reports `ok` in 2.4 s.
- **Scenario and contract tests, run 3 times**: the 10 `// scenario:` tests, `TestReadyForSwitch`, `TestPoolSwitch*`,
  `TestServingPoolMakesNoProbe`, `TestIssue70*`, `TestIssue863*`, `TestPoolRebuild*`,
  `TestPoolResolverAfterRestart`, `TestPoolPinned*` and the Boot/Crash tests all pass, `ok` in 22.4 s.
- **Mutants**, applied through `go test -overlay` with the work left untouched:
  - M5 drops the `switched` override, so `from` drains from `since`. It FAILS `TestPoolSwitchDrainsFromTheSwitch` and is
    killed.
  - M6 changes `elapsed < r.drainGrace` to `<=` in `drainPool`. It FAILS `TestPoolSwitchDrainsFromTheSwitch` and
    `TestPoolRebuildKeepsOldUntilNewListens` and is killed.
- **Identity**: the diff `d03ecd5c..HEAD` has no absolute-path prefix, local username or personal email. The working
  tree is clean.
- **Tracking**: ADR-0224 still reads `Reviewing`. This gate was told not to stamp `Implemented`.

Everything that round 1 verified holds: the contracts, the Review checklist, Decisions 2, 5 and 6, and mutants M1 to
M4. None of that code changed in 8185383f.

### Definition of Done

10 / 10 hold. `just ci` was checked through its sub-steps: build, vet, lint for darwin and linux, and the touched
package's tests with `-race`. The repo-wide `just ci-full` runs once in the PR gate.

### Recommendation

Pass. Both model findings from round 1 are resolved, and both mutants this round are killed. The two adr-attributed
items (Q1, Q2) are decided and need no code change.

```json
{"adr": "0224", "phase": "implementation", "model": "claude-opus-5-5", "verdict": "pass", "blockers": 0, "majors": 0, "minors": 2, "model_attributed": 0, "dod_passed": 10, "dod_total": 10, "report": "docs/reviews/adr-0224-implementation-claude-opus-5-5.md", "notes": "re-review round 2: both round-1 model findings resolved (TestPoolSwitchDrainsFromTheSwitch kills M5; doc comments re-wrapped to 120); adr: Q1 failed member holds switch (#70 tests changed), Q2 restart record has empty keep; mutants M5, M6 killed; race package ok, new test -count=30 ok"}
```
