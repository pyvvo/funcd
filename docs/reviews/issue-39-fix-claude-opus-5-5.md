## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #39 fix, model: claude-opus-5-5)

Change reviewed: commit 0f6780c `fix(catalog): stream request bodies through the catalog PEP proxy`
(`internal/catalog/gateway/proxy.go`, `internal/catalog/gateway/proxy_test.go`), on the group branch
`fix/199-unbounded-memory`. Only this commit was reviewed; the group's other commits are out of scope.

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **One surviving mutant on the short-body length line** · attribution: `model` · evidence: overlay mutant
  `m4` replaced `length = int64(len(head)) // the whole body` with a no-op, and
  `go test ./internal/catalog/gateway/` stayed green (`ok … 0.303s`). For a request with a known length the
  line is equivalent (the server already makes `ContentLength == len(body)`); it matters only for a short
  chunked request (`ContentLength == -1`), which the line forwards with a fixed `Content-Length` (as before
  the fix) rather than chunked. No test pins that framing. Fix: add a subtest that sends a short chunked
  handshake and asserts the engine sees `ContentLength == len(body)`.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 0f6780c`, then the
  test file restored from HEAD (so only `proxy.go` was pre-fix):
  `go test -race -run TestIssue39_ ./internal/catalog/gateway/` → `FAIL`.
  Subtest "a denied handshake is rejected before its body is read": `"16777259" is not less than "1048576"` —
  the proxy read the whole 16 MiB unauthenticated body. Subtest "a forwarded body streams to the engine while it
  is still being sent": `the engine never saw the body while it was being sent` — the whole body was buffered
  before forwarding. The third subtest (length and tail on an allowed handshake) passes pre-fix, as expected for
  a guard on the new length arithmetic.
- **Passes with the fix**, un-skipped, under `-race` (all three subtests `PASS`), and stable over
  `-race -count=30` together with `TestCatalogProxy_*` and `TestManager_*`.
- **The user-visible behavior is fixed.** A scratch overlay probe (not committed) reran the issue's step against
  the real `NewCatalogProxy` behind an httptest front: a 512 MiB chunked unauthenticated body, sampling
  `runtime.ReadMemStats` HeapAlloc. Pre-fix `proxy.go`: peak 1223 MiB (the issue reports about 1223 MiB on this
  path). With the fix: peak under 1 MiB, and the engine still received all 512 MiB as a stream.
- **Cause, not symptom.** The unbounded `io.ReadAll(r.Body)` and the second copy into a `bytes.Reader` are gone.
  The proxy reads at most `handshakeHeadMax` (4116 bytes), runs the PEP on that head, and forwards
  `MultiReader(head, rest-of-body)`. No limit was raised, no error was swallowed, and no test was skipped.
- **Mutants.** `m1` (drop the `length += len(rewritten) - len(head)` adjustment) failed `TestCatalogProxy_AllowDeny`
  and `TestManager_EnsureProxiesThroughToEngine` (the transport reports `ContentLength=138 with Body length 36`).
  `m2` (raise `handshakeHeadMax` to 64 MiB) failed both streaming subtests. `m3` (read the rest of the body into
  memory before forwarding) failed the streaming subtest. `m4` survived (see Minor).
- **Length and framing.** A known `Content-Length` is adjusted by the token-swap delta. An unknown length on a
  body longer than the head drops the header so the transport streams it chunked. The deny path leaves the body
  unread, and `net/http` bounds the post-handler drain.
- **ADR-0137 holds** (status Implemented, file not edited). A handshake whose token does not fit in the head
  returns `ok=false` and is forwarded un-swapped. That is the ADR's own fail-closed rule for a non-handshake body,
  and the engine rejects it. funcd's tokens (a JWT over `{ns, fn}`, or the minted Identity token) are far
  shorter than 4 KiB, so an allowed caller is never misclassified. No other Accepted ADR is touched.
- **Scope.** Both hunks serve the issue. No existing test was changed, weakened or deleted.
- **Reuse.** The fix uses `io.LimitReader`, `io.MultiReader`, the existing `swapHandshakeToken`, and the
  existing `preambleLen`/`tokenHdrLen` constants. The test reuses `makeHandshake`, `engineStub`,
  `seedCatalogWorld`, `buildPDP` and `DeriveCatalogToken`. The two new test readers (`countingReader`,
  `gatedBody`) have no existing counterpart in the package, its neighbours or `internal/testkit`.
- **Conventions.** Imports are at the top level, `slog` is unchanged, and no `any` was added. The one doc comment
  on `handshakeHeadMax` explains why the bound is safe, which is the comment the style rules ask for.
- **Checks** (through `nix develop -c`): `gofmt -l internal/catalog/` is empty; `go build ./...` exit 0;
  `go vet ./internal/catalog/... ./internal/dataplane/...` exit 0 (host and `GOOS=linux`); golangci-lint
  `0 issues.` (host and `GOOS=linux`); `go test -race ./internal/catalog/... ./internal/dataplane/...
  ./internal/edge/... ./internal/function/...` all `ok`. The `pkg/funcd` e2e suite does not exercise the proxy
  (its only catalog scenario covers admission), so it was not run here. Lima lanes were not run, as this stage
  requires; the group's full check set runs later.
- **Shape.** The subject is `fix(catalog): …`, the body has `Fixes #39` and the attribution trailer, and the
  commit covers one issue.

### Definition of Done

11 / 11 items hold (fix checklist). Item 4 holds: the revert and three of four mutants fail a test, and the
survivor covers only the framing of a short chunked body (the Minor). Item 11 covers the commit; the PR does not
exist yet.

### Model scorecard

Not recorded here. The ledger fields go back to the batch stage: issue 39, phase fix, model claude-opus-5-5,
verdict pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation

Sign off. The Minor (a subtest for a short chunked handshake) can go in with the next touch of this file. The
issue's remark that `internal/dataplane` `serveUpstream` applies no body cap without ADR-0112 limits is not a
memory defect once the proxy streams, and it is outside this fix.
