# ADR-0262: The Lake engine runs DuckDB 2.0, with its catalogs kept at DuckLake 1.0

- **Status**: Proposed; judge round 1 said "accept after fixes", its Blockers settled by new checks in experiment 0006.
- **Date**: 2026-10-22
- **Deciders**: green-0-rabbit
- **Tags**: lake, duckdb, ducklake, quack, dev, dependency, breaking-change
- **Realizes**: [FEAT-0003/F181](../feat/0003-feat-data-platform.md)
- **Supersedes (in part)**: [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md), only its DuckDB pin and its
  sandbox's mechanism (a list of disabled filesystems); its "Quack is beta" workaround becomes the version-coupled wire
  workaround below ·
  [ADR-0260](0260-a-loopback-listener-owns-its-port.md), only the Lake engine half of Decision 3 (the held port), its
  scenario `catalog-engine-holds-its-port` (lines 60-62), the Lake driver's port picker and test seam (130-135) and the
  checklist line 164-165. The rest of both ADRs stands.
- **Relates to**: [ADR-0261](0261-rename-catalogservice-to-lake.md) (the names) · [ADR-0087](0087-add-on-provider-runtime.md)
  (readiness) · [ADR-0137](0137-per-caller-catalog-query-rbac.md), [ADR-0138](0138-external-catalog-ingress-and-route-aggregation.md)
  (the PEP proxy, the edge) · [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (language-repo releases) ·
  [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md), [ADR-0207](0207-pre-upgrade-snapshot-and-safe-mode.md) (restores, pre-upgrade generations)

## Context & Need

A Lake (ADR-0086, named by ADR-0261) is a DuckDB engine with the ducklake, quack, httpfs and sqlite extensions. It runs
in the curated `duckdb` image (the Python wheel, driven by `images/runtime/duckdb/shim.py`) and in `funcdctl dev` (the CLI,
driven by `internal/lake/devengine`).

Consumers are functions that bundle their own duckdb and reach a Lake over Quack. They go through the PEP proxy, which
rewrites the token in their handshake (ADR-0137). All of these pin DuckDB 1.5.6.

DuckDB 2.0.0 shipped on 2026-10-21 as a long-term-support release, supported until at least October 2027, together with
DuckLake 1.1. Experiment 0006 ran 50 checks of what funcd relies on, and 47 pass. Three things do not carry over: the
Quack wire fails between 1.5.6 and 2.0.0 in both directions; 2.0.0 creates a new catalog at DuckLake 1.1 unless told
otherwise; and a catalog migrated to 1.1 no longer opens in 1.5.6.

This ADR decides how the engine, the dev engine and the consumers move, and which version the catalogs keep.

## Scenarios

- `scenario: lake-on-duckdb-2`: Given a Lake deployed from the 2.0.0 image, and a consumer bound to it whose bundle
  holds duckdb 2.0.0, When the consumer runs `quack_query` through the PEP proxy, Then it gets the rows.
- `scenario: new-lake-is-1-0`: Given no catalog, When the 2.0.0 engine attaches one and commits, Then the catalog's
  DuckLake version is 1.0, and a 1.5.6 engine opens it.
- `scenario: catalog-stays-1-0`: Given a catalog that a 1.5.6 engine wrote, When the 2.0.0 engine attaches it and
  commits, Then its version is still 1.0.
- `scenario: dev-engine-port-zero`: Given `funcdctl dev` starting a Lake engine, Then the engine's init asks for port 0,
  and the start reports the port that the engine's `listen_url` row names.
- `scenario: dev-engine-early-exit`: Given an engine that ends before its row (an error in its init), Then the start
  fails at once with the engine's error output.
- `scenario: agent-environment-same-output`: Given `funcdctl dev` runs with an AI agent's environment variable set,
  Then the dev engine's row and its error output are rendered as they are without that variable.
- `scenario: no-deprecated-extension-setting`: Given the 2.0.0 engine in the image and in dev, Then neither prints the
  `extension_directory` deprecation warning.
- `scenario: arbitrary-url-confined` (ADR-0086's, on 2.0.0): Given a Lake on the 2.0.0 image, When a query reads any
  path other than an `s3://` path or the engine's own directory, Then the query fails.

## Scope

**In**: the DuckDB pins of the image and the dev engine; both engines' ATTACH; the shim's sandbox; the extension setting; the dev engine's
port, row and environment; the PEP proxy's handshake fixture (`internal/lake/gateway`); the funcd-python consumer
examples, released in step; the duckdb lane's new steps; the release notes.

**Out**: migrating a catalog to DuckLake 1.1 and the features that need it; consumers on `ATTACH 'quack:…'`; the SQL dev
engine (ADR-0260's other half); DuckDB 2.1, which drops macOS on amd64.

## Constraints & Decision drivers

- One DuckDB version serves a Lake and its consumers: the Quack wire is not compatible across 1.5 and 2.0.
- A rollback of funcd, a restore into an older funcd (ADR-0222), a pre-upgrade generation (ADR-0207) and the daemon's
  own catalog reader for workload backups (`internal/backup/workload/lake.go`) must all still open every catalog.
- DuckDB stays out of the daemon (ADR-0086). Every input that is downloaded is pinned by SHA-256. DuckDB is MIT-licensed.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| Stay on 1.5.6 | no work now | no support after March 2027; the move gets bigger later | rejected by the decider |
| Upgrade the engine, keep consumers on 1.5.6 | consumers untouched | the wire fails in both directions (experiment 0006) | not possible |
| Migrate catalogs to DuckLake 1.1, after a backup | exact string statistics, row-group counts, nested schemas, the NULL type, epoch transforms, multi-snapshot deletion vectors | one-way: a rollback or an older funcd cannot open the catalog | rejected by the decider |
| Keep ADR-0260's held port | works on 2.0.0 unchanged | SO_REUSEPORT code that port 0 makes unnecessary | rejected |
| Wait for 2.0.1 | patch fixes | delay; experiment 0006 is the trial run DuckDB advises | rejected by the decider |

## Decision

1. **DuckDB 2.0.0 everywhere funcd runs it.** The image takes the `duckdb==2.0.0` `cp312-abi3` wheels. The dev engine
   bundle takes the 2.0.0 CLI of its target and the four extensions. Every file is pinned.
2. **Consumers move with the engine, in one breaking release.** The funcd-python examples move to duckdb 2.0.0 and are
   released first (ADR-0141), and funcd then pins that release. Both repos mark the change with a `BREAKING CHANGE:`
   footer naming who must move: in-cluster consumer functions; external Quack clients through the ADR-0138 edge; and SQL
   on a Lake, which now runs on 2.0, where single-arrow lambdas are off and the shim's `lock_configuration` keeps them off.
3. **Catalogs stay at DuckLake 1.0.** Both engines' ATTACH passes `DUCKLAKE_VERSION '1.0'`. It must be an ATTACH option,
   because the shim locks the configuration before its ATTACH. A new catalog is then created at 1.0, and an existing one
   stays at 1.0. Nothing passes `AUTOMATIC_MIGRATION`.
4. **The dev Lake engine serves on port 0.** Its init calls `quack_serve('quack:127.0.0.1:0', …)`. The start reads the
   first complete line of standard output that names `http://127.0.0.1:<port>`, and reports that address. No port is
   held first. An engine that ends before its row fails the start at once with its error output, and so does a timeout.
5. **`extension_directories`, not `extension_directory`**, in three places: the dev engine's init SQL, the shim's
   connection and the image build. The examples' code and their install scripts make the same change. funcd's own
   `DUCKDB_EXTENSION_DIRECTORY` variable keeps its name and meaning.
6. **The dev engine runs with `DUCKDB_AGENT_MODE=0`.** In agent mode the CLI prints errors as JSON, and a failed start
   reports those lines.
7. **The PEP proxy's handshake rewrite stays.** In 2.0.0 the token is still the first field after the 8-byte preamble
   (experiment 0006), so `swapHandshakeToken` is unchanged. Its golden fixture is captured again from a 2.0.0 client,
   which is the handshake-format check that ADR-0137 requires at each engine bump.
8. **The shim's sandbox allows instead of denies.** Before it locks the configuration, the shim turns external access off
   and allows only `s3://` (which reaches nothing but funcd's S3 gateway, the client's pinned endpoint) and the engine's
   own directory, where its catalog and DuckDB's temp directory live; persistent secrets are off. An allow-list does not
   depend on the names of the filesystems each DuckDB release registers. The ATTACH and `quack_serve` work under it.

## Temporary workarounds

- **Catalogs stay at DuckLake 1.0**, without the 1.1 features. *Exit*: a per-Lake migration ADR, with a backup first,
  when a Lake needs one of them.
- **Quack's wire is version-coupled**: a Lake and its consumers move together, and the examples stay within
  `>=2.0.0,<2.1`. *Exit*: DuckDB states a wire guarantee across versions.
- **DuckDB 2.0.0 before its first patch.** *Exit*: 2.0.1, in three steps: a funcd-python release moves the examples,
  funcd pins 2.0.1, and experiment 0006's cross-version Quack checks run again between 2.0.0 and 2.0.1.

## Contracts

**Pins.** `images/runtime/duckdb/duckdb.sha256` holds only 2.0.0 lines; the 1.5.6 lines go. The duckdb lane checks the
consumer bundle's httpfs and quack files against these lines (`scripts/lanes.yaml:158`), so the examples' `PINS` must
equal them.

| Input | 1.5.6 today | 2.0.0 |
|---|---|---|
| image wheel | `duckdb==1.5.6` | `duckdb==2.0.0`, the `cp312-abi3` wheels for manylinux x86_64 and aarch64, pinned by hash |
| dev engine CLI | `v1.5.6/duckdb_cli-<linux-amd64, linux-arm64, osx-universal>.zip` | `v2.0.0/duckdb-cli-<linux-amd64, linux-arm64, osx-amd64, osx-arm64>.tar.gz` |
| extensions | `v1.5.6/<platform>/{httpfs,ducklake,quack,sqlite_scanner}.duckdb_extension` | the same names under `v2.0.0/`, for linux_amd64, linux_arm64, osx_amd64 and osx_arm64 |

**`scripts/fetch-lake-engine.sh <os> <arch> [version]`**: the version defaults to `2.0.0`. darwin maps to the CLI of its
own architecture. The script fetches `duckdb-cli-<cli>.tar.gz`, checks it against `duckdb.sha256` and extracts `duckdb`.
The bundle's layout is unchanged.

**`internal/lake/devengine`**:

```go
// buildInitSQL serves on 127.0.0.1:0 and attaches the lake with DUCKLAKE_VERSION '1.0'.
func buildInitSQL(extDir, catalogFile string, env map[string]string) string

// serveRow matches the address on a complete line of the engine's listen_url row.
var serveRow = regexp.MustCompile(`http://(127\.0\.0\.1:[1-9][0-9]*)`)

// rowWatcher buffers the engine's standard output to whole lines. On the first line that serveRow matches, it stores
// the address and closes seen; it never blocks the stdout copier.
type rowWatcher struct {
	seen    chan struct{}
	addr    string
	matched bool
	line    []byte
}

// waitServing returns the address the watcher stored, or the engine's error output when it ends first.
func waitServing(ctx context.Context, p *engineProc, timeout time.Duration) (string, error)
```

- **Removed:** `holdLoopbackPort`, `listenReusePort` and the `Runtime.holdPort` seam.
- **Environment:** the engine runs with its parent's environment plus `DUCKDB_AGENT_MODE=0`.
- **Init SQL:** it starts with `SET extension_directories=[<extDir>];`.

**`images/runtime/duckdb`**: `shim.py` sets `extension_directories` to `[$DUCKDB_EXTENSION_DIRECTORY]`. Its sandbox
(`_configure_and_confine`) keeps the S3 pins and `disabled_filesystems`, and before `lock_configuration` adds
`temp_directory='/var/funcd/ducklake/tmp'`, `allowed_directories=['s3://', '/var/funcd/ducklake/']`,
`allow_persistent_secrets=false` and `enable_external_access=false`. Its ATTACH adds `DUCKLAKE_VERSION '1.0'` and keeps
`OVERRIDE_DATA_PATH TRUE`. Its `quack_serve` call keeps `allow_other_hostname := true`: 2.0 refuses a host other than
localhost without it. The `Dockerfile`'s extension install sets `extension_directories` too.

**`internal/lake/gateway`**: `swapHandshakeToken` is unchanged. `testdata/handshake_golden.bin` becomes a frame captured
from a 2.0.0 client.

**pyvvo/funcd-python** (`examples/lake-quack`, `examples/releve-lakehouse`): duckdb `>=2.0.0,<2.1`, locked at 2.0.0;
`SET extension_directories=['<dir>']` at its five sites (both `scripts/install_extensions.py`, `lake-quack/src/main.py:73`,
`releve-lakehouse/src/lake-reader/main.py:38`, `releve-lakehouse/src/to-gold/main.py:37`); `PINS` with the 2.0.0
linux_amd64 and linux_arm64 hashes of httpfs and quack.

**Dependencies & I/O**

| Consumes | Exposes |
|---|---|
| the pins above; GitHub releases, extensions.duckdb.org and PyPI, at build time only | the same Lake endpoint, on DuckDB 2.0's Quack wire |
| `DUCKDB_EXTENSION_DIRECTORY` (funcd's own variable, unchanged) | `GET /` → 200 for the readiness probe, unchanged |
| the 1.0 catalog tables that `internal/backup/workload/lake.go` reads | the dev engine's address, taken from its row |

## Implementation plan

1. **funcd-python**: the two examples' `pyproject.toml`, `uv.lock`, the five setting sites and `PINS`. One PR with the
   `BREAKING CHANGE:` footer, then a release.
2. **funcd**: `go get github.com/pyvvo/funcd-python@<tag>`; `images/runtime/duckdb/{duckdb.sha256, requirements.txt,
   Dockerfile, shim.py, shim_test.py}`; `scripts/fetch-lake-engine.sh`; `internal/lake/devengine/{devengine.go,
   devengine_test.go}`; `internal/lake/gateway/testdata/handshake_golden.bin`; the 1.5 mentions in
   `internal/services/lake/lake.go`, `handshake.go` and the embedded placeholder; the duckdb lane's new steps; the F181
   row and `docs/PROJECT-SUMMARY.md`; at acceptance, the `Superseded in part by ADR-0262` back-links on ADR-0086 and
   ADR-0260.

**Test plan** (one test per scenario):

| Scenario | Test |
|---|---|
| `lake-on-duckdb-2` | the duckdb Lima lane, on the 2.0.0 image with a 2.0.0 consumer bundle |
| `new-lake-is-1-0` | `TestBuildInitSQL` and `shim_test.py` find `DUCKLAKE_VERSION '1.0'`; the lane reads its new catalog's `ducklake_metadata` version, 1.0 |
| `catalog-stays-1-0` | experiment 0006's check that a catalog 1.5.6 made stays at 1.0 under the option |
| `dev-engine-port-zero` | `TestScenarioDevEnginePortZero`: the init names port 0, and a fake engine prints a row for a port it bound, which the start reports; plus `TestRowWatcherSplitRow`, a row split inside its port |
| `dev-engine-early-exit` | `TestScenarioDevEngineEarlyExit`; the existing no-row timeout subtest stays |
| `agent-environment-same-output` | `TestScenarioNoAgentRendering`: the engine's environment holds `DUCKDB_AGENT_MODE=0` |
| `no-deprecated-extension-setting` | `TestConvergeLaunchesEngineWhenBundled` finds no deprecation line from the real CLI; `shim_test.py` |
| `arbitrary-url-confined` | duckdb lane steps: reads of an http URL and of a local file outside the engine directory fail; `shim_test.py` checks the allow-list settings |

`TestSwapHandshakeToken` passes on the new golden frame.

**Definition of done**: `scripts/agent/gate.sh` passes; the duckdb lane passes with its new steps and records the
engine's idle RSS once; the app-todo lane's Lake restore case (D18, `OVERRIDE_DATA_PATH`) passes; on the real 2.0.0 engine
bundle, `just dev-example lake-quack` serves lake-reader's rows and releve-lakehouse's pipeline runs (`just dev-example
releve-lakehouse`, then `just seed-releves`); a `release.yml` run with no tag builds all four targets; the funcd-python
release is pinned in `go.mod`.

## Review checklist

- [ ] Every DuckDB pin is 2.0.0 and matches its SHA-256. A repo-wide grep outside `docs/` finds no DuckDB 1.5 version.
- [ ] Every ATTACH carries `DUCKLAKE_VERSION '1.0'`, nothing passes `AUTOMATIC_MIGRATION`, and the lane's new catalog
  reads 1.0.
- [ ] No `extension_directory` setting remains in funcd or the examples, `DUCKDB_EXTENSION_DIRECTORY` is unchanged, and
  the examples' `PINS` equal funcd's 2.0.0 lines.
- [ ] `holdLoopbackPort`, `listenReusePort` and `holdPort` are gone; the init asks for port 0; the watcher reads whole
  lines and never blocks; an early end and a timeout still fail the start; the environment holds `DUCKDB_AGENT_MODE=0`.
- [ ] The golden handshake comes from a 2.0.0 client and `TestSwapHandshakeToken` passes on it; readiness is unchanged.
- [ ] The shim sets its allow-list before the lock; the lane's new steps pass: the catalog version,
  `arbitrary-url-confined` and the idle RSS.
- [ ] Both repos' `BREAKING CHANGE:` footers name the consumers who must move.
- [ ] Each scenario has its named test or lane check, and it passes.

## Consequences

- **(+)** A long-term-support DuckDB until at least October 2027; the dev engine loses its port hold; 2.0's Quack client
  offers `ATTACH` and `CONNECT`, unverified through the PEP proxy.
- **(−)** Every consumer's duckdb moves to 2.0 at the same release. The image's extensions and the Linux dev engine
  bundle grow; the macOS bundle stays level (experiment 0006). The DuckLake 1.1 features wait.
- **Risk**: 2.0.0 is a first major release, and experiment 0006 ran on arm64 only (the release build covers amd64).

## Open questions

- Consumers on `ATTACH 'quack:…' (TOKEN …)` through the PEP proxy. The handshake path is verified for `quack_query`
  only. The implementation's lane tries it as a non-gating step, and the examples adopt it in a later change if the
  proxy carries it.
- funcdctl's darwin/amd64 dev engine once DuckDB 2.1 drops macOS on amd64. This is decided at the 2.1 upgrade.

## References

- Issue #1184 (FEAT-0003 F181), under tracker #1032.
- Experiment 0006, `0006-duckdb-2-upgrade` in pyvvo/experiment: the 50 checks this ADR rests on, including the new
  catalogs, the proxy's token swap and the shim's sandbox on 2.0.0.
- [Announcing DuckDB 2.0.0](https://duckdb.org/2026/10/21/announcing-duckdb-200) ·
  [DuckLake 1.1](https://ducklake.select/2026/10/21/ducklake-11/) · DuckLake's ATTACH documentation (`DUCKLAKE_VERSION`).
- ADR-0086, ADR-0087, ADR-0137, ADR-0138, ADR-0141, ADR-0207, ADR-0222, ADR-0260, ADR-0261.
