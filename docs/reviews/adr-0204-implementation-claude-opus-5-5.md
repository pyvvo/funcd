## Verdict: pass — 0 blockers, 0 majors, 4 minors  (ADR-0204 implementation, model: claude-opus-5-5)

Scope: the three commits after `8b2d1804` that belong to ADR-0204 (`4f812692` envelope + escrow, `be3d9c5b` start
rules + master seam, `b4673576` status bump). The merge commit `e4149715` brings in ADR-0201/0203/0213 work already
reviewed separately and is not graded here.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build (darwin) | `scripts/agent/d go build ./...` | exit 0 |
| build (linux) | `GOOS=linux scripts/agent/d go build ./...` | exit 0 |
| vet (darwin, linux) | `go vet ./cmd/funcd/ ./pkg/funcd/ ./internal/backup/... ./internal/blob/s3gateway/ ./internal/platform/config/` | exit 0 both |
| lint (darwin) | `go tool golangci-lint run` on the touched packages | `0 issues.`, exit 0 |
| lint (linux) | `GOOS=linux <golangci-lint> run` on the touched packages (the gate's form) | `0 issues.`, exit 0 |
| modules | `go mod verify`; `go mod tidy -diff` | `all modules verified`; no diff |
| tests | `go test -race -count=1 ./internal/backup/... ./internal/blob/s3gateway/... ./internal/platform/config/... ./cmd/funcd/` | all `ok`, exit 0 |
| tests | `go test -race -count=1 -run Master ./pkg/funcd/` | `ok`, exit 0 |
| scenarios | `-v -run Scenario` with the `age` v1.3.2 CLI on `PATH` (built from the module cache) | all 9 ADR-0204 scenario tests PASS, the CLI branch of `TestScenarioSealedToRecipients` included |
| mutants | `go test -overlay` (work tree untouched) | 3/3 killed, below |

Mutants:
1. `Fingerprint` without the label write → `TestFingerprintIsLabelled` FAIL.
2. `readRecipients` `len(out) < 2` → `< 1` → `TestScenarioRecipientsRequired` FAIL.
3. `PlanMaster` skips the `masterSecretFile` fingerprint compare → `TestScenarioMasterSecretRequired` FAIL.

`just ci` and the repo-wide/e2e runs were not run here; the per-PR gate runs them.

### Minor

- **`escrow.Find` skips symlinks silently** · attribution: model · `internal/backup/escrow/escrow.go:43-44`.
  `filepath.WalkDir` does not follow symlinks and the callback keeps only `d.Type().IsRegular()`, so a key file that
  is a symlink, or a `secrets/`/`master/` directory that is a symlink, is neither matched nor listed. Probe (overlay
  test, not committed): a symlinked key and a symlinked `secrets/` both return `fault.NotFound … found: none` although
  the matching key is there. It fails closed (the restore refuses), but the "found" list then misleads the operator.
  Fix: stat through symlinks (`os.Stat` on the entry, `filepath.EvalSymlinks` on the root), or name skipped
  non-regular entries in the error; document it in `examples/backup-escrow.md` at least.
- **Daemon-level start-rule test is thin** · attribution: model · `cmd/funcd/backup_encryption_test.go:116-118`. The
  `none: true` without a secrets key case asserts the message only, not `fault.Invalid`; nothing exercises the
  comma-separated `FUNCD_BACKUP_ENCRYPTION_RECIPIENTS` overlay or the `none: true` + secrets-key start that the
  plaintext-secrets-refused scenario says "starts". The envelope-level tests cover the rules themselves, so this is
  coverage polish, not a missing scenario.
- **Contracts and dependency list drift from what was built** · attribution: adr. The Contracts block lacks surfaces
  the ADR owner settled: `envelope.Config.KeyPrefix` and `.Logger`, `funcd.WithMasterSecret`, the Opener's refusal of a
  store file whose seal disagrees with its manifest, `Sealer.Keys()` de-duplicated, and `s3gateway.MasterPath`. The
  Implementation plan's go.mod list names `filippo.io/edwards25519`, `filippo.io/nistec` and `golang.org/x/term`, which
  age v1.3.2 does not pull (tidy is clean without them), and omits the MVS raises of `x/net`, `x/sync`, `x/text`,
  `x/mod`, `x/tools` and `rogpeppe/go-internal` (all BSD-3, recorded in `4f812692`'s message). Carry these into the next
  ADR that touches the envelope (ADR-0205/0206).
- **FEAT-0009 F109 cell not moved to `reviewing`** · attribution: env (campaign orchestration) ·
  `docs/feat/0009-feat-disaster-recovery.md:59` still reads "encryption: accepted". `b4673576` defers the cell to the
  disaster-recovery tier PR, which derives it from each ADR's Status line; ADR-0203's cell ("format: accepted") is in the
  same state. The tier PR must set it, or rule 6 (feat row equals ADR status) stays broken after merge.

### ✅ Verified correct (keep it)

- **Envelope (Decision 1)**: `Seal` is one `age.Encrypt` stream per store file to all recipients; the scenario test
  writes a real generation through ADR-0203's writer, cuts a >8 MiB metastore into 2 parts, concatenates them, opens
  them with `age.Decrypt` and the `age` CLI under each identity, and refuses a third. The checksum covers the sealed
  bytes. Manifests stay plain (read as YAML in `TestScenarioManifestNamesKeys`).
- **Recipients (Decision 2)**: only `*age.X25519Recipient`/`*age.HybridRecipient` pass the type switch; SSH and plugin
  lines are refused (`TestRecipientsRefuseSSHAndPlugin`); ≥2 distinct across files; hybrid beside X25519 refused via an
  `age.Encrypt(io.Discard, …)` dry run; every violation is `fault.Invalid` naming the key and file.
- **`none` rules**: needs empty recipients and a secrets key; `NoSecrets` skips only the secrets-key rule and changes the
  warning text, as the Contract says; the unsealed metastore holds Secret ciphertext, never the value or its base64.
- **Fingerprint (Decision 3)**: labelled SHA-256, 16 hex, `funcd-key-fingerprint-v1 ‖ 0x00`; differs from SHA-256(b)
  and from `hs256Key`; recipients fingerprinted over canonical `String()`; no key, master, SHA-256(master) or their hex
  in any written object or in the start log (walked byte-by-byte in the test).
- **Keys**: `Recipients` sorted and copied out; nil under `none`; `SecretsKey`/`MasterSecret` set either way
  (`TestKeysWhenNone`).
- **Restore rules (Decision 5)**: `ReadIdentities` refuses recipients files, SSH keys and passphrase-encrypted files;
  `Opener` matches identities by recipient fingerprint and names both sets on no match; `CheckSecretsKey` covers all
  four presence combinations and names the matching escrow file or "no escrow file matches it"; `PlanMaster` reads and
  never writes (tree compared before/after), installs from escrow at Decision 7's path, honours a set
  `masterSecretFile`, and lists on `--new-master-secret` or an absent `masterSecret`; `ListChanged` reads Functions
  (`spec.blob`, `spec.catalogs`) and CatalogServices (`spec.blob`) from the loaded store, deterministic order.
- **Master location (Decision 7)**: `s3gateway.MasterPath` is the single location rule shared by `MigrateMaster` and
  `PlanMaster`; cmd/funcd loads the master once and hands it to both `envelope.New` and `funcd.WithMasterSecret`;
  `TestScenarioMasterKeyMigrates` covers copy + 0600 + both paths warned + catalog token unchanged, two different keys
  refused, gateway-on keeps its key, and a symlinked working directory equal to `storage.dataDir`.
- **Wiring**: start check only with `backup.target` set; the fingerprints are logged as `backup keys`; config keys and
  env names match the Contract table; `examples/funcdconfig.yaml` documents them in block style; `examples/backup-escrow.md`
  covers the layout, `age-keygen -pq` and the rotation table.
- **Conventions**: `fault` kinds throughout, slog only, no `panic`, no `any` in exported signatures, imports at top
  level, comments state the why with the ADR reference.
- **Tracking**: the ADR diff is the Status line only (`Accepted → Reviewing`); substance unchanged.

### Definition of Done

7 / 7 items verified here (Review checklist 4: seal/unsealed/no SSH or plugin; each Decision 2 rule a start-time
`fault.Invalid`; labelled fingerprint and no key bytes in logs or objects; restore checks before any part plus Decision 7
and each scenario tested · ADR DoD 3: `go test -race -count=1` green on the touched packages, licences recorded in the
commit, no identity or path leak). `just ci` is left to the gate; its parts (build, vet, lint on darwin and linux,
the touched packages' tests) all pass. The feat-cell propagation is deferred to the tier PR (Minor 4).

### Model scorecard

Not recorded by this gate (the DR session records the ledger rows). Row below.

### Recommendation

Pass. No Blocker or Major. The two model Minors (symlinked escrow entries; daemon-level test polish) can ride a
follow-up; the tier PR must move the F109 encryption cell to `reviewing`/`implemented`, and the Contract drift belongs in
the next envelope-touching ADR.

### Ledger row

```json
{
  "date": "2026-10-10",
  "adr": "0204",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 4,
  "model_attributed": 2,
  "dod_passed": 7,
  "dod_total": 7,
  "report": "docs/reviews/adr-0204-implementation-claude-opus-5-5.md",
  "notes": "pass; build/vet/lint darwin+linux exit 0, touched packages -race green, 9/9 Scenarios incl. the age CLI branch, 3/3 mutants killed; Minor(model): escrow.Find skips symlinked key files/dirs and reports found: none; Minor(model): daemon-level start-rule test asserts message only, env overlay untested; Minor(adr): Contracts/go.mod list drift (KeyPrefix, Logger, WithMasterSecret, MasterPath, seal-mismatch refusal; deps); Minor(env): F109 feat cell deferred to the DR tier PR"
}
```
