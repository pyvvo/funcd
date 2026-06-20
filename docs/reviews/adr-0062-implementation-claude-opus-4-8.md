# ADR-0062 Implementation Review — Unify config loading into one env-validated struct

**Verdict**: **pass** — the implementation collapses ADR-0061's `File`/`Resolved`/`resolveStr` triplication into one tag-driven `Config` merged from file→env→default and validated once; every scenario is a passing test, the heavy precedence/no-clobber/env-edge tests hold, the Review checklist + DoD are met, and the four sub-checks are green. No Blockers, no Majors.
**Reviewed**: ADR-0062 (status `Reviewing`) · FEAT-0001/F31 · ADR-0061 (refined) · ADR-0002 conventions · caarlos0/env source + license
**Producing model**: claude-opus-4-8

## Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...` | **exit 0** |
| vet | `go vet ./internal/config/… ./cmd/funcd/…` | **exit 0** |
| lint | `go tool golangci-lint run ./internal/config/… ./cmd/funcd/…` | **0 issues** |
| mod | `go mod verify` | **exit 0** — all modules verified |
| tests | `go test ./internal/config/ ./cmd/funcd/` | **ok** — all scenarios + heavy tests pass |
| full | `go test ./...` | no FAIL/panic |
| leaf | `go list -deps ./internal/config/` | only `api/fault` (+ stdlib + the 3 config libs) — no drivers/`pkg/funcd` |

## Scenario → test traceability (all passing, none skipped)

| ADR Scenario | Test |
|---|---|
| zero-config-defaults | `TestScenarioZeroConfigDefaults` |
| file-over-default / env-over-file / flag-over-env | `TestPrecedence` (the precedence matrix) + `TestEnvDoesNotClobberFile` |
| env-value-validated (closed edge) | `TestScenarioEnvValueValidated` (+ live: `FUNCD_RUNTIME=bogus` → exit 1) |
| unknown-key-rejected | `TestScenarioUnknownKeyRejected` |
| invalid-enum-rejected | `TestScenarioInvalidEnumRejected` (each enum + apiVersion/kind) |
| secrets-keyfile-activates-encryption | `TestScenarioSecretsKeyfileActivatesEncryption` |
| (parity) addresses bind | `TestScenarioFileSetsAddresses` (real daemon via `buildOptions`) |
| (heavy) every env var + map + list | `TestEnvVarsMapToFields`, `TestImageOverrideEnvParsed`, `TestNamespacesEnvList` |

## Live smoke (the heavy ask)

- **env > file**: `FUNCD_LISTEN_ADDR=127.0.0.1:9999 funcd --config examples/funcdconfig.yaml` → `platform starting addr=127.0.0.1:9999 dataPlaneAddr=127.0.0.1:8081` — the env overrode the file's `:8080`, and the file's data-plane addr was **not** clobbered.
- **closed env edge**: `FUNCD_RUNTIME=bogus funcd …` → **exit 1**, `config key "runtime.mode" has invalid value "bogus" (want oneof=process containerd)`. The ADR-0061 silent-default edge is closed on the real binary.

## Review checklist (ADR-0062) — all hold

- ✅ One `Config` struct is the sole config type (no `Resolved`); each key is one field with `json`(=yaml key)/`env`/`validate` tags.
- ✅ `Load` precedence `flag > env > file > default`, proven by `TestPrecedence`; defaults live in one `defaults()`.
- ✅ env overlay does **not** clobber file values whose env var is unset (`TestEnvDoesNotClobberFile`; the caarlos0/env `set()`-only-when-non-empty property, verified against source).
- ✅ Validation runs on the merged struct: a bad value from file **or** env **or** flag → `fault.Invalid` (`TestScenarioEnvValueValidated` + live).
- ✅ Strict decode (`yaml.UnmarshalStrict` → unknown key → `fault.Invalid`); `Locate` non-existent explicit path → `fault.NotFound`.
- ✅ Behavior parity with ADR-0061: zero-config defaults identical, the bound addresses, the secrets encryptor (ciphertext with a key, warn without).
- ✅ `internal/config` is a leaf; no `any` in the new surface; new dep `caarlos0/env` is MIT + zero third-party deps; no identity/path leak.

## ✅ Verified correct — keep it

- **The merge pipeline is exactly as the ADR specifies** (`defaults()` → `yaml.UnmarshalStrict` → `env.Parse` → flag → derive dataDir-relative → `Validate`), and the load-bearing **no-clobber** property is both source-verified and test-proven. This is the crux; keep the order.
- **The env edge is genuinely closed** — validating the *merged* struct (not the file) means a bad `FUNCD_*` value fails fast, demonstrated live. A real correctness win over ADR-0061.
- **Dep discipline**: `caarlos0/env` (MIT, zero-dep, v11 2026-05) reuses the `go-playground/validator` huma already pulls + the `sigs.k8s.io/yaml` from ADR-0061 — one tiny addition, not a config framework. The `json`-tag-as-yaml-key choice (sigs.k8s.io/yaml) is the documented, ADR-0061-consistent reason the Contracts' `yaml:` notation maps to `json:` tags in code — a deliberate, correct deviation, not a defect.
- **Heavy tests beyond the scenarios** — the every-env table, the image-override map, the namespaces list, and the no-clobber test give this behavior-parity refactor the coverage it needs to be trusted. Keep them.

## Findings

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
None blocking. (Observation: the Contracts §'s `yaml:"…"` tag notation is realized as `json:"…"` in code — sigs.k8s.io/yaml reuses json tags, as ADR-0061 established; the package doc comment states this, so it's documented, not a mismatch.)

## Recommendation

**pass** → stamp ADR-0062 `Reviewing → Implemented`. The realizing feat row F31 is already `implemented` (ADR-0061) and stays so — a refining follow-up ADR doesn't walk a feature status back; the row now links both ADRs. No board item to move.
