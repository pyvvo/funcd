# ADR-0244: Watch mode for the Vite plugin — each rebuilt function reaches its output directory

- **Status**: Accepted (2026-10-18), after two judge rounds; the decider kept Decision 5 for watch mode only and
  confirmed the rest: a failed rebuild keeps the last good bundle, a new manifest waits for the next rebuild, a
  manual live check, a release right after the merge.
- **Date**: 2026-10-18
- **Deciders**: green-0-rabbit
- **Tags**: vite, dx, bundle, typescript, watch
- **Realizes**: [FEAT-0001/F157](../feat/0001-feat-v1.1.md) (watch mode for the Vite plugin)
- **Supersedes (in part)**: [ADR-0144](0144-bundling-in-the-language-toolchains.md) Decision 1's *Output* rule, in
  watch mode only: there, each function is delivered when its own environment's write ends, not after every
  environment is built. A plain `vite build` keeps the rule.
- **Relates to**: [ADR-0125](0125-funcdctl-dev-local-run.md) (`funcdctl dev` reloads a function when its files
  change) · [ADR-0124](0124-multi-function-funcdctl-yaml.md) (the `<name>.funcdctl.yaml` naming rule)

## Context & Need

`@funcd-dev/vite-plugin` builds each function as its own Vite environment into a staging directory, and its
`builder.buildApp` copies every `<name>.mjs` and manifest into `outDir` after the last environment is built
(ADR-0144). Under `vite build --watch`, Vite 8's `builder.build(env)` returns the environment's watcher at once and
builds in the background, so that copy runs before any build has ended:

- on a clean cache it fails with `ENOENT` on the first staged file, and the command exits;
- on a warm cache it copies the previous build's output, and no rebuild reaches `outDir` after that.

Both were reproduced with Vite 8.3.2 on 2026-10-18. An app project works around it with its own rebuild-on-save
script.

**Purpose**: `vite build --watch` keeps `outDir` current, function by function. Beside it, `funcdctl dev <outDir>`
reloads a function whenever its manifest's or entry's size or mtime changes (`cmd/funcdctl/dev_reload.go`,
`fingerprint`, polled every 300 ms), and a copied `<name>.funcdctl.yaml` resolves `<name>.mjs` beside it
(`cmd/funcdctl/dev.go`). So a saved handler answers with its new code within about a second, with no script. The
callers are function authors.

## Scenarios

Each scenario's project has two functions, `front` and `env-echo`, which both import `src/shared.ts`, and the
default `outDir` (`dist`).

- **scenario: vite-watch-first-build** — *When* `vite build --watch` starts on a clean cache, *Then* each function's
  `dist/<name>.mjs` and `dist/<name>.funcdctl.yaml` appear when its first build ends, and the command keeps watching.
- **scenario: vite-watch-rebuilds-one** — *When* `src/front.ts` is saved, *Then* `dist/front.mjs` holds the new code
  and `dist/env-echo.mjs` is not rewritten. *When* `src/shared.ts` is saved, *Then* both are rewritten.
- **scenario: vite-watch-manifest** — *Given* each function has its own `<name>.funcdctl.yaml`, *When*
  `front.funcdctl.yaml` is saved, *Then* `dist/front.funcdctl.yaml` holds the edit and env-echo's copy is not
  rewritten. *Given* both functions use one `funcdctl.yaml`, *When* it is saved, *Then* both copies hold the edit.
- **scenario: vite-watch-error-keeps-last** — *When* `src/front.ts` is saved with a syntax error, or front's only
  manifest is deleted, *Then* the error is printed, `dist/front.mjs` keeps the last good build and the command keeps
  watching. *When* the error is fixed or the manifest restored, *Then* the next build reaches `dist`.
- **scenario: vite-build-unchanged** — *Given* `src/front.ts` does not compile, *When* a plain `vite build`, *Then* it
  fails and leaves `dist` as it was.

## Scope

**In**: what the plugin delivers to `outDir` under Vite's watch mode, when, and what a rebuild or a failure does.

**Out**: a `vite dev` integration that runs the functions next to Vite's dev server (the `@cloudflare/vite-plugin`
model), its own ADR if it is wanted; `funcdctl dev`, unchanged; `funcd-bundle` (Python).

## Constraints & Decision drivers

- A plain `vite build` keeps ADR-0144's rule: a failed build changes nothing in `outDir` (the decider, 2026-10-18).
- No new plugin option, export or dependency; Vite ≥ 8 stays the peer range.
- In watch mode `funcdctl dev` reads a changed bundle within 300 ms (`devReloadPoll`), possibly while the plugin
  writes it, so no reader may see a half-written file (the author's addition, kept for watch mode by the decider).

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. In watch mode, deliver each function when its own environment's write ends** | A save reaches `outDir` in one rebuild; the other functions' files stay untouched | Two delivery points in the plugin | **Chosen** |
| B. Deliver per environment in both modes | One delivery point | A plain build that fails part-way leaves new and old bundles mixed in `outDir` | Rejected (the decider keeps the plain rule) |
| C. In watch mode, wait until every environment has rebuilt, then copy all | `outDir` changes in one step | Each environment has its own watcher and they share no round, so "all rebuilt" has no defined end; one function waits for another | Rejected by the decider |
| D. A rebuild-on-save script per project (today) | No plugin change | Every project writes its own; it rebuilds every function on any save | Rejected |
| E. Each environment writes straight into `outDir`, with no staging and no copy | No copy step | A shared `outDir` needs `emptyOutDir: false` and a root one triggers Vite's warning (why ADR-0144 stages); Rolldown writes in place; the manifest copy still needs a hook | Rejected |
| F. In watch mode, copy in place as a plain build does | No temporary file | `funcdctl dev` can load a half-written bundle and roll out a broken revision until its next check | Rejected for watch mode; the plain build keeps it (the decider) |

## Decision

1. **Watch mode is Vite's**: `vite build --watch`, or `build.watch` in the Vite config. The plugin reads it from the
   resolved config, `this.environment.config.build.watch` in a hook and `builder.config.build.watch` in `buildApp`,
   and acts only in the environments it declared.
2. **Delivery in watch mode**: when a function's environment ends a successful write (`writeBundle`), the plugin
   delivers that function — `<name>.mjs` from its staging directory to `<outDir>/<name>.mjs`, then its manifest to
   `<outDir>/<name>.funcdctl.yaml`, except where ADR-0144's rule skips the manifest copy. `buildApp` starts the
   watchers and delivers nothing.
3. **The manifest is watched**: when a function's environment starts a build (`buildStart`), the plugin resolves its
   manifest by ADR-0144's rule and adds it to that environment's watched files (`this.addWatchFile`), so a saved
   manifest rebuilds and delivers the function. The shared `funcdctl.yaml` rebuilds every function that uses it. A
   manifest created during the watch takes effect at the function's next rebuild: a file that did not exist was not
   watched.
4. **A failed rebuild delivers nothing**: `outDir` keeps the function's last good files, Vite prints the error, and
   the watch goes on. A function left with no manifest fails its rebuild with ADR-0144's message.
5. **In watch mode, each delivered file is replaced atomically**: it is written to `.<file>.<pid>.tmp` in `outDir`
   and renamed over the target; `funcdctl dev` skips dot-files when it watches a directory in place. A plain build
   copies in place, as ADR-0144 does.
6. **A plain `vite build` delivers when it did**: `buildApp` delivers every function after the last environment is
   built, and a failure delivers nothing.

## Temporary workarounds

None.

## Contracts

`FuncdPluginOptions`, `funcd()` and `environmentName()` keep ADR-0144's signatures. The plugin's hooks, in each
environment it declared:

| Hook | Plain `vite build` | `vite build --watch` |
|---|---|---|
| `config` | one environment per function (ADR-0144) | same |
| `buildStart` | adds the manifest to the watched files (no watcher reads them) | same: a saved manifest rebuilds the function |
| `writeBundle` | nothing | delivers this function, each file by a rename |
| `builder.buildApp` | builds every environment, then delivers every function | starts every watcher, delivers nothing |

| Consumes | Exposes |
|---|---|
| Vite's resolved `build.watch`; each function's sources and manifest | `<outDir>/<name>.mjs` and `<outDir>/<name>.funcdctl.yaml`; in watch mode each replaced by a rename |

## Implementation plan

**funcd-typescript** (one PR, `feat(vite-plugin): …`, merge queue, then the release-please release right after it):

- `vite-plugin/src/index.ts`: keep the environment-to-function map in the plugin's closure; add `buildStart` and
  `writeBundle`, which delivers with the write-then-rename copy; `buildApp` returns after starting the builds when
  watching, and its plain path is unchanged.
- `vite-plugin/test/watch.test.ts`: each watch scenario runs Vite's CLI (`vite build --watch`) as a child process on
  a fixture from `tempDir`. It waits, with a deadline, on the child's output (each build prints its environment's
  staged file) and then on the delivered file, so a not-rewritten check runs once the other function's rebuild was
  seen. It kills the child and awaits its exit in a `finally`, before `tempDir`'s cleanup removes the fixture.
  `vite-watch-rebuilds-one` runs with `dist` and with `outDir: '.'` (the examples' setting), sees one build per save,
  and checks that a rewritten file is a new inode and that no `.tmp` file is left. `vite-build-unchanged` goes in
  `plugin.test.ts` with `createBuilder`, like ADR-0144's scenarios.
- `vite-plugin/README.md`: a *Watch mode* section, with `vite build --watch` beside `funcdctl dev dist`, and
  Decision 3's limit.
- Live check, recorded in the PR: in a fixture, `vite build --watch` beside `funcdctl dev dist`; after a save, an
  invoke polled for a few seconds answers with the new code.
- The PR description closes the feature issue: `Fixes pyvvo/funcd#1063`.

**funcd** (this repo): the feat row, the `Superseded in part by: ADR-0244` back-link in ADR-0144 at acceptance, and
the statuses. No pin moves: funcd builds nothing with the plugin.

**Done when** the plugin's tests (each scenario named after it), Biome and `tsc` pass in CI, the release is
published to npm, and the live check is recorded.

## Review checklist

- [ ] Under `--watch`, each function is delivered when its own environment's write ends; `buildApp` delivers nothing.
- [ ] A saved source rewrites only the functions whose graph holds it; a saved manifest rebuilds and delivers its
      functions.
- [ ] A failed rebuild leaves `outDir` as it was, and the watch goes on.
- [ ] A plain `vite build` delivers after the last environment, and nothing when one fails.
- [ ] Under `--watch`, every delivered file is renamed into place from a dot-file in `outDir`, and none is left
      behind; a plain build copies as before.
- [ ] No new option, export or dependency; the hooks act only in the plugin's own environments.
- [ ] One test per scenario, named after it.

## Consequences

- **Positive**: `vite build --watch` beside `funcdctl dev` is a save-and-reload loop with no script, and only the
  saved function reloads.
- **Negative**: the plugin delivers from two places, `buildApp` (copy in place) and `writeBundle` (by a rename).
- **Risks accepted**: it relies on two Vite 8 behaviors, `builder.build` returning a watcher and `writeBundle` running
  after each rebuild's write; the watch tests pin both against the Vite version CI installs, and the peer floor, Vite
  8.0.0, is not tested.

## Open questions

None.

## References

- #1063, the feature issue (tracker #1062).
- Vite 8.3.2 (MIT): `buildEnvironment` returns the Rolldown watcher in watch mode (`dist/node/chunks/node.js`); the
  CLI's `build --watch` calls `builder.buildApp()` (`dist/node/cli.js`). Checked on 2026-10-18 with a two-function
  prototype: the clean-cache and warm-cache failures above, then every scenario passing with the hooks of this ADR,
  `outDir: '.'` included (no rebuild loop); beside `funcdctl dev dist`, only the saved function reloaded, and its new
  code answered 0.59 s after the save (the judge's run).
- Rollup plugin hooks, as Rolldown implements them: `writeBundle` (after the output files are written), `buildStart`,
  `this.addWatchFile`.
- `cmd/funcdctl/dev_reload.go` (`fingerprint`), `cmd/funcdctl/dev.go` (`devReloadPoll`, a stem manifest's default
  entry).
- [ADR-0144](0144-bundling-in-the-language-toolchains.md), [ADR-0125](0125-funcdctl-dev-local-run.md).
