## Verdict: pass — 0 blockers, 0 majors  (ADR-0055 implementation, model: claude-opus-4-8)

The sustainability harness is correctly folded into a single `funcd bench` cobra verb whose mode is
chosen by mutually-exclusive `--containerd`/`--doctor` flags (bare = the in-process lane), reusing
`internal/bench` unchanged. The standalone `cmd/funcd-bench` is removed, `just bench` retargeted, and
the dead `.golangci.yml` exception dropped while the rule's substance is kept. The full gate passes.

### Verification (captured)

| Check | Command | Result |
|---|---|---|
| build | `nix develop -c go build ./...` | exit 0 |
| test (full) | `nix develop -c go test ./...` | exit 0 (incl. `ok cmd/funcd`) |
| test (`cmd/funcd`) | `go test ./cmd/funcd/...` | exit 0 |
| lint | `nix develop -c go tool golangci-lint run ./...` | exit 0 — `0 issues.` |
| mod | `nix develop -c go mod verify` | exit 0 — `all modules verified` |
| deps in `funcd` | `go list -deps ./cmd/funcd/... \| grep -E 'gopsutil\|fortio'` | MATCHES (fortio.org/*, gopsutil/v4/*) — expected |
| deps absent from `funcdcli` | `go list -deps ./cmd/funcdcli/... \| grep -E 'gopsutil\|fortio'` | EMPTY (exit 1) — expected |

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **Pre-existing `funcd-bench` string in `internal/bench/footprint.go:86`** ("…run `sudo funcd-bench
  --containerd`") now names a binary that no longer exists; the correct invocation is `sudo funcd bench
  --containerd`. · attribution: **adr/sequencing** (not the model) — `internal/bench` is reused *verbatim*
  by ADR-0055's own decision and the file is untouched by this change (`git diff HEAD -- internal/bench/`
  is empty); it falls outside this ADR's repository surface. Worth a follow-up sweep but not this gate's
  fix. · Same for the `internal/bench/mem.go:28` comment ("funcd-bench … carries the shim path as an
  argument") — descriptive, harmless, in an unchanged file.

### ✅ Verified correct (keep it)
- **Single flag-driven verb, not a subcommand tree.** `newBenchCmd` builds one `bench` cobra command;
  `--containerd`/`--doctor` are local bool flags; `runBench` dispatches `doctor → containerd → default`.
  `TestBenchIsOneFlagDrivenVerb` passes and asserts the absence of an "Available Commands:" tree.
  (scenario `bench-is-one-flag-driven-verb`)
- **Mutual exclusion via `api/fault`.** Both flags set ⇒ `fault.Invalidf("funcd bench", …)`; the failure
  is fast and clean. `TestBenchModeFlagsMutuallyExclusive` asserts `fault.KindOf(err) == fault.Invalid`
  and the "mutually exclusive" message. Passes. (scenario `mode-flags-mutually-exclusive`)
- **Doctor checks, never installs.** `runDoctor` probes node / python≥3.14 / containerd components and
  always returns nil (exit 0) even when components are missing; it spins no VM and ships no Lima. It also
  warns on a live daemon by dialing `127.0.0.1:8080` (the platform's default `0.0.0.0:8080` listener,
  `pkg/funcd/funcd.go:49`) — the ADR-0054 private-socket fallback is correctly omitted because ADR-0054
  is not implemented yet. `TestBenchDoctorReportsAndSkips` passes (all three lane lines + "not available"
  + exit 0). (scenario `doctor-checks-not-installs`)
- **Bench libs in `funcd`, confined out of `funcdcli`.** `go list -deps` confirms gopsutil + fortio enter
  `./cmd/funcd/...` (transitively via `internal/bench`) and stay absent from `./cmd/funcdcli/...` — the
  one-axis supersession of ADR-0051 executed exactly, the `funcdcli` half of the confinement kept.
  (scenario `bench-libs-in-funcd-not-funcdcli`)
- **`.golangci.yml` edit is exactly the sanctioned one.** Only the now-dead `!**/cmd/funcd-bench/**`
  negation is removed; the `internal/bench/**` exception and both deny entries (gopsutil, fortio) remain.
  `cmd/funcd/bench.go` imports `internal/bench`, not the libs directly, so it needs no exception — and the
  lint run is clean (`0 issues.`), proving the rule still bites where intended.
- **In-process lane reused unchanged.** `runInProcess` → `bench.Run` / `WriteReport` / pool + python
  comparisons; the flag set is the faithful 1:1 port of the old `cmd/funcd-bench` flags onto the verb.
  No measurement logic was rewritten (Decision: reuse, don't rebuild). The `--containerd` lane wires to
  the existing `bench.RunContainerd` with current flags, with an explicit in-code NOTE that the ADR-0054
  rewire is a later step — matching the orchestrator-confirmed deferral.
- **Conventions (ADR-0002).** No `any` in any port/exported signature (the lone `any` is `logf`'s variadic,
  mirroring `fmt.Fprintf`, in package `main`); no `panic`/`os.Exit` in the verb (errors returned to cobra);
  output goes through the injected `io.Writer` seam, which is why `fmt.Fprintf`/`fmt.Fprintln` are allowed
  (forbidigo permits them and bans only `fmt.Print*`) — the clean lint run confirms it. ctx-first on
  `runBench`/`runInProcess`/`runContainerdLane`.
- **Tree matches the ADR's repository surface.** NEW `cmd/funcd/bench.go` + `bench_test.go`; MODIFIED
  `cmd/funcd/main.go` (one `AddCommand` line), `justfile` (`just bench` → `go run ./cmd/funcd bench`),
  `.golangci.yml` (one negation dropped); DELETED `cmd/funcd-bench/` (gone — `ls` errors, `git` shows
  `D cmd/funcd-bench/main.go`). Nothing missing, nothing unexplained-extra.
- **Tracking.** ADR substance unchanged — the *only* diff vs HEAD is the `Accepted → Reviewing` status
  line. Module path `github.com/green-0-rabbit/funcd` throughout. F27 already `implemented` (shared across
  ADR-0040/0051/0052/0055; ADR-0055 is a packaging refinement — not downgraded).

### Definition of Done
6 / 6 ADR Review-checklist items hold (the real-container containerd e2e is the documented deferral to
the Linux IT lane + ADR-0054, attributed to sequencing, not the model); all applicable generic-DoD items
hold (full suite green, every non-deferred scenario has an un-skipped passing test, real behaviour/no
shipped stubs, contracts honoured, tree matches, conventions, deps sanctioned, no scope creep, tracking).
The two deferred scenarios (`containerd-lane-reuses-embedded-runtime`, `dedicated-box` namespace
isolation) are expected-absent now — they build-depend on ADR-0054 + the Linux IT lane per the ADR's own
Test plan. Misses: none model-attributed.

### Model scorecard
Recorded: claude-opus-4-8 on ADR-0055 (implementation) → pass, 0/0/1, 0 model-attributed,
DoD 6/6. See docs/reviews/model-scorecard.md.

### Recommendation
Sign off. Stamp the ADR `Reviewing → Implemented` (the one forward edit) and leave F27 at `implemented`.
The single Minor (the stale `funcd-bench` invocation strings in unchanged `internal/bench/` files) is not
this gate's fix — fold it into the eventual ADR-0054 containerd-lane rewire or a small docs/string sweep.
