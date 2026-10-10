# Fix review — issue #877 (claude-opus-5-5)

- **Issue**: #877 — a typed fault from a huma input resolver (ADR-0210's `If-Match` resolver) answers
  `type: about:blank` with detail "validation failed: …" instead of its own problem (`urn:funcd:problem:invalid`).
- **Branch**: `fix/877-resolver-fault-problem-type`, one commit `108888b8`
  (`fix(controlplane): keep a typed fault's problem type when an input resolver refuses a request`).
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass** — 0 Blocker, 0 Major, 0 Minor. Checklist 12/12.

## Change

`internal/controlplane/controlplane.go` `newFaultError` (the `huma.NewError` override, ADR-0005 §4): when huma
passes exactly one error and it is (or wraps) a `*faultError`, return it unchanged. Several errors still join under
`about:blank`; the 413 PayloadTooLarge branch (ADR-0148) is untouched and still runs first. New test
`internal/controlplane/resolver_problem_internal_test.go` registers a resolver that returns N `wrapFaultError(fault.Invalid)`
errors and asserts the problem for N=1 (typed) and N=2 (joined, about:blank).

## Verification (run, not read)

| Check | Result |
|---|---|
| Regression test without the fix (overlay of `origin/main`'s `controlplane.go`) | **FAIL** for the issue's reason: `faults=1: want … Type:urn:funcd:problem:invalid … Detail:controlplane.issue877: bad tag 0, got 400 {Type:about:blank Title:Bad Request … Detail:validation failed: controlplane.issue877: bad tag 0}`; the N=2 case passes on both, as intended |
| Regression test with the fix, `-race`, `-v` | PASS, un-skipped |
| User-visible behavior on the real resolver | Cherry-picked the fix onto `feat/adr-0210-api-optimistic-concurrency` in a scratch worktree and sent `PUT …/functions/f` with `If-Match: W/"3"`: with the fix `400 type=urn:funcd:problem:invalid detail=controlplane.precondition: If-Match W/"3" is a weak entity-tag; send a strong one`; without it `400 type=about:blank detail=validation failed: …`. That branch's `TestScenarioBadPreconditionRejected` still passes with the fix. Scratch worktree removed |
| Mutant 1: `len(errs) == 1` → `>= 1` | killed (N=2 case fails) |
| Mutant 2: lone-fault branch disabled | killed |
| Mutant 3: return the fault rebuilt with `about:blank` | killed |
| `go build ./...` darwin and linux | OK |
| `go test -race ./internal/controlplane/...` | ok (controlplane, admission) |
| `go vet ./internal/controlplane/...` darwin and linux | OK |
| `golangci-lint run ./internal/controlplane/...` darwin and linux | 0 issues |

No e2e suite, repo-wide `go test` or Lima lane was run, as instructed; the repo-wide gate runs once per PR.

## Cause, not symptom

The issue's named cause is `newFaultError` flattening every error to its message under a fresh `about:blank`
problem. The fix removes exactly that loss for the case where one typed fault decides the response. Nothing is
retried, swallowed or skipped.

Edge cases checked against huma v2.38.0 (`huma.go` and `error.go`):
- Handler errors never reach this branch with a faultError: huma's handler path uses `errors.As(err, &StatusError)`
  and writes the faultError directly (`huma.go:1102-1106`), so the `errors.As` in the new branch does not change
  any handler response.
- The lone-fault branch ignores the `status` argument. That is correct: for resolver errors huma picks the status
  with a direct type assertion and defaults to 422, but `WriteErr` re-reads `err.GetStatus()` (`error.go:263`),
  so the response status is the fault's own (400). A resolver that wraps its faultError gets 400 rather than 422,
  which matches the fault.
- With several errors huma takes the status from the last `StatusError` (`huma.go:1079-1080`), so the joined
  problem is 400, as the N=2 case asserts.

## Scope

Two files: the 4-line branch plus the doc-comment update in `newFaultError`, and the new test file. Every hunk
serves the issue. No test was weakened or deleted; no doc or ADR file was touched.

## Reuse

The test reuses `NewAPI`, `NewStubHandlers`, `wrapFaultError`, `humatest.Wrap` and `fault.Problem`. The fix reuses
the existing `faultError` type and the `errors.As` idiom that the same function already uses for `huma.ErrorDetailer`.
No new helper, type or dependency.

## Conventions

The change follows ADR-0002 (`api/fault` errors, no `any` in signatures, no new imports in production code), uses
an internal `_internal_test.go` file like the existing `nslocks_internal_test.go`, and uses top-level imports. The
comments explain why (no single Kind decides several faults) without narrating the code.

## ADR conformance

- ADR-0005 §4 (Implemented): huma's errors render the `fault.Problem` with the Kind-derived type. The fix brings a
  resolver's fault in line with that rule.
- ADR-0148 (Implemented): the 413 branch is unchanged and still runs before the new branch. ADR-0148's mentions of
  `newFaultError`'s about:blank cover the size cap only, and they stay true.
- ADR-0210 (Accepted): its resolver comment says the resolver returns fault.Invalid "400, not huma's 422". The fix
  makes the problem type match, too. No ADR file was edited.

## Siblings and coverage

`huma.NewError` is overridden in one place only (`controlplane.go:322`). No other huma API or input resolver exists
on main. The issue describes one case (a lone resolver fault), which is fixed and tested. The several-faults case
is also pinned down.

## Shape

The subject is `fix(controlplane): …`, the body has `Fixes #877` and the Co-Authored-By trailer, and the branch holds
one commit for one issue. The repo identity is used throughout.

## Findings

None.

## ✅ Verified correct (keep)

- The fix is the smallest one possible, and it sits at the root cause, inside the single `huma.NewError` override.
- The test pins down both sides of the rule: one fault stays typed, several faults join under about:blank. Mutant 1
  shows that the second case protects the `len == 1` guard.
- The commit message explains why several faults keep about:blank, so a reader does not mistake it for an oversight.

## Checklist (12/12)

1 regression test ✅ · 2 fails pre-fix for the reason ✅ · 3 passes with `-race` ✅ · 4 mutants killed ✅ ·
5 root cause ✅ · 6 scope ✅ · 7 ADRs ✅ · 8 build/vet/lint/tests host and Linux ✅ (the repo-wide gate is pending per PR) ·
9 conventions ✅ · 10 reuse ✅ · 11 shape ✅ · 12 cases and siblings ✅

## Recommendation

Pass. Hand back to `/fix` Step 8 (PR).
