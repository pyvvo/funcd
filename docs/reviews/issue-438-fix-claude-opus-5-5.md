## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #438 fix, model: claude-opus-5-5)

Change: `fix/i438`, one commit 17170bd `fix(config): give the TLS, shaping and network keys a FUNCD_* env override`
(`internal/platform/config/config.go` +9/−8, `internal/platform/config/config_test.go` +47).

### 🔴 Blockers
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 17170bd` with the fix's test kept:
  `TestIssue438_EveryKeyHasEnvOverride` FAILs with `Should be empty, but was [server.tls.hosts
  server.shaping.cors.allowOrigins server.shaping.cors.allowMethods server.shaping.cors.allowHeaders
  server.shaping.cors.maxAgeSeconds server.shaping.headers.set server.shaping.headers.remove
  server.network.internalAllow]` — exactly the eight keys the issue lists.
- **Passes with the fix.** After `git reset --hard 17170bd`: `go test -race -count=1 ./internal/platform/config/` → `ok`.
- **User-visible behavior.** The test's second half is the issue's own step: it sets the eight `FUNCD_*` vars with no
  config file and `config.Load("", Flags{})` returns them parsed (lists on `,`, `headers.set` as `name=value` pairs,
  a value containing `=` split on the first `=` only). `config.Load` is the single path `cmd/funcd` uses, so the daemon
  sees the same values. An env-sourced `maxAgeSeconds=-1` is rejected with `fault.Invalid` (the merged-struct
  validation of ADR-0062 Decision 3 covers the new var).
- **Cause, not symptom.** The issue names the missing `env:` tags; the fix adds exactly those tags. Nothing is retried,
  swallowed or skipped.
- **Mutants (3/3 killed)**, run with `-run 'TestIssue438|TestEnv'`:
  1. drop the `env`/`envSeparator` tags from `InternalAllow` → FAIL (the reflective walk);
  2. change `envKeyValSeparator` from `=` to `:` → FAIL (the `headers.set` map assertion);
  3. rename `FUNCD_SHAPING_CORS_MAX_AGE_SECONDS` → FAIL (the parsed-value assertion).
- **Regression guard for the future.** The reflective walk over every `Config` leaf (exempting only the
  `apiVersion`/`kind` envelope) fails for any key added later without an `env:` tag, which closes the class of defect,
  not only these eight keys.
- **Scope.** Every hunk serves the issue; no test was weakened or deleted.
- **Reuse.** The fix uses the existing mechanism — caarlos0/env tags with `envSeparator:","` and
  `envKeyValSeparator:"="` — in the same form as `FUNCD_AUTH_NAMESPACES` and `FUNCD_IMAGE_OVERRIDE`. No new helper,
  type or dependency. The test's reflect walk has no existing counterpart in the package or `internal/testkit`;
  `TestEnvVarsMapToFields` covers string-valued keys only, so asserting the list/map/int values in the new test is not a
  duplicate.
- **Conventions.** The var names follow the existing prefixes of their blocks (`FUNCD_TLS_*`, `FUNCD_SHAPING_*`,
  `FUNCD_NETWORK_*`). Imports are at top level (`reflect` added to the test's import block). The single new code comment
  states a non-obvious constraint (a header value with a comma needs the file), which is a *why* comment, not narration.
- **ADRs.** ADR-0062 Decision 4 ("every key is uniformly `FUNCD_*`-overridable") is now true. ADR-0111, ADR-0114 and
  ADR-0115 do not make these keys file-only, so nothing is contradicted. No ADR file was edited. The header of
  `examples/funcdconfig.yaml` ("EVERY key also has a FUNCD_* env override") is now accurate; no other living doc lists
  the env names.
- **Checks (touched package).** `go test -race` ok, `go vet` ok, `golangci-lint run ./internal/platform/config/` → 0
  issues.
- **Shape.** `fix(config):` subject, cause/fix/test body, `Fixes #438`, attribution trailer, one issue in one commit.
- Worktree left at 17170bd and clean.

### Definition of Done
11 / 11 items hold. Item 8 was checked at the scope of this review (touched package: build, race tests, vet, host
lint); the repo-wide set, Linux lint and e2e are run once by the group gate. No e2e or lane covers env parsing of these
keys beyond the unit path.

### Model scorecard
Ledger fields: claude-opus-5-5 on issue #438 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11
(not recorded by this run; the orchestrator records it).

### Recommendation
Sign off. Hand back to `/fix` for the PR; the group gate runs the repo-wide checks.
