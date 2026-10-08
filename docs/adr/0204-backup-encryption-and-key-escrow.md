# ADR-0204: Backup encryption and key escrow

- **Status**: Proposed
- **Date**: 2026-10-08
- **Deciders**: green-0-rabbit
- **Tags**: backup, disaster-recovery, encryption, secrets, keys
- **Realizes**: [FEAT-0009/F109](../feat/0009-feat-disaster-recovery.md) (platform backup and restore; DR plan item DR-3)
- **Supersedes in part**: none.
- **Relates to**: ADR-0203 (`Seal`, manifest) · ADR-0202 (records as stored) · ADR-0205 (wiring) · ADR-0206 (restore)
  · ADR-0208 (blob mirror) · ADR-0209 (KV) · ADR-0022 (secrets key) · ADR-0085, ADR-0137 (master) · ADR-0111 (TLS)

## Context & Need

ADR-0203 writes each store file, and ADR-0208 each blob mirror object, through a `Seal` it leaves to this ADR. The
metastore stores a Secret whole, AES-256-GCM encrypted under the secrets key when `secrets.encryptionKeyFile` is set,
plain JSON otherwise; ConfigMaps stay plain (`cmd/funcd/main.go` `buildStore`, `secretEncryptor`); startup refuses
Secrets that do not decrypt with that key (`checkSecretsDecode`), so one key covers all. The node master secret
(`s3gateway.LoadOrCreateMaster`, `buildControlPlane`) loads at every start, derives S3 keypairs (`DeriveKeypair`)
and catalog tokens (`gateway.DeriveCatalogToken`, HMAC key `hs256Key` = SHA-256(master)); with the gateway off it
ignores `s3gateway.masterSecretFile` and sits under the working directory. TLS state is in `<storage.dataDir>/tls` or
provided files; the config holds `auth.token`, `auth.credentials`; no key is in a store or a snapshot (ADR-0202).
Purpose: seal every generation to operator-held recipients so the box writes what it cannot read, record the keys it
needs, define the escrow set and the restore key rules (ADR-0205 builds the sealer, ADR-0206 applies the rules).

## Scenarios

- **scenario: sealed-to-recipients** — Given an operator and a recovery recipient, When a generation is written, Then
  each store file's parts, concatenated, open with the `age` CLI and either identity alone, and not with a third.
- **scenario: recipients-required** — Given a target and no `none`, When the daemon starts with no recipient, one, one
  in two files, or an X25519 and a hybrid one, Then it refuses with `fault.Invalid` naming `backup.encryption.recipients`.
- **scenario: plaintext-secrets-refused** — Given a target and `none: true`, When the daemon starts without
  `secrets.encryptionKeyFile`, Then it refuses naming both keys; with it, it starts, warns and stores no plaintext Secret.
- **scenario: manifest-names-keys** — Given secrets key K and master M, When a generation is written, Then its manifest
  records the fingerprints of K, M and both recipients, and no object holds K, M or SHA-256(M).
- **scenario: restore-names-secrets-key** — Given generation 40 written under K1 and a restore configured with K2, When
  the keys are checked, Then it refuses before loading, naming K1's fingerprint and the escrow file matching it, if any.
- **scenario: master-secret-required** — Given a generation recording master F and an escrow without F, When a restore
  runs, Then it refuses naming F; with F escrowed, a restored `spec.blob` Function keeps its S3 keypair (gateway on).
- **scenario: new-master-secret-lists-changes** — Given loaded Functions a (`spec.blob`), b (`spec.catalogs`), d
  (neither), CatalogServices c (`spec.blob`), e (none), When a restore runs with `--new-master-secret`, gateway on or
  off, Then it prints a, b and c, not d or e.
- **scenario: master-key-migrates** — Given a gateway-off node with its master only in the working directory, When it
  starts on this build, Then the key is copied under `storage.dataDir` with a warning naming both paths and its catalog
  tokens stay; two different keys ⇒ it refuses naming both paths; a gateway-on node with both files keeps its
  `storage.dataDir` key and warns naming the ignored one; a working directory equal to `storage.dataDir` is one key.
- **scenario: recipients-change** — Given generations 1 and 2 sealed to A and B, When the recipients become A and C and
  funcd restarts, Then 3 records A and C, 1 and 2 still open with B, and opening 3 with B fails naming both sets.

## Scope

**In**: envelope, library; what is sealed under `backup.target` (ADR-0208's blob mirror too); keys
`backup.encryption.recipients`, `.none` and start rules; fingerprints and the values of `secretsKey`, `masterSecret`,
`recipients`; escrow set, key history; rotation; restore key rules, `--new-master-secret`; the master's location.
**Out**: layout, manifest structure, targets (ADR-0203); schedule, enabling, verification, escrow completeness
(ADR-0205); the restore command, its other flags (ADR-0206); KV and workload backups (ADR-0209, a later ADR); live
secrets-key rotation, re-keying at restore (ADR-0206 or a rotation ADR); a master file's minimum length (an issue).

## Constraints & Decision drivers

- Decided 2026-10-06 (report §5): Q7 (encryption on whenever a target is set, at least 2 recipients, `none` only when
  explicit, no backup carrying Secrets in plaintext, the recovery key the operator's, never funcd's; restore needs the
  escrowed master secret unless a flag accepts a new one and prints what changes); Q8 (the box puts and lists, never
  reads); Q1 (an impossible combination stops the start); Q13 (config read once); Q14. Report §4.E: key material is
  files the config names. No home-made cryptography; dependencies Apache-2.0, MIT or BSD.

## Alternatives considered

| Option | Outcome |
|---|---|
| **age v1.3.2, one stream per store file** ✅ | Chosen: streaming AEAD with truncation detection, many recipients, a recipients-file format, post-quantum hybrid recipients, and the `age` CLI opens a backup without funcd |
| A stdlib envelope (`crypto/ecdh`, `crypto/hkdf`, chunked AES-GCM) | Rejected: no dependency, but a new format to design and review (chunk order, truncation, header MAC) and no outside tool to open it |
| OpenPGP (ProtonMail go-crypto); provider server-side encryption | Rejected: a large packet format for no gain over age; the provider holds the key, nothing for `file://` |
| A symmetric key or passphrase on the box (age scrypt, the secrets key) | Rejected: the box could read every generation, breaking Q8; one key for two jobs |
| One envelope per 8 MiB part | Rejected: a header per part, and parts no longer concatenate into one age file |
| funcd writes the escrow set to a target, sealed to the recipients | Rejected: one identity would then open the data and the keys inside it; the box would need a second credential |
| Fingerprint = SHA-256 of the key | Rejected: SHA-256(master) is the catalog-token HMAC key (`hs256Key`) |

## Decision

**1. Envelope.** `filippo.io/age` v1.3.2. Each store file (`events`, `metastore`, `runs`) is one `age.Encrypt` stream
to all recipients, cut into ADR-0203's parts in order, so `cat part-*` is one age file; ADR-0208's blob mirror objects
and index files are one stream each, and Decision 5 opens every sealed object under `backup.target`. Recipients come
from `age.ParseRecipients`: X25519 (`age1…`) or hybrid ML-KEM-768 + X25519 (`age1pq1…`); no SSH and no plugin
recipients (external programs); age refuses a set mixing hybrid with X25519. Manifests and probe objects are not sealed.

**2. Keys and start rules.** Checked at start when `backup.target` is set; a violation is `fault.Invalid` naming the
key and file, and the daemon does not start (Q1; as `secretEncryptor`).

| Key (`backup.encryption.`) | Meaning | Default |
|---|---|---|
| `recipients` (NEW) | paths of age recipients files (one recipient per line, `#` comments); at least 2 distinct recipients across the files, sealable together (`age.Encrypt` to `io.Discard`). Without a secrets key they are allowed (Q7 refuses only both missing), with a warning that the envelope alone protects Secret values | none |
| `none` (NEW) | the operator's explicit choice to store files unsealed; needs empty `recipients` and a set `secrets.encryptionKeyFile` (Q7); logs a warning that every record but Secret values leaves in plaintext. Under `Config.NoSecrets` (a store holding no Secret, ADR-0209) only the secrets-key rule and the secrets-key warning are skipped; the plaintext warning is still logged, as "every record leaves in plaintext" | `false` |

**3. What a generation holds.** The metastore file holds records as stored (ADR-0202 Decision 1): ConfigMaps as plain
JSON, Secrets as ciphertext under the secrets key (plain JSON without one), storage keys `namespace/name` plain, all
inside the envelope; run state and event store alike; no generation holds a key. The manifest stays plain (ADR-0203's
`sha256` over the sealed bytes); this ADR fills ADR-0203's reserved `backup.Keys` (in `Manifest` and `WriteOptions`):

| Manifest field | Value |
|---|---|
| `secretsKey` | `Fingerprint` of the secrets key bytes; absent ⇒ no secrets key |
| `masterSecret` | `Fingerprint` of the node master secret; absent ⇒ none recorded (Decision 5) |
| `recipients` | sorted `Fingerprint`s of each parsed recipient's canonical `String()` (`*age.X25519Recipient`, `*age.HybridRecipient`); absent ⇒ `none` |

`Fingerprint(b)` (proposed; decider confirms at acceptance) = the first 16 lowercase hex of SHA-256(label ‖ 0x00 ‖
b), label `funcd-key-fingerprint-v1`, apart from `hs256Key`. At start with a target, funcd logs the three, not bytes.

**4. Escrow set** (location and format proposed; decider confirms at acceptance). The operator keeps it outside the
box, every target and every generation; funcd never reads or writes it while serving.

```
<escrow dir>/
  secrets/*          every secrets key a retained generation names; any file name
  master/*           every master secret a retained generation names; any file name
  tls/               a copy of <storage.dataDir>/tls, or the provided certFile and keyFile
  funcdconfig.yaml   the operator config: auth.token, auth.credentials, key file paths
```

A restore fingerprints every file under `secrets/` and `master/` and needs no TLS file. The age identities stay apart
(the recovery one offline, Q7), so neither place alone yields Secret values when a secrets key is set. Key history: a
key or identity leaves only when no retained generation names it: an ADR-0203 class or pin (Decision 6), an ADR-0208
mirror generation `blob/<e>/gen/<n>` (kept `blob.backup.rebaseline` + `retention`), or an ADR-0209 KV chain.

**5. Restore key rules** (ADR-0206 checks them before any part is read; the master's install and list follow the load):
- **Identities**: from files the operator gives the restore. Each is type-switched to `*age.X25519Identity` or
  `*age.HybridIdentity` and fingerprinted over `Recipient().String()`; another type ⇒ `fault.Invalid`. A generation
  whose `recipients` include none of them ⇒ `fault.Invalid` naming both fingerprint sets.
- **Secrets key**: the restored config's key must have the fingerprint `secretsKey` names, or both be absent; else
  `fault.Invalid` naming the fingerprint and the escrow file that matches it (or none). No re-keying at restore.
- **Master secret**: a set `s3gateway.masterSecretFile` must have the fingerprint `masterSecret` names; unset, the
  matching escrow file is written 0600 to Decision 7's path before the first start. Else `fault.Invalid` naming it,
  unless `--new-master-secret` (NEW restore flag): the restore proceeds and prints, from the loaded metastore, each
  credential that changes: `Function <ns>/<name>: s3-keypair` (`spec.blob`, `addS3Env`), `…: catalog-token`
  (`spec.catalogs`, `resolveCatalogEnv`), `CatalogService <ns>/<name>: s3-keypair` (`spec.blob`, `services/catalog`
  `engineEnv`), s3-keypair rows gateway on or off (a copy may predate the config). Workers re-derive at start, so the
  list finds copies held outside funcd; Identity secrets are stored (ADR-0135). `masterSecret` absent (proposed;
  decider confirms at acceptance): no check, nothing written, the list printed as credentials that may change.

**6. Rotation** (proposed; decider confirms at acceptance).

| Key | Procedure | Generations |
|---|---|---|
| recipients | add the new identity, edit the files, restart (Q13) | the next run uses the new set; older ones keep theirs, since the box cannot read to re-seal (Q8); a removed identity stays while a generation lists it |
| secrets key | no live rotation exists (ADR-0022 defers it; `checkSecretsDecode`); any future one puts the new key in escrow before first use | each manifest names its key |
| master secret | none (`DeriveKeypair`: "no rotation"); a change is the `--new-master-secret` path | each manifest names it |
| TLS, config | the operator refreshes the escrow copy after a change | not in generations |

**7. Master location.** `s3gateway.masterSecretFile` when set, else `<storage.dataDir>/s3gateway/master.key`, gateway
on or off. Migration (proposed; decider confirms at acceptance) on a gateway-off node, the only one that reads the
working directory's `s3gateway/master.key` today (References, #850): that file present and the new path absent
⇒ funcd copies it there 0600 and warns naming both, leaving the old file to the operator; both present and different
⇒ `fault.Invalid` naming both paths and `s3gateway.masterSecretFile`. A gateway-on node keeps its key and warns naming
an ignored working-directory file. Paths compare after `filepath.Abs` and `EvalSymlinks`: one file is one key.

## Temporary workarounds

None.

## Contracts

```go
package envelope // internal/backup/envelope (NEW)

// Config: backup.encryption.recipients and .none; SecretsKey: secrets.encryptionKeyFile's bytes (nil ⇒ none);
// Master: the node master secret (s3gateway.LoadOrCreateMaster). NoSecrets (NEW, ADR-0209): a store holding no
// Secret skips only Decision 2's secrets-key rule and secrets-key warning; the `none` plaintext warning stays.
type Config struct{ Recipients []string; None, NoSecrets bool; SecretsKey, Master []byte }
type Sealer struct{ recipients []age.Recipient; keys backup.Keys }

func New(cfg Config) (*Sealer, error) // Decision 2; a violation ⇒ fault.Invalid naming the key and file
func (s *Sealer) Seal() backup.Seal   // nil when None; else age.Encrypt(dst, recipients...)
func (s *Sealer) Keys() backup.Keys   // Decision 3; Recipients sorted, nil when None; SecretsKey, MasterSecret set from Config either way
func Fingerprint(b []byte) string // Decision 3
func ReadIdentities(paths []string) ([]age.Identity, error)
// Opener binds the operator's identities; its result, called with a manifest's recipients, gives that manifest's Unseal: nil recipients ⇒ nil (as is); no id matches ⇒ fault.Invalid naming both sets (Decision 5)
func Opener(ids []age.Identity) backup.Opener // ADR-0205 verify; ADR-0206 restore run, kv, blob; ADR-0208, ADR-0209 readers
```

```go
package escrow // internal/backup/escrow (NEW)

const SecretsDir, MasterDir = "secrets", "master"

// Find returns the file under dir/sub whose envelope.Fingerprint is fp; none ⇒ fault.NotFound naming fp and the found.
func Find(dir, sub, fp string) (string, error)
func CheckSecretsKey(m backup.Manifest, configured []byte, dir string) error // nil configured ⇒ no key
// PlanMaster, before any part is read, writes nothing: Decision 5's match on masterFile, else Find in MasterDir, else
// fault.Invalid unless acceptNew. The restore writes a non-nil Install to Path (Decision 7) 0600 after the load.
func PlanMaster(m backup.Manifest, masterFile, dataDir, dir string, acceptNew bool) (MasterPlan, error)
type MasterPlan struct{ Install []byte; Path string; List bool } // List: acceptNew took another master, or m names none
type Derived struct{ Kind v1.Kind; Namespace, Name, Credential string } // Credential: "s3-keypair" | "catalog-token"
func ListChanged(ctx context.Context, plan MasterPlan, st store.Store) ([]Derived, error) // after the load; nil unless List
```

| consumes | exposes |
|---|---|
| `filippo.io/age` v1.3.2 `Encrypt`, `Decrypt`, `ParseRecipients`, `ParseIdentities`; ADR-0203 `backup.Seal`, `backup.Unseal`, `Opener`, `Keys`, `Manifest`, `WriteOptions`; `secrets.encryptionKeyFile`, `s3gateway.masterSecretFile`, `storage.dataDir`; the loaded `store.Store` | keys `backup.encryption.recipients`, `.none` (env `FUNCD_BACKUP_ENCRYPTION_RECIPIENTS`, comma-separated; `FUNCD_BACKUP_ENCRYPTION_NONE`); values of manifest fields `secretsKey`, `masterSecret`, `recipients`; `envelope.Opener`, `envelope.Config.NoSecrets`; `escrow.Find`, `CheckSecretsKey`, `PlanMaster`, `ListChanged`; the escrow layout; restore flag `--new-master-secret` |

## Implementation plan

**Files**: NEW `internal/backup/envelope/envelope.go`, `internal/backup/escrow/escrow.go`, `examples/backup-escrow.md`
(layout, `age-keygen -pq`, rotation table); `internal/platform/config/config.go` (`Backup.Encryption`);
`examples/funcdconfig.yaml`; Decision 7: `pkg/funcd/funcd.go`, `cmd/funcd/main.go`, `internal/blob/s3gateway/s3gateway.go`.
**go.mod**: `filippo.io/age` v1.3.2 (adds `filippo.io/edwards25519` v1.2.0, `filippo.io/hpke` v0.4.0,
`filippo.io/nistec` v0.0.4, `golang.org/x/term` v0.45.0; raises `golang.org/x/crypto` to v0.55.0, `golang.org/x/sys`
to v0.47.0). **Blueprint** (at acceptance): the "Backup & disaster recovery" bullet gains "generations sealed to age
recipients; keys in an operator-held escrow set outside every backup".

**Test plan**: one `TestScenario<Name>` per scenario; the first opens the concatenated parts with `age.Decrypt` and any
`age` binary on `PATH`. Units: `TestFingerprintIsLabelled`, `TestNewRules` (a row per rule), `TestFindByContent`,
`TestOpenerUnsealed`, `TestIdentityMatchesRecipientFingerprint`, `TestPlanMasterAbsent`, `TestKeysWhenNone` (`none`
records both fingerprints; `CheckSecretsKey` passes with that key). **Definition of done**: `scripts/agent/d go test
-race -count=1` and `scripts/agent/d just ci` green; the new modules' licences recorded; no identity or path leak.

## Review checklist

- [ ] One `Seal` stream per store file unless `none`; manifests and probes unsealed; no SSH or plugin recipient parses.
- [ ] Each Decision 2 rule is a start-time `fault.Invalid` naming its key; the plaintext-Secrets combination never starts.
- [ ] `Fingerprint` is labelled, over a recipient's `String()`; no key bytes or SHA-256(key) reach a log or an object.
- [ ] `CheckSecretsKey`, `PlanMaster` (no write) and the `Opener` match refuse before any part is read; `ListChanged`
      reads Decision 5's three sources on the loaded store; master path and migration per Decision 7; each scenario tested.

## Consequences

**Positive**: a leaked box credential or a stolen target yields ciphertext; the `age` CLI alone opens a generation; a
restore names every key it needs before it starts; hybrid recipients resist later quantum attacks on kept generations.
**Negative (accepted)**: four new modules; a removed identity is kept until its last generation expires; the escrow
set and identities are the operator's to guard and keep complete, unchecked by funcd; no live secrets-key rotation.
**Risk**: a manifest reader can test guesses of a low-entropy operator key file against its fingerprint (funcd's are
32 random bytes); age does not authenticate the sender, so a put credential can write a well-formed false generation.

## Open questions

| Item | Recommended default (proposed; decider confirms at acceptance) | Why |
|---|---|---|
| Manifest sealed or checksummed | plain, ADR-0203's `sha256`, no signature | keys must be named before they are present; a signing key on the box adds nothing against a box compromise |
| Escrow location and format | operator-held directory of Decision 4, matched by content fingerprint; funcd never writes it | keeps keys and data behind different holders |
| Rotation procedure, recipient change | Decision 6: each run uses and records the configured set, old generations are never re-sealed; live secrets-key rotation in a follow-up secrets-at-rest ADR superseding ADR-0022's deferral | the box puts and lists only and cannot re-seal (Q8); no rotation path exists today |
| Fingerprint form | labelled SHA-256, 16 hex characters | identifies, does not expose `hs256Key` |
| Manifest without `masterSecret` | no check, nothing written, the Decision 5 list printed as may change; the alternative refuses unless `--new-master-secret` | ADR-0203's extension rule: an absent field keeps the behavior before it |
| Working-directory `master.key` | gateway off: copy to Decision 7's path with a warning, refuse when both exist and differ; gateway on: keep the key, warn | keeps the tokens a gateway-off node issued; never picks between two keys |

## References

- `docs/reports/platform-disaster-recovery-design.md` §1, §3, §4.A (Encryption, Credential, Master secret, ConfigMaps
  and Secrets, Key rotation), §4.I row 3, §5 (Q1, Q7, Q8, Q13, Q14); FEAT-0009; `docs/roadmap/dr-plan.json` (DR-3).
- Gateway-off master: no `WithS3Gateway`, so `LoadOrCreateMaster` joins an empty `dataDir`; a bug Decision 7 fixes (#850).
- [age](https://github.com/FiloSottile/age), [pkg.go.dev](https://pkg.go.dev/filippo.io/age): v1.3.2 of 2026-08-29;
  v1.3.0 (2025-12-27) added hybrid recipients; `ParseRecipients` takes no SSH or plugin recipient; only concrete
  identities have `Recipient()`. Licences checked 2026-10-08: age, hpke, nistec, edwards25519, `golang.org/x/*` BSD-3.
