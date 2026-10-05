# ADR-0171: Static credential list — admin, developer and viewer tokens from the daemon config

- **Status**: Accepted (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: auth, rbac, config, control-plane, edge, daemon
- **Realizes**: [FEAT-0000/F07](../feat/0000-feat-v1.md) (API server authn — static tokens + scoped API keys,
  namespace RBAC)
- **Supersedes (in part)**, each keeping its status with a `Superseded in part by: ADR-0171` back-link at acceptance:
  - [ADR-0018](0018-api-server-authn-rbac-admission.md) Decision 2's viewer rule, "allowed **iff** namespace ∈
    `Identity.Namespaces` **and** verb ∈ {`VerbGet`, `VerbList`}" (lines 189–191), gains "and the kind is not
    `Secret`" (Decision 9). Its other rules and its `rbac-viewer-is-read-only` scenario stand.
  - [ADR-0028](0028-platform-control-plane-wiring.md): Scope Out "a richer auth surface (multiple roles, token
    issuance …)" (lines 52–53), Consequences "multi-role/issuance is V2" (lines 226–227), Alternatives (line 248),
    Open question "Production credential management (roles, issuance, rotation) | V2" (line 258) and the `WithDevAuth`
    comment (lines 97–98): several tokens with roles are V1; issuance and rotation stay V2. `Production()` sets no
    credential (lines 103–106) and stands.
  - [ADR-0061](0061-funcd-daemon-config-file.md) line 281, `WithDevAuth(Token, Namespaces...)`: applies only while
    `auth.credentials` is absent (Decision 4).
  - [ADR-0062](0062-config-single-env-validated-struct.md): "making **every** key uniformly `FUNCD_*`-overridable"
    (Scope In, line 59), Decision 4 (lines 110–111) and Consequences (line 238): `auth.credentials` has no env var.
  - [ADR-0113](0113-edge-authn-pep.md): Scope Out "Non-namespace-scoped external identities" as V2 (lines 71–73) and
    Consequences (line 238): an admin token is a non-namespace-scoped identity every `authenticated` route accepts in
    V1. A first-class external-caller store and API-key management stay V2.
- **Refines**: ADR-0061's `auth` block (lines 163–165) and `type Auth` (lines 212–215), ADR-0062's `Auth` struct
  (lines 141–144), which gain `credentials` · ADR-0113 line 127: the store it reuses also holds admin and viewer
  identities; after an allow the data plane drops the credential headers (Decision 5).
- **Keeps**: ADR-0018 Decision 3 (lines 198–199, in-memory `CredentialStore` from config) and its workaround row (line 227).
- **Relates to**: [ADR-0110](0110-route-v2-declarative-edge-exposure.md) (`defaultExposure`) ·
  [ADR-0136](0136-roles-and-role-assignments.md) (its `Role` kind is a data-plane permission set, not an RBAC role)
  - Textual overlap only, no ordering dependency (the second to land rebases): ADR-0148, 0151, 0164 also edit
    `dataplane.serveFunction`; ADR-0163, 0164 also edit `cmd/funcd` `buildOptions`.

## Context & Need

On main 1193be6 the daemon builds one credential, always a developer (#92): `WithDevAuth` hard-codes
`auth.RoleDeveloper` (`pkg/funcd/options.go:427`). RBAC denies a developer every cluster-scoped kind
(`internal/auth/rbac/rbac.go:30-32`), so nobody can create a Namespace, RuntimeClass, WorkerNode or Gateway, or set
`defaultExposure` (ADR-0110) and `edgeDefaults.auth` (ADR-0113). The admin and viewer roles exist but no shipped path
builds them. The edge PEP reads the same store (`pkg/funcd/funcd.go:957`). After the PEP allows a call, the caller's
`Authorization` or `X-Api-Key` reaches the Function (`internal/dataplane/dataplane.go:161`), against
`blueprint.md:479`. A viewer can read Secrets (`rbac.go:38-48`). Need: an operator lists static tokens, each in its
own file with a role and a namespace scope, and both PEPs accept them.

## Scenarios

Config scenarios write their token files at mode 0600.

- **scenario: admin-applies-namespace-defaults** — *Given* an `admin` entry, *when* it applies Namespace `public` with
  `defaultExposure: explicit` and `edgeDefaults.auth.mode: authenticated`, *then* Get returns both fields, and List of
  Namespace, RuntimeClass, Gateway and WorkerNode succeeds.
- **scenario: developer-scoped-to-its-namespaces** — *Given* a `developer` scoped to `team-a`, *when* it applies a
  Function in `team-a`, *then* it succeeds; in `team-b`, or any verb on a Namespace or RuntimeClass, 403.
- **scenario: viewer-reads-only** — *Given* a `viewer` scoped to `team-a`, *when* it gets or lists Functions there,
  *then* 200; applies or deletes one, or gets a Namespace, 403.
- **scenario: viewer-cannot-read-secrets** — *Given* a `viewer` and a `developer` scoped to `team-a` and a Secret there,
  *when* the viewer gets or lists Secrets in `team-a`, *then* 403; the developer's get answers 200.
- **scenario: legacy-token-is-developer** — *Given* only `auth.token` with `auth.namespaces` holding `team-a`, it applies
  a Function in `team-a` and a Namespace is 403; *given* neither form, the built-in dev token is a developer in
  `default` and the startup log warns.
- **scenario: bad-token-file-refuses-start** — *Given* a `tokenFile` that is missing, unreadable (0000), a directory, a
  FIFO, empty or whitespace only, two lines, or over 4 KiB, *then* funcd fails with `fault.Invalid` naming
  `auth.credentials[<i>].tokenFile`, before Badger opens.
- **scenario: exposed-token-file-refuses-start** — *Given* a token file at 0640, 0604 or 0620, *then* funcd fails
  naming the key, the mode and the fix (`chmod 0600`).
- **scenario: conflicting-credentials-refuse-start** — *Given* `auth.credentials` beside `auth.token` (file or
  `FUNCD_TOKEN`) or a non-default `auth.namespaces`, or an emptied list (`credentials: []`, or `credentials:` with its
  entries commented out), an unknown role, an `admin` with `namespaces`, an entry with `namespaces: []`, two entries
  with the same token, or a token equal to `funcd.DevToken`, *then* funcd fails before Badger opens, naming the key
  (`auth.token (FUNCD_TOKEN)` for the token) or both entries.
- **scenario: edge-accepts-listed-tokens-in-scope** — *Given* edge auth on and Namespace `team-a` with
  `edgeDefaults.auth.mode: authenticated` and no Function `api`, invoking `team-a/api` with the admin, `team-a`
  developer or `team-a` viewer token gives 404 (the PEP let it through); no or an unlisted token, 401; a developer
  scoped to `team-b`, 403.
- **scenario: edge-drops-the-credential** — *Given* an `authenticated` namespace and a warm Function whose upstream
  records headers, a valid token in `Authorization`, then in `X-Api-Key`, gives 200 and the upstream sees neither; on
  an `open` route an `Authorization` header arrives unchanged.
- **scenario: no-token-in-logs** — *Given* debug logging, *when* funcd starts with two entries, serves the scenarios
  above on both planes, and refuses each bad config above, *then* no token value appears in any log or error.
- **scenario: library-credentials-replace-dev-token** — *Given* `funcd.New(funcd.InMemory(),
  funcd.WithCredentials(<an admin>))`, `funcd.DevToken` gets 401 and the admin token lists Namespaces.

## Scope

- **In**: the `auth.credentials` schema and validation; token files; the shorthand and the both-set rule;
  `funcd.Credential`, `funcd.WithCredentials`; dropping credential headers on `authenticated` routes; the viewer's
  Secret rule; the example config; tests.
- **Out**: issuance, rotation, revocation without restart, OIDC (V2); a separate data-plane credential list (ADR-0113
  line 101, V2); other RBAC changes; reload on file change; hashing tokens at rest; moving e2e tests off store-seeded
  Namespaces; the ADR-0110 explicit-Namespace lane (ADR-0110 lines 309–313) and an ADR-0113 `edgeDefaults` lane (one
  follow-up issue, Implementation plan step 9).

## Constraints & Decision drivers

- ADR-0018: an in-memory `CredentialStore` from config, resolving to `Identity{Subject, Role, Namespaces}`. ADR-0113:
  the edge reuses it and asks the PDP `VerbGet`. ADR-0028: `Production()` ships no credential. ADR-0062: one validated
  config struct; errors name the yaml key. `blueprint.md:479`: functions never receive long-lived platform credentials.
- The decider (decision card 17, 2026-10-04; recorded on #92 at acceptance): one file per entry, never inline; three
  roles; `auth.token` stays a shorthand; a restart applies a change; no token value is logged; a viewer is read-only
  and does not read Secrets; the drafter's choices in Decisions 1–5 are accepted.

## Alternatives considered

| Option | Outcome |
|---|---|
| **A credential list, one token file per entry** | **chosen**: reaches all three roles, no secret in the config |
| A `role` key on the one token | rejected: no admin beside a developer |
| A generated bootstrap admin token in the data dir (k3s, Nomad) | rejected: needs persistence and reset; changes ADR-0018/0028 |
| Namespaces declared in config, no admin | rejected: cluster kinds stay unreachable; two sources of truth |
| Tokens inline in the list | rejected (decider): secrets in a copied, committed file |
| Indexed env vars (`FUNCD_AUTH_CREDENTIALS_0_ROLE`) | rejected: an index gap silently drops entries |
| Warn on a group- or world-accessible token file | rejected: starts with a token others can read or replace |
| A viewer that reads Secrets (ADR-0018 as built) | rejected (decider): a credential is not read-only data |

## Decision

All choices below are decided; none is open.

1. **The list.** `auth.credentials` (default: absent) is a list of `{tokenFile, role, namespaces}`; `role` is `admin`,
   `developer` or `viewer`. `namespaces` scopes a developer or viewer; omitted or null, it is `default`. An explicit
   `namespaces: []` and an `admin` with `namespaces` are refused. The key is file-only, tagged `env:"-"`: caarlos0/env
   v11.4.1 walks a slice of structs under an empty prefix (`env.go:411-412`), so stray `0_*` variables would add
   zero-value entries; the ignore check returns first (`env.go:394`). `TestIssue438_EveryKeyHasEnvOverride` names
   `auth.credentials` as its one exception.
2. **Token files.** Each `tokenFile` holds one token; a relative path resolves against the working directory. It is
   opened read-only, following symlinks, with `O_NONBLOCK`, and the opened file (`(*os.File).Stat`) must be a regular
   file with no group or other bit (`mode & 0o077 == 0`), owned by the daemon's effective UID or root; a `Sys()` that
   is not a `*syscall.Stat_t` is refused. It is read through `io.LimitReader(f, maxTokenFileBytes+1)`; more than 4 KiB
   is refused. The token is the content with ASCII whitespace trimmed; it must be non-empty, bytes 0x21–0x7E only.
   Any failure is `fault.Invalid` naming the key, path and rule, never the content, and funcd does not start. An
   exposed file's error names the fix: `chmod 0600` or `0400`; a Kubernetes secret volume `defaultMode: 0400`; a
   Docker secret `mode=0400` with `uid=` the daemon's UID.
3. **Entry checks before anything opens.** `credentialOption` reads every file, fills in `default`, and refuses
   `namespaces: []`, an admin with namespaces, two entries with the same token (naming `[i]` and `[j]`) and a token
   equal to `funcd.DevToken`, returning before `buildOptions` opens Badger, the substrate or KV. `(Config).Validate`
   checks the tags and Decision 4; `WithCredentials` checks again (Decision 7).
4. **Shorthand and both-set rule.** With `auth.credentials` absent, today's path is unchanged: `auth.token` (default:
   empty, then the built-in dev token with a warning) becomes one developer, Subject `dev`, scoped to
   `auth.namespaces` (default: `default`), via `WithDevAuth`. With `auth.credentials` set, a non-empty `auth.token` or
   an `auth.namespaces` whose value is not the single entry `default` refuses startup (the value is compared, not
   the key's presence, so an explicit `default` passes), naming `auth.token (FUNCD_TOKEN)` (noting funcdctl reads
   `FUNCD_TOKEN` too) or `auth.namespaces (FUNCD_AUTH_NAMESPACES)`. A present but empty list (`credentials: []`, or
   `credentials:` decoding to null) is refused. `CredentialList.UnmarshalJSON`, called for null too, sets a non-nil
   list, so nil means absent; it decodes entries with `DisallowUnknownFields`. Below an Unmarshaler sigs.k8s.io/yaml
   v1.6.0 does not coerce numbers to strings (`yaml.go:189-195`): quote a numeric namespace or `tokenFile`. The forms
   never merge.
5. **One store for both PEPs.** `funcd.WithCredentials` builds the one `middleware.NewStaticCredentials` map that the
   control-plane authn and the edge PEP read. `internal/edge/authn` does not change; ADR-0113's namespace check is
   kept (developer and viewer pass within their namespaces, admin everywhere). After `Enforce` allows an external
   `authenticated` call, `serveFunction` deletes `Authorization` and `X-Api-Key` (the headers the PEP reads,
   `internal/edge/authn/authn.go:88,94`) from the clone it hands the activator. An `open` route forwards every header.
6. **A restart applies a change.** Files are read once at startup.
7. **Library.** `funcd.WithCredentials` replaces any earlier credential store (InMemory's dev token included); a later
   `InMemory()` or `WithDevAuth` replaces it. `New` returns `fault.Invalid`, naming the index, never the token, for
   no credential, a token empty or with a byte outside 0x21–0x7E, a duplicate, `DevToken`, an unknown role, an admin
   with namespaces, or a developer or viewer without them. Entry *i* gets Subject `credentials[i]`. `WithDevAuth`
   does not change.
8. **No token value is logged.** Errors name keys, paths and indexes; no token-holding field has a `validate` tag (the
   validator prints values, `config.go:415`). Startup logs one info line with the count per role.
9. **The viewer does not read Secrets.** A viewer is allowed iff the kind is namespaced and not `Secret`, the
   namespace is in `Identity.Namespaces`, and the verb is `VerbGet` or `VerbList`. `authcontract.Run`, run by the rbac
   driver and the routing authorizer, asserts it. Admin and developer rules and the edge's `get Function` do not
   change. No shipped path builds a viewer and the secrets PEP reads as a developer, so no migration is needed.

## Temporary workarounds

| Workaround | Exit criterion |
|---|---|
| A change to the list or a file needs a restart; revoking is edit + restart | V2 API-key resources with issuance and rotation (ADR-0018 line 227) |
| Edge callers use control-plane identities; an admin token passes every `authenticated` route | V2's first-class external-caller store (ADR-0113 lines 71–73) |

## Contracts

```go
// internal/platform/config/config.go — Auth gains Credentials (file-only, ADR-0171 Decision 1).
	Auth struct {
		Token       string         `json:"token,omitempty" env:"FUNCD_TOKEN"`
		Namespaces  []string       `json:"namespaces,omitempty" env:"FUNCD_AUTH_NAMESPACES" envSeparator:","`
		Credentials CredentialList `json:"credentials,omitempty" env:"-" validate:"dive"`
	} `json:"auth,omitempty"`

// CredentialList: nil means absent; UnmarshalJSON makes a present key (null included) non-nil, DisallowUnknownFields.
type CredentialList []Credential
func (l *CredentialList) UnmarshalJSON(b []byte) error
type Credential struct {
	TokenFile  string   `json:"tokenFile" validate:"required"`
	Role       string   `json:"role" validate:"oneof=admin developer viewer"`
	Namespaces []string `json:"namespaces,omitempty"`
}

// pkg/funcd/options.go
type Credential struct {
	Token      string
	Role       string   // "admin", "developer" or "viewer"
	Namespaces []string // developer or viewer scope, at least one; empty for admin
}
func WithCredentials(creds ...Credential) Option // replaces an earlier store; apply after the preset

// cmd/funcd/credentials.go
func credentialOption(cfg config.Config, log *slog.Logger) (funcd.Option, error) // credentials ⇒ WithCredentials, else WithDevAuth
func readTokenFile(key, path string) (string, error)
func readToken(r io.Reader) (string, error)           // ≤ maxTokenFileBytes, trimmed, checked
func checkTokenFile(fi os.FileInfo, euid int) error   // non-Stat_t Sys() refused
const maxTokenFileBytes = 4096
```

`(Config).Validate` runs Decision 4's checks after `v.Struct(c)` passes, before its early `return nil`, naming keys only.

```yaml
auth:
  credentials:
    - tokenFile: /etc/funcd/tokens/ops
      role: admin
    - tokenFile: /etc/funcd/tokens/team-a
      role: developer
      namespaces:
        - team-a
```

| Dependencies & I/O | |
|---|---|
| Consumes | `auth.credentials[]` (file only); one token file per entry; `auth.token` / `FUNCD_TOKEN` and `auth.namespaces` / `FUNCD_AUTH_NAMESPACES` (the shorthand) |
| Exposes | `funcd.Credential`, `funcd.WithCredentials`; three roles to both PEPs; no credential header to an `authenticated` route's Function; a viewer denied Secrets; `fault.Invalid` at startup |

## Implementation plan

1. `internal/platform/config`: the types, `Auth.Credentials`, Decision 4 in `Validate`. `config_test.go`: the two-entry
   YAML decodes; a missing `tokenFile`, unknown role, unknown entry key, unquoted numeric namespace, `credentials: []`,
   commented-out entries and each both-set case (incl. `FUNCD_TOKEN`) fail naming their key; `0_*`/`1_*` vars change
   nothing. `TestIssue438_EveryKeyHasEnvOverride` counts `env:"-"` as missing, `auth.credentials` its exception.
2. `internal/auth/rbac`: the viewer denies `KindSecret`; the `RoleViewer` comment says so. `TestRbacAuthorizationMatrix`
   gains five viewer-Secret deny rows (get, list, create, update, delete) and a developer get allow; `authcontract.Run`
   gains viewer get and list of Secret denied.
3. `internal/dataplane`: `serveFunction` deletes the two headers after `Enforce`. `TestScenarioEdgeDropsTheCredential`
   (`authn_test.go`, `authDoor`'s wiring, a header-recording upstream).
4. `pkg/funcd`: `Credential`, `WithCredentials`; `WithDevAuth`, `Production()` and `New`'s error name it.
   `credentials_test.go`: `TestWithCredentialsRejects` (Decision 7, no token in errors) and
   `TestScenarioLibraryCredentialsReplaceDevToken`.
5. `cmd/funcd/credentials.go`: the four functions and the info line; `buildOptions` calls `credentialOption` first and
   uses its option in place of `WithDevAuth`.
6. `cmd/funcd/credentials_test.go` (token files at 0600, a debug logger into a buffer):
   `TestScenarioAdminAppliesNamespaceDefaults`, `TestScenarioDeveloperScopedToItsNamespaces`,
   `TestScenarioViewerReadsOnly`, `TestScenarioViewerCannotReadSecrets` (viewer `Get` and `List` of `KindSecret` 403,
   developer `Get` 200), `TestScenarioLegacyTokenIsDeveloper`, `TestScenarioEdgeAcceptsListedTokensInScope` and
   `TestScenarioNoTokenInLogs` on `funcd.New(funcd.InMemory(), opt, funcd.WithEdgeAuth(), funcd.WithLogger(log))`;
   `TestScenarioBadTokenFileRefusesStart` (FIFO returns within 1 s; 0000 skipped under euid 0),
   `TestScenarioExposedTokenFileRefusesStart` and `TestScenarioConflictingCredentialsRefuseStart` via `config.Load` +
   `buildOptions` in a `shortDataDir`, asserting `fault.Invalid` and no `store/` directory; `TestCheckTokenFile` (fake
   `os.FileInfo`) and `TestReadToken` (endless reader, trailing newline, two lines, whitespace only).
7. `examples/funcdconfig.yaml`: a commented `credentials:` block (`default: none`; remove `token` and non-default
   `namespaces`; quote numeric namespaces), `umask 077; openssl rand -hex 32 > /etc/funcd/tokens/ops`, and
   `auth.credentials` as the env-override exception (`TestIssue341_ExampleDocumentsEveryKeyWithDefault` stays green).
8. Feat row F07: `(+ [ADR-0171] — static credential list)`, status `authn/RBAC: implemented · credential list: adr`,
   advanced at each move. Blueprint: no change.
9. File one follow-up issue (`/issue-management`) for the ADR-0110 explicit-Namespace and ADR-0113 `edgeDefaults`
   Venom lanes, applied with the admin token.
10. Verify: `scripts/agent/d go build ./...`, `go test` on the touched packages, `go tool golangci-lint run ./...`.

**Done when** every scenario test, `TestRbacAuthorizationMatrix`, `TestScenarioAuthorizerContractHolds`,
`TestWithCredentialsRejects`, `TestCheckTokenFile` and `TestReadToken` pass, `just ci` is green, the lane issue is
filed, and the PR says `Fixes #92, Fixes #212` (GitHub does not close a parent when its sub-issues close).

## Review checklist

- [ ] `auth.credentials` is `env:"-"`, validates role and `tokenFile`, refuses unknown entry keys; the env test names it.
- [ ] Token files follow Decision 2 exactly; errors name key and path, an exposed file's the fix.
- [ ] Decisions 3–4's refusals happen before anything opens; without the key the shorthand path is unchanged.
- [ ] One map feeds both PEPs; `internal/edge/authn` unchanged; credential headers dropped on `authenticated` only.
- [ ] The viewer is denied every verb on Secret (matrix, `authcontract.Run`, API scenario); other roles unchanged.
- [ ] `WithCredentials` refuses Decision 7's cases; no token in any log or error; every scenario has a passing test.
- [ ] Changed files carry no local username or absolute path of the dev machine.

## Consequences

- (+) An operator gets an admin identity; ADR-0018's `admin-spans-namespaces-and-cluster-kinds` holds on the shipped
  binary. No token sits in the config file.
- (note) A viewer still sees Secret names referenced by `spec.secrets` and `spec.credentialSecretName`.
- (−) Token files are protected by mode and owner only; directories are not checked.
- (−) Common secret mounts (0640 `root:funcd`, Kubernetes default 0644 or `fsGroup`, Docker 0444) refuse startup until fixed.
- (−) A shell that exported `FUNCD_TOKEN` for funcdctl is refused when it starts funcd with `auth.credentials`.
- (−) An edge token also works on the API within its role; on an `open` route a sent token reaches the Function.
- (−) A Function behind an `authenticated` route receives neither header, even one the PEP did not consume (its own
  key in `X-Api-Key` beside a platform `Bearer`).
- (−) `auth.credentials` is the first key with no `FUNCD_*` env var.
- (−) Subjects are positional (`credentials[i]`): reordering the list renames them in logs and audit. A per-entry
  `name` key is a V2 note, not part of this decision.

## Open questions

None.

## References

- Issues [#92](https://github.com/pyvvo/funcd/issues/92), tracker [#212](https://github.com/pyvvo/funcd/issues/212).
- Kubernetes [static token file](https://kubernetes.io/docs/reference/access-authn-authz/authentication/) and
  [RBAC `view` role](https://kubernetes.io/docs/reference/access-authn-authz/rbac/), read 2026-10-04; OpenSSH
  `sshkey_perm_ok`; caarlos0/env v11.4.1; sigs.k8s.io/yaml v1.6.0; docker/cli `opts/swarmopts/secret.go`.
