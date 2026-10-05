# ADR-0062: Unify config loading into one env-validated struct (caarlos0/env)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0171](0171-static-credential-list.md) (2026-10-05) — Every key FUNCD_*-overridable: auth.credentials has no env var.
- **Date**: 2026-06-20
- **Deciders**: green-0-rabbit
- **Tags**: operability, config, daemon, refactor
- **Judge note (accepted 2026-06-20)**: folded the judge's Blocker — dropped the `validate:"hostname_port"` tags on the
  addresses (they'd have rejected the ephemeral `:0` / wildcard defaults and broken zero-config; ADR-0061 didn't validate
  addresses) — and two Minors: tightened the "behavior-preserving" claim to name the one intended change (the stricter
  env edge), and specified the `FUNCD_IMAGE_OVERRIDE` map separators (`envSeparator:"," envKeyValSeparator:"="`, verified
  against the lib source), deleting the now-dead temporary workaround. Decision unchanged.
- **Realizes**: [FEAT-0001/F31](../feat/0001-feat-v1.1.md) (operator config file) — a follow-up to ADR-0061 on the same feature.
- **Refines**: [ADR-0061](0061-funcd-daemon-config-file.md) — replaces its `internal/config` **Contracts** (the
  `File` + `Resolved` two-struct split and the per-field `resolveStr("FUNCD_*", …)` env handling) with **one struct**
  populated from file + env + default and validated once. It **preserves every ADR-0061 behavior** — the kubeconfig-style
  optional file, precedence `flag > env > file > default`, strict decode, enum validation, the secrets-keyfile encryptor,
  and the config keys — so it reverses no ADR-0061 decision; it changes only the loading mechanism.
- **Relates to**: [ADR-0048](0048-dto-validation-reference.md) (the per-field validation convention this
  follows), [ADR-0022](0022-secrets-service.md) (the encryptor wiring, unchanged), the `go-playground/validator` huma
  already pulls + the `sigs.k8s.io/yaml` ADR-0061 added.

## Context & Need

ADR-0061 shipped `funcdconfig.yaml`, but its loader carries each setting in **three places**: the `File` struct (yaml
decode), a per-field `resolveStr("FUNCD_X", file.X, default)` line in `Resolve` (env + default precedence), and the
`Resolved` struct (the effective value) — plus a hand-rolled `resolveImageOverride` env parser. Two costs:

- **Duplication.** Adding one config key means editing the `File` struct, the `Resolved` struct, *and* a `resolveStr`
  line — three edits for one knob, easy to get out of sync.
- **The env edge.** Validation runs on the `File` (the yaml only), so a bad value injected via an env var
  (`FUNCD_RUNTIME=bogus`) is **never validated** — it silently falls through to the default lane.

This is the classic 12-factor / NestJS-`ConfigModule` pattern done wrong way round. The fix: **one struct** whose field
tags declare its *yaml key + env var + validation rule*, populated from file → env → default in a single `Load`, and
**validated once on the merged result** — so a key is defined in one place and a bad value from *any* source is caught.

## Scenarios

- **scenario: zero-config-defaults** — *Given* no file + no env, *When* Load, *Then* every field is its built-in default
  (control plane `0.0.0.0:8080`, file substrate, process runtime, json/info) — unchanged from ADR-0061.
- **scenario: file-over-default** — *Given* a `funcdconfig.yaml` setting `server.listenAddr`, *When* Load, *Then* the
  file value wins over the default.
- **scenario: env-over-file** — *Given* `storage.dataDir` in the file **and** `FUNCD_DATA_DIR` in the env, *When* Load,
  *Then* the **env** value wins (precedence env > file), and the file's *other* values are **not** clobbered.
- **scenario: flag-over-env** — *Given* `--memory` **and** `storage.mode: file` in the file, *When* Load, *Then* the
  flag wins (`memory`).
- **scenario: env-value-validated (the closed edge)** — *Given* `FUNCD_RUNTIME=bogus` (no file value), *When* Load,
  *Then* it is rejected with a `fault.Invalid` — an env-sourced bad value is validated like a file one.
- **scenario: unknown-key-rejected** — *Given* a typo'd key in the file, *When* Load, *Then* `fault.Invalid` (strict decode).
- **scenario: invalid-enum-rejected** — *Given* `storage.mode: bogus` (from any source), *When* Load, *Then*
  `fault.Invalid` naming the key + the allowed set.
- **scenario: secrets-keyfile-activates-encryption** — *Given* `secrets.encryptionKeyFile` (32-byte key), *When* the
  daemon builds the store, *Then* `Secret` values are ciphertext at rest; absent → unencrypted + a warning (unchanged).

## Scope

**In:** collapsing `internal/config` to **one `Config` struct** (nested by group) with `yaml`/`env`/`validate` tags; a
`Load` that merges file + env + default + the `--memory` flag and validates once; adopting **caarlos0/env** for the env
overlay; making **every** key uniformly `FUNCD_*`-overridable (the file-only keys gain an env too); the
`go-playground/validator` validation moving to the merged struct (closing the env edge); updating `cmd/funcd` to consume
the new surface; **heavy tests** (a precedence matrix across all sources + the env-edge + every enum).

**Out:** any change to the config **keys, defaults, precedence order, or behavior** (all preserved from ADR-0061); the
secrets-encryptor wiring (unchanged); `funcdcli apply` YAML (still a separate follow-up); new config groups (E/F from
ADR-0061 stay deferred). This ADR is a mechanism refactor, not a feature change.

## Constraints & Decision drivers

- **One source of truth per key** — a key is one struct field with its tags; no second struct, no `resolveStr` line.
- **Validate the effective value, not the input** — validation runs after the merge, so file *and* env *and* flag values
  are all checked (the env edge is closed by construction).
- **Behavior-preserving for valid configs** — same keys, same defaults, same `flag > env > file > default`, same strict
  decode. ADR-0061's scenarios still pass. The **one intended change** is stricter: a bad value from an env var (the
  ADR-0061 env edge) now fails validation instead of silently defaulting. Addresses keep ADR-0061's behavior — **not**
  format-validated (a malformed bind fails at listen time), so the ephemeral `:0` / wildcard defaults are untouched.
- **Lean + maintained deps** (blueprint) — reuse `go-playground/validator` (already via huma) + `sigs.k8s.io/yaml`
  (ADR-0061); add only a **zero-dependency, actively-maintained** env lib.

## Alternatives considered

- **caarlos0/env** (chosen) — MIT, **zero third-party deps**, v11 (released 2026-05, actively maintained). Overlays env
  onto an already-decoded struct via `env:"NAME"` tags, leaving unset-env fields untouched — so `yaml.Unmarshal` →
  `env.Parse` → defaults composes to exactly `env > file > default`. Composes with the yaml + validator funcd already has.
- **cleanenv** — a single `ReadConfig` does file+env+default in one call (nice), but the **last release was 2023-07**
  (~3 yr stale) and it has no `validate` tag (still needs the validator). Rejected on maintenance cadence (the blueprint
  rejects libs on that basis — cf. firecracker-containerd).
- **viper** — feature-rich but heavy: a large hardcoded dependency tree that bloats the single binary, and it lowercases
  keys (breaks the yaml spec). Rejected against the embed-first / lean rule.
- **koanf** — lightweight + maintained, but its provider/Unmarshal model (separate file/env/defaults providers, its own
  `koanf` tags) is more wiring than caarlos0/env for one file, and doesn't reuse the validator as directly. Rejected as
  more machinery than needed.
- **Keep ADR-0061's `File`/`Resolved`/`resolveStr`** — works, but is the duplication + open env edge this ADR exists to fix.

## Decision

1. **One `Config` struct**, nested by group (`Server`/`Storage`/`Auth`/`Secrets`/`Runtime`(+`Containerd`)/`Log`/
   `Telemetry`). Each scalar field carries up to three tags: `yaml:"<key>"` (decode), `env:"FUNCD_<NAME>"` (overlay),
   `validate:"<rule>"` (check). The struct **is** the effective config — there is no separate `Resolved`.

2. **`Load` merges in one pass, precedence `flag > env > file > default`:**
   `defaults()` (a struct literal — the single home for built-in defaults) → `yaml.UnmarshalStrict` over it (file
   overrides defaults; unknown key → `fault.Invalid`) → `env.Parse` (caarlos0/env; an env var that is **set** overrides,
   an **unset** one leaves the field untouched — no clobber) → apply the `--memory` flag (→ `storage.mode: memory`) →
   derive the dataDir-relative containerd defaults (`root`, `cniConfDir`) → `Config.Validate()`.

3. **Validate once, on the merged struct.** `(Config).Validate()` runs `go-playground/validator` over the `validate`
   tags and maps the first failure to a `fault.Invalid` naming the yaml key. Because it runs *after* the merge, a bad
   value from the file, a `FUNCD_*` env, **or** the flag is caught — **the ADR-0061 env edge is closed**.

4. **Every key is uniformly `FUNCD_*`-overridable.** The file-only ADR-0061 keys (`listenAddr`, `dataPlaneAddr`, `log.*`,
   `telemetry.*`, `auth.*`, `secrets.encryptionKeyFile`) gain a `FUNCD_*` env; the existing env names are preserved by
   their `env:` tags (`FUNCD_DATA_DIR`, `FUNCD_RUNTIME`, `FUNCD_CONTAINERD_SOCKET`, …). `FUNCD_IMAGE_OVERRIDE` is parsed
   by the lib into the `map[string]string` field (the hand-rolled `resolveImageOverride` is removed).

5. **`cmd/funcd` consumes the new surface:** `config.Load(path, Flags{MemoryOnly})` → a `Config`; `buildOptions(ctx,
   Config)` maps it to `[]funcd.Option` exactly as before (addresses, logger, telemetry, store-encryptor, execution).

## Temporary workarounds

None — `FUNCD_IMAGE_OVERRIDE`'s `rt=ref,rt=ref` format is parsed directly by the lib via
`envSeparator:"," envKeyValSeparator:"="` (verified against the caarlos0/env source), so no custom parser is needed.

## Contracts

### `internal/config` (the new surface — replaces ADR-0061's File/Resolved)

```go
// Config is the funcd daemon config: one struct, populated from file + env + default and validated once
// (ADR-0062). yaml: decode key; env: the FUNCD_* overlay var; validate: the go-playground/validator rule.
type Config struct {
	APIVersion string `yaml:"apiVersion,omitempty" validate:"omitempty,eq=funcd.io/v1alpha1"`
	Kind       string `yaml:"kind,omitempty"       validate:"omitempty,eq=FuncdConfig"`
	Server     struct {
		ListenAddr    string `yaml:"listenAddr"    env:"FUNCD_LISTEN_ADDR"`
		DataPlaneAddr string `yaml:"dataPlaneAddr" env:"FUNCD_DATA_PLANE_ADDR"`
	} `yaml:"server"`
	Storage struct {
		Mode    string `yaml:"mode"    env:"FUNCD_STORAGE_MODE" validate:"oneof=file memory"`
		DataDir string `yaml:"dataDir" env:"FUNCD_DATA_DIR"`
	} `yaml:"storage"`
	Auth struct {
		Token      string   `yaml:"token"      env:"FUNCD_TOKEN"`
		Namespaces []string `yaml:"namespaces" env:"FUNCD_AUTH_NAMESPACES" envSeparator:","`
	} `yaml:"auth"`
	Secrets struct {
		EncryptionKeyFile string `yaml:"encryptionKeyFile" env:"FUNCD_SECRETS_ENCRYPTION_KEY_FILE"`
	} `yaml:"secrets"`
	Runtime struct {
		Mode       string `yaml:"mode" env:"FUNCD_RUNTIME" validate:"oneof=process containerd"`
		Containerd struct {
			Socket        string            `yaml:"socket"        env:"FUNCD_CONTAINERD_SOCKET"`
			Root          string            `yaml:"root"          env:"FUNCD_CONTAINERD_ROOT"`
			Snapshotter   string            `yaml:"snapshotter"   env:"FUNCD_SNAPSHOTTER"`
			CNIBinDir     string            `yaml:"cniBinDir"     env:"FUNCD_CNI_BIN_DIR"`
			CNIConfDir    string            `yaml:"cniConfDir"    env:"FUNCD_CNI_CONF_DIR"`
			SubnetCIDR    string            `yaml:"subnetCIDR"    env:"FUNCD_SUBNET_CIDR"`
			ImagePrefix   string            `yaml:"imagePrefix"   env:"FUNCD_IMAGE_PREFIX"`
			ImageOverride map[string]string `yaml:"imageOverride" env:"FUNCD_IMAGE_OVERRIDE" envSeparator:"," envKeyValSeparator:"="`
		} `yaml:"containerd"`
	} `yaml:"runtime"`
	Log struct {
		Format string `yaml:"format" env:"FUNCD_LOG_FORMAT" validate:"oneof=json text"`
		Level  string `yaml:"level"  env:"FUNCD_LOG_LEVEL"  validate:"oneof=debug info warn error"`
	} `yaml:"log"`
	Telemetry struct {
		Endpoint string `yaml:"endpoint" env:"FUNCD_TELEMETRY_ENDPOINT"`
		Insecure bool   `yaml:"insecure" env:"FUNCD_TELEMETRY_INSECURE"`
	} `yaml:"telemetry"`
}

// Flags are the top precedence tier (CLI flags with no env). MemoryOnly nil ⇒ --memory not set.
type Flags struct{ MemoryOnly *bool }

// Locate finds the config file: explicit (--config; must exist → fault.NotFound) → $FUNCD_CONFIG → first
// existing of ./funcdconfig.yaml, /etc/funcd/funcdconfig.yaml → "" (none, zero-config). (Unchanged from ADR-0061.)
func Locate(explicit string) (path string, err error)

// Load builds the effective Config: defaults() → strict-decode the file at path (""=skip) → overlay env
// (caarlos0/env) → apply flags → derive dataDir-relative defaults → Validate. A read/parse/unknown-key/enum
// error ⇒ fault.Invalid. The returned Config is fully populated + validated.
func Load(path string, flags Flags) (Config, error)

// Validate runs the struct's validate tags (go-playground/validator) over the merged values, returning a
// fault.Invalid naming the offending yaml key + allowed set. Called by Load; exported for direct checks.
func (c Config) Validate() error
```

### Dependencies & I/O

| Consumes | From | Notes |
|---|---|---|
| `funcdconfig.yaml` | `--config`/`$FUNCD_CONFIG`/search | optional; strict decode |
| `FUNCD_*` env vars | process env | overlaid by caarlos0/env onto the decoded struct |
| Exposes: `config.Config` | → `cmd/funcd` | mapped to `[]funcd.Option` (buildOptions) |
| New dep: `github.com/caarlos0/env/v11` | go.mod | MIT, zero third-party deps, actively maintained |

## Implementation plan

- **`internal/config/config.go`** — replace `File`/`Resolved`/`Resolve`/`resolveStr`/`resolveImageOverride` with the
  single `Config` struct (tags above), `defaults()`, `Load`, and `(Config).Validate()` (validator on the merged struct).
  Keep `Locate`. No driver imports (still a leaf).
- **`cmd/funcd/main.go`** — `serve()` calls `config.Locate` + `config.Load`; `buildOptions(ctx, config.Config)` reads
  `cfg.Server.ListenAddr` etc. (mechanical rename from the flat `Resolved`). `executionOptions(ctx, config.Config)` reads
  `cfg.Runtime.*`.
- **`go.mod`** — `go get github.com/caarlos0/env/v11` (pin + record).
- **Tests (non-gated, heavy) — `internal/config`:**
  - a **precedence matrix**: for a representative key, assert default-only, file-over-default, env-over-file, and
    flag-over-env in one table — proving the full `flag > env > file > default` chain;
  - **no-clobber**: a file that sets several keys + one env var → the env key changes, the others keep their file values;
  - **every env var** maps to its field (table over the `FUNCD_*` set incl. the `FUNCD_IMAGE_OVERRIDE` map);
  - **the closed env edge**: `FUNCD_RUNTIME=bogus` ⇒ `fault.Invalid` (and the same for a bad file value);
  - `zero-config-defaults`, `unknown-key-rejected`, `invalid-enum-rejected` (each enum), `partial-file`, `Locate`.
- **Tests — `cmd/funcd`:** `file-sets-addresses` (the daemon binds the config addresses, via `buildOptions`) and
  `secrets-keyfile-activates-encryption` (ciphertext with a key, warn without) — carried over, retargeted to `Config`;
  plus the example `funcdconfig.yaml` still loads (`TestExampleConfigResolves`).
- **Verify green** via the four sub-checks (`go build` · `go tool golangci-lint run` · `go test` · `go mod verify`).

**Definition of done:** one `Config` struct is the only config type; a key is defined once (its three tags); `Load`
yields the same effective config as ADR-0061 for every input (precedence preserved); a bad value from the file, an env
var, or the flag is rejected (env edge closed); `internal/config` imports no drivers; `cmd/funcd` maps `Config` →
options unchanged; the example config loads; all scenario + matrix tests pass; new dep is MIT/zero-dep; `just ci` green.

## Review checklist

- [ ] One `Config` struct is the sole config type (no `Resolved`); each key is one field with `yaml`/`env`/`validate` tags.
- [ ] `Load` precedence is `flag > env > file > default`, proven by the precedence-matrix test; defaults live in one `defaults()`.
- [ ] env overlay does **not** clobber file values whose env var is unset (the no-clobber test).
- [ ] Validation runs on the merged struct: a bad value from file **or** env **or** flag ⇒ `fault.Invalid` (env-edge test).
- [ ] Strict decode (unknown key → `fault.Invalid`); `Locate` non-existent explicit path → `fault.NotFound`.
- [ ] Behavior parity with ADR-0061: zero-config defaults, the bound addresses, the secrets encryptor, the keys/defaults.
- [ ] `internal/config` imports no driver/`pkg/funcd` packages; no `any` in the new surface; no identity/path leak; new dep MIT/zero-dep.

## Consequences

- A config key is **defined once** (one struct field, three tags) instead of in three places — adding/changing keys is local.
- **The env edge is closed**: validation runs on the effective value, so `FUNCD_RUNTIME=bogus` (or any bad env) fails fast
  rather than silently defaulting.
- Every key is uniformly `FUNCD_*`-overridable (the precedence model is now total, not partial) — a small, additive surface gain.
- One new dependency (caarlos0/env, zero-dep MIT); `go-playground/validator` + `sigs.k8s.io/yaml` are unchanged.
- `internal/config`'s exported surface changes (`File`/`Resolved`/`Resolve` → `Config`/`Load`); it is `internal`, consumed
  only by `cmd/funcd`, so the blast radius is one caller. ADR-0061 stays the feature's decision; this refines its mechanism.

## Open questions

- **Address format validation** — deliberately omitted (parity with ADR-0061; a bad bind fails at listen). If operators
  want fail-fast address checks later, a follow-up can add a lenient host:port check that accepts wildcard + ephemeral `:0`.
- **Wider env coverage docs** — the new file-only-key envs (`FUNCD_LISTEN_ADDR`, …) should be reflected in the example
  `funcdconfig.yaml` comments; done at implementation, not a separate ADR.

## References

- [ADR-0061](0061-funcd-daemon-config-file.md) (the config file this refines) · [ADR-0048](0048-dto-validation-reference.md)
  (validation convention) · [ADR-0022](0022-secrets-service.md) (encryptor).
- [caarlos0/env](https://github.com/caarlos0/env) (MIT, zero-dep, v11) · [go-playground/validator](https://github.com/go-playground/validator)
  (already via huma) · [sigs.k8s.io/yaml](https://github.com/kubernetes-sigs/yaml).
- [FEAT-0001/F31](../feat/0001-feat-v1.1.md) · [blueprint.md](../../blueprint.md) (lean, library-first).
