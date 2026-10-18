## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0244 implementation, model: claude-opus-5-5)

This reviews pyvvo/funcd-typescript#75 (branch `feat/vite-plugin-watch`, commit `e60eed8`). The PR changes
`vite-plugin/src/index.ts` and `vite-plugin/README.md`, adds `vite-plugin/test/watch.test.ts`, and adds
`vite-build-unchanged` to `vite-plugin/test/plugin.test.ts`. The revert, mutant and error-path checks ran on scratch
copies of the plugin and its tests, so the tree was not changed.

### Minor

- **Nothing tests the error branch of `replaceFile`.** Attribution: model. Evidence: `vite-plugin/src/index.ts:141-143`.
  - **What the branch does**: when the copy or the rename fails, the `catch` removes the temporary file and throws
    the error again.
  - **Why it matters**: a mutant without that `rmSync` passes the whole suite.
  - **The branch can be tested**: a probe put a directory at `dist/front.mjs` and then saved `src/front.ts`. The
    rename failed with `EISDIR`, Vite printed the error, and the watch went on. With the PR's code no dot-file
    stayed in `dist`; with the mutant, `.front.mjs.<pid>.tmp` stayed.
  - **Fix**: add a watch test that puts a directory at the target, saves the source and waits for the error. It
    asserts that no `.tmp` file is left and that the watch goes on. This is the failure case of checklist item 5
    ("none is left behind").

### ✅ Verified correct (keep it)

- **Checks**:
  - `just ci` in funcd-typescript, run through `scripts/agent/d`, exits 0 in 29 s. It covers `biome ci` (84
    files), the typecheck, the 172 shim tests and the 14 vite-plugin tests. It also covers the example builds, after
    which the tree is clean, and the Go checks.
  - The PR's `ci` check, which runs `just ci` under Nix, passed on `e60eed8`.
- **Revert check**: with main's `index.ts`, the six watch tests fail and `vite-build-unchanged` passes. That test pins
  ADR-0144's plain rule, which this ADR keeps.
- **Mutants**:
  - Three are killed:
    - Copying in place in watch mode fails both `vite-watch-rebuilds-one` runs on the inode check.
    - Dropping `addWatchFile` fails both `vite-watch-manifest` tests and `vite-watch-error-keeps-last`.
    - Delivering from `writeBundle` in a plain build too fails `vite-build-unchanged`.
  - One survives: a temporary file without the leading dot. No test can observe the transient name without a new
    export, which the ADR forbids. The code matches Decision 5 (`index.ts:137`).
- **Stability**: 16 concurrent runs of `watch.test.ts` all passed, each file in under 3 s. In `just ci` the six watch
  tests take 0.14 s to 0.73 s each.
- **Decisions in the code**:
  - **Decision 1**: the plugin reads `builder.config.build.watch` in `buildApp` (`index.ts:94`) and
    `this.environment.config.build.watch` in `writeBundle` (`:110`). It acts only in its own environments, through
    the `byEnvironment` map, which now lives in the closure and is refilled by `config` (`:40`, `:52`).
  - **Decision 2**: `writeBundle` delivers only its own function, the `.mjs` and then the manifest (`:108-113`,
    `deliver`), and `buildApp` returns before delivering when watching (`:94-96`).
  - **Decision 3**: `buildStart` adds the resolved manifest to the watched files (`:102-107`).
  - **Decision 4**: a failed build never reaches `writeBundle`, and a missing manifest fails in `buildStart` with
    ADR-0144's message.
  - **Decision 5**: in watch mode only, `replaceFile` writes `.<file>.<pid>.tmp` beside the target and renames it
    over the target (`:136-145`).
  - **Decision 6**: the plain path delivers with `copyFileSync`, as before (`:97`).
- **Tests**:
  - **Coverage**: each of the five scenarios has a named test. `vite-watch-rebuilds-one` runs with `dist` and with
    `outDir: '.'`, and `vite-watch-manifest` runs with its two Givens. `vite-build-unchanged` also proves that the
    failure is part-way: env-echo was staged before front failed. No existing test was changed.
  - **Teardown**: each watch test kills the child and awaits its exit in a `finally`, before `tempDir`'s cleanup.
    The child runs with `NODE_DISABLE_COMPILE_CACHE`, so the r33 temp-dir test, which re-runs the watch tests,
    passes.
  - **Deliberate departure from the plan (keep it)**: the plan says the test "sees one build per save". The tests
    instead assert that env-echo's build count and file stamp stay put, and that only the saved sources start
    builds (a `[changed]` probe, `watch.test.ts:32`, with a 500 ms settle for the loop check). The reason is
    documented at `watch.test.ts:57-59`: macOS can report one write twice, which starts a second, harmless build.
- **Live check**: re-run with the PR's plugin beside `funcdctl dev dist` (a `-tags dev` build of funcd main). Only
  front reloaded, and its new code answered 0.31 s after the save. env-echo kept its answer, and no dot-file was left
  in `dist`. The PR records the same check (0.32 s).
- **Scope and Contracts**:
  - The PR touches the four files the plan lists and no others.
  - It adds no option, export or dependency: `package.json` is unchanged, and `deliver` and `replaceFile` are private
    to the module.
  - `FuncdPluginOptions`, `funcd()` and `environmentName()` keep their signatures.
- **README**: the *Watch mode* section covers delivery by a rename, the manifest watch with Decision 3's limit, a
  failed rebuild, the pairing with `funcdctl dev dist`, and the unchanged plain build.
- **Tracking**: ADR-0244 is at `Reviewing`, and only its status line changed. F157 is at `reviewing`. ADR-0144
  carries the *Superseded in part by* back-link. The PR description carries `Fixes pyvvo/funcd#1063`.

### Definition of Done

20 / 20 items hold:

- the ADR's Review checklist, 7 of 7;
- its done-when, 4 counted: the tests, Biome and `tsc` pass in CI, and the live check is recorded;
- the generic items, 9.

The fifth done-when item, the npm release, follows the merge, as the ADR orders it (merge queue, then the
release). It is not counted here.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0244 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 20/20. See
docs/reviews/model-scorecard.md.

### Recommendation

ADR-0244 is stamped `Reviewing → Implemented`, and F157 `reviewing → implemented`.

Next steps:

1. Merge pyvvo/funcd-typescript#75. It closes #1063 through `Fixes pyvvo/funcd#1063`.
2. Release it right away with release-please, as the ADR orders.
3. In the funcd status PR, cite #1063 without a closing keyword.

The Minor finding can go into #75 or into a follow-up test.
