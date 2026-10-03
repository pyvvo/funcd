# Fix review — issue #449 (claude-opus-5-5)

- **Issue**: #449 — the Secret/ConfigMap env gate names a random bad key when several are bad.
- **Change**: branch `fix/i449`, commit `061a91b` `fix(secrets): name the first bad Secret/ConfigMap key in sorted order`.
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Counts**: 0 Blocker · 0 Major · 0 Minor (0 model-attributed)
- **Fix checklist**: 11 / 11

## Summary

Both value checks named in the issue (the ConfigMap loop in `internal/envresolve/envresolve.go` and the
Secret loop in `internal/secrets/secrets.go`) iterated `spec.data` in Go's random map order and returned on
the first bad value. They now call the new `secrets.EnvDataProblem`, which walks the keys through
`slices.Sorted(maps.Keys(data))` and returns the first key whose value `EnvValueProblem` rejects. The error
text, and so the Function's `RevisionReady`/`Ready` message, is now the same on every reconcile. The idiom
mirrors `validateEnvKeys` (`api/types/v1alpha1/configmap.go`), which the issue cites as the expected behavior.

## Verification run

| Check | Result |
|---|---|
| Regression test without the fix (`git revert --no-commit 061a91b`, the test file restored from HEAD) | **FAIL**, both subtests: config named `key "BAD_10"`, secret named `key "BAD_13"` instead of `BAD_00`, which is the issue's reason |
| With the fix, `-race` | `TestIssue449_FirstBadKeyInSortedOrder` PASS (config and secret subtests, 50 resolves each) |
| Mutant 1: `EnvDataProblem` ranges `maps.Keys(data)` unsorted | FAIL: both `TestIssue449_…` subtests |
| Mutant 2: the Secret check in `secrets.ResolveEnv` disabled | FAIL: `TestIssue449_…/secret` and `TestIssue356_NULValueRejected` |
| Mutant 3: `EnvDataProblem` returns the last bad key in sorted order | FAIL: both `TestIssue449_…` subtests (`BAD_15`) |
| `go test -race ./internal/envresolve/ ./internal/secrets/` | ok |
| `go vet` (both packages) | clean |
| `golangci-lint` (both packages) | 0 issues |
| Worktree after the revert check and mutants | reset to `061a91b`, clean |

Not run here by design: repo-wide tests, the e2e suite, Linux lint, Lima lanes (the group gate runs them).

## Findings

### Blocker
None.

### Major
None.

### Minor
None.

## ✅ Verified correct

- **Cause, not symptom**: both random-order loops are gone; no other caller of `EnvValueProblem` iterates a
  map (grepped every non-test use).
- **Scope**: four files; every hunk serves the issue. The Secret path still copies all values into `env`
  after the check, so the merge semantics are unchanged; the two doc comments that named `EnvValueProblem`
  now name `EnvDataProblem`.
- **Reuse**: `EnvDataProblem` is the shared helper for both call sites (no copy-pasted sort), uses the
  standard library (`slices.Sorted`, `maps.Keys`), and has the same generic `string | []byte` shape as
  `validateEnvKeys`. It cannot reuse `validateEnvKeys` itself: that one checks key names, lives in
  `api/types`, and returns a formatted `fault` error.
- **Conventions**: `fault.Invalidf` kept, errors still joined with `ErrConfig`/`ErrSecret` (ADR-0093 §4),
  top-level imports, no comment bloat, test follows the `TestIssue356_…` neighbour's setup.
- **ADRs**: no ADR file touched; ADR-0057/ADR-0093 delivery semantics unchanged (only which bad key is named).
- **Shape**: `fix(secrets):` subject, `Fixes #449`, attribution trailer, one issue in one commit.

## Recommendation

Pass. Hand back to `/fix` Step 8.
