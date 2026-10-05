# ADR-0171 implementation review: claude-opus-5-5 (loop 1)

- **ADR**: `docs/adr/0171-static-credential-list.md` (Realizes FEAT-0000/F07)
- **Work**: branch `feat/adr-0171-static-credential-list`, one commit `3931301c` on `origin/main` `1193be63`;
  16 files, +1033 / −24
- **Producing model**: claude-opus-5-5
- **Date**: 2026-10-05
- **Verdict**: **pass**. No Blocker and no Major findings. There is one Minor test-coverage gap.

## Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet` on the touched packages, darwin and `GOOS=linux` | exit 0 / exit 0 |
| `golangci-lint run` on the touched packages (darwin) | `0 issues.`, exit 0 |
| The same run with `GOOS=linux` (host-built lint binary, as `scripts/agent/gate.sh` does) | `0 issues.`, exit 0 |
| `go test -race -count=1 ./cmd/funcd ./internal/auth/... ./internal/dataplane ./internal/platform/config ./pkg/funcd` | all `ok`, exit 0, 16 s |
| `-v` run of the 12 scenario tests and the named Done-when tests | every test passes; none is skipped |

Not run, as instructed: e2e, `go test ./...`, Lima and the full `just ci`. The PR gate runs these once.

## Overlay mutants (`go test -overlay`, the work itself left unchanged)

| # | Mutation | Failing test | Result |
|---|---|---|---|
| M1 | `internal/dataplane/dataplane.go:165-166`: remove both `out.Header.Del` calls | `TestScenarioEdgeDropsTheCredential` ("Should be empty, but was Bearer tok") | killed |
| M2 | `internal/platform/config/config.go:148`: remove `env:"-"` from `Credentials` | `TestCredentialsIgnoreIndexedEnv` | killed |
| M3 | `cmd/funcd/main.go:182`: move `credentialOption` after `buildStore` | every `TestScenarioBadTokenFileRefusesStart/*` subtest ("refused before the metastore opened") | killed |
| M4 | `cmd/funcd/credentials.go:116`: `perm&0o077` → `perm&0o066` | none | **survived** (Minor 1) |

M2 is worth noting. `TestIssue438_EveryKeyHasEnvOverride` counts both `env:""` and `env:"-"` as missing, so that
test alone would not catch a dropped tag. The indexed-env test does catch it.

## Findings

### 🔴 Blocker: none

### 🟡 Major: none

### Minor

1. **(model, test-coverage) The test suite does not cover the execute bits of the "no group or other bit" rule.**
   Decision 2 requires `mode & 0o077 == 0`. The code at `cmd/funcd/credentials.go:116` is correct. However,
   `TestCheckTokenFile` covers only 0640, 0604 and 0602, and `TestScenarioExposedTokenFileRefusesStart` covers only
   0640, 0604 and 0620. With M4 applied (a mask that ignores the group and other execute bits), every test still
   passes. A later edit could therefore accept a 0610 or 0601 token file without any test failing. The fix is one
   or two rows, for example 0610 and 0601, in `TestCheckTokenFile`.

Nits (not scored):
- `cmd/funcd/credentials.go:41`: an admin entry with `namespaces: []` is correctly refused, but the message says
  "omit it for default", which reads oddly for an admin.

## Contracts compared with the code

| Contract | Code | Holds |
|---|---|---|
| `Auth.Credentials CredentialList` with `json:"credentials,omitempty" env:"-" validate:"dive"` | `config.go:148` | yes |
| `CredentialList`: nil means absent; `UnmarshalJSON` makes null non-nil and uses `DisallowUnknownFields` | `config.go:449-466`; `TestCredentialsRejected` covers the commented-out entries (null) and the unknown entry key | yes |
| `config.Credential{TokenFile required, Role oneof, Namespaces}` | `config.go:441-446` | yes |
| `(Config).Validate` runs Decision 4 after `v.Struct(c)` passes, in place of the early `return nil` | `config.go:421`, `validateCredentials` at `:470` (names keys only, compares the namespaces value) | yes |
| `funcd.Credential{Token, Role, Namespaces}`, `WithCredentials(...Credential) Option` | `options.go:454-491` | yes |
| `credentialOption`, `readTokenFile`, `readToken`, `checkTokenFile`, `maxTokenFileBytes = 4096` | `cmd/funcd/credentials.go:18-128`, signatures exact | yes |
| `buildOptions` calls `credentialOption` first, in place of `WithDevAuth` | `main.go:182` before `buildStore` at `:196`; `credOpt` in the option list (M3 proves the order) | yes |

Decision details checked in the code:
- **Decision 2.** `O_RDONLY|O_NONBLOCK` follows symlinks (`:70`). The opened file is checked through
  `f.Stat()`. The code refuses a file that is not regular, has `perm&0o077 != 0`, or is owned by neither the
  effective UID nor root (`:113-127`), and it refuses a non-`Stat_t` `Sys()` (`:120-123`). The read goes through
  `LimitReader(max+1)` (`:91`). Whitespace is trimmed and only bytes 0x21–0x7E are accepted. Errors name the key,
  the path and the rule, never the content. The exposed-file error names `chmod 0600`/`0400`, the Kubernetes
  `defaultMode: 0400` and the Docker `mode=0400`/`uid=`.
- **Decision 3.** `namespaces: []`, an admin with namespaces, `DevToken` and a duplicate (naming `[j]` and `[i]`)
  are refused in `credentialOption` before anything opens. `default` is filled in for a developer or viewer whose
  `namespaces` is omitted or null.
- **Decision 4.** The shorthand path is the original code moved unchanged: the same warning and the same
  `WithDevAuth(token, cfg.Auth.Namespaces...)`.
- **Decision 5.** The deletion runs only for `!internal && stance == AuthAuthenticated`, after `Enforce` has
  allowed the call, and it applies to the clone handed to the activator (`dataplane.go:162-167`).
  `internal/edge/authn` is untouched.
- **Decision 7.** `checkCredential` refuses all of Decision 7's cases. Errors name the index and never the token.
  Entry *i* gets the Subject `credentials[i]`.
- **Decision 8.** One info line reports the count per role (`credentials.go:61`). No field that holds a token has a
  `validate` tag.
- **Decision 9.** The viewer is denied `KindSecret` before the namespace check (`rbac.go:43`). The viewer's verb
  test changed from `IsWrite()` to an explicit Get/List allow-list. With the five verbs
  (`internal/auth/authorizer.go:30-41`) the two are equivalent, and the new form matches the ADR wording.

## Scenarios and their tests (all pass under `-race`)

| Scenario | Test |
|---|---|
| admin-applies-namespace-defaults | `cmd/funcd` `TestScenarioAdminAppliesNamespaceDefaults` |
| developer-scoped-to-its-namespaces | `TestScenarioDeveloperScopedToItsNamespaces` |
| viewer-reads-only | `TestScenarioViewerReadsOnly` |
| viewer-cannot-read-secrets | `TestScenarioViewerCannotReadSecrets` (viewer get and list are 403; developer get is 200) |
| legacy-token-is-developer | `TestScenarioLegacyTokenIsDeveloper` (the `auth.token` and built-in subtests, warning asserted) |
| bad-token-file-refuses-start | `TestScenarioBadTokenFileRefusesStart` (missing, directory, FIFO in under 1 s, empty, whitespace, two lines, over 4 KiB, 0000 when euid ≠ 0; no `store/` directory) |
| exposed-token-file-refuses-start | `TestScenarioExposedTokenFileRefusesStart` (0640, 0604, 0620; mode and `chmod 0600` asserted) |
| conflicting-credentials-refuse-start | `TestScenarioConflictingCredentialsRefuseStart` (10 cases, including `FUNCD_TOKEN` and commented-out entries) |
| edge-accepts-listed-tokens-in-scope | `TestScenarioEdgeAcceptsListedTokensInScope` (404 for admin, team-a developer and viewer; 401 for no token or an unlisted one; 403 for the team-b developer) |
| edge-drops-the-credential | `internal/dataplane` `TestScenarioEdgeDropsTheCredential` (Bearer, then `X-Api-Key`; an open route keeps the header) |
| no-token-in-logs | `TestScenarioNoTokenInLogs` (both planes, every bad config, debug log, and errors checked for all tokens and `DevToken`) |
| library-credentials-replace-dev-token | `pkg/funcd` `TestScenarioLibraryCredentialsReplaceDevToken` |

The other named tests also pass: `TestRbacAuthorizationMatrix` (5 viewer-Secret deny rows and 1 developer allow
row), `TestScenarioAuthorizerContractHolds` (routing) and the rbac `authcontract.Run` (viewer get and list of a
Secret denied), `TestWithCredentialsRejects` (11 cases), `TestCheckTokenFile`, `TestReadToken`,
`TestCredentialsDecode`, `TestCredentialsRejected`, `TestCredentialsIgnoreIndexedEnv`, and
`TestIssue438_EveryKeyHasEnvOverride` (exactly `auth.credentials` is exempt). `TestIssue341_ExampleDocumentsEveryKeyWithDefault`
passes as well; `examples/funcdconfig.yaml` has the commented block, `default: none`, the `umask 077; openssl rand`
line, the advice to quote a numeric namespace, and the env-override exception.

## Review checklist: 7 of 7 hold

| Item | Evidence |
|---|---|
| `env:"-"`, role and `tokenFile` validated, unknown entry keys refused, the env test names the key | `config.go:148`, `:441-466`; `TestCredentialsRejected`; `TestIssue438`; M2 |
| Token files follow Decision 2; errors name the key and path; the exposed-file error names the fix | `credentials.go:66-128`; `TestCheckTokenFile`; the exposed and bad-file scenarios |
| Decision 3 and 4 refusals happen before anything opens; the shorthand is unchanged | `main.go:182` before `:196`; M3; `TestScenarioLegacyTokenIsDeveloper` |
| One map feeds both PEPs; `internal/edge/authn` unchanged; headers dropped on `authenticated` routes only | `options.go:489`; the edge scenarios; M1 |
| The viewer is denied every verb on Secret (matrix, `authcontract.Run`, API scenario); other roles unchanged | `rbac.go:43`; the matrix rows; the contract rows; `TestScenarioViewerCannotReadSecrets` |
| `WithCredentials` refuses Decision 7's cases; no token in any log or error; every scenario has a passing test | `TestWithCredentialsRejects`; `TestScenarioNoTokenInLogs`; the table above |

## ✅ Verified correct (keep this)

- The credential check runs first in `buildOptions`, and the tests prove the ordering (M3). They use the file
  substrate in a short data directory and assert that no `store/` directory exists.
- One `NewStaticCredentials` map serves both PEPs. The edge package was not touched, and the header drop is
  limited to external `authenticated` calls.
- The no-token-in-logs scenario is thorough. It covers every token, including `DevToken` and the conflicting
  shorthand token, across both planes, every refused config and every returned error.
- `CredentialList.UnmarshalJSON` handles null, the commented-out list and unknown entry keys, as Decision 4
  requires. `TestCredentialsIgnoreIndexedEnv` is the test that gives the `env:"-"` tag meaning.
- `fakeInfo.Sys` returns an untyped nil, so the "no owner information" case tests the real branch.
- The change stays in scope. There is no new dependency, no `//nolint` without a reason, and the comments explain
  why rather than what.

## Tracking and pipeline steps (not scored; the review stamps nothing, as instructed)

- The ADR is still `Accepted`, and the F07 row has not advanced. The branch does not touch `docs/`, so the ADR's
  substance is unchanged. Under this workflow the docs PR for the wave makes the status and row moves.
- Implementation plan step 9, the follow-up issue for the ADR-0110 and ADR-0113 Venom lanes, could not be
  confirmed. A search for issues that mention ADR-0171 on the tracker found none. The PR text
  `Fixes #92, Fixes #212` also cannot be checked yet, because no PR exists. Both items are integrator or PR steps
  of the wave and must be done before merge. They are recorded as `env`.

## Recommendation

**pass**. The work matches every Contract, every Decision and every scenario, and three of the four mutants on
key lines were killed. As an optional follow-up, add one or two execute-bit rows to `TestCheckTokenFile` to close
the M4 gap. Before merge, the wave pipeline must file the lane issue and put `Fixes #92, Fixes #212` in the PR.

## Ledger row

```json
{
  "date": "2026-10-05",
  "adr": "0171",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 9,
  "dod_total": 9,
  "report": "docs/reviews/adr-0171-implementation-claude-opus-5-5.md",
  "notes": "all contracts and Decisions 1-9 match; 12/12 scenario tests and every named DoD test pass under -race; darwin and Linux build, vet and lint green; 3/4 mutants killed (edge header drop, env:\"-\", credentials-before-Badger); the 0o077 mask has no exec-bit test case (M4 survived) (model); lane issue and PR Fixes line not yet done (wave pipeline, env)"
}
```
