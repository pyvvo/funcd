## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1445 fix, model: claude-opus-5-5)

This report reviews branch `fix/1445-flag-defaults-resolved-at-run`, commit a9e2350f `fix(funcdctl): a flag's default is
the same on every machine and holds no variable's value`, on `origin/main` 674e79b5 (`git diff origin/main...HEAD`: 4
files, 141 insertions, 18 deletions). Issue #1445: four funcdctl flags took their default from the machine when funcdctl
built its command tree (`--server`, `--token` and `bench --data-plane` from their variables, `pull --platform` from the
host's architecture), so their help differed from one machine and one shell to the next. The issue is a `kind/task`
with no Design check: the decider asked on 2026-11-12 for it to go through `/fix` without an ADR.

This review ran in an agent separate from the session that wrote the fix. Every check below was run, not read.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

None.

### Routed (not this work's)

None.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** With `origin/main`'s `cmd/funcdctl/cli.go`,
  `bench.go` and `plugin.go` as an overlay, `TestIssue1445_FlagDefaultsAreTheSameOnEveryMachine` fails at its first
  assertion: the root's help, with the variables set, shows the value of `$FUNCD_SERVER` as the default of `--server`
  (`funcdctl  --help holds $FUNCD_SERVER`). `TestIssue1445_CommandsReadTheVariablesWhenTheyRun` passes on the pre-fix
  code, as it should: it pins what must not change.
- **It passes with the fix**, un-skipped, under `-race`: `--- PASS: TestIssue1445_FlagDefaultsAreTheSameOnEveryMachine
  (0.05s)` and `--- PASS: TestIssue1445_CommandsReadTheVariablesWhenTheyRun (3.42s)`.
- **The user-visible behavior is fixed.** Two scratch builds of funcdctl, one with `origin/main`'s three files and one
  from the branch, each printed `--help` for all 61 commands of the tree (the root, every subcommand, and cobra's help
  and completion commands), once with the three variables unset and once with them set to neutral values
  (`http://server-from-env.example`, `http://data-plane-from-env.example`, and one for the token).
  - Before the fix, all 61 helps differed between the two runs. Each one showed the value of `$FUNCD_SERVER` as the
    default of `--server`, `bench --help` showed the value of `$FUNCD_DATA_PLANE` as the default of `--data-plane`, and
    `pull --help` showed `(default "linux/arm64")` on the arm64 host this review ran on.
  - With the fix, none of the 61 helps differs, none holds a variable's value, `--server` reads `($FUNCD_SERVER, else
    http://localhost:8080)`, and `pull --platform` reads `(linux and this machine's architecture when not given)`. The
    only architecture left in any help is `push --platform`'s fixed list, `linux/amd64 or linux/arm64`.
- **What a command does did not change.** The same run-time probes gave the same output on both builds:
  - the fixture plugin (`funcdctl hello env`) is handed `FUNCD_SERVER=http://localhost:8080` and no token when nothing
    is set; the variable's server and a token when the variables are set; no token with `--token ""`; and the flag's
    server with `--server`;
  - `get functions` targets the server that `$FUNCD_SERVER` names, and a `--server` given after the verb wins over it;
  - `bench -f hello -n 1` targets `$FUNCD_DATA_PLANE/function/hello`, and `--data-plane ""` is refused with `specify
    --url, or --function together with --data-plane`;
  - `pull --platform ""` is refused on `--platform`, as before.
- **Mutants: 7 of 7 killed.**
  - M1: `flagOrEnv` ignores a flag the command line gives. It fails `TestIssue1445_CommandsReadTheVariablesWhenTheyRun`
    (`--token ""` and `--server`), `TestPlugin_FlagsBeforeTheNameAreFuncdctls`,
    `TestPlugin_EnvNamesTheCallingFuncdctlAndWhatItResolved` and `TestPlugin_CallsBackThroughFuncdctlWithTheResolvedToken`.
  - M2: `flagOrEnv` ignores the variable and the default. It fails `TestIssue1445_CommandsReadTheVariablesWhenTheyRun`,
    `TestPlugin_EnvNamesTheCallingFuncdctlAndWhatItResolved` and `TestPlugin_CallsBackThroughFuncdctlWithTheResolvedToken`.
  - M3: `pull` sets the host's platform even when `--platform` is given. It fails
    `TestIssue1445_FlagDefaultsAreTheSameOnEveryMachine` (`--platform ""` is no longer refused) and
    `TestCLIPushPlatformAndIndex`.
  - M4: `pull` never sets the host's platform. It fails `TestIssue1445_FlagDefaultsAreTheSameOnEveryMachine` (a pull
    without `--platform` fails).
  - M5: `findPlugin` reads the flags' values again, as on `origin/main`. It fails
    `TestPlugin_EnvNamesTheCallingFuncdctlAndWhatItResolved`: the plugin is handed an empty `FUNCD_SERVER`.
  - M6: `bench` reads only its flag. It fails `TestIssue1445_CommandsReadTheVariablesWhenTheyRun`.
  - M7: `connection` passes no default server. It fails `TestPlugin_EnvNamesTheCallingFuncdctlAndWhatItResolved`.
- **Cause, not symptom.** The defaults were computed from the variables and from `goruntime.GOARCH` when the tree was
  built, so cobra printed them. Now each of the four defaults is empty and the value is resolved when the command
  runs. `flagOrEnv` (`cmd/funcdctl/cli.go:94`) returns the flag when the command line gives it (pflag's `Changed`, so an
  empty value counts), else the variable when it is not empty, else the default. `connection` (`cli.go:88`) applies it
  to `--server` and `--token`, with the named constant `defaultServer` (`cli.go:38`). `bench` applies it to
  `--data-plane` (`bench.go:31`), and `pull` sets `linux/` and the host's architecture only when `--platform` was not
  given (`cli.go:370`). No retry, timeout, skip or swallowed error was added.
- **One resolver.** `connection` serves `sdkClient` (`cli.go:78`) and `findPlugin` (`plugin.go:56`), and `bench` uses
  `flagOrEnv` too, as the issue asks and as ADR-0319's driver "One resolution" requires. `findPlugin`'s own flag set
  adds the root's persistent flags with `AddFlagSet`, which shares their `*pflag.Flag` values, and pflag v1.0.10's
  `FlagSet.Set` marks `Changed` on the shared flag. A flag parsed before a plugin's name therefore counts as given.
- **Scope.** Every hunk serves the issue: the resolver and its three callers, the four flag definitions and their usage
  texts, and the two tests. No test was weakened or removed. The `(ADR-0145)` of the `pull --platform` usage text is
  gone, as the issue asks, and no new usage text names an ADR. The text says "when not given" instead of the issue's
  "when empty", which is the more exact rule, because `--platform ""` is refused (`cli_test.go:251`).
- **Reuse, no duplication.** `flagOrEnv` builds on the package's `envOr`. No helper in `cmd/`, `internal/` or `pkg/`
  already resolves a flag over a variable, pflag has no environment binding, and viper stays out of funcdctl (ADR-0042
  Scope). The tests reuse `execCLI`, `runFuncdctl`, `sysPath`, `newTestServerVia`, `devToken` and `pushPlatform`, and add
  only `helpByCommand`, the walk over the tree that the first test needs.
- **ADRs.** No Accepted or Implemented ADR is contradicted, and no ADR file changed.
  - ADR-0042 Decision 1 and `scenario: cli-flag-compat`: `--server` and `--token` stay persistent root flags that read
    `$FUNCD_SERVER` and `$FUNCD_TOKEN` and work before or after the verb.
  - ADR-0145 Decision 3 and its Contracts (`# default linux/<host arch>`): a pull without `--platform` still pulls
    `linux/` and the host's architecture.
  - ADR-0319's Contracts table (`--server`, else `$FUNCD_SERVER`, else `http://localhost:8080`; `--token`, else
    `$FUNCD_TOKEN`, absent when that is empty) is exactly what `connection` returns.
- **Registry and living docs.** No row of `docs/behavior/` cites the changed lines or #1445. The funcdctl chapter's rows
  "Flags before a plugin's name" and "What a plugin is handed" still hold, as the plugin probes show. `go run
  ./scripts/behavior -check` passes: the new string constant adds no generated value. No guide page, lane suite or
  script asserts the old help text.
- **Siblings and coverage.** A scan of the syntax tree of every flag definition in `cmd/` (`cmd/funcd`,
  `cmd/funcd-rqlite` and `cmd/funcdctl`, the `-tags dev` files included) finds four non-literal defaults: two durations,
  `config.DefaultImagePrefix` and `devPersistDir`, both constants. No flag default reads a variable or the host any
  more. Every case of the issue has a test: the help of every command (the first test), `pull` without `--platform`
  (the first test), the client's server and token and `bench`'s data plane from their variables, with an empty flag
  winning (the second test), and the plugin's variables (`TestPlugin_EnvNamesTheCallingFuncdctlAndWhatItResolved`,
  which mutants M5 and M7 show guards the new path).
- **Conventions.** `defaultServer` is a named package-level constant. The doc comments of `connection` and `flagOrEnv`
  state the rule and why the default stays empty, and do not narrate the code. Imports are at the top, and no signature
  takes `any`. The first test uses `t.Setenv` and so stays serial; the second calls `t.Parallel()` and runs funcdctl as
  a process with a bare environment, as the plugin tests do.
- **Shape.** One commit for one issue, with the subject `fix(funcdctl): …`, `Fixes #1445` and the `Co-Authored-By`
  trailer.
- **Checks.**
  - `gofmt -l cmd/funcdctl/`: clean. `go build ./...` and a Linux build of `./cmd/funcdctl`: pass.
  - `go vet ./cmd/funcdctl/`, with and without `-tags dev`, on macOS and with `GOOS=linux`: pass.
  - `golangci-lint run` on `./cmd/funcdctl/...` and with `--build-tags dev`, on macOS and with `GOOS=linux`: 0 issues.
  - `go test -race -count=1 ./cmd/funcdctl/`: `ok` in 8.1 s on the default build, and `ok` in 190.8 s with `-tags dev`.
  - `scripts/agent/gate.sh` has not run on this branch yet. It runs once, after this review, with `just ci-full` (the
    e2e suite), the Linux tests, the stress run and the bloat audit, so there is no `.cache/gate/audit.log` to cite.

### Definition of Done

12 / 12 items that apply hold. Item 13 does not apply, because no design spec models funcdctl's flags. Item 7 holds
without a Design check, which a `kind/task` does not carry; its ADR, registry and living-doc parts hold. Item 8 holds
for the checks above; the gate's run is still to come.

### Model scorecard

Recorded: claude-opus-5-5 on #1445 (fix) → pass, 0/0/0, 0 model-attributed, DoD 12/12. See
docs/reviews/model-scorecard.md.

### Recommendation

Pass. Hand back to `/fix` Step 8: run the gate once on the branch, then open the PR with `Fixes #1445` in its body.
