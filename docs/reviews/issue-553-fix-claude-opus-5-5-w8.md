# Review — issue #553 (split funclog CompactOnce), fix by claude-opus-5-5

- **Issue**: #553, kind/task: "Split funclog CompactOnce into group, compact-window and prune steps"
- **Change**: branch `fix/w8-i553`, commit 1d066de `refactor(funclog): split CompactOnce into group, compact-window and prune steps`; one file, `internal/funclog/compact/compact.go` (+80/-58)
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**: 0 Blocker, 0 Major, 0 Minor

## The decision this change is judged against

The person decided: a refactor with no behavior change. CompactOnce calls three named helpers and comes back under
funlen, gocyclo and gocognit, and the funclog tests, including the #217 data-loss regression tests, pass unchanged.

## Verification run

| Check | Result |
|---|---|
| Audit linters (`scripts/agent/audit.golangci.yml`: funlen 80 lines / 50 statements, gocyclo 20, gocognit 30) on `internal/funclog/compact` at `origin/main` | 3 issues: funlen, gocognit, and gocyclo 24 on `(*Compactor).CompactOnce` (compact.go:159) |
| The same linters at the branch head | `0 issues.` CompactOnce and the three helpers are all under the limits |
| Test files changed | none (`git diff origin/main...HEAD -- '*_test.go'` is empty), so the tests pass unchanged |
| `go test -race -count=1 ./internal/funclog/...` | `ok` for funclog, funclog/compact and funclog/logread |
| `go vet ./internal/funclog/...` | clean |
| `golangci-lint run ./internal/funclog/...` (repo config) | `0 issues.` |
| Revert check | not applicable: a task with no `TestIssue553` test. The "Done when" items were verified directly (rows 1 to 3) |

### Mutants (overlay on compact.go, compact package tests only)

| Mutant | Line | Result |
|---|---|---|
| m1: the pending dedupe always re-adds raw (`if true`) | compactWindow | killed: `TestScenarioCrashSafeNoLoss` |
| m2: the undecodable-window skip returns the error instead of `nil` | compactWindow | killed: `TestIssue84_UndecodableRawObjectDoesNotBlockCompaction` (pass 1 returns the readRaw decode error) |
| m3: CompactOnce never counts a written window (`rows > 0` set to never true) | CompactOnce | killed: `TestScenarioCompactsClosedWindow`, `TestIssue84_…` |
| m4: compactWindow never counts a deleted raw object | compactWindow | killed: `TestScenarioCompactsClosedWindow`, `TestIssue84_…` |

The worktree was clean after every run.

## Behavior-equivalence read

- **Order of operations**: raw is still deleted only after `writeCompacted` succeeds (compactWindow), and pruning
  still runs on the pre-write listing after every window. This keeps the issue's ordering guarantee.
- **Partial Stats on an error**: the old loop returned `st` with `RawDeleted` counted up to the failing delete.
  compactWindow returns the `rawDeleted` count with the error, and CompactOnce adds it before it returns, so the
  value is the same. A prune error returns the partial `pruned` count, which is assigned to `CompactedPruned`. This
  is the same as before. A readCompacted, readRaw or write error returns `st` without the current window. This is
  also the same as before.
- **Skip paths**: an undecodable window and an empty window both return `rows == 0` with no error, so they are not
  counted in `Windows`. This is the same as the old `continue`.
- **The fault op on the delete and prune errors** is now the helper's name (`compact.Compactor.compactWindow` /
  `.pruneCompacted`) instead of `CompactOnce`. The fault kind is unchanged, the commit message states the change,
  and it follows the existing per-helper `op` convention (`readCompacted`, `readRaw`, `writeCompacted`). No test or
  caller matches on the op string. This is not a finding.

## Findings

None.

## Verified correct

- The "Done when" items hold: CompactOnce is under funlen 50 and gocyclo 20 (gocognit too), and the compact
  tests, including `TestIssue83_LateSegmentKeepsCompactedRows` and `TestIssue84_…`, pass unchanged under `-race`.
- The helpers match the issue's requested signatures in name and shape (`groupClosedWindows`, `compactWindow`
  returning rows, rawDeleted and err, and `pruneCompacted` returning count and err).
- Scope: one file, and every hunk is the extraction. No test was touched and no unrelated change was made.
- Reuse: the change only moves code. No new type, helper duplicate or dependency was added.
- Conventions (ADR-0002): `api/fault` wrapping with a per-function `op`, ctx-first, slog only, no `any`. The
  removed inline comments became the helpers' doc comments, with no narration added.
- ADRs: no ADR file was edited, and the ADR-0081 compaction behavior (closed windows only, delete after Put,
  retention) is unchanged.
- Shape: `refactor(funclog):` is the right type for a task, with `Fixes #553` and the attribution trailer. One
  issue is in one commit.
- No new defect was found next to the changed code.

## Recommendation

Pass. Hand the change to the group gate.
