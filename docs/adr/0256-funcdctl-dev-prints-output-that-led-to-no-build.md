# ADR-0256: `funcdctl dev` prints a watch command's output that led to no build

- **Status**: Implemented (2026-10-21), review passed on its second round
  (docs/reviews/adr-0256-implementation-claude-opus-5-5.md); Reviewing (2026-10-21), implemented in the PR that closes
  #1131; Accepted (2026-10-21), after six judge rounds and a live pilot, under the decider's "go ahead with the
  recommended approach" (#1131); the 3 s and 1 s windows and the ownership rules are the drafting agent's choices,
  brought back to the decider. The judge moved the verdict from pushes to the owned builds' files, narrowed a folder to
  its two JavaScript build paths, moved ADR-0255's fixture to a layout whose command owns its build and set the 1 s
  window that splits a build's burst from a failing save after it; the pilot, with real `vite build --watch`, showed
  that removed files must not count as a write.
- **Date**: 2026-10-21
- **Deciders**: green-0-rabbit
- **Tags**: dx, cli, dev
- **Realizes**: [FEAT-0001/F167](../feat/0001-feat-v1.1.md) (#1131, tracker #1062)
- **Supersedes (in part)**: [ADR-0255](0255-funcdctl-dev-runs-the-dev-loop.md) Decision 4, with the checklist line and
  the Negative consequence that restate it, where the terminal shows a command's output only as the last lines of a
  non-zero exit, and its scenarios' fixture layout, whose command owns no build; it answers ADR-0255's open question
  on a watch build's error that does not end its command.
- **Relates to**: [ADR-0244](0244-vite-plugin-watch-mode.md) (a failed rebuild keeps the last good bundle)

## Context & Need

A `dev.watch` command such as `vite build --watch` reports a compile error and keeps running (ADR-0244), so under
ADR-0255 Decision 4 the error reaches only its log file. In dev's terminal, the only sign of a broken save is that no
`pushed images.<key>` line appears. The decider chose, on 2026-10-21, that dev prints a command's output when it led to
no build: this works for any tool, without a new key and without parsing.

## Scenarios

Fixture: ADR-0255's, laid out so each command owns its build. The Site's command runs in `web/` (`dir: web`, build
`web=web/dist`); its `build.sh` prints `build started`, copies `web/site/` into `web/dist` and, 0.3 s later, prints a
line on each stream; while a `BROKEN` file exists it empties `web/dist` instead, as Vite does before a build that fails,
writes `build error: broken` to standard error and runs on. A second command, `api` in `functions/ts`, rewrites
`api-1.0.0.mjs` on a change.

- **scenario: dev-watch-silent-failure** — *Given* dev running, *When* a save happens while `BROKEN` exists, *Then*
  within the window plus a poll the terminal shows `web | build error: broken` and no `pushed images.web` line; *when*
  `BROKEN` is removed and the file saved again, *Then* `pushed images.web` appears and no new `web |` line.
- **scenario: dev-watch-start-failure** — *Given* `BROKEN` at start, with `web/dist` absent or holding an earlier good
  build, *Then* the terminal shows `web | build error: broken`.
- **scenario: dev-watch-own-build** — *When* two saves in one window make `api` rebuild and `web` fail, *Then*
  `web | build error: broken` shows, though a build changed, and no `api |` line.
- **scenario: dev-watch-quiet-success** — *When* a save rebuilds the Site, *Then* the terminal shows no `web |` line,
  as ADR-0255's `dev-watch-quiet` asserts on this layout.
- **scenario: dev-watch-quick-failure** — *When* a save fails more than 1 s plus a poll, but less than 3 s, after the
  last line of a save that rebuilt the Site, *Then* `web | build error: broken` shows.

## Scope

- **In**: when dev prints a watch command's output, and the layout of ADR-0255's scenario fixture; nothing else of
  ADR-0255 changes.
- **Out**: parsing a tool's output; a key or flag for it; folder mode (ADR-0255 has no watch there).

## Constraints & Decision drivers

- No new key and no tool-specific rule (the decider).
- A successful rebuild keeps the terminal as quiet as ADR-0255 made it.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| Chosen: print output that led to no build of the command's own | No key, no parsing, any tool that writes its build under its `dir` | A rebuild silent for more than 3 s mid-way shows its first lines; a command that owns no build shows every burst | Chosen |
| A. A per-entry error pattern | Precise | Every project writes a regex | Rejected by the decider |
| B. A per-entry switch to show all output | Simple | Noisy for that command | Rejected by the decider |
| C. Print standard error | Shows Vite's errors | Floods with warnings; misses `tsc --watch` on standard output | Rejected in ADR-0255 |

## Decision

1. **Output, as it grows.** A command's standard output and error still go only to its log file (ADR-0255 Decision
   4); dev reads the file as it grows, every `devReloadPoll`, and keeps the lines since the command last went quiet, at
   most 64 KiB. No pipe stands between the command and its file, so ADR-0255's exit handling is unchanged.
2. **What a command builds.** A command owns each build path inside its `dir`, present or not: the path a `--build
   key=path` names, and, for each image of the App that no `key=path` names, `<folder>/<key>.mjs` and
   `<folder>/dist/<key>.mjs` in each `--build` folder, where standalone dev reads a JavaScript build (ADR-0247
   Decision 6). A Python build runs from its sources, so no folder path stands for it. Paths compare absolute and
   cleaned; commands whose dirs hold the same build share it. Dev takes them at start, so an image added later is
   owned from the next start.
3. **A burst and its verdict.** A burst ends when the log has not grown for 3 s, or for 1 s once a build the command
   owns was written, so a save that fails soon after a build gets a burst of its own. A build was written when a file
   under it, as dev's build walk sees them (dot-entries, `node_modules` and `__pycache__` left out), was added or
   rewritten (its size or mtime differs) since the last poll before the burst's first line, or the command's start when
   that came later. Removing files writes nothing, so a tool that empties its output before a build that fails, as Vite
   does, still shows its error. When no owned build was written, dev prints the burst's last 20 lines, each prefixed
   `<name> | `; otherwise it drops them. A command that owns no build prints every burst.
4. **At start too.** The rule holds while dev waits for builds (ADR-0255 Decision 2), so a first build that fails shows
   its error, also when an earlier good build is still on disk.
5. **An exit.** A non-zero exit drops the open burst and prints the log's last lines as ADR-0255 Decision 3 says, which
   may repeat lines a burst already printed. An exit 0 ends the open burst and judges it.

## Temporary workarounds

None.

## Contracts

| Constant | Value |
|---|---|
| burst quiet window (`devWatchQuiet`; a unit test sets a burst's windows shorter) | 3 s |
| burst quiet window once an owned build was written (`devWatchBuiltQuiet`) | 1 s |
| lines printed per burst | 20, the last ones |
| kept per command | 64 KiB |

- **Line**: `<name> | <line>`, as ADR-0255's exit lines.
- No key, no flag, no state file.

## Implementation plan

- **funcd**, one PR closing #1131 (`Fixes #1131`):
  - `cmd/funcdctl/devwatch.go`: reading each log file as it grows, the burst, the verdict from the owned builds'
    files, printing or dropping a burst, and the exit rules.
  - `cmd/funcdctl/dev_app.go`: the build paths each command owns, from the `--build` entries and the App's images.
  - `cmd/funcdctl/devwatch_test.go`: ADR-0255's fixture moved to the owning layout, its assertions kept; one test per
    scenario, named after it, serial; unit tests of the verdict (an owned build written or not, an emptied build, the
    end 1 s after a write, a shorter pause after a write, no owned build, an exit 0, a non-zero exit), of the exits'
    wiring in the supervisor, and of ownership (a `key=path`, a folder's two paths, a path outside `dir`).
- **Live check**: experiment 0001 with `vite build --watch`, and `tsc --noEmit --watch` in a `dir` that holds no build,
  recording what each shows on a broken and a good save (a good Vite save shows nothing), and against the windows the
  rebuild times, Vite's pause after its write and how long its output folder stays empty.
- **Done when** `go build`, `go vet`, `golangci-lint` and `go test` pass with `-tags dev` and without, and the live
  check is recorded.

## Review checklist

- [ ] The verdict reads whether an owned build was written, not pushes: a file added or rewritten counts, a removed one
      does not; a burst after which none was written prints its last 20 lines, prefixed; one after which one was
      written prints nothing and ends 1 s after its last line; a command owning no build prints every burst.
- [ ] Ownership comes from the `--build` entries and the App's images against the command's `dir`; a folder stands
      only for its two JavaScript build paths per image.
- [ ] The rule holds during the wait at start, with or without an earlier build on disk; the exits behave as Decision
      5 says.
- [ ] The log file still holds all output; ADR-0255's tests keep their assertions on the moved fixture.
- [ ] One test per scenario, named after it.

## Consequences

- **Positive**: a broken save shows its error in dev's terminal, for any tool that writes its build under its `dir`.
- **Negative**:
  - a rebuild silent for more than 3 s between its first lines and its build shows those lines (`vite build --watch`
    prints `build started...` before a silent transform);
  - a command that owns no build shows every burst, and a checker that shares its bundler's `dir`, such as `tsc
    --noEmit --watch` beside Vite, shows nothing while the bundler rebuilds: to show its errors, it runs in a `dir`
    that holds no build (`src`, with `tsc -p ..`);
  - a tool that writes its build before its first line shows a successful rebuild now and then, when a poll falls
    between the two;
  - a save that fails within 1 s of a build's last line joins that build's burst, which shows nothing, and a tool
    silent for more than 1 s after its write shows the rest of its output (Vite's size report on a large app's first
    build; `build.reportCompressedSize: false` avoids it);
  - a command that writes at least every 3 s never ends a burst, so it shows nothing;
  - a non-zero exit may repeat lines a burst printed.
- **Risks accepted**: `tsc --watch` emits despite type errors unless `noEmitOnError` is set, and a `key=path` naming a
  Python build's directory counts each save there as a build, so those errors stay in the log file; on a file system
  with whole-second mtimes, a rewrite of the same size within the second reads as no write.

## Open questions

None.

## References

- #1131; ADR-0255 Decision 4 and its open question; ADR-0247 Decision 6 (a folder's build paths);
  `cmd/funcdctl/devwatch.go` (the supervisor).
