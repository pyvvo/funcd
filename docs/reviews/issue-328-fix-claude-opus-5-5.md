# Fix review — issue #328 (Create stores client-sent deletionTimestamp and ownerReferences unchanged)

- **Change**: branch `fix/i328`, commit 36c97d0 `fix(controlplane): ignore client-sent ownerReferences and deletionTimestamp`
- **Producing model**: claude-opus-5-5
- **Reviewer**: fix-review gate (independent)
- **Governing ADR**: ADR-0048 (ObjectMeta field table: `deletionTimestamp` and `ownerReferences` are "server-set; ignored on input")
- **Verdict**: **pass** — no Blocker, no Major, 1 Minor (test gap)

## Summary

`createObj` now clears both fields and `replaceObj` copies the stored values over the client's, through a new
`withServerMeta` helper beside `withStatus` (`internal/controlplane/handlers.go`). Every typed Create/Replace
handler routes through `createObj`/`replaceObj`, so the fix covers all kinds. Reconcilers still set owner
references through the store, which the change leaves alone, as the issue requires.

## Verification run

| Check | Result |
|---|---|
| Revert of 36c97d0 (production code only, tests kept), `TestIssue328_…` | **FAIL** for the issue's reason: `Should be empty, but was [{{Site  forged} u-forged …}]` — "a create takes no owner reference from the client" |
| Fix in place, `go test -race -count=1 ./internal/controlplane/` | `ok` (7.6 s) |
| Mutant 1: `replaceObj` passes `nil` instead of `cur` | killed — "a replace keeps the stored owner references, not the client's" |
| Mutant 2: create clears only `OwnerReferences`, not `DeletionTime` | killed — "a create takes no deletionTimestamp from the client" |
| Mutant 3: replace copies only `fm.OwnerReferences`, not `fm.DeletionTime` | **survived** (see Minor 1) |
| `go vet ./internal/controlplane/` | clean |
| `golangci-lint run ./internal/controlplane/` | 0 issues |
| Worktree after checks | at 36c97d0, clean |

Repo-wide tests, Linux lint and e2e are left to the group gate, as the task scopes them.

## Blockers

None.

## Majors

None.

## Minors

1. **The test does not cover "replace keeps a stored deletionTimestamp".** (attribution: `model`)
   Evidence: mutant 3 drops `fm.DeletionTime` from the copy in `withServerMeta`, so a PUT erases a
   server-set `deletionTimestamp`, and `TestIssue328_ApplyIgnoresClientOwnerAndDeletionMeta` still passes. The
   test's stored object never has a `DeletionTime`, so `require.Nil(t, after.DeletionTime, …)` holds either
   way. The impact is low today, because nothing reads or writes `deletionTimestamp` yet (as the issue notes).
   A one-line seed of `created.DeletionTime` next to the owned references in the store update would close the gap.

## Verified correct

- **Cause fixed, not masked**: the cause named in the issue (`createObj`/`replaceObj` pass both fields through)
  is removed at the API boundary. The store stays permissive, which the issue requires, because reconcilers in
  `internal/site`, `internal/function`, `internal/workflow` and `internal/services/identity` write owner references
  through it.
- **Coverage of all kinds**: every `Create<Kind>`/`Replace<Kind>` handler in `handlers.go` delegates to
  `createObj`/`replaceObj`. There is no other API write path for ObjectMeta.
- **Scope**: three files, all for the issue. The `TestIssue166_WireShapeMatchesSpec` edit is the change the issue
  predicted ("a fix must change that assertion"). It still asserts that `ownerReferences` round-trips on the wire,
  now seeded through the store. The test is not weakened.
- **ADR conformance**: matches ADR-0048's "server-set; ignored on input". No ADR file is touched.
- **Reuse**: the test reuses the existing `newServerOn`, `apply`, `storedEcho` and `fnManifest` helpers from
  `status_test.go`. `withServerMeta` mirrors the `withStatus(obj, from)` shape (nil `from` means a create) and
  duplicates no existing helper. The store has no equivalent, because it handles only uid, generation,
  resourceVersion and creationTimestamp.
- **Conventions**: top-level imports, ctx-first, no `any`, no new errors needed, doc comment states the why
  (ADR-0048 plus the reason the store cannot do it). No comment bloat.
- **Shape**: `fix(controlplane):` subject, `Fixes #328`, attribution trailer, one issue in one commit.

## Checklist (Definition of Done)

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue328_…` reproduces the behavior | yes |
| 2 | Fails on pre-fix code, for the reported reason | yes |
| 3 | Passes with the fix under `-race` | yes |
| 4 | Reverting or mutating the key lines fails a test | partly — revert and 2/3 mutants fail; mutant 3 survives |
| 5 | Root cause fixed | yes |
| 6 | Only the issue's scope; no test weakened | yes |
| 7 | No ADR contradicted or edited | yes |
| 8 | Build, vet, lint, tests green (touched package; repo-wide and Linux at the group gate) | yes |
| 9 | Conventions hold | yes |
| 10 | Reuse, no duplication | yes |
| 11 | Commit shape | yes |

**10 of 11.**

## Recommendation

Pass. Optionally, before the PR, seed a stored `DeletionTime` in the regression test so that mutant 3 is
killed (Minor 1). This is not required for merge.
