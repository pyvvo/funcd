# ADR-0042: Adopt cobra for the CLI surface (refines ADR-0024)

- **Status**: Implemented
- **Date**: 2026-06-16 (**Accepted 2026-06-16**, **Implemented 2026-06-16** — judge: right decision, honestly grounded (ADR-0024 pre-blessed the swap),
  advance as-is; folded 4 Minors: clarified the SDK-vs-artifact verb reach, scoped the `*sdk.Client` to control-plane
  verbs only (artifact verbs need no server), gave `funcd`'s root an `out` seam.)
- **Deciders**: green-0-rabbit
- **Tags**: cli, cobra, funcdcli, daemon, dx, dependency
- **Realizes**: [FEAT-0000/F18](../feat/0000-feat-v1.md) (`funcdcli`) + [F19](../feat/0000-feat-v1.md) (the `funcd` daemon CLI)
- **Relates to / refines**: [ADR-0024](0024-funcdcli-and-sdk.md) — which built the stdlib `funcdcli` and **explicitly
  deferred cobra as "a clean later swap if the command set grows"** (ADR-0024 §Out of scope; "already in the module
  graph"). This is that swap. Also [ADR-0026](0026-packaging-and-release.md) (the daemon's `version` handling).

## Context & Need

`funcdcli` is a kubectl-style CLI with **eight** verbs (`get`/`describe`/`apply`/`delete`/`push`/`pull`/`login`/`logout`),
each hand-rolling a `flag.FlagSet` + manual positional/flag splitting + usage strings (`cmd/funcdcli/cli.go`). ADR-0024
chose stdlib for V1 but named cobra the swap "if the command set grows" — it has. Hand-rolled dispatch reinvents what a
CLI framework gives for free: subcommands, POSIX flags (`--namespace`/`-n`), generated `--help`, **shell completion**, and
man pages. **cobra** is the Go standard (kubectl, Helm, Docker, `gh`), Apache-2.0, stable (v1.10.2, Dec 2025), and is
**already an indirect dependency** (`go.mod`: `spf13/cobra v1.10.2`, `spf13/pflag v1.0.10`) — adopting it is a promotion,
not a new fetch. The same framework also tidies the `funcd` daemon's hand-rolled `version` dispatch.

## Scenarios

- **scenario: cli-verbs** — *when* `funcdcli <verb> …` runs for each of the eight verbs through the cobra root, *then* it
  behaves exactly as the stdlib CLI did (the ADR-0024 verb scenarios — `cli-apply-then-get`, `cli-delete`, … — still pass).
- **scenario: cli-flag-compat** — *given* the existing short flags (`-n`, `-o`, `-f`, `-u`, `-p`), *when* used, *then* they
  still work (now as pflag shorthands) **and** gain long forms (`--namespace`, `--output`, …); `--server`/`--token` (or
  `$FUNCD_SERVER`/`$FUNCD_TOKEN`) are persistent root flags usable before or after the verb.
- **scenario: cli-help-and-completion** — *when* `funcdcli --help` (or `-h`) runs, *then* cobra lists the commands +
  flags; *and* `funcdcli completion <shell>` emits a completion script — both free from cobra, neither hand-written.
- **scenario: cli-unknown-command** — *when* an unknown verb is given, *then* cobra reports it and exits non-zero.
- **scenario: daemon-version-and-serve** — *when* `funcd version` runs it prints the stamped identity (ADR-0026) and
  exits; *when* `funcd` runs with no subcommand it starts the daemon as before.

## Scope

**In:** promote `spf13/cobra` (+ `spf13/pflag`) to **direct** deps; restructure `cmd/funcdcli` to a cobra root + one
subcommand per verb (reusing the existing SDK/artifact call logic), persistent `--server`/`--token`, the SDK client built
in `PersistentPreRunE`; restructure `cmd/funcd` to a cobra root (`RunE` = serve) + a `version` subcommand. Free help +
completion.

**Out:** `spf13/viper` (config stays env/flag); YAML manifests (still JSON — separate ADR-0024 follow-up); **new** verbs
(logs/scale/watch — their own work); `cmd/funcd-bench` (a dev tool — keeps its small stdlib `flag` set); any SDK
(`pkg/sdk`) change — cobra is a CLI-shell concern only.

## Constraints & Decision drivers

- **Stop reinventing the wheel** (the ask) — subcommands/flags/help/completion are a solved problem; hand-rolling them
  scales badly as verbs grow.
- **Import discipline preserved** — cobra is an external CLI framework, not a funcd internal package, and adds **no new
  internal reach**. The CLI's existing reach is unchanged: `pkg/sdk` for the 4 **control-plane** verbs (get/describe/apply/
  delete) and `internal/artifact` for the 4 **artifact** verbs (push/pull/login/logout — ADR-0031's direct seam, which
  bypass the SDK). pflag replaces stdlib `flag` in the cmds only.
- **Apache-2.0 / MIT-compatible** — cobra Apache-2.0, pflag BSD-3-Clause. Both already vendored (indirect).
- **No behavior regression** — every existing verb + flag keeps working; the change is the dispatch/parse layer.

## Alternatives considered

- **Stay stdlib (status quo)** — rejected: it *is* the wheel-reinvention the ask targets; eight hand-rolled flag sets, no
  completion, no generated help, growing with every verb. ADR-0024 already earmarked the swap.
- **urfave/cli** — lighter, commander.js-feel (MIT). Rejected: cobra is the kubectl-parity *standard* (funcdcli is
  kubectl-shaped), is **already in the module graph**, and ships completion/man-pages — urfave would be a new dep for less.
- **alecthomas/kong** (declarative struct-tags) — elegant + type-safe, but a different paradigm, a new dep, and less
  ubiquitous than cobra. Rejected for V1; cobra's familiarity + zero-new-fetch win.

## Decision

Adopt **cobra** (with **pflag**) as the command framework for `funcdcli` and `funcd`, promoting both to direct deps.

1. **`funcdcli`** — a cobra root `funcdcli` with persistent `--server` (`$FUNCD_SERVER`) + `--token` (`$FUNCD_TOKEN`)
   flags; a testable `newRootCmd(out io.Writer) *cobra.Command` builds it. The `*sdk.Client` is built **only for the 4
   control-plane verbs** (get/describe/apply/delete) — in their own `PreRunE` (not a blanket `PersistentPreRunE`), so the
   4 artifact verbs (push/pull/login/logout → `internal/artifact`) need **no** `--server`/`--token`. One subcommand per
   verb, each declaring its flags via pflag (`-n/--namespace`, `-o/--output`, `-f/--file`, `-u/--user`, `-p/--password`)
   and calling the **unchanged** SDK/artifact logic in its `RunE`. cobra supplies `--help`, the `completion` command, and usage-on-error.
2. **`funcd`** — a cobra root `newRootCmd(out io.Writer)` whose `RunE` runs the daemon (the existing startup, unchanged)
   and a `version` subcommand that prints `version.Get().String()` (ADR-0026) to `out`, replacing the hand-rolled
   `isVersionArg` (the `out` seam makes `daemon-version` testable against a buffer).
3. **Errors/exit** — `RunE` returns the existing `api/fault` errors; `main` calls `Execute()` and maps a non-nil error to
   a non-zero exit (cobra's `SilenceUsage`/`SilenceErrors` set so funcd's own error text is printed once, not cobra's).

## Temporary workarounds

None.

## Contracts

No SDK/API change. CLI shells only:
```go
// cmd/funcdcli: newRootCmd(out io.Writer) *cobra.Command  // root + 8 verb subcommands; testable (SetArgs+Execute)
//   persistent flags: --server (FUNCD_SERVER), --token (FUNCD_TOKEN); the *sdk.Client is built (PreRunE) only for the
//   control-plane verbs (get/describe/apply/delete) — push/pull/login/logout (internal/artifact) need no server.
//   verbs reuse the existing cmdGet/cmdApply/cmdDelete/cmdPush/cmdPull/cmdLogin/cmdLogout logic.
// cmd/funcd: newRootCmd(out io.Writer) *cobra.Command  // RunE = serve (existing daemon); `version` subcommand → version.Get().String() (testable)
// go.mod: spf13/cobra + spf13/pflag promoted indirect → direct (already in go.sum).
```

## Implementation plan

1. **`go.mod`** — `go get github.com/spf13/cobra github.com/spf13/pflag` (promotes the existing indirect entries to direct).
2. **`cmd/funcdcli/cli.go` + `main.go`** — replace `run()`'s switch with `newRootCmd(out)`: root persistent flags +
   `PersistentPreRunE` (build `*sdk.Client`), one `*cobra.Command` per verb whose `RunE` calls the existing per-verb logic
   (kept; only the `flag.FlagSet` parsing is replaced by pflag flags + `cobra.Command.Args`). `main` = `newRootCmd(os.Stdout).Execute()`.
3. **`cmd/funcd/main.go`** — wrap startup in a cobra root `RunE`; add a `version` subcommand; drop `isVersionArg`.
4. **Tests** — `cmd/funcdcli/cli_test.go`: drive `newRootCmd(buf)` via `SetArgs(...)` + `Execute()` (re-expressing the
   ADR-0024 verb scenarios: `cli-apply-then-get`, `cli-delete`, invalid-manifest, etc.) + a `cli-help`/`cli-flag-compat`
   case; `cmd/funcd`: a `daemon-version` test.
5. **Verify**: `go build ./...`, `go vet`, `golangci-lint`, `go test ./cmd/...` green; `go mod verify`/`tidy` clean
   (cobra/pflag now direct); the daemon + cli still behave; no identity/path leak.
6. **Definition of done**: both binaries on cobra; every verb + flag works (short + new long forms); `--help` + completion
   present; ADR-0024's verb scenarios pass via the root command; deps Apache-2.0/BSD; no SDK change.

## Review checklist

- [ ] `funcdcli` is a cobra root + 8 verb subcommands; `--server`/`--token` persistent (+ env); `newRootCmd` testable (`cli-verbs`).
- [ ] Short flags (`-n/-o/-f/-u/-p`) still work + gain long forms (`cli-flag-compat`); `--help` + `completion` present (`cli-help-and-completion`).
- [ ] `funcd` is a cobra root (`RunE`=serve) + `version` subcommand; `isVersionArg` gone (`daemon-version-and-serve`).
- [ ] cobra + pflag promoted to direct deps (Apache-2.0 / BSD-3); `go mod verify`/`tidy` clean; no new *runtime* SDK dep.
- [ ] ADR-0024 verb scenarios pass through the cobra root; no SDK/API change; no identity/path leak.

## Consequences

- (+) Stop reinventing the wheel: subcommands/flags/help/**completion**/man-pages for free; adding a verb is a `cobra.Command`,
  not a new flag-set + switch case. Kubectl-familiar UX. Zero new module fetch (cobra was already vendored).
- (+) One consistent CLI style across `funcdcli` + `funcd`.
- (−) Two deps move indirect→direct (cobra + pflag) — but already in the graph, Apache-2.0/BSD, the ecosystem standard;
  the dependency-minimalism driver of ADR-0024 is consciously relaxed for the CLI (only), as ADR-0024 foresaw.
- (−) Flag parsing semantics shift from stdlib `flag` to pflag (POSIX) — short flags stay compatible; this is a UX gain,
  but any script relying on stdlib-only `-flag=val` quirks should be checked (none known in-repo).

## Open questions

- **`spf13/viper` for layered config** (file + env + flags) for the daemon — deferred; env/flags suffice for V1.
- **YAML manifests** (`apply -f x.yaml`) — still ADR-0024's deferral; orthogonal to cobra.
- **Generated man pages / docs** (`cobra-cli`/`doc.GenMarkdownTree`) into `docs/` — a packaging follow-up (ADR-0026).

## References

- [cobra](https://github.com/spf13/cobra) v1.10.2 (Apache-2.0) · [pflag](https://github.com/spf13/pflag) (BSD-3-Clause). Verified 2026-06-16 (already in `go.mod`/`go.sum`).
- [ADR-0024](0024-funcdcli-and-sdk.md) (the stdlib CLI this refines — pre-blessed the swap) · [ADR-0026](0026-packaging-and-release.md) (daemon version).
