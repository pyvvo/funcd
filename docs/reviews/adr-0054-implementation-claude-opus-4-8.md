# Review — ADR-0054 implementation (self-contained runtime: embedded images + managed containerd)

## Verdict: pass — 0 blockers, 0 majors  (ADR-0054 implementation, model: claude-opus-4-8)

The implementation makes funcd self-contained per ADR-0054 (the k3s/dockerd model): distroless curated
bases, `go:embed`'d per-arch OCI image tars imported into a privately-managed containerd, bundled crun
execed as a separate binary, and a single-unit `funcd install`/`uninstall`. The **non-gated unit bar is
met and green**; the real container bring-up is correctly **Linux+root-gated and deferred** to the
integration lane exactly as the ADR's Test plan specifies. No Blockers, no Majors.

### Verification (captured)

| Check | Command | Result |
|---|---|---|
| build | `nix develop -c go build ./...` | **exit 0** |
| linux cross-build (supervision compiles) | `GOOS=linux GOARCH=arm64 nix develop -c go build ./...` | **exit 0** |
| tests | `nix develop -c go test ./...` | **exit 0** (incl. `internal/runtime/ctrmanager` 0.422s, `internal/runtime/embedimg`, `cmd/funcd`) |
| lint | `nix develop -c go tool golangci-lint run ./...` | **0 issues** |
| mod verify | `nix develop -c go mod verify` | **all modules verified** |
| no new Go dep | `git diff HEAD -- go.mod go.sum` | **empty** — reused present `containerd/containerd/v2 v2.3.1` |
| install dry-run | `nix develop -c go run ./cmd/funcd install --print` | **exit 0** — one valid `funcd.service` unit, touched nothing |
| no CRI/kubelet | `go list -deps ./cmd/funcd/... \| grep -iE 'cri\|kubelet'` | only `oras.land/oras-go/v2/internal/des**cri**ptor` — a substring false-positive, **no real CRI/kubelet** (client API only, per ADR) |

`install --print` emitted a single `[Unit]/[Service]/[Install]` unit with one `ExecStart=/usr/local/bin/funcd`,
`KillMode=control-group` (reaps the managed containerd child — no separate containerd unit), `User=root`, and the
version stamp — byte-identical to what the real install would write (shared `renderUnit`).

### ✅ Verified correct (keep it)

- **Contracts match exactly.**
  - `embedimg.Tar(runtime string) (r io.Reader, ok bool)` — `internal/runtime/embedimg/embedimg.go:42`; fresh
    `bytes.Reader` per call, `ok==false` for unknown runtime (falls through to override), no panic on the
    theoretically-impossible read miss.
  - `ctrmanager.Manager{ Ensure(ctx) (string, error); Close() error }` and `Config{ ExternalSocket, DataRoot,
    ImageOverride }` — `internal/runtime/ctrmanager/manager.go:27-48` — match the ADR's Contracts verbatim.
  - `installCmd`/`uninstallCmd` cobra commands present (`newInstallCmd`/`newUninstallCmd`,
    `cmd/funcd/install.go:93,122`), registered in `newRootCmd` (`cmd/funcd/main.go`).
- **Every non-gated Scenario has a named, un-skipped, passing test.**
  - `system-containerd-override` → `TestSystemContainerdOverride` (returns the external socket as-is, builds
    `*externalManager`, no child) — `manager_test.go:11`.
  - `embedded-image-no-registry` (embed half) → `TestEmbeddedImageNoRegistry` + negative
    `TestUnknownRuntimeHasNoEmbeddedTar` + `TestTarReturnsFreshReader` — `embedimg_test.go`.
  - `one-service-install` (`--print` half) → `TestOneServiceInstallPrint` (asserts exactly one `ExecStart`,
    `KillMode=control-group`, touches nothing) — `install_test.go:14`.
  - `version-locked-shim` (unit facet) → `TestInstallPrintIsVersionStamped` — `install_test.go:51`.
  - `lighter-curated-bases` → enforced by `TestScenarioCuratedImageBuilds` asserting
    `FROM gcr.io/distroless/nodejs22-debian12` + `CMD ["/opt/funcd/shim.mjs"]` — `internal/function/shim_test.go`.
- **Deferred Scenarios genuinely OS-gated, not silently dropped.** The private-containerd supervision (start →
  socket health-wait → `client.Import` → crun lay-down) is real Go in `manager_linux.go` behind `//go:build
  linux` + an explicit `os.Geteuid()!=0 → fault.Forbiddenf` root gate; `manager_other.go` returns a clear
  `fault.Unavailablef("private containerd is Linux-only; pass --containerd <socket>")`. `runs-out-of-the-box`,
  the real halves of `embedded-image-no-registry` and `one-service-install` are the deferred `FUNCD_IT=1`/root
  lane per the ADR Test plan. The Linux cross-build proves the supervision code compiles.
- **executionOptions rewiring preserves behavior + wires Close.** `FUNCD_RUNTIME=containerd` still selects the
  containerd driver; `FUNCD_CONTAINERD_SOCKET` now feeds `Config.ExternalSocket` (the system-override escape
  hatch) and still reaches the driver via the socket `Ensure` yields; the new closer (`mgr.Close`) is returned
  and `defer`-wired in `serve` (`cmd/funcd/main.go`), with `noopClose` (never nil) on the process lane and every
  early-return path. Tests updated to the new 3-value signature without weakening assertions.
- **crun licence boundary held.** crun is only ever `exec.LookPath`'d / execed as a separate binary
  (`manager_linux.go:122 layDownCrun`); no crun / runc Go import anywhere — the matches for "crun" are
  doc-comment strings only. Matches the ADR-0011 boundary.
- **Embedded-no-registry by default.** `importImages` (`manager_linux.go:134`) imports each curated tar via
  `client.Import`, skipping any runtime with an `ImageOverride` (the `--image runtime=ref` registry path); a
  runtime in neither embed nor override returns `fault.NotFound` — no silent miss, no default registry pull.
- **ADR-0002 conventions hold.** No `any`/`interface{}` in the new APIs; no `panic`/`fmt.Print*` in shipped code;
  `api/fault` used throughout; ctx-first on `Ensure`/`importImages`; one-file-per-driver for the OS split
  (`manager_linux.go`/`manager_other.go`); the two `//go:embed` static lookup globals carry justified
  `//nolint:gochecknoglobals` matching the sanctioned pattern.
- **Tree matches the ADR's repository surface.** `embedimg/` (+ README + two placeholder tars + test),
  `ctrmanager/` (manager + linux/other split + test), `cmd/funcd/install.go` + test, `images/runtime/python314/
  Dockerfile`, the nodejs22 rebase, justfile + scripts/build.sh release wiring. Nothing missing; nothing
  unexplained-extra.
- **Placeholder honesty (the documented dev-build pattern, NOT a defect).** `nodejs22.tar`/`python314.tar` are
  ~630-byte stand-ins, explicitly labeled as `<1 KB` placeholders in the package doc (`embedimg.go:7-13`) and
  `README.md`; `Tar` returns a non-empty reader (the ADR's non-gated bar). Real per-arch distroless tars are
  built by `just build-runtime-images` (docker, gzip -9, per-arch) at release time, not committed, not in ci —
  exactly the ADR's design.
- **Tracking correct.** ADR-0054 substance unchanged; the only diff is `Status: Accepted → Reviewing` (the
  builder's one permitted edit). F12 feat row already reads `implemented` and links ADR-0054. No new Go dep.
  (The silent no-dev-machine-leak check passed.)

### Minor / nits (non-blocking, not counted against pass)

- `install.go:83` defines a local `stringBuilder` io.Writer instead of reusing `strings.Builder` (which already
  satisfies `io.Writer`); a small reinvention, lint-clean and harmless. Attribution: **model** (style nit only).

### Definition of Done

**12 / 12** hold (ADR Review-checklist + applicable generic DoD):

1. node on distroless/nodejs22, python on custom distroless 3.14, artifact still read-only bind-mount — ✅ (Dockerfiles + `TestScenarioCuratedImageBuilds`).
2. per-arch image `go:embed`'d + imported at startup, no registry pull by default, `--image` override works — ✅ (`importImages`, `imageOverrides`).
3. private managed containerd default (private socket + data-root, supervised); `--containerd` uses system one — ✅ (`manager_linux.go`, `externalManager`).
4. crun bundled + laid down, execed separate binary, no GPL linkage — ✅ (`layDownCrun`, no Go import).
5. `funcd install` writes one `funcd.service` (containerd as child), `--print` dry-runs, `uninstall` reverses, idempotent — ✅ (`install.go`, `--print` verified).
6. execution mechanics (netns, lateral-deny, fixed-port, bind-mount) unchanged from ADR-0011/0032 — ✅ (no touch to the driver execution path; only the dialed socket changed).
7. non-gated unit tests pass in ci; real-container scenarios deferred to integration lane; no identity/path leak — ✅.
8. full suite green (`go build`/`test`/lint/`mod verify` all exit 0) — ✅.
9. real behaviour, no stubs in the shipped path (the non-Linux `privateManager` is an honest platform-gate error, not a shipped stub) — ✅.
10. contracts honoured (signatures verbatim) — ✅.
11. tree matches repository surface — ✅.
12. tracking (module path, ADR at Reviewing substance-unchanged, no new dep) — ✅.

No misses. (The real container halves are *deferred*, not *failed* — attributed to the ADR's own sequencing,
not the model.)

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0054 (implementation) → pass, 0/0/1, 1 model-attributed (the
`strings.Builder` style nit), DoD 12/12. See docs/reviews/model-scorecard.md.

### Recommendation

Sign off. The non-gated DoD is fully met with green evidence, the Contracts match exactly, the existing
`FUNCD_RUNTIME=containerd` path is preserved and the manager `Close` is wired into shutdown, the crun licence
boundary holds, and the real container bring-up is correctly Linux+root-gated and deferred per the ADR's Test
plan — verified by the green Linux cross-build. **Verified-real now:** the embed/Tar contract, the external-socket
override, `install --print`, the executionOptions rewiring, all OS-gating, and the build/lint/test gates.
**Correctly-deferred** (cannot run on darwin, by the ADR's own design): private containerd start/health/stop,
`client.Import` of real images, crun lay-down, real systemd install — the `FUNCD_IT=1`/root integration lane.
Stamp ADR-0054 `Reviewing → Implemented`; F12 stays `implemented`. The one nit (local `stringBuilder`) is
optional cleanup for the builder, not a sign-off blocker.
