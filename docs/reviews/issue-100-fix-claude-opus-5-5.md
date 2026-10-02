## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #100 fix, model: claude-opus-5-5)

Change: branch `fix/i100`, commit 336eb8c `fix(workernode): store kv and blob keys with empty or dot segments verbatim`
(`internal/workernode/local/local.go`, `internal/workernode/local/kv_test.go`; 62 insertions, 1 deletion).

The issue: the shims keep a key's `/` separators raw, so a key with an empty or dot segment (`/lead`, `a//b`,
`a/./b`, `a/../b`, `.`, `..`) reaches the worker-node local API as a non-canonical path, and `http.ServeMux`
answers it with a 307 to the cleaned path before the `{key...}` handlers run.

The fix: `NewHandler` now returns `opaqueKeys(mux)`. For a `/kv/<binding>/<key>` or `/blob/<binding>/<key>`
request, the wrapper rewrites the escaped key part into one canonical segment (`/` becomes `%2F`, `.` becomes
`%2E`) and sets `URL.Path`/`URL.RawPath` on a shallow request copy. ServeMux in Go 1.26 matches and cleans on
`URL.EscapedPath()` (`net/http/server.go`, `findHandler`), so the escaped path is now clean and is not
redirected. The `{key...}` wildcard unescapes it back to the exact key.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

None.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** With `git revert --no-commit 336eb8c`
  and the new test kept, `go test -run TestIssue100 ./internal/workernode/local/` fails in both subtests with
  `expected: 204, actual: 307` on the first PUT. That is the issue's 307.
- **It passes with the fix under `-race`.** After `git reset --hard 336eb8c`,
  `go test -race -run TestIssue100 -v` gives `--- PASS: TestIssue100_PathLikeKeysRoundTrip` (subtests `kv` and
  `blob`). The whole package passes under `-race` (`ok … internal/workernode/local`).
- **The test is thorough.** It covers all six failing keys from the issue and two control keys (`a/b`,
  `trail/`). It checks put, get with the body, list (`ElementsMatch` against the exact keys, which proves the
  keys are stored verbatim and not cleaned), delete, and a 404 after delete. It covers both KV and blob. The
  issue suspected blob but did not test it.
- **Mutants: 3 of 3 killed.**
  - M1: no `.` escape (`NewReplacer("/", "%2F")`) fails the test.
  - M2: no `/` escape (`NewReplacer(".", "%2E")`) fails the test.
  - M3: `RawPath` not set (`r2.URL.Path = path` only) fails the test.
- **The root cause is fixed, not masked.** The redirect can no longer happen for key paths. The fix adds no
  redirect-follow in a client, no retry, and no key rejection. Keys stay opaque strings, as the kvstore port
  contract (`internal/kvstore/kvstore.go`) and ADR-0069's `/kv/{binding}/{key}` surface require. The fix is on
  the server side, so both shims (TypeScript and Python) are fixed at once with no language-repo release.
- **The production path is covered.** `internal/workernode/local/manager.go:81` builds every sandbox's handler
  through `NewHandler`, so the daemon serves the wrapped mux.
- **Edge cases hold.** The list route (`/kv/b`, no key separator) and an empty key (`/kv/b/`) pass through
  unchanged. A key the shim already percent-encodes (`a%2Fb` arrives as `a%252Fb`) keeps its `%25` and
  round-trips. `/invoke/{alias}` is untouched. If `url.PathUnescape` fails, the request goes through unchanged
  and gets the old behavior, so it cannot crash.
- **Scope.** Both hunks serve the issue. No existing test was weakened or deleted.
- **Reuse.** No helper elsewhere does the same job: the only other `RawPath` handling
  (`internal/dataplane/dataplane.go:257`) clears it for a different purpose. The shallow request and URL copy
  follows the standard library's own `http.StripPrefix` pattern. The fix uses only `strings` and `net/url` and
  adds no dependency.
- **Conventions.** Imports are at the top level. There are no `any` signatures. The doc comment states the why
  (the ServeMux redirect, issue #100, opaque keys) and does not narrate the code. Naming fits the package.
- **ADRs.** No ADR file was touched. The fix conforms to ADR-0064 and ADR-0069 (the local-API verb surface is
  unchanged) and to ADR-0002.
- **Checks on the touched package.** `go vet` is clean, `golangci-lint run ./internal/workernode/local/`
  reports 0 issues, and `gofmt -l` is clean. As instructed, the group gate runs the e2e suite, the Linux lint
  and the lanes, including the issue's own chaos probe with the real shim.
- **Commit shape.** The subject is `fix(workernode): …`, the body has `Fixes #100`, the attribution trailer is
  present, and the commit covers one issue.

### Recommendation

Pass. Hand back to `/fix` Step 8. The worktree is left clean at 336eb8c.
