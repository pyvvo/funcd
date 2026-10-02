# Fix review — issue #165 (claude-opus-5-5)

- **Issue**: #165 "A create with an existing name plus generateName makes a new object, not 409"
- **Change**: branch `fix/i165`, commit `96e6b07` — `fix(controlplane): return 409 for a duplicate explicit name sent with generateName`
- **Files**: `internal/controlplane/handlers.go` (+4/-1), `internal/controlplane/server_test.go` (+38)
- **Governing ADR**: ADR-0133 (server-side name generation); ADR-0002 conventions
- **Verdict**: **pass** — 0 Blocker, 0 Major, 1 Minor (model)

## Verification run

| Check | Result |
|---|---|
| Regression test without the fix (`git revert --no-commit 96e6b07`, test file kept) | `TestIssue165_ExplicitNameWithGenerateNameConflicts` FAIL: `expected: 409 actual: 200` — the issue's exact symptom |
| With the fix, `-race` | PASS |
| Mutant 1 — clear `GenerateName` only when `Name == ""` (explicit-name path keeps it) | FAIL (killed) |
| Mutant 2 — clear `GenerateName` unconditionally (also on the generated-name path) | survived (see Minor 1) |
| `gofmt -l internal/controlplane` | clean |
| `go build ./...` | ok |
| `go vet ./internal/controlplane/` | ok |
| `golangci-lint run ./internal/controlplane/...` | 0 issues |
| `go test -race ./internal/controlplane/... ./internal/store/...` | all ok |
| Worktree after review | at `96e6b07`, clean |

Not run here (the group gate runs them): Linux lint, e2e, Lima lanes, repo-wide tests.

## 🔴 Blocker

None.

## 🟡 Major

None.

## Minor

1. **The regression test does not pin that `GenerateName` survives on the name-less path** (`model`).
   Mutant 2 (drop `GenerateName` for every create, not only for an explicit Name) passes all
   controlplane, sdk and funcdctl tests. That mutant silently disables `store.Create`'s
   regenerate-on-collision retry for server-generated names and stops persisting `generateName`.
   One extra assertion in the test's third step — `require.Equal(t, "dup-", generated.GenerateName)` —
   would kill it. Non-blocking: the fix's own lines are correct; this is a test-strength gap.

## Observation (not scored)

- `store.Create` still enters its retry loop whenever `GenerateName != ""`, although its doc comment says
  "an empty Name but a GenerateName prefix". After this fix the only path that reaches it with both set is
  the handler's own generated-name path, and no internal caller (`sensor`, `workflow`, `site`, `identity`,
  `function`) sets `GenerateName`, so there is no live defect. The store cannot tell an explicit Name from
  a handler-filled one (the handler must fill Name before admission), so the handler is the right fix
  point; the issue's "root cause in store.go" is the mechanism, the missing distinction is in `createObj`.

## ✅ Verified correct

- **Root cause, not symptom**: the explicit-vs-generated distinction is known only in `createObj`; dropping
  `GenerateName` there when the client supplied a Name makes the store take its single-attempt path, so a
  duplicate is `fault.Conflict` → 409. No retry, timeout or swallowed error.
- **Matches the contract**: `ObjectMeta.GenerateName` doc ("Ignored once Name is set") and ADR-0133
  ("an explicit `Name` behaves exactly as before"). It also stops the stray `generateName` from being
  persisted on an explicitly named object, the second symptom in the issue.
- **Generated-name path unchanged**: the name-less create still yields `dup-<8hex>` (asserted), and the
  list holds exactly two objects (no renamed duplicate).
- **Scope**: two files, every hunk serves the issue; no test weakened or deleted; no ADR edited.
- **Reuse**: the test reuses `newServer`, `do`, `functionBody`, `fnBase`, `devToken`; it builds the
  generateName body by decoding `functionBody` rather than duplicating it. No new helper, type or dependency.
- **Conventions**: `interface{}` in the test matches the file's existing idiom (40 uses, 0 `any`); the
  comment is a short why-comment; top-level imports; no YAML touched.
- **Commit shape**: `fix(controlplane):` subject, body explains cause and fix, `Fixes #165`, attribution trailer, one issue.

## Fix checklist

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue165_…` reproduces the behavior | yes |
| 2 | Fails on pre-fix code for the reported reason | yes (409 expected, 200 actual) |
| 3 | Passes with the fix, un-skipped, `-race` | yes |
| 4 | Reverting / mutating key lines fails a test | yes (revert and mutant 1 killed; mutant 2 is Minor 1) |
| 5 | Root cause fixed, not masked | yes |
| 6 | Only the issue's scope; no test weakened | yes |
| 7 | No ADR contradicted or edited | yes |
| 8 | Build, vet, lint, tests green (host; group gate runs the rest) | yes |
| 9 | Conventions hold | yes |
| 10 | Reuses what exists | yes |
| 11 | Commit shape | yes |

**11 / 11.**

## Recommendation

Pass. Optionally add the one-line `GenerateName` assertion from Minor 1 before the PR.
