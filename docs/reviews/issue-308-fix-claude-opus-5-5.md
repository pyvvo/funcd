# Fix review — issue #308 (claude-opus-5-5)

- **Issue**: #308 — A `when` or `pass` guard on an absent optional field fails the run at runtime
- **Change**: branch `fix/i308`, commit `318074b fix(workflow): let a guard on an absent optional field evaluate to false`
- **Governing ADRs**: ADR-0095 (guard rule, scenario `guard-allows-optional`), ADR-0094 (control flow), ADR-0096 (pass/wait), ADR-0002 (conventions)
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass** — 0 Blocker, 0 Major, 2 Minor (both `model`)
- **Definition of Done**: 10 of 11

## Verification run

| Check | Result |
|---|---|
| Revert of `318074b` (non-test files only), `TestIssue308_*` | **FAIL**: `internal/workflow` fails with `when condition for step "b": expr.check: unknown field "x" under "step.a.output"` (the issue's exact message); `internal/expr` `TestIssue308_AbsentFieldOnlyProbed` fails as well |
| Back at `318074b`, `TestIssue308_*` with `-race` | PASS (`internal/expr`, `internal/workflow`) |
| Mutant M1: drop the `lt.absent` short-circuit in `checkBinary` | killed (both `TestIssue308_*`) |
| Mutant M2: let a non-probe read of an absent field through in `resolveRefExpr` | **survived** (see Minor 1) |
| Mutant M3: do not propagate `absent` from the existence probe | killed (both `TestIssue308_*`) |
| `go test -race` on `internal/expr`, `internal/workflow`, `internal/sensor` (the third holds the other `expr.Resolver`) | ok |
| `go build ./...`, `go vet` (touched packages), `golangci-lint` (touched packages), `gofmt -l` | clean, 0 issues |
| Worktree after review | at `318074b`, clean |

E2E, the Linux lint and the repo-wide tests are left to the group gate.

## Blockers

None.

## Majors

None.

## Minors

1. **Test gap: a non-probe read of an absent field is pinned only through `>`** (`model`).
   Evidence: mutant M2 changes `if !existenceProbe || idx < len(segs)` to `if idx < len(segs)` in
   `internal/expr/check.go` (`resolveRefExpr`), and the suite still passes. The negative cases in
   `TestIssue308_AbsentFieldOnlyProbed` (`input.x > 1`, `input.x === undefined && input.x > 1`) still fail
   under the mutant, but for a different reason: `>` rejects the unknown kind. A scratch probe under the
   mutant shows that `${{ {v: input.x} }}` and `${{ input.x }}` in `Select` mode then pass Check, while the
   real code rejects them with `unknown field "x"`. Adding one `Select` case such as `${{ {v: input.x} }}`
   to the `mustFailCheck` list would pin the rule.
2. **The absent signal is an in-band zero value** (`model`).
   `Field.absent()` is `Type == "" && !Required && !HasDefault`, so `docResolver` returns `expr.Field{}`
   with a nil error to mean "missing". All three current resolvers (`schemaResolver`, `docResolver`,
   `sensor.eventResolver`) set `Required: true` on every present field, so today nothing collides. A
   future resolver that returns a zero `Field` by mistake, however, would be treated silently as absent.
   An explicit marker (an `Absent bool` on `Field`, or a dedicated sentinel) would make the contract
   self-evident. The public `Field` contract of ADR-0095 is extended only through a doc comment, and
   the change contradicts no ADR.

## ✅ Verified correct

- **Cause, not symptom.** The fix removes the cause named in the issue: `docResolver` turned a missing
  last segment into `NotFound`, and `Check` turned that into `unknown field` on the probe itself. Now a
  missing last segment is reported as absent. Only an existence probe at the end of the path may read it,
  and the right operand of `X !== undefined && …` goes unchecked when `X` is absent. This matches
  ADR-0095 ("`&&` short-circuits before the guarded reference is read"). No error is swallowed and no
  timeout or retry was added.
- **Strictness is kept.** An unguarded read still fails (the second run in the workflow test). So do
  `=== undefined &&` (which `guardPath` does not accept as a guard) and a misspelled path (`input.y`).
  A missing parent segment is still `NotFound`. The schema resolver's reconcile-time `NotFound` is
  unchanged.
- **The user-visible behavior is covered on all three runtime paths** named in the issue: `when`
  (`b` Skipped, 0 calls), `pass` (output `{"big":false}`) and dynamic `wait` (Succeeded). The run ends
  `Succeeded`.
- **Scope.** All five hunks serve the issue. No test was weakened or deleted.
- **Reuse.** The `unknown` closure de-duplicates the existing error. `addRoot` and `guardPath` are reused.
  No new helper, type, harness or dependency duplicates existing code.
- **Conventions.** `api/fault` errors are used, there are no new imports, and comments are short and say
  why. The test names follow `TestIssue<N>_…`. No ADR file was touched.
- **Commit shape.** The subject is `fix(workflow):`. The body names the cause and the fix and carries
  `Fixes #308` and the attribution trailer. The change is one commit for one issue.

## Recommendation

Pass. Before or during the group PR, the fixer may add the `Select`-mode negative case from Minor 1. A
follow-up may replace the zero-value sentinel with an explicit marker (Minor 2). Neither one blocks the
merge.

## Ledger fields

- verdict: pass; blockers 0; majors 0; minors 2; model-attributed 2; dod 10/11
- notes: "Minor1 model: mutant letting a non-probe read of an absent field survives (negatives only via >); Minor2 model: absent signalled by zero-value Field sentinel"
