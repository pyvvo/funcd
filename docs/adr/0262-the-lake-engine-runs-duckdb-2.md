# ADR-0262: The Lake engine runs DuckDB 2.0, with its catalogs kept at DuckLake 1.0

- **Status**: Proposed
- **Date**: 2026-10-22
- **Deciders**: green-0-rabbit
- **Tags**: lake, duckdb, ducklake, quack, dev, dependency, breaking-change
- **Realizes**: [FEAT-0003/F181](../feat/0003-feat-data-platform.md)
- **Supersedes (in part)**: [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md), only its DuckDB pin
  and its "Quack is beta" workaround, whose exit (DuckDB 2.0) this ADR takes ·
  [ADR-0260](0260-a-loopback-listener-owns-its-port.md), only Decision 3's Lake engine half: the held port. The rest
  of both ADRs stands.
- **Relates to**: [ADR-0261](0261-rename-catalogservice-to-lake.md) (the Lake names) ·
  [ADR-0125](0125-funcdctl-dev-local-run.md) and [ADR-0130](0130-funcdctl-dev-catalog-consumer-and-query-lane.md)
  (the dev engine) · [ADR-0137](0137-per-caller-catalog-query-rbac.md) (the PEP proxy in front of the engine) ·
  [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (how a language-repo change ships) ·
  [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) (restore with `OVERRIDE_DATA_PATH`)

## Context & Need

A Lake (ADR-0086, named by ADR-0261) is a DuckDB engine with the ducklake, quack, httpfs and sqlite extensions. It runs
in two places: the curated `duckdb` runtime image (the Python wheel driven by `images/runtime/duckdb/shim.py`) and the
engine `funcdctl dev` embeds (the CLI driven by `internal/lake/devengine`). Consumers are functions that bundle their
own duckdb and reach a Lake over Quack. All three pin DuckDB 1.5.6.

DuckDB 2.0.0 shipped on 2026-10-21 as a long-term-support release (support until at least October 2027), with DuckLake
1.1. ADR-0086's "Quack is beta" workaround named DuckDB 2.0 as the time to bump the pin. Experiment 0006 ran 37 checks of
what funcd relies on against 2.0.0: 34 pass. Two things do not carry over: the Quack wire fails between 1.5.6 and 2.0.0
in both directions, and a catalog migrated to DuckLake 1.1 no longer opens in 1.5.6. This ADR decides how the engine,
the dev engine and the consumers move, and what the catalogs do.

## Scenarios

- `scenario: lake-on-duckdb-2` — Given a Lake deployed from the 2.0.0 image and a consumer Function bundled with duckdb
  2.0.0 and bound to it, When the consumer runs `quack_query` through its binding, Then it gets the rows.
- `scenario: catalog-stays-1-0` — Given a Lake whose catalog a 1.5.6 engine wrote, When the 2.0.0 engine attaches it
  and commits, Then the catalog's DuckLake version is still 1.0.
- `scenario: dev-engine-port-zero` — Given `funcdctl dev` starts a Lake engine, When the engine serves, Then the start
  reports the port the engine's `listen_url` row names, and no port was held before the engine started.
- `scenario: dev-engine-early-exit` — Given an engine that ends before its row (an error in its init), When the start
  waits, Then the start fails at once with the engine's error output.
- `scenario: no-agent-rendering` — Given `funcdctl dev` runs with an AI agent's environment variable set, When it starts
  a Lake engine, Then the engine runs with `DUCKDB_AGENT_MODE=0`.
- `scenario: no-deprecated-extension-setting` — Given the 2.0.0 engine in the image and in dev, When each loads its
  extensions, Then neither prints the `extension_directory` deprecation warning.

## Scope

**In**: the DuckDB pins of the image and the dev engine; the shim's and the image build's extension setting; the dev
engine's port, row and environment; the funcd-python consumer examples' duckdb, released in step; the release notes.

**Out**: migrating catalogs to DuckLake 1.1 and the features that need it (a later, per-Lake ADR); consumers on
`ATTACH 'quack:…'` instead of `quack_query` (see Open questions); the SQL dev engine (ADR-0260's other half);
DuckDB 2.1, which drops macOS on amd64.

## Constraints & Decision drivers

- One DuckDB version serves a Lake and its consumers: the Quack wire has no compatibility across 1.5 and 2.0.
- A rollback of funcd, or a restore into an older funcd (ADR-0222, the pre-upgrade generations), must still open every
  catalog.
- DuckDB stays out of the daemon (ADR-0086); every downloaded input is pinned by SHA-256; DuckDB is MIT.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| Stay on 1.5.6, supported until March 2027 | no work now | keeps the "Quack is beta" workaround; a bigger move later; no port 0 | rejected by the decider |
| Upgrade the engine and keep consumers on 1.5.6 | consumers untouched | the wire fails both ways (experiment 0006) | not possible |
| Migrate catalogs to DuckLake 1.1 on upgrade, after a backup | exact string statistics, row-group counts, nested schemas, the NULL type, epoch transforms, multi-snapshot deletion vectors | one-way: a rollback or an older funcd cannot open the catalog | rejected by the decider; a later ADR per Lake |
| Keep ADR-0260's held port | works on 2.0.0 unchanged | SO_REUSEPORT code that port 0 makes unnecessary | rejected |
| Wait for the first patch, 2.0.1 | patch fixes | delay; experiment 0006 is the trial run DuckDB advises; 2.0.1 is a pin change | rejected by the decider |

## Decision

1. **DuckDB 2.0.0 everywhere funcd runs it.** The image's wheel is `duckdb==2.0.0` (the `cp312-abi3` manylinux
   wheels). The dev engine bundle is the 2.0.0 CLI of its target with the four extensions. Every file is pinned in
   `images/runtime/duckdb/duckdb.sha256` or by hash in `requirements.txt`. A later 2.0 patch comes in by a pin change.
2. **Consumers move with the engine, in one breaking release.** The funcd-python examples move to duckdb 2.0.0 and are
   released first (ADR-0141); funcd pins that release; funcd's release notes say a consumer's duckdb must be 2.0.
3. **Catalogs stay at DuckLake 1.0.** Nothing passes `AUTOMATIC_MIGRATION`. DuckDB 2.0.0 reads and writes 1.0 catalogs,
   and a 1.5.6 engine still opens them afterwards, so a rollback or a restore into an older funcd keeps working.
4. **The dev Lake engine serves on port 0 and takes its address from its row.** Its init calls
   `quack_serve('quack:127.0.0.1:0', …)`; the start reads `http://127.0.0.1:<port>` from the engine's standard output
   and reports that address. No port is held first (this replaces ADR-0260 Decision 3's Lake half). An engine that ends
   before its row still fails the start at once with its error output, and a timeout still fails it.
5. **`extension_directories`, not `extension_directory`.** The dev engine's init SQL, the shim's connection and the
   image build set the plural setting, which DuckDB 1.5 also has. funcd's own `DUCKDB_EXTENSION_DIRECTORY` variable
   keeps its name and meaning.
6. **The dev engine runs with `DUCKDB_AGENT_MODE=0`**, so an AI agent's environment never changes the engine's output.
7. The shim keeps `allow_other_hostname := true` behind the PEP proxy: 2.0 refuses a host other than localhost without it.

## Temporary workarounds

- **Catalogs stay at DuckLake 1.0**, without the 1.1 features. *Exit*: a per-Lake migration ADR, with a backup first,
  when a Lake needs one of them.
- **DuckDB 2.0.0 before its first patch.** *Exit*: the pin moves to 2.0.1 when it ships (announced for November 2026).

## Contracts

**Pins** (`images/runtime/duckdb/duckdb.sha256` holds 2.0.0 lines only; the 1.5.6 lines go):

| Input | 1.5.6 today | 2.0.0 |
|---|---|---|
| image wheel | `duckdb==1.5.6` | `duckdb==2.0.0`, the `cp312-abi3` wheels for manylinux x86_64 and aarch64, by hash |
| dev engine CLI | `v1.5.6/duckdb_cli-<linux-amd64, linux-arm64, osx-universal>.zip` | `v2.0.0/duckdb-cli-<linux-amd64, linux-arm64, osx-amd64, osx-arm64>.tar.gz` |
| extensions | `v1.5.6/<platform>/{httpfs,ducklake,quack,sqlite_scanner}.duckdb_extension` | the same names under `v2.0.0/`, for linux_amd64, linux_arm64, osx_amd64, osx_arm64 |

**`scripts/fetch-lake-engine.sh <os> <arch> [version]`**: the version defaults to `2.0.0`; darwin maps to the CLI of its
own architecture (`osx-amd64`, `osx-arm64`); the script fetches `duckdb-cli-<cli>.tar.gz`, checks it against
`duckdb.sha256`, and extracts `duckdb`. The bundle's layout (`extensions/v<version>/<platform>/…`) is unchanged.

**`internal/lake/devengine`**:

```go
// buildInitSQL serves on 127.0.0.1:0; the engine names the port it took in its listen_url row.
func buildInitSQL(extDir, catalogFile string, env map[string]string) string

// serveRow matches the address of the engine's listen_url row.
var serveRow = regexp.MustCompile(`http://(127\.0\.0\.1:[1-9][0-9]*)`)

// rowWatcher reads the engine's standard output and sends the first address serveRow matches, once. A row split
// across writes still matches.
type rowWatcher struct {
	addr    chan<- string
	matched bool
	tail    string
}

// waitServing returns the address the engine names, or the engine's error output when it ends first.
func waitServing(ctx context.Context, p *engineProc, timeout time.Duration) (string, error)
```

`holdLoopbackPort`, `listenReusePort` and the `Runtime.holdPort` seam are removed. The engine's command runs with the
parent environment plus `DUCKDB_AGENT_MODE=0`. The init SQL starts with `SET extension_directories=[<extDir>];` and
never passes `AUTOMATIC_MIGRATION`.

**`images/runtime/duckdb`**: `shim.py` sets `extension_directories` to `[$DUCKDB_EXTENSION_DIRECTORY]` on its connection;
its ATTACH (with `OVERRIDE_DATA_PATH TRUE`, no `AUTOMATIC_MIGRATION`) and `quack_serve` calls are unchanged. The
`Dockerfile`'s extension install step sets the same setting.

**pyvvo/funcd-python** (`examples/lake-quack`, `examples/releve-lakehouse`): duckdb `>=2.0.0`, locked at 2.0.0; their
extension setting is `SET extension_directories=['<dir>']`.

**Dependencies & I/O**

| Consumes | Exposes |
|---|---|
| the pins above; GitHub releases, extensions.duckdb.org and PyPI at build time only | a Lake's Quack endpoint, unchanged |
| `DUCKDB_EXTENSION_DIRECTORY` (funcd's variable, unchanged) | the dev engine's address, from its row |

## Implementation plan

1. **funcd-python**: the two examples' `pyproject.toml`, `uv.lock` and extension setting; a PR and a release (ADR-0141).
2. **funcd**: `go get github.com/pyvvo/funcd-python@<tag>`; `images/runtime/duckdb/{duckdb.sha256, requirements.txt,
   Dockerfile, shim.py, shim_test.py}`; `scripts/fetch-lake-engine.sh`; `internal/lake/devengine/{devengine.go,
   devengine_test.go}`; the F181 row; `Superseded in part by ADR-0262` back-links on ADR-0086 and ADR-0260 at
   acceptance; the release notes.

**Test plan** (one per scenario):

| Scenario | Test |
|---|---|
| `lake-on-duckdb-2` | the duckdb Lima lane, on the 2.0.0 image and a 2.0.0 consumer bundle |
| `catalog-stays-1-0` | `TestBuildInitSQL` and `shim_test.py` find no `AUTOMATIC_MIGRATION`; the lane reads the catalog's `ducklake_metadata` version, 1.0, after its run |
| `dev-engine-port-zero` | `TestScenarioDevEnginePortZero`: a fake engine binds a free port and prints its row; the start reports that port |
| `dev-engine-early-exit` | `TestScenarioDevEngineEarlyExit`: a fake engine exits 1 before its row; the start fails at once with its output |
| `no-agent-rendering` | `TestScenarioNoAgentRendering`: the engine's environment holds `DUCKDB_AGENT_MODE=0` |
| `no-deprecated-extension-setting` | `TestBuildInitSQL` and `shim_test.py` find `extension_directories` only; the lane's engine log has no deprecation warning |

**Definition of done**: `scripts/agent/gate.sh` passes; the duckdb lane passes on the 2.0.0 image; a dev Lake runs on the
real 2.0.0 engine bundle built by `scripts/fetch-lake-engine.sh`; the funcd-python release is pinned in `go.mod`.

## Review checklist

- [ ] Every DuckDB pin is 2.0.0 and matches its SHA-256; no 1.5 version string is left in the image, the scripts or
  the dev engine.
- [ ] No `extension_directory` setting in funcd or the examples; `DUCKDB_EXTENSION_DIRECTORY` is unchanged.
- [ ] No `AUTOMATIC_MIGRATION` anywhere; the lane's catalog reads version 1.0.
- [ ] `holdLoopbackPort`, `listenReusePort` and `holdPort` are gone; the dev engine serves on port 0 and reports its
  row's port; an early end and a timeout still fail the start.
- [ ] The dev engine's environment holds `DUCKDB_AGENT_MODE=0`.
- [ ] The funcd-python release with duckdb 2.0.0 is pinned in `go.mod`, and the release notes mark the consumer break.
- [ ] Each scenario has its named test or lane check, and it passes.

## Consequences

- **(+)** A long-term-support DuckDB until at least October 2027; ADR-0086's "Quack is beta" workaround closes; the dev
  engine loses its port hold; 2.0's Quack client (`ATTACH`, `CONNECT`) becomes available to consumers.
- **(−)** Every consumer's duckdb must move to 2.0 at the same release. The image's extensions grow, and so does the
  Linux dev engine bundle, while the macOS bundle stays level (experiment 0006). The DuckLake 1.1 features wait.
- **Risk**: 2.0.0 is a first major release; its first patch is a pin change.

## Open questions

- Migrating a Lake to DuckLake 1.1: when, on whose request, with which backup. A later ADR.
- Consumers on `ATTACH 'quack:…' (TOKEN …)` through the PEP proxy, which swaps the token in the handshake: the
  implementation's lane checks it; the examples adopt it in a later change if the proxy carries it.
- funcdctl's darwin/amd64 dev engine once DuckDB 2.1 drops macOS on amd64: at the 2.1 upgrade.

## References

- Issue #1184 (FEAT-0003 F181), under tracker #1032.
- Experiment 0006, `0006-duckdb-2-upgrade` in pyvvo/experiment: the 37 checks this ADR rests on.
- [Announcing DuckDB 2.0.0](https://duckdb.org/2026/10/21/announcing-duckdb-200) ·
  [DuckLake 1.1](https://ducklake.select/2026/10/21/ducklake-11/).
- ADR-0086, ADR-0260, ADR-0261, ADR-0125, ADR-0130, ADR-0137, ADR-0141, ADR-0222.
