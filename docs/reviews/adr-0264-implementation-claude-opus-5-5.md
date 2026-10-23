## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0264 implementation, model: claude-opus-5-5)

This report reviews branch `feat/adr-0264-cgroup-view` at 9b1203ea, on main 621f7f25:

- cc814f4d → f21a5058: the ADR, judged twice (round 1's Blocker cleared by a measured spike that moved the spill
  fixture to 1Gi), and its acceptance: the back-link on ADR-0011, the F12 entry, the blueprint's isolation clause and
  the PROJECT-SUMMARY row;
- 9b1203ea feat(runtime): `withCgroupView` in `ociOpts` on cgroup v2 hosts, its test, and the duckdb lane's cases.

ADR-0264 realizes FEAT-0000/F12; the PR that carries this branch closes #1185.

### Verified correct (keep)
- **The spec matches containerd CRI's cgroup v2 default**: a `cgroup` namespace, and a `cgroup` mount at
  `/sys/fs/cgroup` with `ro`, `nosuid`, `noexec`, `nodev`, `relatime`, each added once
  (`internal/runtime/containerd/containerd_linux.go`, `withCgroupView`), applied only when `cgroups.Mode()` is
  `cgroups.Unified`, as the ADR's Decision 2 says.
- **No other OCI change**; the process driver is untouched. `go.mod` only moves `containerd/cgroups/v3` from indirect to
  direct (same version).
- **`TestScenarioWorkerSeesOnlyItsCgroup`** applies the option twice to a bare spec and checks one namespace and one
  read-only mount, after what the spec held, and the no-`linux` case. It passes on Linux.
- **The duckdb lane carries one case per scenario**, named for it: the 1Gi engine reports `819.1 MiB` (`engine-sizes-to-its-limit`); a
  40M-row window query returns `[[39999999]]` and the Lake keeps serving (`engine-spills-under-its-limit`); in the
  engine `CGROUP=0::/ MEMORY_MAX=1073741824 CPU_MAX=200000/100000 WRITE=refused`, and in `lake-reader`
  `MEMORY_MAX=max CPU_MAX=max/100000 WRITE=refused` (`worker-sees-only-its-cgroup`).
- **The spill probe is gone**: `e2e/fixtures/spill-probe.py` and its stage entry are removed; the only remaining
  mention is the ADR's own plan.
- **Docs**: the ADR's substance matches the code; ADR-0011 has only its `Superseded in part by` line; the F12 entry and
  the blueprint clause follow the decision.

### Checks run (reviewer)
- `GOOS=linux go build ./...`: exit 0. `GOOS=linux go vet ./internal/runtime/containerd/`: exit 0.
- golangci-lint (host-built, run with `GOOS=linux`) on `internal/runtime/containerd`: 0 issues.
- `go mod verify`: all modules verified.
- The lane guard tests (`go test -run Lane ./cmd/funcd/ ./internal/testkit/langmod/`): ok.
- `just test-linux`: all 10 Linux packages ok, `internal/runtime/containerd` included.

### Evidence read, not rerun
- `just lima-example-all` on 9b1203ea (the builder's run): "ALL VENOM E2E LANES PASSED" for env-echo, fn-to-fn,
  http-api, duckdb, s3, kv, workflow, funclog, egress, sql, sql-examples and metastore.
- The ADR's References: the measured spike at 256Mi, 512Mi (engine killed) and 1Gi (query returns, no OOM kill).

### Definition of done
`TestScenarioWorkerSeesOnlyItsCgroup` passes and every lane passes. `scripts/agent/gate.sh` runs on the PR's head after
this stamp, as the one-PR flow orders it.
