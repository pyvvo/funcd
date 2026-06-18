# ADR-0026 Implementation Review — Packaging & release (model: claude-opus-4-8)

## Verdict: **pass** — 0 blockers, 0 majors, 0 minors  (ADR-0026 implementation, model: claude-opus-4-8)

The packaging deliverable is realized: `internal/version` (the ldflags-stamp seam), `funcd version`, a
version-stamping `scripts/build.sh`, a hardened `configs/systemd/funcd.service`, Debian/RHEL `docs/install.md`,
and a `just release` recipe. The headline claim is **proven, not asserted**: a shell-out builds `cmd/funcd`
with a real `-ldflags -X` stamp (pure-Go, `CGO_ENABLED=0`) and asserts `funcd version` reports it. Both judge
fixes landed (the `//nolint:gochecknoglobals` on the stamp vars — lint is 0 issues; `funcd version` prints via
`fmt.Fprintln(os.Stdout, …)`, not the banned `fmt.Print*`). This closes the exit criterion's first clause
("one binary + systemd unit") and the critical-path tail `P-S → P-T`. No new dependency.

**Reviewed against**: ADR-0026 Contracts/Scenarios/Review-checklist/DoD · blueprint single-binary +
`internal/version`/`systemd`/`build.sh` + the ADR-0006 cgo note · ADR-0014 (`cmd/funcd`), ADR-0002
(no-globals exception), ADR-0025 (CI gate) · FEAT-0000/F19 + the V1 exit criterion.
**Date**: 2026-06-15

## Verification (captured evidence)
| Check | Result |
|---|---|
| `go build ./...` / `go vet ./...` | exit 0 |
| `go test ./...` | PASS (full suite — exit 0, ALL PASS) |
| scenarios | 4/4 PASS (`version-defaults-unstamped`, `version-string-well-formed`, `funcd-version-subcommand-stamped`, `packaging-artifacts-present`) — each a named, un-skipped test |
| stamp proof (manual) | `VERSION=v1.2.3-test ./scripts/build.sh` → `funcd v1.2.3-test (commit 774cdcb, built …, go1.26.4, …)`; `go run ./cmd/funcd version` (un-stamped) → `funcd dev (commit none, built unknown, …)` |
| `golangci-lint run ./...` | **0 issues** — the `//nolint:gochecknoglobals` on the stamp `var` block works (B1 fix verified) |
| `go mod verify` + `git diff go.mod go.sum` | verified; **no diff** (stdlib `runtime`/`fmt`; a shell script + a unit file — **no new dep**) |
| `funcd version` print path | `fmt.Fprintln(os.Stdout, …)` (M1 fix — not banned `fmt.Print*`); exits 0 **before** assembling the platform |
| identity | clean |

## 🔴 Blockers / 🟡 Major / Minor
None.

## ✅ Verified correct — keep it
- **The stamp is proven end-to-end in pure-Go CI** (`cmd/funcd/version_test.go`,
  `funcd-version-subcommand-stamped`): builds `./cmd/funcd` with `-ldflags "-X …internal/version.Version=v9.9.9"`
  (`CGO_ENABLED=0`, so it runs on macOS CI) and asserts `funcd version` prints it. This is the right discipline
  — the packaging claim is executed, not asserted. Keep the `-short`-skippable shell-out shape.
- **The no-globals exception is correctly enforced** (`internal/version/version.go`): the `var Version/Commit/Date`
  carry the `//nolint:gochecknoglobals` (the B1 fix) in the same inline form as `api/fault/problem.go` /
  `pkg/sdk/kinds.go`; lint is 0 issues. Honest `dev`/`none`/`unknown` defaults make an un-stamped build obvious
  (`version-defaults-unstamped`); `Get()` always fills GoVersion/Platform from the runtime.
- **`cmd/funcd` stays a thin shell**: the `version` arg-check (`version`/`-version`/`--version`) prints +
  returns **before** `funcd.New(...)` (no drivers needed); a single startup `slog` line logs the version. No
  business logic added.
- **Honest cgo/slatedb split** (`scripts/build.sh`): default pure-Go (`CGO_ENABLED=0`) build; the release
  static-link (`CGO_ENABLED=1 -tags slatedb` after `just slatedb-lib`, ADR-0006) is **documented**, not
  pretended-built-in-CI.
- **Hardened unit + real install docs** (`configs/systemd/funcd.service`, `docs/install.md`): `NoNewPrivileges`,
  `ProtectSystem=strict`, `PrivateTmp`, `StateDirectory=funcd`, empty `AmbientCapabilities` + the deferred
  `CAP_NET_ADMIN` netns note (F12/F13); Debian/RHEL install/build/uninstall. The `packaging-artifacts-present`
  guard keeps them coherent (drift fails CI).

## Definition of Done
ADR Review-checklist: **4/4** hold (stamps end-to-end + honest defaults + well-formed String · clean shell add
· artifacts present+coherent · sanctioned-globals-only + pure-Go CI + no dep + deferrals + no leak). Scenarios:
4/4 named, un-skipped, passing. ADR substance unchanged beyond the `Accepted→Reviewing` bump.

## Model scorecard
Recorded: claude-opus-4-8 on ADR-0026 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 4/4.
See docs/reviews/model-scorecard.md.

## Recommendation
**pass** → stamp ADR-0026 `Reviewing → Implemented`, feat F19 → `implemented`. Real edges P-R + P-S; links
`cmd/funcd` (ADR-0014) + the ADR-0006 cgo note. This is the **terminal** tier-1 item — the Step-6 roadmap
reconcile graduates ADR-0017–0026 and recomputes the (now complete) critical path.
