# ADR-0026: Packaging & release — single binary, version stamping, systemd unit, install docs

- **Status**: Implemented
- **Date**: 2026-06-15 (**Implemented 2026-06-15** · **Accepted 2026-06-15** after judge pass — no Blockers left open. Folded the judge's
  **Blocker** (B1: the stamp `var` block needs the `//nolint:gochecknoglobals` the repo requires — same inline
  form as `api/fault/problem.go`/`pkg/sdk/kinds.go` — added to Decision §1 + Contracts) and **Major** (M1:
  pinned `funcd version` to `fmt.Fprintln(os.Stdout, …)` per the `cmd/funcdcli` precedent since `fmt.Print*` is
  banned, + the `String()` format stated once). Minors: clarified `Get()` always fills GoVersion/Platform from
  the runtime while Version/Commit/Date are the static stamp seam; noted `internal/version` is a stdlib-only
  leaf importable by `cmd/**`+`pkg/**`. Decision unchanged: `internal/version` ldflags seam + `funcd version`
  + `scripts/build.sh` (pure-Go dev / cgo+slatedb release) + a hardened `funcd.service` + Debian/RHEL install
  docs; the stamp proof is a pure-Go-CI shell-out. No new deps.)
- **Deciders**: green-0-rabbit
- **Tags**: packaging, release, systemd, version, ldflags, single-binary, ops
- **Realizes**: [FEAT-0000/F19](../feat/0000-feat-v1.md) (packaging: static single binary, systemd unit, install docs — Debian/RHEL)
- **Relates to**: [ADR-0014](0014-platform-facade-lifecycle-harness.md) (`cmd/funcd` — the thin shell this
  packages + stamps), [ADR-0006](0006-metastore-engine-and-state-management.md) (the cgo/slatedb static-link
  the release build links — `CGO_ENABLED=1`, the `slatedb_uniffi` archive), [ADR-0001](0001-project-setup-and-structure.md)
  (the repo layout + `justfile` the release recipe extends), [ADR-0025](0025-testing-strategy-and-e2e-harness.md)
  (the CI gate the stamping proof runs under), [ADR-0002](0002-source-code-conventions-and-patterns.md)
  (typed surface, `log/slog`, no globals — except the one sanctioned ldflags-stamp seam)

## Context & Need

FEAT-0000/F19 closes the exit criterion's first clause — *"on a fresh Linux box, one binary + systemd
unit"*. Everything compiles and runs (P-R/P-S done); what's missing is the **deliverable**: a built,
version-stamped single binary, a hardened systemd unit to run it as a service, a build script that produces
it (with the ADR-0006 cgo static-link for the real release), and install docs for Debian/RHEL.

The blueprint fixes the shape: `internal/version/version.go` "filled by `-ldflags` at build time";
`configs/systemd/funcd.service`; `scripts/build.sh` "single binary + version stamping (cgo: static-links
`slatedb_uniffi` — ADR-0006)"; `cmd/funcd` is the thin shell. funcd is a **single binary** (no longer
pure-Go static once slatedb is linked) — the default dev build stays pure-Go (memory store + process
runtime), and the release build turns on cgo + `-tags slatedb`.

This ADR decides the packaging: the version-stamp seam (the one Go-testable core), the `funcd version`
surface, the build script, the systemd unit, and the install docs — one topic at one altitude ("how funcd is
built, stamped, shipped, and run as a service"). It is the terminal V1 deliverable; the critical-path tail
`P-S → P-T` ends here.

## Scope

- **In**: `internal/version` (ldflags-stamped build info + `Get()`/`String()`); a `funcd version` subcommand
  + a one-line startup version log; `scripts/build.sh` (the single-binary build with version stamping,
  documenting the cgo/slatedb release link); `configs/systemd/funcd.service` (a hardened unit); install docs
  (Debian/RHEL); a `just release` recipe.
- **Out**: distro **package** building (`.deb`/`.rpm`, `nfpm`/`goreleaser`) — V1 ships a static binary + unit
  + docs; that's a follow-up. Also out: container/OCI images, binary signing/notarization, the curated-runtime
  images (F12/F13 lane), `funcdcli version` (the CLI's own lane — `internal/version` is ready for it), and
  multi-arch matrices beyond linux/amd64+arm64 (a build-matrix follow-up).

## Constraints & Decision drivers

- **Single binary** (blueprint): one artifact; the dev build is pure-Go (CI-buildable on macOS), the release
  static-links `slatedb_uniffi` (ADR-0006, `CGO_ENABLED=1 -tags slatedb`) — the script documents both.
- **Stamp without globals-as-config** (ADR-0002 bans mutable globals): the `var Version/Commit/Date` in
  `internal/version` are the one **sanctioned** ldflags-stamp seam (write-once at link time, read-only after) —
  documented as the deliberate exception, defaulting to `dev`/`none`/`unknown` so an un-stamped build is honest.
- **CI-provable** (ADR-0025): the stamping must be proven by a test that runs in pure-Go `just ci` — a
  shell-out that builds `cmd/funcd` with real `-ldflags -X` and asserts `funcd version` reports the value
  (mirrors the lint-fixture shell-outs). `cmd/funcd` builds pure-Go, so this runs on macOS CI.
- **Hardened by default** (the blueprint security model): the systemd unit ships with the standard sandboxing
  directives on (NoNewPrivileges, ProtectSystem, etc.) — a service that's safe out of the box.

## Scenarios

- **scenario: version-defaults-unstamped** — *Given* a build with no `-ldflags`, *when* `version.Get()` is
  read, *then* it returns the honest defaults (`Version=="dev"`, non-empty `Commit`/`Date`/`GoVersion`/`Platform`).
- **scenario: version-string-well-formed** — *Given* a `version.Info`, *when* `String()` is called, *then* it
  is a single human line containing the version, commit, date, Go version, and OS/arch.
- **scenario: funcd-version-subcommand-stamped** — *Given* `cmd/funcd` built with `-ldflags "-X …Version=v9.9.9"`,
  *when* it is run as `funcd version`, *then* it prints `v9.9.9` and exits 0 **without** starting the platform
  (a shell-out test that proves the stamp end-to-end, in pure-Go CI).
- **scenario: packaging-artifacts-present** — *Given* the repo, *when* a guard reads `configs/systemd/funcd.service`
  and `scripts/build.sh`, *then* the unit has `ExecStart` + the key hardening directives and the script stamps
  via the `internal/version` ldflags path — a drift guard that the shipped artifacts stay coherent.

## Decision

### 1. `internal/version` — the stamped build-info core (the Go-testable seam)
```go
package version

// Stamped at link time via -ldflags "-X github.com/green-0-rabbit/funcd/internal/version.Version=…".
// These are the ONE sanctioned ldflags-stamp seam (write-once at link, read-only after) — the
// ADR-0002 no-mutable-globals exception. Honest defaults make an un-stamped build obvious. The
// //nolint is REQUIRED (gochecknoglobals fires on these names) — same inline form as the lookup-table
// globals in api/fault/problem.go and pkg/sdk/kinds.go.
//
//nolint:gochecknoglobals // the one sanctioned ldflags-stamp seam (write-once at link, read-only after) — ADR-0002 §5 exception
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

type Info struct { Version, Commit, Date, GoVersion, Platform string }

// Get always fills GoVersion (runtime.Version()) + Platform (GOOS/GOARCH) from the runtime; Version/
// Commit/Date are the static stamp seam, so an un-stamped build returns the literal "dev"/"none"/"unknown".
func Get() Info
// String is one human line: "funcd <version> (commit <c>, built <d>, <go>, <os>/<arch>)".
func (i Info) String() string
```

### 2. `cmd/funcd version` + startup log
The thin shell gains a tiny arg check **before** assembling the platform: `funcd version` (or `-version`)
does `fmt.Fprintln(os.Stdout, version.Get().String())` and exits 0 (using `Fprintln` to stdout — the
`cmd/funcdcli` precedent — since forbidigo bans bare `fmt.Print*`); otherwise the platform runs as today,
logging the version once at startup (`slog … "funcd starting" version=…`). No business logic added — it
stays a shell.

### 3. `scripts/build.sh` — the single-binary build
A POSIX `sh` script that computes `VERSION`/`COMMIT`/`DATE` (from `git describe`/`rev-parse`/`date -u`, with
fallbacks) and builds `cmd/funcd` with `-ldflags "-X …version.Version=$VERSION -X …Commit -X …Date"`. It
defaults to the **pure-Go** build (`CGO_ENABLED=0`) and documents the **release** invocation
(`CGO_ENABLED=1 go build -tags slatedb …` after `just slatedb-lib`, static-linking `slatedb_uniffi` per
ADR-0006). Output: `dist/funcd`. A `just release` recipe wraps it.

### 4. `configs/systemd/funcd.service` — the hardened unit
A `Type=simple` unit running `funcd` as a dedicated `User=funcd`, `Restart=on-failure`, with the standard
sandboxing on: `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `StateDirectory=funcd`
(writable state), `AmbientCapabilities=` empty by default. A `# requires CAP_NET_ADMIN for sandbox netns`
note documents what a real runtime lane adds (deferred to F12/F13).

### 5. Install docs — `docs/install.md`
Debian/RHEL install: drop the binary in `/usr/local/bin`, the unit in `/etc/systemd/system`, create the
`funcd` user + `StateDirectory`, `systemctl enable --now funcd`; plus the `scripts/build.sh` build-from-source
path and the cgo/slatedb note. Linked from the README.

## Contracts

### `internal/version` (`internal/version/version.go`)
```go
//nolint:gochecknoglobals // sanctioned ldflags-stamp seam — ADR-0002 §5 exception (see Decision §1)
var ( Version = "dev"; Commit = "none"; Date = "unknown" )
type Info struct { Version, Commit, Date, GoVersion, Platform string }
func Get() Info               // GoVersion/Platform from runtime; Version/Commit/Date from the stamp seam
func (i Info) String() string // "funcd <version> (commit <c>, built <d>, <go>, <os>/<arch>)"
```
### `cmd/funcd` (`cmd/funcd/main.go`)
```go
// `funcd version` / `funcd -version` → prints version.Get().String(), exits 0, no platform start.
```
### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `internal/version`, stdlib `runtime`/`os`/`fmt`; `git`/`go` (build-time, in the script) | `internal/version` is a **stdlib-only leaf** (no `internal/**` imports — not under `platform/`), importable by `cmd/**` and `pkg/**` |
| Adds (lib) | none | ldflags stamping + a shell script + a unit file |
| Exposes | `version.Get()`/`Info`; `funcd version`; `scripts/build.sh`; `configs/systemd/funcd.service`; `docs/install.md`; `just release` | `funcdcli version` is a ready follow-up (internal/version reusable) |

## Implementation plan

1. **`internal/version/version.go`** — the `Version/Commit/Date` stamp vars + `Info` + `Get()` (fills
   `runtime.Version()` + `runtime.GOOS/GOARCH`) + `String()`.
2. **`cmd/funcd/main.go`** — the `version` arg check before `funcd.New(...)`; the startup version log line.
3. **`scripts/build.sh`** — the stamping build (pure-Go default + documented cgo/slatedb release path); `chmod +x`.
4. **`configs/systemd/funcd.service`** — the hardened unit.
5. **`docs/install.md`** — Debian/RHEL install + build-from-source; link from `README`.
6. **`justfile`** — a `release` recipe wrapping `scripts/build.sh`.
7. **Test plan** (one named test per Scenario):
   - `internal/version/version_test.go` → `version-defaults-unstamped`, `version-string-well-formed`.
   - `cmd/funcd/version_test.go` → `funcd-version-subcommand-stamped` (shell-out: `go build -ldflags` →
     run `funcd version` → assert the stamp; `-short`-skippable).
   - `internal/version/artifacts_test.go` (or in cmd/funcd) → `packaging-artifacts-present` (reads the unit +
     script from disk; no new dep).
8. **Definition of done**: `just ci` green (four sub-checks); `funcd version` reports a stamped build; the
   unit + script + docs exist and are coherent (guarded); no new dependency; no globals beyond the sanctioned
   stamp seam; no identity/path leak.

## Review checklist

- [ ] **Version stamps end-to-end** (`funcd-version-subcommand-stamped`): a real `-ldflags -X` build makes
      `funcd version` report the value; un-stamped → honest `dev` defaults (`version-defaults-unstamped`);
      `String()` is well-formed (`version-string-well-formed`).
- [ ] **`funcd version` is a clean shell add**: prints + exits 0 **without** starting the platform; the
      startup path logs the version once; `cmd/funcd` still holds no business logic.
- [ ] **Artifacts present + coherent** (`packaging-artifacts-present`): `configs/systemd/funcd.service` has
      `ExecStart` + the hardening directives (NoNewPrivileges/ProtectSystem/…); `scripts/build.sh` stamps via
      the `internal/version` ldflags path + documents the cgo/slatedb release link; `docs/install.md` covers
      Debian/RHEL.
- [ ] The stamp vars are the **only** mutable globals (the sanctioned ADR-0002 exception, documented); built
      pure-Go in CI; **no new dependency**; deferrals (.deb/.rpm, images, signing, `funcdcli version`)
      recorded; no identity/path leak; every Scenario a named passing test.

## Consequences

- (+) **The V1 deliverable exists**: a version-stamped single binary + a hardened systemd unit + install docs
  — the exit criterion's "one binary + systemd unit". The critical-path tail `P-S → P-T` is complete.
- (+) **Honest, provable versioning**: `internal/version` defaults make an un-stamped build obvious, and the
  shell-out test proves real ldflags stamping in pure-Go CI — not an assertion.
- (+) **Safe-by-default service**: the unit ships hardened; the cgo/slatedb release link is documented, not a
  surprise.
- (−) **No distro packages (.deb/.rpm) or images in V1** — a static binary + unit + docs is the shipped form;
  packaging tooling (nfpm/goreleaser) + images are bounded follow-ups.
- (note) **Roadmap build edges**: P-T's edges are `P-R` (the CLI/SDK that ship alongside) and `P-S` (the CI
  gate the stamp test runs under); it links `cmd/funcd` (ADR-0014) and the ADR-0006 cgo note. Step-6 records this.

## Temporary workarounds

None. The V1 cuts (.deb/.rpm, images, signing) are bounded scope reductions with named follow-ups, not
stopgaps. The pure-Go default build is the real dev artifact; the cgo/slatedb release path is documented and
real (ADR-0006), gated on the `slatedb_uniffi` archive the existing `just slatedb-lib` recipe builds.

## Alternatives considered

- **goreleaser / nfpm for full .deb/.rpm + multi-arch now** — the natural release tooling, but it pulls a
  heavyweight build dependency + config for a V1 whose exit criterion only asks for "one binary + systemd
  unit". Rejected for V1 as scope; the `build.sh` + ldflags seam is what goreleaser would wrap, so it's a
  clean later adoption, not a rewrite.
- **`runtime/debug.ReadBuildInfo()` instead of ldflags `-X`** — zero-stamp (Go embeds VCS info
  automatically). Rejected as the *primary*: `ReadBuildInfo` VCS stamping is absent in many build modes
  (`go build` from a dirty tree, `-buildvcs=false`, cross-compile caches) and can't carry a release *version*
  tag distinct from the commit; explicit `-ldflags -X` is deterministic and is what `build.sh` controls.
  (`Get()` may *augment* with `ReadBuildInfo` later, behind the same `Info`.)
- **A `version` field in a config file** — rejected: the version is a property of the *build*, not the
  deployment; baking it at link time is what makes a downloaded binary self-describing.
- **`funcdcli version` in this ADR** — deferred: `internal/version` is the shared source and is ready for it,
  but the CLI's surface is ADR-0024's lane; adding the verb there is a trivial additive follow-up.

## Open questions

| Question | Where it gets answered |
|---|---|
| `.deb`/`.rpm` packaging + multi-arch matrix (nfpm/goreleaser) | a release-tooling follow-up over the `build.sh`/ldflags seam |
| Container/OCI images + signing/notarization | a follow-up (V1 is a host binary + systemd) |
| `funcdcli version` | additive, ADR-0024's CLI lane (internal/version is ready) |
| `CAP_NET_ADMIN` / sandbox netns in the unit | the runtime lane (F12/F13) — the unit notes the seam |

## References

- [blueprint.md](../../blueprint.md) — "single binary" + the cgo/slatedb static-link (ADR-0006);
  `internal/version/version.go` "filled by -ldflags"; `configs/systemd/funcd.service`; `scripts/build.sh`.
- [ADR-0014](0014-platform-facade-lifecycle-harness.md) — `cmd/funcd`, the thin shell this stamps + packages.
- [ADR-0006](0006-metastore-engine-and-state-management.md) — the slatedb cgo static-link the release build links.
