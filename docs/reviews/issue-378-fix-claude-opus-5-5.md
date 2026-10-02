## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #378 fix, model: claude-opus-5-5)

Change: branch `fix/i378`, commit 3f9be01 `fix(catalog): answer catalog PEP proxy errors as problem+json logged via slog`
(`internal/catalog/gateway/proxy.go`, `internal/catalog/gateway/proxy_test.go`; 136 insertions, 6 deletions).

The issue: the catalog PEP proxy (ADR-0137) built its `httputil.ReverseProxy` without an `ErrorHandler`, so a
failed engine call got the default bare 502, logged through the stdlib `log` package. Its own rejections
(403, 400, 500, 502) were plain-text `http.Error` answers.

The fix: `NewCatalogProxy` sets `rp.ErrorHandler = p.engineFailed`, which logs once through slog (`WarnContext`)
and writes `fault.Unavailablef` as problem+json (503). The engine endpoint stays in the log and out of the
answer. Each `http.Error` is replaced by `fault.WriteProblem` with the matching kind: Forbidden (both deny
paths), Invalid (unreadable body), Internal (PDP error, malformed upstream).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **The policy-deny branch's problem+json answer is not covered by a test** (attribution: `model`). The
  test case named "a denied caller" sends an unresolvable token, so it exercises the `PrincipalFor` miss
  (`proxy.go:95-98`), not the `!dec.Allowed` branch (`proxy.go:112-115`). An overlay mutant that puts back
  `http.Error(w, "forbidden", http.StatusForbidden)` on the `!dec.Allowed` branch only passes the whole
  package. The existing deny tests check the 403 status only. Fix: add a case with a resolvable token
  that has no grant (`DeriveCatalogToken` for an ungranted function), and name the current case "an unresolved
  credential".

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** With `git revert --no-commit 3f9be01`
  and the new test kept, `go test -race -run TestIssue378 ./internal/catalog/gateway/` fails in all six
  subtests. The two engine-failure cases fail with `expected: 503, actual: 502`. The deny, unreadable-body and
  PDP-failure cases fail with `expected: "application/problem+json", actual: "text/plain; charset=utf-8"`. The
  malformed-upstream case fails with `expected: 500, actual: 502`. The final assertion fails because the stdlib
  logger received `http: proxy error: dial tcp …: connect: connection refused`. These are the issue's exact
  symptoms.
- **It passes with the fix under `-race`.** After `git reset --hard 3f9be01`, the whole package passes under
  `-race` (`ok … internal/catalog/gateway`).
- **The issue's own steps are reproduced with real servers.** The test uses a closed `httptest` server (a
  stopped engine) and a server that hijacks and closes the connection (an engine that fails mid-request). It
  swaps both the slog default and the stdlib `log` output, and asserts that nothing reaches the stdlib logger
  and that exactly two WARN lines reach slog (one per engine failure). The swap is done in the right order
  (`log.SetOutput` after `slog.SetDefault`) and is restored in `t.Cleanup`. The test is correctly not parallel.
- **Mutants: 2 of 3 killed.**
  - M1: no `p.rp.ErrorHandler = p.engineFailed` fails the test.
  - M2: no `p.log.WarnContext(...)` in `engineFailed` fails the test (the WARN count).
  - M3: plain-text `http.Error` on the `!dec.Allowed` branch survives (the Minor above).
- **The root cause is fixed, not masked.** The missing `ErrorHandler` is the cause the issue names. The fix
  adds no retry, no swallowed error and no status rewrite after the fact. Every `http.Error` site in the file
  is replaced.
- **The status choices are sound.** An engine failure is Unavailable (503), as the issue expects and as the
  activator answers. A malformed `Upstream` is a configuration defect inside funcd, not an upstream answer, so
  Internal (500) is a better fit than the old 502. ADR-0137 fixes only the 403 for deny and unresolved
  credentials, and both deny paths still answer 403 without calling the upstream.
- **No information leaks.** The 503 detail is a fixed message. The engine's netns URL and the transport error
  stay in the log. The PDP error goes to the log (`p.log.Error`) and the answer carries a fixed Internal
  message.
- **Scope.** Both files serve the issue. No existing test was weakened or deleted. The only comment change on
  an existing line (the `rp` field) keeps the comment true.
- **Reuse.** The `ErrorHandler` follows the existing pattern in `internal/activator/activator.go:414`
  (`WarnContext` plus `fault.WriteProblem` with Unavailable), which is the #141 precedent the issue cites. The
  dataplane edge proxy (`internal/dataplane/dataplane.go:240`) has the same shape. These are three short
  closures over different loggers and op names, so a shared helper would not remove real duplication. The fix
  uses `api/fault` (`WriteProblem`, `Unavailablef`, `Forbiddenf`, `Internalf`, `Wrapf`) and adds no
  dependency. The test reuses the package's fixtures (`seedCatalogWorld`, `buildPDP`, `makeHandshake`,
  `engineToken`).
- **Conventions.** slog only, `api/fault` errors on the wire (ADR-0002), ctx-first logging, top-level imports,
  no `any` in signatures. The `engineFailed` doc comment states the why (ADR-0002, the default it replaces,
  why the cause stays in the log) and does not narrate. The log key `err` matches the rest of the file.
  The op constant `catalog.gateway.catalogProxy` matches `catalog.gateway.Manager.Ensure` in `manager.go`.
- **ADRs.** No Accepted or Implemented ADR is contradicted or edited. ADR-0137's deny contract (403, upstream
  never called) is unchanged.
- **Checks (touched package).** `go test -race ./internal/catalog/gateway/` ok; `go vet` clean;
  `golangci-lint run ./internal/catalog/gateway/` reports 0 issues. The repo-wide set, Linux lint and the
  catalog lane are left to the group gate.
- **Shape.** A `fix(catalog):` subject, a body that states the cause and the fix, `Fixes #378`, the
  attribution trailer, one issue in one commit.

### Recommendation

Pass. The Minor can be folded into the group PR or left: add a resolvable-but-ungranted case to
`TestIssue378_ProxyErrorsAreProblemJSONViaSlog` so the `!dec.Allowed` branch's problem+json answer is pinned.
