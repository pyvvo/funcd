# ADR-0263: The Lake engine runs DuckDB 2.0, and its new catalogs use DuckLake 1.1

- **Status**: Reviewing (2026-10-23), implemented for #1184. Accepted (2026-10-23), under the decider's standing go of
  that day. Revised that day for DuckLake 1.1 and no consumers (the decider); judged four times, the last: accept after
  one Major (a 1.1 catalog with files), folded. Numbered 0263: 0262 went to the function-owned volumes ADR, merged
  first (#1189); the judge rounds, experiment PRs #11–#15 and funcd-python #100 name it ADR-0262.
- **Date**: 2026-10-22
- **Deciders**: green-0-rabbit
- **Tags**: lake, duckdb, ducklake, quack, dev, dependency
- **Realizes**: [FEAT-0003/F183](../feat/0003-feat-data-platform.md)
- **Supersedes (in part)**: [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md), only its DuckDB and
  DuckLake pins (lines 60, 90-92, 113-117, 233, 246, 281), its "Quack is beta" workaround (170-172, now the
  version-coupled wire workaround below, without its HTTP-server fallback) and its shim's sandbox statements (219-221),
  which Decision 8 extends · [ADR-0260](0260-a-loopback-listener-owns-its-port.md), only the Lake half of Decision 3 (the
  held port), the scenario `catalog-engine-holds-its-port` (60-62), lines 130-132 and the Lake half of 135, and the
  checklist line 164-165. The rest of both ADRs stands.
- **Relates to**: [ADR-0261](0261-rename-catalogservice-to-lake.md) (the names) · [ADR-0087](0087-add-on-provider-runtime.md)
  (readiness) · [ADR-0137](0137-per-caller-catalog-query-rbac.md), [ADR-0138](0138-external-catalog-ingress-and-route-aggregation.md)
  (the PEP proxy, the edge) · [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (language-repo releases) ·
  [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md), [ADR-0207](0207-pre-upgrade-snapshot-and-safe-mode.md) (restores, pre-upgrade generations)

## Context & Need

A Lake (ADR-0086, named by ADR-0261) is a DuckDB engine with the ducklake, quack, httpfs and sqlite extensions. It runs
in the curated `duckdb` image (the Python wheel, driven by `images/runtime/duckdb/shim.py`) and in `funcdctl dev` (the CLI,
driven by `internal/lake/devengine`). A consumer is a function that bundles its own duckdb and reaches a Lake over Quack
through the PEP proxy, which rewrites the token in its handshake (ADR-0137). All of these pin DuckDB 1.5.6.

DuckDB 2.0.0 shipped on 2026-10-21 as a long-term-support release, supported until at least October 2027, together with
DuckLake 1.1. Experiment 0006 ran 62 checks of what funcd relies on, and 59 pass. Two facts shape the move: the Quack
wire fails between 1.5.6 and 2.0.0 in both directions, and 1.5.6 does not open a DuckLake 1.1 catalog, which 2.0.0
creates by default. No Lake catalog exists yet, and the funcd-python examples are the only consumers (the decider,
2026-10-23).

## Scenarios

- `scenario: lake-on-duckdb-2`: Given a Lake deployed from the 2.0.0 image, and a consumer bound to it whose bundle
  holds duckdb 2.0.0, When the consumer runs `quack_query` through the PEP proxy, Then it gets the rows.
- `scenario: new-lake-is-1-1`: Given no catalog, When the 2.0.0 engine attaches one and commits, Then the catalog's
  DuckLake version is 1.1, and the Lake's workload backup copies it and the files it names.
- `scenario: dev-engine-port-zero`: Given `funcdctl dev` starting a Lake engine, Then the engine's init asks for port 0,
  and the start reports the port that the engine's `listen_url` row names.
- `scenario: dev-engine-early-exit`: Given an engine that ends before its row (an error in its init), Then the start
  fails at once with the engine's error output.
- `scenario: agent-environment-same-output`: Given `funcdctl dev` runs with an AI agent's environment variable set,
  Then the dev engine's row and its error output are rendered as they are without that variable.
- `scenario: no-deprecated-extension-setting`: Given the 2.0.0 engine in the image and in dev, Then neither prints the
  `extension_directory` deprecation warning.
- `scenario: only-allowed-paths`: Given a Lake on the 2.0.0 image, When a query reads any path other than an `s3://`
  path or the engine's temp directory, Then the query fails.

## Scope

**In**: the DuckDB pins of the image and the dev engine; both engines' ATTACH; the shim's sandbox; the extension
setting; the dev engine's port, row and environment; the PEP proxy's handshake fixture (`internal/lake/gateway`); the
funcd-python examples, released in step; the duckdb lane's new steps.

**Out**: catalog migrations (no catalog exists); consumers on `ATTACH 'quack:…'`; the SQL dev engine (ADR-0260's other
half); DuckDB 2.1, which drops macOS on amd64.

## Constraints & Decision drivers

- One DuckDB version serves a Lake and its consumers: the Quack wire is not compatible across 1.5 and 2.0.
- A catalog's format changes only by an ADR, never by a DuckDB default. The daemon's own catalog reader for workload
  backups (`internal/backup/workload/lake.go`) must read every catalog a Lake writes.
- DuckDB stays out of the daemon (ADR-0086). Every input that is downloaded is pinned by SHA-256. DuckDB is MIT-licensed.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| Stay on 1.5.6 | no work now | no support after March 2027; the move gets bigger later | rejected by the decider |
| New catalogs at DuckLake 1.0 (`DUCKLAKE_VERSION '1.0'`) | a funcd older than this release still opens them | no catalog exists to keep readable; the 1.1 features would wait for a migration of every Lake | rejected by the decider |
| DuckDB's default catalog version (no `DUCKLAKE_VERSION`) | one option less | a later DuckDB would change the format of new catalogs on its own | rejected |
| Keep ADR-0260's held port | works on 2.0.0 unchanged | SO_REUSEPORT code that port 0 makes unnecessary | rejected |
| Wait for 2.0.1 | patch fixes | delay; experiment 0006 is the trial run DuckDB advises | rejected by the decider |

## Decision

1. **DuckDB 2.0.0 everywhere funcd runs it.** The image takes the `duckdb==2.0.0` `cp312-abi3` wheels. The dev engine
   bundle takes the 2.0.0 CLI of its target and the four extensions. Every file is pinned.
2. **The examples move with the engine.** The funcd-python examples move to duckdb 2.0.0 and are released first
   (ADR-0141), and funcd then pins that release. No other consumer exists, so neither repo marks a breaking change.
3. **New catalogs are DuckLake 1.1.** Both engines' ATTACH passes `DUCKLAKE_VERSION '1.1'`, so a later DuckDB cannot
   change the format of a new catalog. Nothing passes `AUTOMATIC_MIGRATION`: a 1.0 catalog fails the ATTACH with
   DuckLake's version mismatch and stays at 1.0 (experiment 0006).
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
8. **The shim's sandbox is an allow-list.** After its catalog's ATTACH, the shim allows only `s3://` paths and the
   engine's temp directory, turns external access off and locks the configuration (the order is in Contracts). A query
   then reaches no other path; the remaining outbound traffic stays bounded by the egress policy (ADR-0086). The ATTACH,
   catalog writes, `quack_serve` and spilling to disk work under it (experiment 0006).

## Temporary workarounds

- **Quack's wire is version-coupled**: a Lake and its consumers move together, and the examples stay within
  `>=2.0.0,<2.1`. *Exit*: DuckDB states a wire guarantee across versions.
- **DuckDB 2.0.0 before its first patch.** *Exit*: 2.0.1, in three steps: experiment 0006's cross-version Quack checks
  run between 2.0.0 and 2.0.1, a funcd-python release moves the examples, and funcd pins 2.0.1.

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
// buildInitSQL serves on 127.0.0.1:0 and attaches the lake with DUCKLAKE_VERSION '1.1'.
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

**`images/runtime/duckdb`**: `shim.py` sets `extension_directories` to `[$DUCKDB_EXTENSION_DIRECTORY]`, creates
`/var/funcd/ducklake/tmp`, and runs its statements in this order, which `shim_test.py` asserts: the S3 pins,
`s3_enable_deprecated_url_parameters=false`, `disabled_filesystems='HTTPFileSystem'`,
`temp_directory='/var/funcd/ducklake/tmp'`, `allow_persistent_secrets=false`; the catalog's recovery and its ATTACH, which
adds `DUCKLAKE_VERSION '1.1'` and keeps `OVERRIDE_DATA_PATH TRUE`; `allowed_directories=['s3://',
'/var/funcd/ducklake/tmp/']`, `enable_external_access=false`, `lock_configuration=true`; then `quack_serve`, which keeps
`allow_other_hostname := true` (2.0 refuses a host other than localhost without it). The `Dockerfile` sets
`extension_directories` too.

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
| the catalog tables that `internal/backup/workload/lake.go` reads, unchanged in 1.1 (experiment 0006) | the dev engine's address, taken from its row |

## Implementation plan

1. **funcd-python**: the two examples' `pyproject.toml`, `uv.lock`, the five setting sites and `PINS`. One PR, then a
   release.
2. **funcd**: `go get github.com/pyvvo/funcd-python@<tag>`; `images/runtime/duckdb/{duckdb.sha256, requirements.txt,
   Dockerfile, shim.py, shim_test.py}`; `scripts/fetch-lake-engine.sh`; `internal/lake/devengine/{devengine.go,
   devengine_test.go}`; `internal/backup/workload/{workload_test.go, testdata/ducklake-1.1.sql}`;
   `internal/lake/gateway/testdata/handshake_golden.bin`; the 1.5 mentions in
   `internal/services/lake/lake.go`, `handshake.go` and the embedded placeholder; the duckdb lane's new steps; the F183
   row and `docs/PROJECT-SUMMARY.md`; at acceptance, the `Superseded in part by ADR-0263` back-links on ADR-0086 and
   ADR-0260.

**Test plan** (one test per scenario):

| Scenario | Test |
|---|---|
| `lake-on-duckdb-2` | the duckdb Lima lane, on the 2.0.0 image with a 2.0.0 consumer bundle |
| `new-lake-is-1-1` | `TestBuildInitSQLAttachesDuckLake` and `shim_test.py` find `DUCKLAKE_VERSION '1.1'`; the duckdb lane reads its new catalog's version, 1.1; `TestLakeFilesDuckLake11Catalog` lists the data and delete files of a real 1.1 catalog with a nested schema (experiment 0006's fixture); experiment 0006 restores such a catalog under another path and opens its `VACUUM INTO` snapshot; the app-todo lane's D18 copies a 1.1 catalog, and a new Lake serves its restore |
| `dev-engine-port-zero` | `TestScenarioDevEnginePortZero`: the init names port 0, and a fake engine prints a row for a port it bound, which the start reports; plus `TestRowWatcherSplitRow`, a row split inside its port |
| `dev-engine-early-exit` | `TestScenarioDevEngineEarlyExit`; the existing no-row timeout subtest stays |
| `agent-environment-same-output` | `TestScenarioAgentEnvironmentSameOutput`: the engine's environment holds `DUCKDB_AGENT_MODE=0` |
| `no-deprecated-extension-setting` | `TestConvergeLaunchesEngineWhenBundled` finds no deprecation line from the real CLI; `shim_test.py` |
| `only-allowed-paths` | duckdb lane steps: reads of an http URL and of a local file outside the temp directory fail; `shim_test.py` checks the order |
| (none, non-gating) | a lane step records whether `ATTACH 'quack:…'` works through the PEP proxy; it never fails the lane |

`TestSwapHandshakeToken` passes on the new golden frame.

**Definition of done**: `scripts/agent/gate.sh` passes; the duckdb lane passes with its new steps (its spill step on a
256Mi Lake fixture, with experiment 0006's window query scaled past it) and records the engine's idle RSS once; the
app-todo lane's Lake restore case (D18, `OVERRIDE_DATA_PATH`) passes; on the real 2.0.0 engine bundle, `just dev-example
lake-quack` serves lake-reader's rows and releve-lakehouse's pipeline runs (`just dev-example releve-lakehouse`, then
`just seed-releves`); a `release.yml` run with no tag builds all four targets; the funcd-python release is pinned in
`go.mod`.

## Review checklist

- [ ] Every DuckDB pin is 2.0.0 and matches its SHA-256. A repo-wide grep outside `docs/` finds no DuckDB 1.5 version.
- [ ] Every ATTACH carries `DUCKLAKE_VERSION '1.1'`, nothing passes `AUTOMATIC_MIGRATION`, and the lane's new catalog
  reads 1.1.
- [ ] No `extension_directory` setting remains in funcd or the examples, `DUCKDB_EXTENSION_DIRECTORY` is unchanged, and
  the examples' `PINS` equal funcd's 2.0.0 lines.
- [ ] `holdLoopbackPort`, `listenReusePort` and `holdPort` are gone; the init asks for port 0; the watcher reads whole
  lines and never blocks; an early end and a timeout still fail the start; the environment holds `DUCKDB_AGENT_MODE=0`.
- [ ] The golden handshake comes from a 2.0.0 client and `TestSwapHandshakeToken` passes on it; readiness is unchanged.
- [ ] The shim runs its statements in Contracts' order; the lane's new steps pass: the catalog version,
  `only-allowed-paths`, the spill and the idle RSS; each scenario's test or check passes.

## Consequences

- **(+)** A long-term-support DuckDB until at least October 2027; DuckLake 1.1's features from the first catalog (exact
  string statistics, row-group counts, nested schemas, the NULL type, epoch transforms); the engine's sandbox is an
  allow-list; the dev engine loses its port hold; 2.0's Quack client offers `ATTACH` and `CONNECT`, unverified through
  the PEP proxy.
- **(−)** A funcd older than this release cannot open a catalog that this release creates: a manual downgrade, or a
  restore of its workload backups into such a funcd (ADR-0222), cannot serve those Lakes, and ADR-0207's rollback
  already loses every write after its pin. The image's extensions grow by about 71 MiB and the Linux dev bundle by
  19 MiB gzipped (experiment 0006).
- **Risk**: 2.0.0 is a first major release; experiment 0006 ran on arm64, and the release build only fetches and checks the
  amd64 pins.

## Open questions

- Consumers on `ATTACH 'quack:…' (TOKEN …)` through the PEP proxy: only `quack_query`'s handshake is verified; the
  lane's non-gating step tells, and the examples adopt it in a later change if the proxy carries it.
- funcdctl's darwin/amd64 dev engine once DuckDB 2.1 drops macOS on amd64. This is decided at the 2.1 upgrade.

## References

- Issue #1184 (FEAT-0003 F183), under tracker #1032.
- Experiment 0006, `0006-duckdb-2-upgrade` in pyvvo/experiment: the 62 checks this ADR rests on, among them the new
  catalogs at 1.0 and 1.1, a 1.1 catalog with files under the backup reader, a restore and a snapshot, the proxy's
  token swap and the allow-list sandbox on 2.0.0.
- [Announcing DuckDB 2.0.0](https://duckdb.org/2026/10/21/announcing-duckdb-200) ·
  [DuckLake 1.1](https://ducklake.select/2026/10/21/ducklake-11/) · DuckLake's ATTACH documentation (`DUCKLAKE_VERSION`).
