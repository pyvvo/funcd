# Fix review — issue #379 (catalog PEP proxy logs via slog.Default)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #379 fix, model: claude-opus-5-5)

Change: branch `fix/i379`, one commit `d41a4be fix(catalog): log the catalog PEP proxy through funcd's logger`
(`internal/catalog/gateway/{manager.go,proxy.go,manager_test.go}`, +64/-11).

The fix removes the cause the issue names. The `Manager` now builds every proxy through the unexported
`newCatalogProxy` over a `catalog.gateway` child of its injected logger, so the proxy no longer reads
`slog.Default()`. Both the first build and the retarget call site are covered (`manager.go:120`, `:132`).
The `ReverseProxy` now has an `ErrorHandler`, which logs at Warn through that logger and returns 502, and an
`ErrorLog` routed to the same handler. This is the activator's #141 pattern (`internal/activator/activator.go:414`).

### 🟡 Minor 1 — the two upstream-error routes are only tested together  ·  attribution: model

Evidence: the overlay mutants on `proxy.go`:

| Mutant | Result |
|---|---|
| M1: drop `p.rp.ErrorHandler` | survives (`ok`) |
| M2: drop `p.rp.ErrorLog` | survives (`ok`) |
| M3: drop both | fails `TestIssue379_ProxyLogsThroughManagerLogger` |
| M4: the Manager passes `slog.Default().With(...)` | fails `TestIssue379_ProxyLogsThroughManagerLogger` |

Either route alone sends the 502 line to the injected logger at WARN. The test asserts only `"level":"WARN"` and
that the stdlib `log` stayed empty, so neither line is pinned on its own. Asserting
`"msg":"catalog engine call failed"` would pin the `ErrorHandler`. The behavior is correct; this is only a gap
in the test.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit d41a4be`, with the new test kept,
  then `go test -race -run TestIssue379`: FAIL at `manager_test.go:183`. The injected JSON logger held only the
  Manager's own "catalog proxy ensured" line, not `"msg":"catalog PEP: unresolved credential"`. The proxy logged
  through `slog.Default`, as the issue says.
- **Passes with the fix.** After `git reset --hard` back to `d41a4be`, `go test -race -count=1 ./internal/catalog/gateway/`
  passed. The worktree is clean at that HEAD.
- **User-visible behavior.** The test is the issue's own steps on a real listener. It uses a JSON logger at debug
  level, an unresolvable token (403, and the debug line appears), and then a granted token against a closed engine
  (502, a WARN line, and no stdlib `log` output).
- **Cause, not symptom.** The logger is now injected. There is no suppression, retry or skipped test.
- **Scope.** Every hunk serves #379. No test was weakened or deleted. `proxy_test.go` still uses the exported
  `NewCatalogProxy`.
- **Reuse.** The `ErrorHandler` follows the activator's #141 shape. `slog.NewLogLogger` is the standard-library
  bridge for `ErrorLog`. The change adds no new helper or dependency. The 502 body uses `http.Error`, the same as
  the proxy's other 403 and 500 answers, and the `"err"` key matches the rest of the file.
- **ADRs.** The exported `NewCatalogProxy` keeps its ADR-0137 signature, and no ADR file was touched. ADR-0002 §6
  now holds on the production path: the daemon's proxies log through a named child of the root logger. The
  exported constructor still falls back to `slog.Default` for direct (test) callers. Its doc comment says so, and
  the daemon never calls it.
- **Checks (touched package).** `go test -race` ok, `go vet` clean, `golangci-lint` 0 issues.
- **Shape.** The commit subject is `fix(catalog):`, the body has `Fixes #379` and the attribution trailer, and the
  commit covers one issue.

### Definition of Done

10 of 11 apply and hold. Item 4 (mutating the key lines fails a test) holds only in part: M3 and M4 fail, but M1
and M2 survive on their own (Minor 1). Item 8 was run on the touched package only. The repo-wide gate, the Linux
lint and the e2e suite belong to the group gate.

### Model scorecard

claude-opus-5-5: pass. 0 blockers, 0 majors, 1 minor (model). DoD 10/11.

### Recommendation

Pass. You can merge this as it is. Optional: in the regression test, assert the `ErrorHandler`'s message
`"catalog engine call failed"`.
