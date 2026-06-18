# ADR-0036 Implementation Review — Standalone daemon execution wiring

**Verdict**: **pass** — the standalone `funcd` binary now executes functions: `cmd/funcd` wires
process mode (the **embedded** Node shim + artifact store) by default and containerd mode by env,
extracting the shim from `go:embed` so the binary is self-contained. No new dependency.
**Producing model**: claude-opus-4-8 · **ADR status at review**: Reviewing

## Evidence (four sub-checks)

- `go build ./...` (darwin) + `GOOS=linux go build ./cmd/funcd/...` exit 0; `golangci-lint` → `0 issues`;
  `go mod verify` verified, `go.mod`/`go.sum` unchanged (`go:embed` is stdlib; containerd already vendored).
  `go run ./cmd/funcd version` prints the stamped identity. Identity grep clean.
- `go test ./...` green.

## Scenario → test (`cmd/funcd/main_test.go`, all pass)

| Scenario | Test |
|---|---|
| shim-is-self-contained | `TestShimEmbedded` (`shimnode.Shim` non-empty, is the shim contract) + `TestExecutionOptionsProcessExtractsShim` (extracted to the data dir == the embed) |
| daemon-executes-process | `TestDaemonExecutesFunction` (node-gated) — a platform assembled the daemon's way (embedded shim via `executionOptions` + artifact store) deploys an OCI function **with no digest** and serves it over HTTP |
| container-mode-selected | `TestExecutionOptionsContainerdMode` — `FUNCD_RUNTIME=containerd` fails fast off Linux |
| node-absent-degrades | `TestExecutionOptionsNodeAbsentDegrades` — no node → runtime-only options, no error |

## Judge findings verified folded

- **Major (containerd.Config)** — `executionOptions` builds `containerd.Config{Socket, Snapshotter,
  CNIBinDir, CNIConfDir, SubnetCIDR}` from `FUNCD_CONTAINERD_SOCKET`/`FUNCD_SNAPSHOTTER`/`FUNCD_CNI_*`/
  `FUNCD_SUBNET_CIDR` with the documented defaults — no invented config.
- **Minors** — `os.MkdirAll($FUNCD_DATA_DIR, 0o700)` before extracting the shim; `WithArtifactStore` set
  in both modes; the image prefix is the documented operator convention (`funcd/runtime-<rt>:latest`).

## ✅ Verified correct — keep it

- `cmd/funcd` stays a **thin shell** (ADR-0014): all wiring is `pkg/funcd` options + env reads, no business
  logic. `executionOptions` is the one helper.
- Importing `internal/runtime/containerd` compiles cross-platform (the `_other` stub) — confirmed by the
  linux build.
- The headline proof (`TestDaemonExecutesFunction`) exercises the **embedded** shim + the ADR-0035 no-digest
  resolution + the data-plane invoke together — the gap ADR-0034 recorded is closed.

## Findings

🔴/🟡 None. **Minor (env):** the daemon-executes + extract tests are node-gated (skip without `node`).

**Recommendation**: stamp **Implemented**. The container-mode runtime path is exercised at the wiring level
(fail-fast off Linux); its live crun walk is the Linux integration lane (ADR-0032), as before.
