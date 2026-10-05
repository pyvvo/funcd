# ADR-0061: funcd daemon config file — `funcdconfig.yaml` (kubeconfig-style operator config)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0163](0163-retry-times-in-config.md) (2026-10-05) — Scope Out scaling/activator intervals: adds activationTimeout, reclaimInterval keys.
- **Superseded in part by**: [ADR-0171](0171-static-credential-list.md) (2026-10-05) — WithDevAuth(Token, Namespaces...) applies only while auth.credentials is absent.
- **Date**: 2026-06-19
- **Deciders**: green-0-rabbit
- **Tags**: operability, config, daemon, packaging, cli
- **Judge note (accepted 2026-06-19)**: folded the judge's two Majors — reframed the at-rest claim (the default store is
  in-memory/ephemeral; the encryptor protects the durable slatedb-`-tags` lane, not a live on-disk leak) and the test
  assertion (stored-value bytes are ciphertext, no disk); aligned the telemetry keys to the real
  `observability.TelemetryConfig{Endpoint, Insecure}` (dropped the non-existent `sampleRatio`) — plus the two Minors
  (definitive `sigs.k8s.io/yaml` MIT+BSD-3 license; `storage.mode` scoped to the blob+bus substrate). Decision unchanged.
- **Realizes**: [FEAT-0001/F31](../feat/0001-feat-v1.1.md) (operator config file) — v1.1. *Scope note:* v1.1's primary
  theme is I/O contracts (F29/F30); F31 is a deliberate **operability** increment the decider scoped in while making the
  examples runnable (an example needs a `funcdconfig.yaml` to start the daemon). It refines no contract decision.
- **Relates to**: [ADR-0042](0042-cobra-cli-framework.md) (the cobra CLI this adds `--config` to), [ADR-0028](0028-platform-control-plane-wiring.md)
  (the dev token surfaced as `auth.token`), [ADR-0022](0022-secrets-service.md) (the at-rest
  encryptor this finally **wires into the daemon** via `secrets.encryptionKeyFile`), [ADR-0043](0043-single-binary-substrate-selection.md)
  (the `file`/`memory` substrate, today the `--memory` flag), [ADR-0033](0033-data-plane-serving-and-trigger-wake.md) (the data-plane
  address), [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md)/[ADR-0056](0056-temporary-runtime-self-provisioning.md)
  (the runtime/containerd lane settings), and the `pkg/funcd` Option + preset surface this resolves into.

## Context & Need

The `funcd` daemon is configured almost entirely by **~20 `FUNCD_*` environment variables** plus one `--memory` flag and
the hardcoded `Production()`/`Development()` presets in `pkg/funcd/presets.go`. Two consequences:

- **Some settings can't be set at all without recompiling.** The control-plane **`listenAddr`** and **`dataPlaneAddr`**
  are only reachable through `WithListenAddr`/`WithDataPlaneAddr` in code; `Production()` pins `0.0.0.0:8080` and an
  ephemeral `127.0.0.1:0`. An operator can't change the port. (The headline gap.)
- **No single, declarative, reviewable config.** Env-var sprawl is hard to version, diff, or hand to a teammate. funcd
  ships "like k3s" — k3s has a config file; funcd has none.

This ADR adds a **kubeconfig-style daemon config file, `funcdconfig.yaml`**: one declarative file an operator points the
daemon at. It is **optional** (every key has a built-in default → zero-config startup is unchanged) and slots into a
clear precedence chain below the existing env vars. It is **daemon-only** — distinct from the per-function CRD
(`function.yaml`) and the `funcdcli` client config (`FUNCD_SERVER`/`FUNCD_TOKEN`). It also **activates ADR-0022's at-rest
secrets encryptor**, which `serve()` never wires today (`store.New(memory.New())` has no `WithEncryptor`). The default
store is the **in-memory** engine (slatedb is a `-tags` lane), so secrets aren't persisted — this is **not** a live
on-disk leak; rather, `secrets.encryptionKeyFile` makes at-rest encryption correct and ready for the **durable store
lane**.

## Scenarios

- **scenario: zero-config-defaults** — *Given* no config file and no `FUNCD_*` env, *When* `funcd` starts, *Then* every
  setting takes its built-in default (control plane on `0.0.0.0:8080`, file substrate under `/var/lib/funcd`, process
  runtime, json logs at info) — identical to today's preset behavior.
- **scenario: file-sets-addresses** — *Given* a `funcdconfig.yaml` setting `server.listenAddr` + `server.dataPlaneAddr`,
  *When* `funcd --config funcdconfig.yaml` starts, *Then* the control plane and data plane bind those addresses (the
  previously code-only gap is closed).
- **scenario: env-overrides-file** — *Given* `storage.dataDir` set in the file **and** `FUNCD_DATA_DIR` set in the env,
  *When* `funcd` starts, *Then* the **env** value wins (precedence: env > file).
- **scenario: partial-file-fills-rest** — *Given* a file that sets only `server:`, *When* `funcd` starts, *Then*
  `server` comes from the file and every other group takes its default (partial files are valid).
- **scenario: secrets-keyfile-activates-encryption** — *Given* `secrets.encryptionKeyFile` pointing at a 32-byte key,
  *When* `funcd` starts, *Then* the store encrypts `Secret` values at rest (ADR-0022); *and Given* no
  `encryptionKeyFile`, *Then* secrets are stored unencrypted **with a startup warning** (never silently weak).
- **scenario: unknown-key-rejected** — *Given* a `funcdconfig.yaml` with a misspelled/unknown key (e.g. `server.listen`),
  *When* `funcd` starts, *Then* it **refuses to start** with a `fault.Invalid` naming the bad key (strict decode), not a
  silently-ignored typo.
- **scenario: invalid-enum-rejected** — *Given* `storage.mode: bogus`, *When* `funcd` starts, *Then* `fault.Invalid`
  naming the bad value + the allowed set (`file`|`memory`).

## Scope

**In:** a typed `funcdconfig.yaml` (groups **server, storage, auth, secrets, runtime, log, telemetry**); locating it
(`--config` flag → `FUNCD_CONFIG` → `./funcdconfig.yaml` → `/etc/funcd/funcdconfig.yaml`); a precedence resolver
(`flag > env > file > default`) with a built-in default for every key; strict decode (unknown key → error); enum
validation; wiring the resolved config into the `pkg/funcd` Option surface from `cmd/funcd/serve()`; **activating
ADR-0022's store encryptor** when `secrets.encryptionKeyFile` is set; an example `funcdconfig.yaml` under `examples/`.

**Out:** the process-lane **shim/interpreter paths** (`FUNCD_SHIM`/`POOL_MANIFEST`/`NODE`/`PYTHON`) — build-time/embedded
config (the shim is embedded, ADR-0054; production uses the containerd lane), **not** operator config. **Scaling/activator
intervals** (`activationTimeout`/`pollInterval`/`reclaimInterval`) and **`pooling.limit`** — additive in a later ADR.
`funcd bench` flags. The per-function CRD (`function.yaml`) and the `funcdcli` **client** config. A live-reload/watch on
the file (start-time read only). Converting `funcdcli apply` to accept YAML (funcd has no YAML decoder today; this ADR
adds one for the daemon, but reusing it in `apply` is a separate change).

## Constraints & Decision drivers

- **Optional + defaulted** — the file is never required; a missing/partial file yields the same defaults funcd ships
  with today. Zero-config behavior must not change.
- **Env stays authoritative over the file** — the ~20 `FUNCD_*` vars keep working and **override** the file (ops muscle
  memory + container-env deploys must not break).
- **No driver imports in the config package** — `internal/config` parses/resolves/validates into a flat struct and
  imports **no** drivers (ADR-0002 import discipline: a leaf). `cmd/funcd` maps the result → `[]funcd.Option`, preserving
  `Production()`/`Development()` and the whole Option API untouched.
- **Secrets are never inline** — the at-rest key is referenced by **file path**, never written into `funcdconfig.yaml`
  (a config file gets committed/shared; an AES key must not). Absent → a loud warning, never silent weak crypto.
- **Strict, not lenient** — an unknown key is an operator typo that would silently lose a setting; reject it.
- **library-first, Apache-2.0/MIT** — reuse a maintained YAML decoder, don't hand-roll one.

## Alternatives considered

- **Keep env-vars only (status quo)** — zero new code, but leaves `listenAddr`/`dataPlaneAddr` unreachable without a
  recompile and gives no declarative, reviewable config. Rejected — it is the problem.
- **YAML decoder: `sigs.k8s.io/yaml`** (chosen) vs **`go.yaml.in/yaml/v3`** directly. `sigs.k8s.io/yaml` converts
  YAML→JSON then `json.Unmarshal`, so it **reuses the repo's existing `json` struct tags** (the codebase tags everything
  for JSON; `DecodeManifest` is already `json.Unmarshal`) and accepts JSON input too — consistent with the JSON-manifest
  story and the kubeconfig analogy. `yaml/v3` would need a second set of `yaml:` tags on every field. Rejected on tag
  duplication. (`sigs.k8s.io/yaml`: MIT + BSD-3-Clause — both Apache-2.0/MIT-compatible; its `UnmarshalStrict` gives the
  strict, unknown-key-rejecting decode.)
- **A versioned `apiVersion`/`kind` envelope** (chosen, lenient) vs **a bare config struct**. The CRDs use
  `apiVersion: funcd.io/v1alpha1` + `kind`; mirroring it (`kind: FuncdConfig`) buys forward-compat and familiarity. Made
  **optional** (validated only if present) so a minimal file needn't carry boilerplate.
- **Inline the secrets key in the yaml** — simplest, but a config file is shared/committed; an AES key must not be.
  Rejected for a keyfile reference.
- **Lenient decode (ignore unknown keys)** — a misspelled key silently loses its setting (a config file's worst failure
  mode). Rejected for strict decode.

## Decision

1. **`funcdconfig.yaml`, decoded via `sigs.k8s.io/yaml` (strict).** A typed `config.File` struct with the repo's `json`
   tags; `UnmarshalStrict` so an unknown key is a `fault.Invalid`. An optional `apiVersion: funcd.io/v1alpha1` /
   `kind: FuncdConfig` envelope (validated only when present).

2. **Location (kubeconfig-style):** `--config <path>` (a new `funcd` persistent flag) → else `$FUNCD_CONFIG` → else the
   first existing of `./funcdconfig.yaml`, `/etc/funcd/funcdconfig.yaml` → else **none** (all defaults). An explicit
   `--config`/`$FUNCD_CONFIG` path that doesn't exist is a `fault.NotFound` (a typo'd path must not silently fall through
   to defaults); the implicit search paths may be absent.

3. **Precedence, per field: `flag > env (FUNCD_*) > file > built-in default`.** Every key resolves independently, so a
   partial file is valid. The only daemon flag mapping to a config value is `--memory` (→ `storage.mode: memory`); the
   rest resolve `env > file > default`. The existing `FUNCD_*` vars are unchanged and win over the file.

4. **`internal/config` is a leaf:** it `Locate`s + `Load`s + `Resolve`s into a flat `config.Resolved` (every field a
   concrete value, precedence + defaults applied, enums validated). It imports no drivers. `cmd/funcd/serve()` maps
   `Resolved` → `[]funcd.Option` (substrate, store + encryptor, dev-auth, listen/data-plane addrs, logger, execution),
   leaving `pkg/funcd` and the presets untouched.

5. **`secrets.encryptionKeyFile` activates ADR-0022.** When set, `cmd/funcd` reads the file, builds
   `aesgcm.NewAESEncryptor(key)` (exactly 32 bytes — else `fault.Invalid`), and wires
   `store.WithEncryptor([]v1.Kind{KindSecret}, enc)`. Absent → no encryptor (today's behavior) **plus a `slog.Warn`**
   that `Secret` values are unencrypted in the durable-store lane. The default in-memory store is ephemeral (not
   persisted), so this matters when the store is durable (slatedb, `-tags`); wiring the key now makes at-rest encryption
   correct-and-ready. This ADR is what finally wires ADR-0022's encryptor into the daemon.

6. **`log` + `telemetry` become configurable.** `cmd/funcd` builds the logger from `log.format`/`log.level` (the
   observability `LevelVar` already supports it) and passes it via `WithLogger`, overriding the preset's logger; OTel is
   configured from `telemetry.endpoint`/`insecure`, mapped to the existing `observability.TelemetryConfig{Endpoint,
   Insecure}` (empty endpoint = disabled, the default).

## Temporary workarounds

- **Start-time read, no live reload.** The file is read once at startup; changing it needs a restart. **Exit:** a future
  ADR adds a watch/reload if operators need it (crash-only restart is cheap for V1).
- **`funcdcli apply` stays JSON-only.** This ADR adds a YAML decoder for the *daemon* but does not (yet) make `apply`
  accept YAML. **Exit:** a follow-up can reuse `sigs.k8s.io/yaml` in `sdk.DecodeManifest` so manifests may be YAML too.

## Contracts

### `funcdconfig.yaml` (the document)

```yaml
apiVersion: funcd.io/v1alpha1      # optional (validated if present)
kind: FuncdConfig                  # optional
server:
  listenAddr: "0.0.0.0:8080"       # control-plane API bind (default 0.0.0.0:8080)
  dataPlaneAddr: "127.0.0.1:0"     # data-plane invoke bind (default 127.0.0.1:0, ephemeral)
storage:
  mode: file                       # file | memory — the blob+bus substrate (ADR-0043); default file. (The metastore
                                   #   store is the in-memory / slatedb-`-tags` lane, selected separately at build.)
  dataDir: /var/lib/funcd          # default /var/lib/funcd
auth:
  token: ""                        # control-plane dev credential (default: built-in dev token, warned)
  namespaces: [default]            # dev-token namespaces (default [default])
secrets:
  encryptionKeyFile: ""            # path to a 32-byte AES-256 key; empty ⇒ secrets UNENCRYPTED at rest (warned)
runtime:
  mode: process                    # process | containerd (default process)
  containerd:                      # used only when mode: containerd
    socket: ""                     # external containerd socket; empty ⇒ private managed (ADR-0054)
    root: ""                       # default <dataDir>/containerd
    snapshotter: overlayfs
    cniBinDir: /opt/cni/bin
    cniConfDir: ""                 # default <dataDir>/cni
    subnetCIDR: 10.63.0.0/16
    imagePrefix: funcd/runtime-
    imageOverride: {}              # map[runtimeFamily]imageRef
log:
  format: json                     # json | text (default json)
  level: info                      # debug | info | warn | error (default info)
telemetry:
  endpoint: ""                     # OTLP gRPC endpoint; empty ⇒ telemetry disabled (the default)
  insecure: false                  # plaintext OTLP (no TLS) — for a local collector
```

### `internal/config` (the resolver — imports no drivers)

```go
// File is the decoded funcdconfig.yaml (zero value ⇒ nothing set; all defaults apply). json tags; decoded with
// sigs.k8s.io/yaml UnmarshalStrict (unknown key ⇒ error).
type File struct {
	APIVersion string    `json:"apiVersion,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	Server     Server    `json:"server,omitempty"`
	Storage    Storage   `json:"storage,omitempty"`
	Auth       Auth      `json:"auth,omitempty"`
	Secrets    Secrets   `json:"secrets,omitempty"`
	Runtime    Runtime   `json:"runtime,omitempty"`
	Log        Log       `json:"log,omitempty"`
	Telemetry  Telemetry `json:"telemetry,omitempty"`
}

type Server struct {
	ListenAddr    string `json:"listenAddr,omitempty"`
	DataPlaneAddr string `json:"dataPlaneAddr,omitempty"`
}
type Storage struct {
	Mode    string `json:"mode,omitempty"`
	DataDir string `json:"dataDir,omitempty"`
}
type Auth struct {
	Token      string   `json:"token,omitempty"`
	Namespaces []string `json:"namespaces,omitempty"`
}
type Secrets struct {
	EncryptionKeyFile string `json:"encryptionKeyFile,omitempty"`
}
type Runtime struct {
	Mode       string     `json:"mode,omitempty"`
	Containerd Containerd `json:"containerd,omitempty"`
}
type Containerd struct {
	Socket        string            `json:"socket,omitempty"`
	Root          string            `json:"root,omitempty"`
	Snapshotter   string            `json:"snapshotter,omitempty"`
	CNIBinDir     string            `json:"cniBinDir,omitempty"`
	CNIConfDir    string            `json:"cniConfDir,omitempty"`
	SubnetCIDR    string            `json:"subnetCIDR,omitempty"`
	ImagePrefix   string            `json:"imagePrefix,omitempty"`
	ImageOverride map[string]string `json:"imageOverride,omitempty"`
}
type Log struct {
	Format string `json:"format,omitempty"`
	Level  string `json:"level,omitempty"`
}
type Telemetry struct {
	Endpoint string `json:"endpoint,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
}

// Flags are the CLI-flag overrides (the top precedence tier). A nil pointer ⇒ "flag not set".
type Flags struct {
	MemoryOnly *bool // --memory; non-nil ⇒ overrides storage.mode
}

// Resolved is the effective config: precedence applied (flag > env > file > default), every field a concrete value,
// enums validated. cmd/funcd consumes it; it carries no driver types.
type Resolved struct {
	ListenAddr, DataPlaneAddr string
	StorageMode, DataDir      string   // StorageMode ∈ {file, memory}
	Token                     string
	Namespaces                []string
	SecretsEncryptionKeyFile  string   // "" ⇒ no at-rest encryption (warned)
	RuntimeMode               string   // {process, containerd}
	Containerd                Containerd
	LogFormat, LogLevel       string   // {json,text}, {debug,info,warn,error}
	TelemetryEndpoint         string
	TelemetryInsecure         bool
}

// Locate returns the config path to load: explicit (non-empty; must exist → else fault.NotFound) → $FUNCD_CONFIG →
// first existing of ./funcdconfig.yaml, /etc/funcd/funcdconfig.yaml → "" (none; zero-config).
func Locate(explicit string) (path string, err error)

// Load strict-decodes the file at path ("" ⇒ a zero File). Parse error / unknown key ⇒ fault.Invalid.
func Load(path string) (File, error)

// Resolve applies precedence + defaults + enum validation, reading FUNCD_* internally. A bad enum/value (or a present
// apiVersion/kind that isn't funcd.io/v1alpha1 / FuncdConfig) ⇒ fault.Invalid naming the key + allowed set.
func Resolve(file File, flags Flags) (Resolved, error)
```

### `cmd/funcd` (the wiring — consumes `Resolved`)

```
funcd [--config <path>] [--memory]      # --config: new persistent flag (env: FUNCD_CONFIG)
```
`serve()` calls `config.Locate` → `Load` → `Resolve`, then maps `Resolved` → `[]funcd.Option`: substrate from
`StorageMode`/`DataDir`; `WithStore(store.New(memory.New(), store.WithEncryptor(...)?))` (encryptor iff a 32-byte
`SecretsEncryptionKeyFile`); `WithDevAuth(Token, Namespaces...)`; `WithListenAddr`/`WithDataPlaneAddr`; `WithLogger`
(format+level); execution from `RuntimeMode` + `Containerd`; telemetry from the endpoint.

### Dependencies & I/O

| Consumes | From | Notes |
|---|---|---|
| `funcdconfig.yaml` | `--config`/`$FUNCD_CONFIG`/search paths | optional; strict YAML/JSON decode |
| `FUNCD_*` env vars | process env | override the file (precedence) |
| the 32-byte key | `secrets.encryptionKeyFile` | read by `cmd/funcd`; → `aesgcm.NewAESEncryptor` |
| Exposes: `config.Resolved` | → `cmd/funcd/serve()` | mapped to `[]funcd.Option` |
| New dep: `sigs.k8s.io/yaml` | go.mod | MIT + BSD-3-Clause (Apache-2.0/MIT-compatible) |

## Implementation plan

- **`internal/config/config.go`** — the structs above + `Locate`, `Load` (sigs.k8s.io/yaml `UnmarshalStrict`), `Resolve`
  (precedence via an `envOr`-style helper per field + default table + enum validation). No driver imports.
- **`cmd/funcd/main.go`** — add the `--config` persistent flag; `serve()` reads `config.Locate/Load/Resolve` and maps
  `Resolved` → options (replacing the ad-hoc `envOr` calls + `--memory` plumbing with the resolver); wire
  `store.WithEncryptor` from the keyfile (+ the unencrypted-warning); build the logger from `log.*`.
- **`go.mod`** — `go get sigs.k8s.io/yaml` (pin + record the version).
- **`examples/funcdconfig.yaml`** — a zero-infra example (`storage.mode: memory`, `runtime.mode: process`,
  `server.listenAddr`/`dataPlaneAddr` on localhost) referenced by the example READMEs.
- **Tests (non-gated, `just ci`)** — one per Scenario, in `internal/config` (table-driven over `File`+env+flags →
  `Resolved`/error) for `zero-config-defaults`, `env-overrides-file`, `partial-file-fills-rest`, `unknown-key-rejected`,
  `invalid-enum-rejected`; and in `cmd/funcd` (the daemon assembled with a temp `funcdconfig.yaml`) for
  `file-sets-addresses` (assert the bound addresses) and `secrets-keyfile-activates-encryption` (with a key, a stored
  `Secret`'s value **bytes are ciphertext** — read back through the store engine; without a key, plaintext + a warning).
- **Verify green** via the four sub-checks (`go build` · `go tool golangci-lint run` · `go test` · `go mod verify`).

**Definition of done:** `funcd` starts with no file (all defaults) and with a `funcdconfig.yaml` that sets each group;
env overrides the file; an unknown key / bad enum is rejected with a clear `fault.Invalid`; `secrets.encryptionKeyFile`
encrypts `Secret` values at rest (and its absence warns); `internal/config` imports no drivers; `pkg/funcd`/presets
unchanged; the example `funcdconfig.yaml` exists; all scenario tests pass; new dep is Apache-2.0/MIT-compatible;
`just ci` green.

## Review checklist

- [ ] `internal/config` imports no driver/`pkg/funcd` packages (a leaf; verified by the import graph).
- [ ] Precedence is `flag > env > file > default`, per field, with a default for every key (zero-config = today's behavior).
- [ ] Strict decode: an unknown key → `fault.Invalid`; a bad enum → `fault.Invalid` naming the allowed set.
- [ ] `--config`/`$FUNCD_CONFIG` non-existent path → `fault.NotFound`; implicit search paths may be absent.
- [ ] `server.listenAddr`/`dataPlaneAddr` from the file actually change the binds (the headline gap closed — asserted).
- [ ] `secrets.encryptionKeyFile` (32 bytes) wires `store.WithEncryptor([KindSecret], aesgcm…)`; absent → unencrypted + a `slog.Warn`; a non-32-byte key → `fault.Invalid`.
- [ ] The at-rest key is never read from inline YAML (keyfile path only); no secret value logged.
- [ ] `pkg/funcd` Option API + `Production()`/`Development()` presets unchanged; no `any` in the new surface; no identity/path leak.

## Consequences

- An operator configures funcd with **one declarative file**, including the previously unreachable control-plane and
  data-plane addresses. The `FUNCD_*` vars keep working and override it.
- **ADR-0022's at-rest encryptor is finally wired in the daemon** (it was built but never wired), gated on an explicit
  keyfile. It protects `Secret` values in the **durable store lane** (slatedb, `-tags`); the default in-memory store is
  ephemeral, so the warned-unencrypted default is honest (not a live on-disk leak), not silently weak.
- funcd gains a **YAML decoder** (`sigs.k8s.io/yaml`) — the substrate to later let `funcdcli apply` accept YAML too
  (noted as a workaround exit, not done here).
- The examples become **runnable end-to-end** (the missing `funcdconfig.yaml` the operator pairs with `function.yaml`).
- v1.1 gains an operability feature beyond its contract theme — a deliberate, decider-scoped extension recorded in the
  feat row, not silent creep.

## Open questions

- **Live reload / `funcd config` introspection verb** — deferred; answered by a follow-up ADR if operators need
  hot-reload or a "print effective config" command.
- **YAML in `funcdcli apply`** — the decoder this adds could back YAML manifests; decided in a follow-up (a workaround
  exit), not here.
- **Key rotation / external KMS for the secrets key** — V2 (the external secret drivers ADR-0022 deferred); here the key
  is a local 32-byte file.

## References

- [ADR-0042](0042-cobra-cli-framework.md) (cobra CLI) · [ADR-0028](0028-platform-control-plane-wiring.md) (dev token) ·
  [ADR-0022](0022-secrets-service.md) (at-rest encryptor) · [ADR-0043](0043-single-binary-substrate-selection.md)
  (substrate) · [ADR-0033](0033-data-plane-serving-and-trigger-wake.md) (data plane) · [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md)/[ADR-0056](0056-temporary-runtime-self-provisioning.md) (runtime/install).
- [sigs.k8s.io/yaml](https://github.com/kubernetes-sigs/yaml) (the proposed YAML decoder; reuses json tags).
- [FEAT-0001/F31](../feat/0001-feat-v1.1.md) · [blueprint.md](../../blueprint.md) (single-binary, library-first).
