# ADR-0255: `funcdctl dev` runs a project's watch builds and reloads its Site pages

- **Status**: Implemented (2026-10-21), review passed on its second round
  (docs/reviews/adr-0255-implementation-claude-opus-5-5.md); Reviewing (2026-10-21), implemented in #1124; Accepted
  (2026-10-20), after two judge rounds, under the decider's "refine and ship it" (#1119); the restart backoff, the 20
  last lines, a taken port's replacement and reading answer 4's "one line per build" as ADR-0253's `pushed` line are the
  drafting agent's choices, brought back to the decider. The judge drew the wait/stop line, kept ADR-0253's output,
  added the crash reap and moved folder mode out.
- **Date**: 2026-10-20
- **Deciders**: green-0-rabbit
- **Tags**: dx, cli, dev, app, site
- **Realizes**: [FEAT-0001/F166](../feat/0001-feat-v1.1.md) (#1119, tracker #1062)
- **Supersedes (in part)**: [ADR-0253](0253-funcdctl-dev-runs-an-app-template.md) Decision 2, under `watch` (a build
  missing at start makes dev wait instead of stopping, Decision 2 here) · ADR-0253 Decision 3, under `liveReload` (a
  Site's build is pushed from a copy carrying one script tag).
- **Relates to**: [ADR-0254](0254-funcdctl-dev-config-file.md) (two App-only keys added to its file) ·
  [ADR-0167](0167-process-worker-crash-recovery.md) (dev's state dir and process registry) ·
  [ADR-0239](0239-site-runtime-configuration.md) (a Site's index copy for `baseHref`) ·
  [ADR-0244](0244-vite-plugin-watch-mode.md) (the Vite plugin's watch mode) · #1118 (a reload waits until its inputs
  stop changing) · #1066 (`vite dev`) · [ADR-0061](0061-funcd-daemon-config-file.md) (its deferred "live reload"
  re-reads the daemon's config, unrelated)

## Context & Need

A developer or an agent runs an App template from its sources with `funcdctl dev` (ADR-0253, ADR-0254), but the
project still runs the builds itself. Its script starts each part's watch build, builds everything once first because
dev stops on a build missing at start (`cmd/funcdctl/dev_app.go:408-474`), and the developer reloads the page after
each web change. This ADR lets `funcdctl.config.yaml` declare those commands, so `funcdctl dev` alone runs the loop,
keeps their output out of dev's terminal, and reloads open Site pages after a Site change. Everything is opt-in
(#1119, decided 2026-10-20).

## Scenarios

Fixture: a copy of the todo App (ADR-0253's) with `web/` absent and a `web-src/build.sh` (a stand-in for
`vite build --watch`): it copies `web-src/site/` into `../web/` at start and, under `--watch`, on each change, prints
one line to standard output and one to standard error per copy, and exits 3 while a `FAIL` file exists. This file is at
the root:

```yaml
dev:
  dir: app
  build:
    - functions/ts
    - functions/py
    - web=web
    - api=functions/ts/api-1.0.0.mjs
    - migrate=functions/ts/migrate-1.0.0.mjs
  watch:
    - name: web
      dir: web-src
      run: sh build.sh --watch
  liveReload: true
```

- **scenario: dev-watch-waits** — *When* `funcdctl dev`, *Then* dev prints the command's log file, starts it, prints
  that it waits for `images.web` naming `web`, and applies the App once `web/` holds a settled build with its index; the
  Site serves it.
- **scenario: dev-watch-quiet** — *Then* neither of the command's lines reaches the terminal, both are in its log
  file, which the banner names, and a save prints dev's lines as ADR-0253 says (`pushed images.web …`,
  `reloaded in <d>`).
- **scenario: dev-watch-restart** — *When* `FAIL` exists, *Then* dev prints the command's last lines and starts it
  again after 1 s; *given* a second entry running `sh build.sh` once, *when* it exits 0, *Then* dev prints that it
  finished and does not start it again.
- **scenario: dev-watch-stop** — *When* dev gets SIGINT, SIGTERM or SIGHUP, or fails after the command started,
  *Then* the command and every process it started stop.
- **scenario: dev-watch-crash-reap** — *Given* dev was killed with SIGKILL, *When* it starts again, *Then* the earlier
  run's command is stopped before the new one starts.
- **scenario: dev-live-reload** — *Then* the Site's index carries one script tag. *When* a save changes the Site's
  build, *Then* a client of dev's stream gets one event once a revision with the new Site build is current (the event
  rule's unit test covers a newer revision replacing it first). *When* only a function build changes, *Then* none.
  *Given* `--persist`, *When* dev restarts with the same builds, *Then* no revision is stamped.
- **scenario: dev-watch-refusals** — *Then* dev stops, naming the key, on `watch` not a list, an unknown key in an
  entry, a `name` not matching or used twice, an empty `run`, a `dir` that is not a directory, and either key when
  `dev.dir` is not a template; and, under `watch`, on a refusal no command can fix, such as a Site key without
  `key=path`.
- **scenario: dev-loop-absent** — *Given* neither key, *Then* dev behaves as ADR-0253 and ADR-0254 say: a build missing
  at start stops it, and the Site serves the build's bytes unchanged.

## Scope

- **In**: `dev.watch` and `dev.liveReload` in App mode; the commands' start, wait, restart, output and stop; the Site
  copy and the event stream.
- **Out**: folder mode (ADR-0247 is unchanged); HMR and `vite dev` (#1066); a command run on a part's change; flags for
  the keys; Windows (no `sh`, no process groups).

## Constraints & Decision drivers

- Opt-in: without the new keys, dev behaves exactly as today (the decider).
- Language-agnostic: dev runs a command and reads the builds it already reads; it parses no tool's output.
- The production path stays untouched: live reload changes only the bytes dev pushes for a Site.
- The processes dev starts outlive it only until its next start (ADR-0167).

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| Chosen: commands supervised by dev; live reload through a Site copy and dev's own stream | `funcdctl dev` alone runs the loop; the platform is untouched | Dev supervises processes, copies a Site's build per push and opens a second loopback port; a build error that does not end its command shows only in the log file | Chosen |
| A. A project script around dev (today) | Nothing to build | Each project writes the same glue; dev stops on a missing build | Rejected |
| B. Each entry names the build it feeds | Waits and labels per build | Couples commands to builds; dev's lookup already names a miss | Rejected by the decider |
| C. The platform's Site reconciler writes the tag, as for `baseHref` | One HTML change path | Dev-only behavior in production code | Rejected by the decider |
| D. Print each command's standard error | Shows a watch build's rebuild error | Floods the terminal (Vite's warnings, Python's `logging`); misses `tsc --watch`, which reports errors on standard output | Rejected: the decider chose output only on failure |
| E. HMR through `vite dev` | Fastest frontend loop | Tool-specific; a dev server replaces the production Site | Out of scope (#1066) |

## Decision

1. **Keys.** `dev.watch` lists entries of exactly `name`, `run` and `dir`; `dev.liveReload` is a bool. Both are
   App-only keys under ADR-0254 Decisions 2 and 3: refused when `dev.dir` is not a template, ignored when a
   non-template argument replaces it. `name` matches `[a-z0-9][a-z0-9-]*` and is unique; `run` is a non-empty shell
   command line; `dir`, relative to the file, defaults to its directory and must be a directory. `--print-env` starts
   no command.
2. **Start and wait.** Dev first runs the checks that need no build: the template, the values, the `--apply` files and
   the Secrets the stand-in render declares, `spec.requires` and the SQL engine. It then starts each command as
   `sh -c <run>` in `dir`, with dev's environment and standard input from `/dev/null`, in its own process group,
   recorded in a registry of dev's process dir (ADR-0167). Where ADR-0253 Decision 2 stops on a build missing, dev
   waits while the lookup misses a build it names (a function build or its contract file, a `key=path`, or a Site folder
   that is absent, or, under `watch` only, empty or without its `spec.index`), printing the miss once with its paths and
   hint, until every build is found and its inputs read the same on two polls (#1118). Every other refusal stops dev as
   today once it holds on a settled poll, and so does a miss once every command has exited 0.
3. **Exit and stop.** A command that exits non-zero is printed with the last 20 lines of its log file and started again
   after 1 s, the delay doubling to 30 s and back to 1 s after a run of 30 s. One that exits 0 is printed as finished.
   Either way, what remains of its process group is killed first, as a worker's exit does (ADR-0011 C4). When dev stops,
   on any signal of `stopsignal.Signals()`, a refusal after the commands started or a platform failure, it sends SIGTERM
   to each command's process group, then SIGKILL after dev's grace (`process.go:31-33`); the registry's guard skips a
   group it does not own or numbered 0 or 1 (`procreg.go:104`). A start reaps what the registry holds from a run that
   did not stop.
4. **Output.** A command's standard output and error go only to its log file, emptied when dev starts. The terminal
   shows dev's lines as ADR-0253's Contracts say, plus the lines below, and never a command's output except those last
   lines.
5. **Live reload.** With `dev.liveReload: true`:
   - dev serves an event stream on a loopback port kept in its process dir: picked free at the first start and reused
     while free, so a restart pushes the same bytes; a taken port is replaced, and that start stamps one Site revision;
   - before pushing a Site's build, dev copies it into its stage dir, keeping and refusing what `packDir` does, and
     inserts the tag
     before `</head>` of the root `spec.index`, found with the `x/net/html` tokenizer as ADR-0239's copy does, or at
     its end without one; the copy is removed after the push;
   - when a revision dev applied becomes current with Site image digests that differ from those at the last event, or
     current when the stream started, dev sends one event, so a follow cancelled by a newer revision loses none; a
     revision made current by `funcdctl app retry` or `rollback` reloads no page;
   - without the key, dev pushes the build as built.
6. **The file.** Both keys are read at start; a change to either prints ADR-0254 Decision 6's restart line.

## Temporary workarounds

None.

## Contracts

| Key | Type | Default |
|---|---|---|
| `dev.watch[].name` | string, `[a-z0-9][a-z0-9-]*`, unique | required |
| `dev.watch[].run` | string, a shell command line | required |
| `dev.watch[].dir` | path to a directory, relative to the file | the file's directory |
| `dev.liveReload` | bool | `false` |

| State, in dev's state dir (ADR-0167) | Holds |
|---|---|
| `process/commands.json` | the commands' process groups (`procreg.Open(<process dir>, "commands")`) |
| `process/logs/<name>.log` | a command's standard output and error, emptied at start |
| `process/livereload.port` | the stream's port |
| `stage/site-<key>-*/` | the Site copy, removed after its push |

- **Stream**: `GET http://127.0.0.1:<port>/events`, `Content-Type: text/event-stream`,
  `Access-Control-Allow-Origin: *`; one `data: reload` message per Site change.
- **Tag**: `<script>new EventSource("http://127.0.0.1:<port>/events").onmessage=function(){location.reload()}</script>`.
  Only the root index carries it, and a changed non-hashed file stays cached up to `max-age=300` after a reload
  (`internal/edge/static/static.go:195-208`).
- **Lines**: `watch <name>: <log file>` as a command first starts; `waiting for <miss>` once per miss; `<name> exited
  <code>; restarting in <d>`, then its last lines, each prefixed `<name> | `; `<name> finished`. The banner adds a
  `WATCH` section of `<name>  <log file>` and a `reload` service line for the stream.
- **Refusals**, naming the file: `dev.watch is not a list`, `dev.watch[<i>]: unknown key <key>`,
  `dev.watch[<i>].name <name> is not [a-z0-9][a-z0-9-]*`, `dev.watch[<i>].name <name> is used twice`,
  `dev.watch[<i>].run is empty`, `dev.watch[<i>].dir <dir> is not a directory`, and ADR-0254's App-only refusal for
  either key. The two Site misses read `images.<key>: <dir> is empty` and `images.<key>: <dir> holds no <index>`.

## Implementation plan

- **funcd**, one PR closing #1119 (`Fixes #1119`), after #1118's:
  - `cmd/funcdctl/devwatch.go`: the registry, start, wait, restart, output and stop of the commands.
  - `cmd/funcdctl/devlivereload.go`: the stream, its kept port, the Site copy and the tag.
  - `cmd/funcdctl/devconfig.go`: the keys, their App-only rules, their refusals and the restart line.
  - `cmd/funcdctl/dev_app.go`: the early checks, the wait before the first push, the copy before `PushSite`, the event
    after the follow; the lookups' misses carry a sentinel error (`errors.Is`) and keep today's messages.
  - Tests (`-tags dev`): one per scenario, named after it, and serial, since they fork; a table test of the refusals;
    unit tests of the tag's insertion (with and without `</head>`, one inside a comment) and of the event rule.
  - `funcdctl dev --help` names the keys.
- **Live check**: experiment 0001 rerun with `watch` running `vite build --watch` for the Site and `liveReload`,
  recording start-to-serving time, a web save to the event, and the terminal lines per save.
- **Done when** `go build`, `go vet`, `golangci-lint` and `go test` pass with `-tags dev` and without, and the live
  check is recorded.

## Review checklist

- [ ] Without `watch` and `liveReload`, dev's behavior, its output and the Site's bytes are unchanged.
- [ ] The early checks run first; commands start in their own process groups, recorded in the registry; dev waits on a
      named miss only, and stops on every other refusal and on a miss once every command exited 0.
- [ ] A non-zero exit restarts with the backoff; an exit 0 does not; every stop path ends every group; a start reaps a
      crashed run's commands; groups 0 and 1 are never signalled.
- [ ] A command's output reaches only its log file, except its last lines after a non-zero exit.
- [ ] The Site copy differs from the build only by the tag; the port survives a restart; one event per Site change,
      also across a cancelled follow.
- [ ] The refusals and the restart line hold; one test per scenario, named after it; the live check is recorded.

## Consequences

- **Positive**: `funcdctl dev` alone runs an App's whole loop; dev's terminal stays readable; pages reload after a web
  change.
- **Negative**: dev supervises processes, copies a Site's build per push under `liveReload` and opens a second
  loopback port. A watch build that reports an error and keeps running, as `vite build --watch` keeps the last good
  bundle (ADR-0244), shows it only in its log file; the terminal shows no new `pushed` line.
- **Risks accepted**: `funcdctl.config.yaml` runs the commands it names, as a justfile does; dev runs only what the
  project's own file declares.

## Open questions

- Windows support: a later ADR if asked.
- A per-command environment: later, if a project needs one beyond dev's own.
- Printing a watch build's error that does not end its command: a later ADR if the log file proves too quiet.

## References

- #1119 (the request and the decisions of 2026-10-20), #1118, #1066.
- `cmd/funcdctl/dev_app.go:408-474` (the lookups that miss); `internal/runtime/procreg/procreg.go:104` (the guard);
  `internal/runtime/process/process.go:31-33` (dev's grace); `internal/site/basehref.go:107` (the tokenizer);
  `internal/artifact/bundle.go:111-120` (`packDir`); `internal/runtime/process/process.go:282-284` (a worker's exit
  kills its group).
