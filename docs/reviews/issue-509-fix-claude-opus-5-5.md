# Fix review — issue #509 (claude-opus-5-5)

- **Issue**: #509 — `FUNCD_SHAPING_HEADERS_SET` splits a header value at a comma into a bogus header
- **Change**: branch `fix/i509`, commit 2878a23 `fix(config): reject an invalid server.shaping.headers.set header name`
- **Files**: `internal/platform/config/config.go`, `internal/platform/config/config_test.go`
- **Governing ADRs**: ADR-0062 (single env-validated config struct), ADR-0114 (edge shaping)
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**

## Blockers

None.

## Majors

None.

## Minors

None.

## Verified correct

- **Regression test fails without the fix, for the issue's reason.** With `git revert --no-commit 2878a23`
  and the branch's test file restored, `TestIssue509_InvalidHeaderSetNameRejected/env` and `/file` fail:
  `config.Load` returns no error (`fault.Kind ""`, expected `invalid`) for
  `Cache-Control=no-store, max-age=0` from the env and for a `"X Bad"` key from the file. That is
  the issue's step 2: the bogus ` max-age` key loads silently.
- **Passes with the fix.** After `git reset --hard` to 2878a23 the worktree is clean, and
  `go test -race -count=1 ./internal/platform/config/` returns `ok`. The test is not skipped.
- **Cause, not symptom.** The issue names the cause: `Validate` has no rule for header names. The fix
  adds a `header_name` validator (`httpguts.ValidHeaderFieldName`) and applies it to every map key with
  `dive,keys,header_name,endkeys`. The file path and the env path both go through it, as the issue's
  Expected behavior asks. The error is `fault.Invalid` and names `server.shaping.headers.set`, through
  the existing yaml-key tag-name function.
- **No over-reach.** The third subtest checks that a comma inside a value from the file still loads
  unchanged, so the documented file workaround (config.go:82 comment) still works.
- **Mutants (overlay, `-run TestIssue509`)**, both fail the test:
  1. remove the `validate:"dive,keys,header_name,endkeys"` tag → FAIL;
  2. make the validator always return true (`… || true`) → FAIL.
- **Scope.** Two hunks in `config.go` (the import plus the tag, and the registration) and one new test.
  Every hunk serves the issue. No test was weakened or deleted.
- **Reuse.** No header-name check exists elsewhere in `internal`, `api`, `pkg` or `cmd`. The fix uses the
  library's `httpguts.ValidHeaderFieldName`, the same rule net/http applies when it drops the header.
  `golang.org/x/net` is already a direct requirement in `go.mod`, so no new dependency and no `go.mod`
  change. The fix extends the existing go-playground validator, as ADR-0062 specifies, rather than adding
  a hand-written check.
- **Conventions.** The import is at the top level. Errors are `api/fault` (`fault.Wrapf … fault.Internal`
  when registration fails). There is one comment, and it gives the why with the issue reference. No YAML
  was added outside a test string.
- **ADRs.** The fix does not contradict ADR-0062 or ADR-0114, and no ADR file was edited.
- **Checks (touched package).** `go test -race` ok, `go vet` clean, `golangci-lint run` 0 issues.
  The repo-wide, Linux-lint and e2e runs are left to the group gate.
- **Shape.** The subject is `fix(config): …`, the body has `Fixes #509`, the Co-Authored-By trailer is
  present, and the commit covers one issue.

## Recommendation

Pass. The fix goes to `/fix` Step 8. `server.shaping.headers.remove` entries are not checked as header
names either. That is outside this issue's scope and is not counted as a finding.
