# ADR-0326: Secret files that other users can open warn at start

- **Status**: Accepted (2026-11-13), by the decider with choices 1 to 3 of its brief as proposed; judged once: no Blocker or
  Major, six Minors folded in
- **Date**: 2026-11-13
- **Deciders**: green-0-rabbit
- **Tags**: security, config, daemon, secrets
- **Realizes**: [FEAT-0001/F238](../feat/0001-feat-v1.1.md) (secret files that other users can open are named at start)
- **Answers**: the decider's choice of 2026-11-13: a warning, not a refusal
- **Keeps**: [ADR-0171](0171-static-credential-list.md) Decision 2: a token file that other users can open refuses the start
- **Relates to**: [ADR-0022](0022-secrets-service.md) (the secrets key) · [ADR-0203](0203-backup-format-targets-and-fencing.md),
  [ADR-0208](0208-blob-store-backend-and-backup-target.md), [ADR-0209](0209-kv-backup-on-the-common-format.md) (the credentials
  files) · [ADR-0085](0085-s3-in-platform-identity-funcd-keypair.md) (the master secret) ·
  [ADR-0111](0111-tls-termination-certmagic.md) (the provided TLS key) · [ADR-0204](0204-backup-encryption-and-key-escrow.md) (the
  escrow set and identities stay the operator's) · [ADR-0313](0313-one-place-says-what-the-platform-does-and-who-decided-it.md) (the row below)

## Context & Need

funcd reads secret files from seven keys of `funcdconfig.yaml`. A token file must be private: one that
group or others can open, or that another user owns, refuses the start (ADR-0171 Decision 2, `checkTokenFile` in
`cmd/funcd/credentials.go`). The other six are read as they are: the secrets key (`envelope.ReadSecretsKey`), the three S3
credentials files (`gocloud.credentialsOf`, through the AWS SDK), the master secret (`internal/blob/s3gateway`) and the
provided TLS key (`internal/edge/tls/static`). A key written by `echo > file` under umask 022 is mode 0644: funcd loads it
with no word, and every local user can read it.

**Purpose.** At each start, the operator learns which of those six files other users can open, with the fix, and funcd still
starts. Caller: the operator who reads the start log.

## Scenarios

- `scenario: exposed-secret-file-warns`: Given `secrets.encryptionKeyFile` naming a 32-byte key at mode 0644, and in turn each
  other key of Decision 1 naming a file at 0640, or at 0600 owned by another user, When funcd starts, Then it serves, and
  its log holds one warning per set key, naming the key, the path and the rule's text (`chmod 0600` for a mode, the owning
  uid for an owner), and never the file's content.
- `scenario: private-secret-file-is-quiet`: Given each key of Decision 1 naming a file at 0600 or 0400 owned by funcd's user,
  or unset, or naming a path that does not exist, When funcd starts, Then it logs no such warning; a missing path fails in
  its loader as today.

## Scope

**In**: the warning at the daemon's start for the six keys; the rule of ADR-0171 Decision 2, reused; the keys' `doc` tags;
the behavior rows. **Out**: refusing the start (the decider); token files, unchanged; the commands `funcd restore`,
`funcd upgrade` and `funcdctl backup`, and the files their flags name: the operator's restore credentials (ADR-0203
Decision 5, ADR-0208's `--store-credentials-file`) and age identities (ADR-0204 Consequences); the directories on a file's
path (as ADR-0171); `backup.encryption.recipients` files, which hold public keys; the files funcd writes itself at 0600
(the default master secret).

## Constraints & Decision drivers

- The decider (2026-11-13): a warning, not a refusal.
- ADR-0171 Decision 8: no secret value in any log or error. One rule for every secret file, so an operator meets one fix.
- No new key, state or dependency.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| **Warn, with the token files' rule** | the operator learns it at start, with the fix; the start goes on | a `root:funcd` 0640 mount warns at each start | **chosen** (decider, 2026-11-13) |
| Refuse, as token files do | nothing starts with an open key | a secret mount at 0640, 0644 or 0444 stops the start | rejected (decider) |
| Check in each loader, on the file it opens, as `readTokenFile` does | only the keys in use, and the very file read | the rule lives in package `main` (`cmd/funcd`), so four `internal/` packages would need it moved or copied | rejected |
| Exempt a root-owned file at 0640 (PostgreSQL's rule for its TLS key) | quiet for a group-shared mount | a second rule, for a warning that stops nothing | rejected |
| A check that is off by default (Vault's `VAULT_ENABLE_FILE_PERMISSIONS_CHECK`) | no noise | it warns only operators who know to turn it on | rejected |
| Nothing, as today; an audit tool checks modes (Kubernetes' CIS benchmark with kube-bench) | no change | no audit tool runs beside funcd, so nothing tells the operator | rejected |

## Decision

1. **The files.** At the daemon's start, each of these keys that is set names a secret file: `secrets.encryptionKeyFile`,
   `backup.credentialsFile`, `blob.credentialsFile`, `kvstore.backup.credentialsFile`, `s3gateway.masterSecretFile` and
   `server.tls.keyFile`. A key whose feature is off is checked too.
2. **The rule.** funcd stats the path, following symlinks, and applies `checkTokenFile` (ADR-0171 Decision 2): a regular
   file, no group or other bit, owned by the daemon's effective UID or root. A path that does not stat is left to its loader,
   which reports it as today.
3. **A warning, not a refusal.** Each set key whose file breaks the rule gets one warning, naming the key, the path and the
   rule's text, never the content; the start goes on. `serve` runs the check once, right after it builds the logger, before
   any store opens.
4. **The docs.** The `doc` tag of each of the six keys says to keep the file at mode 0600 or 0400, owned by funcd's user or
   root, and that funcd warns at start otherwise.

## Design model

None: one stat of up to six paths at a start, with no lifecycle, no second actor and no timer.

## Behavior rows

Added (ADR-0313):

| Chapter | Where | What happens | Time | You see | Status | Source |
|---|---|---|---|---|---|---|
| Security | A secret file other than a token file that group or others can open, or that another user owns | funcd starts, with one warning per set key | none | `secret file open to other users`, with the key, the path and the rule's text | decided | Decisions 1 to 3, `TestScenarioExposedSecretFileWarns` |
| Security | A token file that group or others can open, or that another user owns | funcd does not start | none | `token file "<path>" has mode 0644, open to group or others: chmod 0600 (or 0400)`, or its owner's uid | decided | ADR-0171 Decision 2, `TestScenarioExposedTokenFileRefusesStart` |
| Security | A key whose feature is off, or a group-readable mount such as `root:funcd` at 0640 or a Kubernetes secret volume under `fsGroup` | Warns at each start like any other open file | none | `secret file open to other users` | accepted | Consequences |

Changed: none. Removed: none.

## Temporary workarounds

None.

## Contracts

```go
// cmd/funcd/credentials.go

// secretFile is one set key of ADR-0326 Decision 1 and the path it names.
type secretFile struct{ key, path string }

// secretFiles returns the set keys of Decision 1 in its order.
func secretFiles(cfg config.Config) []secretFile

// warnExposedSecretFiles logs one warning per file of secretFiles that checkTokenFile refuses for euid; it never fails.
func warnExposedSecretFiles(cfg config.Config, euid int, log *slog.Logger)
```

The warning, at the default `log.format: json`:

```
{"level":"WARN","msg":"funcd: secret file open to other users","key":"secrets.encryptionKeyFile","path":"/etc/funcd/secret.key","problem":"has mode 0644, open to group or others: chmod 0600 (or 0400); a Kubernetes secret volume needs defaultMode: 0400, a Docker secret mode=0400 with uid=0"}
```

| Dependencies & I/O | |
|---|---|
| Consumes | the six keys of Decision 1; `checkTokenFile`; `os.Stat`, `os.Geteuid` |
| Exposes | one warning per such file at start; no new key, flag, error or status reason |

## Implementation plan

1. `cmd/funcd/credentials.go`: `secretFile`, `secretFiles` and `warnExposedSecretFiles`, which reuse `checkTokenFile`.
2. `cmd/funcd/main.go`: `serve` calls `warnExposedSecretFiles(cfg, os.Geteuid(), root)` right after `root := logger.Root()`.
3. `cmd/funcd/credentials_test.go`, with a buffer logger: `TestScenarioExposedSecretFileWarns` sets each key in turn to a file
   at 0644 or 0640, and one at 0600 with another euid, and asserts one warning naming the key and the path, without the
   file's content; `TestScenarioPrivateSecretFileIsQuiet` covers 0600, 0400, unset and a missing path.
   `TestServeWarnsOnAnOpenSecretFile` starts `serve` under `--memory` with a 0644 key and asserts the line and a served API call.
4. The six `doc` tags in `internal/platform/config/config.go` (Decision 4), then `scripts/agent/d just generate`.
5. The rows above in `docs/behavior/security.md`, then `scripts/agent/d just behavior-values` and `just check-behavior`.
6. Feat row F238 advances at each move. Blueprint: none.
7. Verify: `scripts/agent/d go build ./...`, the touched packages' tests, `go vet` and lint.

**Done when** each test passes and fails under its mutant: the warn test when `checkTokenFile`'s verdict is ignored, the quiet
test when every set path warns, and `TestServeWarnsOnAnOpenSecretFile` with step 2's call removed; and the touched packages'
tests, vet, lint, the generated spec pages and `just check-behavior` pass.

## Review checklist

- [ ] One list holds the six keys of Decision 1; each set one is checked once per start, before any store opens.
- [ ] The rule is `checkTokenFile`, unchanged; token files still refuse the start.
- [ ] A broken rule logs a warning and never stops the start; a path that does not stat logs nothing here.
- [ ] No warning holds a file's content; each names the key and the path.
- [ ] The six `doc` tags and the generated spec pages say the mode; the rows match; `just check-behavior` passes.
- [ ] Changed files carry no local username or absolute path of the dev machine.

## Consequences

- (+) An operator learns at start that a key or credential is open to other local users, and how to fix it.
- (−) funcd still starts with such a file, and a warning can go unread: the decider's choice over a refused start.
- (−) A group-readable mount warns at each start: `root:funcd` at 0640, or a Kubernetes secret volume under `fsGroup`; such a
  volume is quiet only at `defaultMode: 0400` with funcd as root.
- (−) A key whose feature is off is checked too, such as `blob.credentialsFile` under `storage.mode: memory`.
- (−) The directories on a path are not checked, as for token files (ADR-0171 Consequences).

## Open questions

None.

## References

- [FEAT-0001/F238](../feat/0001-feat-v1.1.md), which takes no feature issue, like F228.
- [ADR-0171](0171-static-credential-list.md) Decision 2, its rejected "warn on a group- or world-accessible token file", and its
  OpenSSH `sshkey_perm_ok` reference.
- Read 2026-11-13: PostgreSQL refuses a server key that group or others can access, or 0640 when root owns it
  ([thread](https://www.postgresql.org/message-id/20170227225845.GG421%40momjian.us)); Helm warns on a group-readable
  kubeconfig ([Red Hat](https://access.redhat.com/solutions/6267231)); Vault's permission check is off by default
  ([configuration](https://developer.hashicorp.com/vault/docs/configuration)); Kubernetes leaves file modes to the CIS
  benchmark ([PKI key files at 600](https://tenable.com/audits/items/CIS_Kubernetes_v1.5.1_Level_1.audit:c850dc5c02a41ac6da7ab648b8d2d416)).
