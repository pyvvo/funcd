# Fix review — issue #571 (Close the remaining paths to the shared HTTP transport)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #571 fix, model: claude-opus-5-5)

Change: branch `fix/w7-i571`, commit 357dd45 `fix(artifact): give oras-go a transport of its own`.
Judged against the decision already taken for this issue: oras-go gets an `auth.Client` over
`internal/platform/httpx`, the bench harness uses `httpx.Client`, and forbidigo also rejects `http.Get`,
`http.Head`, `http.Post` and `http.PostForm` with the same exclusions (tests and `pkg/sdk`).

## Evidence

| Check | Command / action | Result |
|---|---|---|
| revert check (oras-go) | overlay of `origin/main` `internal/artifact/artifact.go` + `platform.go`, `go test -run TestIssue571 ./internal/artifact/` | FAIL in both subtests: `expected: 1, actual: 2` connections, "closing the default transport's idle connections must not touch the target's" — the issue's reason |
| revert check (lint rule) | `origin/main` `.golangci.yml` restored temporarily, `go test -run 'TestIssue571\|TestIssue564' ./tests/lint-fixtures/` | `TestIssue571_LintBlocksImplicitSharedHTTPClient` FAILS, #564 test still passes; config restored, tree clean |
| with the fix | `go test -race -count=3 -run TestIssue571 ./internal/artifact/` | PASS, both subtests, not skipped |
| mutant M1 | `retry.NewTransport(httpx.Transport())` → `retry.NewTransport(nil)` | killed (both subtests fail on the connection count) |
| mutant M2 | drop `reg.Client = registryClient(nil)` in `Login` | **survives** (`./internal/artifact/`, `./cmd/funcdctl/` green) — see Minor 1 |
| mutant M3 | the lint-config revert above | killed |
| touched packages | `go test -race ./internal/artifact/ ./internal/testkit/bench/ ./tests/lint-fixtures/` | ok, ok, ok |
| vet | `go vet` on the same packages | clean |
| lint | `golangci-lint run` on the same packages | 0 issues |
| Done when | grep of non-test code (fixtures and `pkg/sdk` aside) for `auth.DefaultClient`, `retry.DefaultClient`, `remote.NewRepository`/`NewRegistry` with a nil Client, `http.Get/Head/Post/PostForm`, and `http.DefaultTransport/DefaultClient` | none left: both `remote.New*` sites now set `Client = registryClient(...)`; the only `DefaultTransport` read is `httpx.Transport()`'s settings copy; `pkg/funcd`'s `calls.Wrap(nil)` falls back to `httpx.Transport()` |

Not run here (the group gate runs them): repo-wide tests, e2e, Linux lint, lanes.

### 🟡 Minor 1 — the `Login` path has no regression test  ·  attribution: model

`Login` (`internal/artifact/artifact.go`) now sets `reg.Client = registryClient(nil)`, which is correct:
oras-go's `credentials.Login` copies a non-nil `*auth.Client` and only falls back to `auth.DefaultClient`
when the Client is nil (verified in oras-go v2.6.1 `registry/remote/credentials/registry.go`). But
deleting that line leaves every test green (M2). The `TestIssue571` test covers only `resolveTarget`.
A subtest that runs `Login` against the same `httptest` registry (with `DOCKER_CONFIG` in a temp dir) and
then closes the default transport's idle connections would cover it. Not blocking: the line is one
assignment of a helper the covered path exercises, and the "Done when" is met.

### ✅ Verified correct (keep it)

- `registryClient` mirrors oras-go's own `retry.NewClient` (`&http.Client{Transport: retry.NewTransport(nil)}`)
  with the inner transport replaced by `httpx.Transport()`: retry behavior is kept, only the pool changes.
  This reuses `httpx` and oras-go's `retry` package rather than reinventing either.
- `resolveTarget` now sets the client in both branches; the unreadable-credential-store subtest proves the
  branch where `repo.Client` used to stay nil.
- `repositoryOf` and `resolveTargetRef` switch to `registry.ParseReference`: they only parse, so no
  Repository with a nil Client is built. Same parse function `remote.NewRepository` calls internally, so the
  result is unchanged (existing `internal/artifact` tests pass).
- Bench: `httpx.Client(30 * time.Second)` keeps the 30 s timeout — behavior unchanged except the pool.
- forbidigo: the new pattern `^http\.(Get|Head|Post|PostForm)$` and the widened exclusion `text` keep tests
  and `pkg/sdk` exempt for both rules; the fixture exercises all four helpers and the test asserts each finding.
- Scope: every hunk serves the issue; no test weakened or deleted; no ADR file touched; no ADR contradicted
  (ADR-0002: `fault` errors kept, top-level imports, no comment bloat).
- Commit shape: `fix(artifact):`, `Fixes #571`, the attribution trailer, one issue.

## Checklist

10 of 11 hold. Item 4 (mutating the key lines fails a test) holds only partly: the `Login` line survives (Minor 1).
Item 8 holds for the host checks of the touched packages; the Linux lint and e2e belong to the group gate.

## Recommendation

Pass. Optionally add a `Login` subtest in a follow-up or in this PR before it is opened.
