# ADR-0126: funcdctl dev — developer experience (live logs, colored banner, fixed ports, naming)

- **Status**: Implemented
- **Date**: 2026-07-12 (**Implemented 2026-07-12** — RETROACTIVE documentation ADR: these dev-UX decisions were
  made interactively and shipped as additive dev-tooling *during* the ADR-0125 session, after ADR-0125 was
  stamped Implemented/frozen. The implementation + its unit tests (banner, log-styler, name-resolution, tee-sink)
  already landed and are green (both build tags, lint, `go mod verify`); this ADR records the decisions so the
  doc trail is honest. No separate judge/adr-impl-review gate ran — the coverage is the shipped tests, and the
  code predates this ADR rather than being implemented from it.)
- **Deciders**: green-0-rabbit
- **Tags**: dx, tooling, funcdctl, dev, observability
- **Realizes**: FEAT-0001/F91
- **Relates to**: [ADR-0125](0125-funcdctl-dev-local-run.md) (the dev command this polishes) · [ADR-0081](0081-function-log-capture-side-channel-blob.md) (the funclog capture the live stream taps)

## Context & Need

ADR-0125 shipped a working `funcdctl dev` — a function or workflow run locally from source with real contract
enforcement, an S3 frontend, `--persist`, and a process-mode catalog engine. In use, four ergonomic gaps
surfaced and were fixed interactively: (1) the terminal showed **no function logs**, so you couldn't see what a
handler did on invoke; (2) the endpoint print was plain and easy to miss; (3) the gateway/S3 ports were
**random each run**, so invoke URLs weren't reproducible; (4) a single generic `funcdctl.yaml` took its name
from `filepath.Base(dir)`, so running `funcdctl dev` *inside* the dir (path `.`) produced the degenerate name
`dev`. This ADR records those decisions.

## Scenarios

- **scenario: dev-logs-stream** — Given a function running under `funcdctl dev`, When it is invoked and its
  handler calls `context.log(...)`, Then that log line prints to the terminal in real time, tagged by the
  function name (and by step name for a workflow — the observer is platform-wide, not per-function).
- **scenario: dev-logs-colored** — Given the terminal is a TTY, When a log line prints, Then the function name
  is yellow and the severity is colored (INFO blue, WARN yellow, ERROR/FATAL red, DEBUG/TRACE dim); When output
  is piped or captured (non-TTY), Then it is plain text (no ANSI).
- **scenario: dev-fixed-ports** — Given `funcdctl dev --gport 3005 --s3port 3006`, When it serves, Then the
  gateway binds 3005 and the S3 frontend 3006 (and the banner shows those); `0`/unset keeps a random free port.
- **scenario: dev-name-resolution** — Given a dir with a generic `funcdctl.yaml` and no `--name`, When run as
  `funcdctl dev` inside it (path `.`), Then the function is named after the real directory (not `dev`); `--name`
  overrides it; a `<stem>.funcdctl.yaml` always names by its file stem.
- **scenario: dev-example-recipe** — Given `just dev-example <project> [gport] [s3port]`, When invoked, Then it
  resolves the example, builds the `-tags dev` funcdctl (fetching the catalog engine only if the example binds a
  catalog), and runs it from source on the given ports.

## Scope

**In**: the `funcdctl dev` developer experience — live log streaming, colored banner + logs, fixed-port flags,
robust function naming, and the `just dev-example` reproducer. **Out**: production behavior — every addition is
dev-only (behind the `dev` command / `-tags dev`) or nil-defaulted in the platform (`WithLogObserver` is unset
in prod, so logs are only persisted as before). No change to ADR-0125's contracts, fidelity boundary, or the
thin release client.

## Constraints & Decision drivers

- **Dev-only, thin client preserved**: the release `funcdctl` must not fatten — the color library and the dev
  wiring compile only under `-tags dev` / the dev command.
- **TTY-aware**: color on a terminal, plain when piped or captured (pipes and tests stay ANSI-free).
- **No hot-path cost**: no per-log-line terminal re-detection; no adaptive-background query on the log path.
- **Library licensing**: any new dependency is Apache-2.0/MIT (lipgloss is MIT).
- **Additive, behavior-preserving** for the platform: a new option that defaults to the prior behavior.

## Alternatives considered

- **Color library** — chose **charmbracelet/lipgloss** (MIT): borders/layout + adaptive color + a writer-bound
  renderer that auto-plains non-TTY. Rejected: `fatih/color` (colors only, no layout/boxes), `pterm` (heavier,
  more than needed), hand-rolled ANSI (re-implements TTY detection + layout, error-prone).
- **Log streaming mechanism** — chose an **observer tee'd onto the funclog sink** (`WithLogObserver`): logs are
  captured once and both persisted and streamed. Rejected: tailing the blob-persisted log segments (≥10 s lag,
  not real-time) and a bespoke second capture path (duplicates the runtime's log plumbing).
- **Function naming** — chose **manifest stem, else `--name`, else abs-resolved dir**. Rejected: the raw dir
  basename (breaks on `.`), and the handler-file stem (`handler.mjs` → the unhelpful `handler` for every generic
  example).

## Decision

1. **Live log streaming.** `pkg/funcd` gains an additive `WithLogObserver(LogObserver)` that tees a stable
   `LogLine` observer onto the funclog logs sink (`teeLogSink`): every Append both persists (blob, unchanged)
   and streams. `funcdctl dev` installs a mutex-guarded observer that prints each line. Platform-wide, so
   workflow step logs stream too, tagged per step. `LogObserver`/`LogLine` are public and carry no internal
   type (`LogLine` projects the internal `funclog.Entry`).
2. **Colored, boxed CLI (lipgloss).** The banner is a rounded, colored box (accent headers, blue endpoint URLs,
   green service names, amber egress warning). Log lines are colored by severity (function yellow; INFO blue,
   WARN yellow, ERROR/FATAL red, DEBUG/TRACE dim). Both bind a `lipgloss.NewRenderer(a.out)`, so a non-TTY
   writer renders plain. The log styler is built once per session; severities use fixed ANSI colors (no adaptive
   query). lipgloss is a **dev-only** import (`//go:build dev`), so the thin client never compiles it in.
3. **Reproducible ports.** `--gport`/`--s3port` fix the gateway (data-plane, via `WithDataPlaneAddr`) and S3
   frontend ports; `0`/unset keeps the ephemeral free port. They must differ.
4. **Robust naming.** A `<stem>.funcdctl.yaml` names by stem (unchanged). A single generic `funcdctl.yaml` uses
   `--name` if given, else the directory basename **abs-resolved first**, so `.` yields the real dir name.
5. **`just dev-example` reproducer.** One command resolves a bundled example (path / `js|python/<name>` / bare
   unique name), builds the `-tags dev` funcdctl (fetching the catalog engine only when the example binds a
   catalog), and runs it from source. Ports are recipe args (default 3005/3006).

## Temporary workarounds

None. (This ADR is itself the exit for the "dev-UX shipped beyond ADR-0125's frozen scope" debt — it records
those decisions so the four layers are consistent.)

## Contracts

```go
// pkg/funcd (additive; nil in production ⇒ logs are only persisted, no behavior change)
type LogLine struct {
    Namespace, Function, Replica, Invocation string
    Time                                     time.Time
    Severity                                 string // TRACE|DEBUG|INFO|WARN|ERROR|FATAL
    Body                                     string
    Attrs                                    map[string]string
}
type LogObserver func(LogLine)
func WithLogObserver(obs LogObserver) Option
```

**CLI (`funcdctl dev`, `-tags dev`)**: `--gport <int>` · `--s3port <int>` · `--name <string>` (plus the
existing `--persist`/`--persist-to`/`--entry`).

**Dependencies & I/O**: consumes `internal/funclog` (the log sink it tees), the `provider`/data-plane addr
options, and — for rendering — `github.com/charmbracelet/lipgloss` (MIT, **dev-only import**). Exposes the
`WithLogObserver` platform option and the three dev CLI flags. No new production dependency.

## Implementation plan

Already implemented on `feat/funcdctl-contract-codegen` (this ADR is retroactive): log streaming (`76be136`),
lipgloss banner + `--gport`/`--s3port` (`eff457d`), colored logs (`a5d077f`), `just dev-example` (`697adf2`),
naming fix + `--name` (`3f689d7`). Tests: `TestTeeLogSinkStreamsAndPersists` (pkg/funcd), `TestPrintLogFormatsLine`,
`TestScenarioDevEgressNotIsolated` (banner services + egress), `TestResolveDevFunctionsNameFlagOverridesGeneric`,
`TestResolveDevFunctionsGenericDotResolvesToDir` (cmd/funcdctl). Verified green: `go build ./...` and
`-tags dev`, `golangci-lint run ./...` and `--build-tags dev`, `go mod verify`.

## Review checklist

- [x] `WithLogObserver` is additive; production (nil) behavior unchanged — logs only persist.
- [x] Log stream is platform-wide (function + workflow steps), tagged by function/step name.
- [x] lipgloss is a dev-only import; the thin release `funcdctl` does not compile it in.
- [x] Banner + logs are colored on a TTY and plain when piped/captured (verified: no ANSI in captured output).
- [x] `--gport`/`--s3port` bind fixed ports (must differ); `0`/unset keeps a free port.
- [x] Naming: stem → stem; generic → `--name` else abs-resolved dir; `.` yields the real dir name.
- [x] `just dev-example` builds `-tags dev` + runs an example; fetches the catalog engine only when needed.

## Consequences

- (+) `funcdctl dev` is a pleasant local loop: reproducible URLs, a legible services banner, and live colorized
  per-function/-step logs.
- (−) A new public `pkg/funcd` option (`WithLogObserver`) to maintain, and a dev-only dependency (lipgloss +
  its charmbracelet transitive set) in `go.mod` (not compiled into the thin client).

## Open questions

- Quiet the platform's own `slog` daemon lines in dev (WARN+ only) so the terminal shows mostly function logs?
  (Noted; not decided here.)
- Surface the control-plane URL in the banner (for `funcdctl apply`-ing a WorkflowRun)? (Follow-up.)

## References

- [charmbracelet/lipgloss](https://github.com/charmbracelet/lipgloss) (MIT) — terminal styling.
- [ADR-0125](0125-funcdctl-dev-local-run.md) · [ADR-0081](0081-function-log-capture-side-channel-blob.md).
