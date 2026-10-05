# Fix review — issue #698 (unescaped SDK URL names), claude-opus-5-5

- **Change**: branch `fix/w15c-i698`, commit `1ac58dfc` `fix(sdk): refuse a namespace or name that is not a DNS label before any request`
- **Files**: `pkg/sdk/sdk.go`, `pkg/sdk/deadletter.go`, `pkg/sdk/logs.go`, tests in `pkg/sdk/sdk_test.go`, `pkg/sdk/kinds_test.go`, `cmd/funcdctl/cli_test.go`
- **Verdict**: **pass** — 0 Blocker, 0 Major, 0 Minor. DoD 12/12 (Linux lint, repo-wide tests and e2e are deferred to the group gate).

## Decision being judged

PROVE FIRST: each case of the issue gets a `TestIssue698` proof that fails on current `origin/main`, and only then a fix. The review ran each proof against an overlay of the current `origin/main` versions of the three changed production files, so the test files from the branch stayed in place.

## Verification run

| Check | Result |
|---|---|
| Revert overlay (`origin/main` versions of `sdk.go`, `deadletter.go`, `logs.go`), `-run 'TestIssue698\|TestSDKKindPaths'` in `pkg/sdk` | All 3 SDK regression tests FAIL. All 14 `SendsNoRequest` subtests fail with "a request was sent" (each issue row: `a#b`, `a?force=true`, `rg1?force=true` without `Force()`, `default/functions/other`). `LeavesOtherObjects/Get` returns object `b` for `b?anything`. `LeavesOtherObjects/Delete` reports "Delete(a#zzz) deleted a; Delete returned <nil>". `DeadLetterIDStaysOnePathSegment` shows `deadletters/a?force=true/replay` and `deadletters/a/b`. These are the reasons the issue gives. `TestSDKKindPaths_MatchServerRoutes` still passes. |
| Revert overlay, `cmd/funcdctl` `TestIssue698_DeleteRefusesNameThatIsNotALabel` | FAIL: `delete fn1#zzz deleted fn1: err=<nil> out="deleted Function/fn1#zzz\n"`, which is the CLI output from the issue |
| With the fix, `-race -count=1` | All 4 `TestIssue698_*` tests PASS without a skip, as do `TestSDKKindPaths_MatchServerRoutes` and `TestForceIsRefusedForAnyKindButResourceGroup` |
| Mutant M1: drop `name.Validate()` in `itemURL` | Killed (`SendsNoRequest`, `LeavesOtherObjects`, CLI test) |
| Mutant M2: drop `ns.Validate()` in `namespaceURL` | Killed (`SendsNoRequest`) |
| Mutant M3: drop `url.PathEscape(id)` in `deadLetterURL` | Killed (`DeadLetterIDStaysOnePathSegment`) |
| `go test -race` for `./pkg/sdk/` and `./cmd/funcdctl/` | ok, ok |
| `go vet` and `golangci-lint` on the touched packages | clean, 0 issues |
| Worktree after the review | clean (only overlays were used; nothing was reverted or stashed) |

The real-daemon steps were not rerun. The CLI test and `LeavesOtherObjects` already run against the real store-backed control plane with authn and RBAC, which is the same setup the issue used.

## Findings

None.

## ✅ Verified correct

- **Cause, not symptom.** The SDK now checks every path segment that comes from the caller in one place. `namespaceURL` checks the namespace, `itemURL` checks the name, and both use the typed IDs' existing `Validate()` (`api/types/v1alpha1/ids.go`), which returns `fault.Invalid` before any request is sent. Before the fix, the issue's request-shape table could reach another object. After it, no request is sent at all.
- **All siblings are covered.** A grep of `pkg/sdk` finds that every request builder now goes through `collectionURL`, `itemURL` or `namespaceURL`. `Logs` and `RunLogs` were moved onto `itemURL`. The 4 dead-letter calls check the namespace. The dead-letter id is opaque and not a typed DNS-label ID, so it is path-escaped to keep it one segment. `HandoverKVStore` already used `itemURL` and is covered by a test. No production code outside `pkg/sdk` builds these URLs, so `funcdctl` is fixed through the SDK.
- **The `Force()` guard is closed.** A name such as `rg1?force=true` can no longer carry the query. `TestForceIsRefusedForAnyKindButResourceGroup` still passes.
- **No regression for valid names.** The server already checks `metadata.name` with the same `ObjectName.Validate` (`api/types/v1alpha1/metadata.go`), so a name the SDK now refuses could never have been stored.
- **Reuse.** The fix uses the existing typed-ID validators and `url.PathEscape` from the standard library, and adds no new validation logic. `namespaceURL` replaces the namespace prefix that was copied into 7 call sites before.
- **Scope.** Every hunk serves the issue. The `kinds_test.go` change was needed because the builders now refuse the `{namespace}`/`{name}` templates. The test builds paths with DNS labels and maps them back to the templates, and it adds an `item == col+"/name"` assertion, so the route check got stronger, not weaker.
- **Conventions.** Errors are `api/fault`, functions take ctx first, imports are at the top level, and comments are short and explain why (with an issue reference). ADR-0024 keeps URL building in the SDK through the `kindDescriptor` table, and no ADR file was edited.
- **Shape.** The commit has a `fix(sdk):` subject, `Fixes #698`, the attribution trailer, and covers one issue.

## Recommendation

Pass. Hand back to `/fix` Step 8 for the group integration.
