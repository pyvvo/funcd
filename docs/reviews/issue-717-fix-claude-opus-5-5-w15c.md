## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #717 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i717`, one commit d5054c79 `fix(funcd): refuse or trim unusable auth.token and
auth.namespaces shorthand values` (`cmd/funcd/credentials.go`, `cmd/funcd/credentials_test.go`). Merge base
6baf945a; `cmd/funcd`, `internal/platform/config` and `pkg/funcd/options.go` are identical between the merge
base and the current `origin/main` (a394c6f1), so the proof below holds on current main.

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)

- **Proof first, on current `origin/main`, one proof per case.** Overlaying the `origin/main` version of
  `cmd/funcd/credentials.go` (`go test -overlay`, test file kept) makes every subtest of
  `TestIssue717_ShorthandTrimsOrRefusesUnusableValues` fail, each for the issue's reason:
  - `FUNCD_TOKEN-trailing-space` (`probetok `) and `block-scalar-token` (`token: |`): `sdk: controlplane.authn: invalid credential`.
  - `FUNCD_AUTH_NAMESPACES-space-after-comma` (`default, team-a`): `controlplane.authz: update Function in "team-a" denied: developer not scoped to namespace "team-a"`.
  - `empty-namespaces` (`namespaces: []`), `empty-namespace-entries` (`,`), `whitespace-token`, `two-line-token`,
    `credentials-empty-namespace-entry`: `An error is expected but got nil` (startup accepted the value).
  All five cases listed in the issue's step 3 have their own subtest.
- **Passes with the fix**: `go test -race -count=1 ./cmd/funcd/` → `ok github.com/pyvvo/funcd/cmd/funcd 18.166s`
  (the regression test and every existing credentials/startup test, un-skipped).
- **Mutants** (overlay, `-run TestIssue717…`), each killed:
  - M1 `devAuthOption` keeps the untrimmed `cfg.Auth.Token` instead of the parsed token → `FUNCD_TOKEN-trailing-space`, `block-scalar-token` fail.
  - M2 `namespaceList` checks the trimmed entry but stores the raw one → `FUNCD_AUTH_NAMESPACES-space-after-comma` fails.
  - M3 `namespaceList` drops the empty-list refusal → `empty-namespaces` fails.
  - M4 credential entries skip `namespaceList` → `credentials-empty-namespace-entry` fails.
- **Cause, not symptom**: the issue's root cause (`credentialOption` passing the shorthand unchecked to
  `WithDevAuth`) is removed at that point; the request-side `bearerToken`/`Lookup` exact match is untouched, and
  the behaviour stays fail-closed (refusal at startup, `fault.Invalid`, before `buildOptions` opens Badger, which
  `requireRefused` asserts).
- **Errors never carry the token**: the parse errors come from `parseToken`'s fixed strings; the test asserts
  `NotContains(err, "probe")` on every refusal.
- **Reuse**: the token rule is not re-implemented; `readToken`'s trim + 0x21–0x7E check is split into
  `parseToken` and shared by the token-file path and the shorthand. `namespaceList` is the single namespace
  check for both the shorthand and `auth.credentials[i].namespaces`, replacing the old inline empty-list case.
  No existing trim/split helper in `internal/platform/config` or `cmd/funcd` does this. The test reuses
  `loadConfig`, `startCredPlatform`, `requireRefused`, `startCase`, `entry`, `tokenFile` and `shortDataDir`.
- **Scope**: every hunk serves #717. Applying `namespaceList` to `auth.credentials` entries closes the same
  cause on the sibling path (an empty or space-padded entry there), is stated in the commit message and has its
  own subtest. The admin `namespaces: []` case is still refused (now with the "set on an admin" message, which
  is the accurate one); the existing credentials tests pass.
- **ADRs**: ADR-0171 Decisions 4 and 7 hold — the shorthand still maps to one developer, Subject `dev`, via an
  unchanged `WithDevAuth`; only values no request could use are refused or trimmed, matching what the
  `auth.credentials` path already did. No ADR file is edited. Config defaults keep `auth.namespaces` at
  `["default"]`, so an unset list does not trip the new empty-list refusal.
- **Conventions**: `api/fault` `Invalid` errors naming the key, `const op`, top-level imports, short doc comments
  stating the why with the issue number, surrounding naming kept.
- **Checks (touched package)**: `go vet ./cmd/funcd/` clean; `golangci-lint run ./cmd/funcd/...` → `0 issues.`
  Repo-wide, Linux lint and e2e are left to the group gate.
- **Shape**: `fix(funcd):` subject, `Fixes #717`, attribution trailer, one commit for one issue. Worktree left clean.

### Definition of Done
12 / 12 items hold (item 8 scoped to the touched package's host build, vet, lint and `-race` tests; the
repo-wide and Linux checks run in the group gate). Misses: none.

### Model scorecard
Not recorded here (the batch's ledger PR records it): claude-opus-5-5 on issue #717 (fix) → pass, 0/0/0,
0 model-attributed, DoD 12/12.

### Recommendation
Ready for the group integration and gate; no changes requested.
