# ADR-0061 Implementation Review — funcd daemon config file (`funcdconfig.yaml`)

**Verdict**: **pass** — the implementation conforms to the ADR's Contracts, all seven Scenarios are named/un-skipped/passing tests, the Review checklist + Definition of Done hold, and the four sub-checks are green. No Blockers, no Majors.
**Reviewed**: ADR-0061 (status `Reviewing`) · FEAT-0001/F31 · blueprint (single-binary, import discipline) · ADR-0002 conventions
**Producing model**: claude-opus-4-8

## Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...` | **exit 0** |
| vet | `go vet ./internal/config/… ./cmd/funcd/…` | **exit 0** |
| lint | `go tool golangci-lint run ./internal/config/… ./cmd/funcd/…` | **0 issues** |
| mod | `go mod verify` | **exit 0** — all modules verified |
| tests | `go test ./internal/config/ ./cmd/funcd/` | **ok** — all scenarios pass |

(The full-suite `internal/bench` `TestBenchSmoke` flaked once on a razor-thin RSS margin — 54.5 vs 54.42 MiB — and **passed on re-run**; it is an `env`/measurement flake, unrelated to this change, which only added the `imageOverrides` env-parser to `bench.go`.)

## Scenario → test traceability (all passing, none skipped)

| ADR Scenario | Test | Asserts |
|---|---|---|
| zero-config-defaults | `TestScenarioZeroConfigDefaults` | empty File + no env ⇒ every field its default |
| file-sets-addresses | `TestScenarioFileSetsAddresses` | a config file's `127.0.0.1:0` drives the bind (`p.Addr()`/`p.DataPlaneAddr()` loopback, ≠ the `0.0.0.0:8080` default) |
| env-overrides-file | `TestScenarioEnvOverridesFile` | `FUNCD_DATA_DIR` wins over `storage.dataDir` |
| partial-file-fills-rest | `TestScenarioPartialFileFillsRest` | only `server:` set ⇒ the rest default |
| secrets-keyfile-activates-encryption | `TestScenarioSecretsKeyfileActivatesEncryption` | 32-byte key ⇒ ciphertext bytes + `store.WithEncryptor`; none ⇒ unencrypted+warn; non-32 ⇒ error |
| unknown-key-rejected | `TestScenarioUnknownKeyRejected` | a typo'd key ⇒ strict-decode `fault.Invalid` |
| invalid-enum-rejected | `TestScenarioInvalidEnumRejected` | bad storage.mode/runtime.mode/log.format/log.level/apiVersion/kind ⇒ `fault.Invalid` |
| (guard) | `TestExampleConfigResolves` | the shipped `examples/funcdconfig.yaml` loads + resolves |

## Review checklist (ADR-0061) — all hold

- ✅ `internal/config` imports **no** driver/`pkg/funcd` packages — `go list -deps` shows only `api/fault` (+ stdlib + `sigs.k8s.io/yaml`). A clean leaf.
- ✅ Precedence is `flag > env > file > default`, per field, with a default for every key — `config.Resolve` + the zero-config test.
- ✅ Strict decode: unknown key → `fault.Invalid` (`yaml.UnmarshalStrict`); bad enum → `fault.Invalid` naming the allowed set.
- ✅ `--config`/`$FUNCD_CONFIG` non-existent path → `fault.NotFound`; implicit search paths may be absent (`config.Locate` + `TestLocate`).
- ✅ `server.listenAddr`/`dataPlaneAddr` from the file change the binds — asserted via `p.Addr()`/`p.DataPlaneAddr()`.
- ✅ `secrets.encryptionKeyFile` (32 bytes) wires `store.WithEncryptor([KindSecret], aesgcm…)`; absent → unencrypted + a `slog.Warn`; a non-32-byte key → `fault.Invalid` (`buildStore`/`secretEncryptor`).
- ✅ The key is read from a **file path** only (never inline YAML); no secret value is logged. No `any` in the new surface.

## ✅ Verified correct — keep it

- **`internal/config` as a true leaf** (verified by `go list -deps`) with `cmd/funcd` doing the option-mapping — exactly the ADR-0002 import-discipline the ADR promised; `pkg/funcd` + the presets are **byte-untouched** (`git diff pkg/funcd` empty). Keep this split.
- **The headline gap is closed and *proven*, not asserted**: `TestScenarioFileSetsAddresses` builds the real daemon options via the extracted `buildOptions`, then reads `p.Addr()` — the config's loopback address overrides Production's `0.0.0.0:8080`. The `buildOptions` extraction is what makes this testable; keep it.
- **The secrets path is honest**: `secretEncryptor` reads the keyfile → `aesgcm.NewAESEncryptor` (32-byte gate), and the test asserts the **value bytes are ciphertext** (`enc.Encrypt(...) != plaintext`) — the judge's Major reframing landed; absence warns rather than silently leaving weak crypto. This finally wires ADR-0022's encryptor into the daemon.
- **Env precedence preserved**: the ~20 `FUNCD_*` vars still override the file (`resolveStr` reads env first); the daemon's behavior with no config file is byte-identical to before. Keep.
- **`buildLogger`/`parseLevel`** map `log.format`/`level` into the existing observability `Config`/`LevelVar`, overriding the preset logger via `WithLogger` — `log.*`/`telemetry.*` are configurable for the first time, mapped to the *real* `TelemetryConfig{Endpoint, Insecure}` (the judge's second Major).

## Findings

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
None blocking. (Observation, not a finding: the new keys without a pre-existing `FUNCD_*` var — `listenAddr`, `dataPlaneAddr`, `log.*`, `telemetry.*`, `auth.namespaces`, `secrets.encryptionKeyFile` — resolve `file > default` with no env tier. This matches the ADR, which mapped only the envs that already exist; a future ADR could add envs for parity if wanted.)

## Recommendation

**pass** → stamp ADR-0061 `Reviewing → Implemented`, advance FEAT-0001/F31 → `implemented`. The operator config file is complete, the headline gap (code-only addresses) is closed and proven, and ADR-0022's at-rest encryptor is finally wired in the daemon. No board item to move (F31 is a feat-doc roadmap item).
