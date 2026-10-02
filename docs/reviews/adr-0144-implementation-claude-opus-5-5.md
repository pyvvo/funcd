# ADR-0144 Implementation Review — Bundling in the language toolchains (F105)

**Verdict**: **pass**. Both packages are released and published, funcd pins the two tags, every scenario has a named
test that passes, the four sub-checks and the e2e suite are green, and all nine Lima lanes passed. The findings are
six Minors; none blocks sign-off.

**Producing model**: claude-opus-5-5
**Reviewed against**: ADR-0144 Contracts / Scenarios / Implementation plan / Review checklist / Definition of done ·
ADR-0089 · ADR-0122 · ADR-0124 · ADR-0141 · blueprint.md · FEAT-0001/F105

The work spans three repositories:
- `pyvvo/funcd-typescript`: PR #14 (`f2c04dc`), released as v0.4.0 (`0bb8be4`, PR #15);
- `pyvvo/funcd-python`: PR #12 (`e553def`), released as v0.3.0 (`0e6a50c`, PR #13);
- funcd: the uncommitted tree on `feat/toolchain-bundling-multi-arch` (`go.mod`/`go.sum`, `scripts/lanes.yaml`,
  `pkg/funcd/{invoke,kv,redeploy}_e2e_test.go`, the new `pkg/funcd/examples_test.go`, `blueprint.md`, the F105 row
  and the ADR-0089 back-link).

I read both language repos at their release tags in scratch worktrees. The ADR-0017/0031 edits, the ADR-0145 file,
the F106 row and the ADR-0145 sentence in the blueprint belong to ADR-0145 and are out of scope here.

## Verdict: pass — 0 blockers, 0 majors  (ADR-0144 implementation, model: claude-opus-5-5)

### Minor
- **Minor · model — `funcd-bundle` exits 1, not 2, for its one named usage error.** Decision 2 calls `post-install`
  without hermetic "a usage error", and the Contracts table maps usage to exit `2`. The check raises `BundleError`
  (`bundle/src/funcd_bundle/bundle.py:125`), which the CLI maps to `1` (`cli.py:46-48`). I ran it on a scratch
  project: the message is right, and the exit code is 1. An unsupported `--platform` exits 2 as specified.
  **Fix**: report this case through `parser.error` (or a dedicated exit path), and assert the code in
  `test_post_install_needs_hermetic`.
- **Minor · model — funcd-typescript's release PR no longer skips CI.** The `nonrelease` filter lists the files a
  release PR may touch (`.github/workflows/ci.yml:39-44`), and it still names only `shim/package.json`.
  release-please now also bumps `vite-plugin/package.json`, so the full `ci` job ran on release PR #15 (1 min 12 s;
  the funcd-python release PR #13 skipped it in 8 s). Nothing broke, but the change from PR #13 of that repo no
  longer applies. **Fix**: add `!vite-plugin/package.json` to the filter.
- **Minor · model — two scenario tests assert less than their names claim.**
  - `vite-missing-manifest-fails` (`vite-plugin/test/plugin.test.ts:82-89`): the second pattern, `/funcdctl\.yaml/`,
    also matches inside `front.funcdctl.yaml`, so the test passes when only the stem path is named. The code does
    name both paths (`src/index.ts:115`). **Fix**: match the generic path with a path separator before it.
  - `bundle-check-catches-mismatch` (`bundle/tests/test_bundle.py:173-180`) calls the private `_check` with a module
    list, not `funcd-bundle` or `bundle(check=True)`, and asserts the step label (`import check`), not the import
    error. The selection of modules from the handler's imports is therefore not covered by a failing case.
    **Fix**: build for the foreign platform with `check=True` and a `_check` platform override, or assert on the
    `ImportError` text.
- **Minor · model — the import check drops vendored directories named like project caches.** `_check` streams the
  bundle through `_copy_in`, whose filter (`NOT_COPIED`, `bundle.py:43-45`, applied at `:302`) is meant for the
  project copy. It removes every entry named `dist`, `node_modules`, `.git`, `.venv` and others at any depth. A wheel
  that ships such a directory reaches the check container without it, so the check can fail where the runtime would
  not. No current example is affected. **Fix**: apply the filter only to the project copy.
- **Minor · model — gate order: implementation began before acceptance, and two refinements entered the ADR after
  the re-judge.** The re-judge noted that both language branches already held the implementation before the ADR was
  Accepted. The Date line records two "refinements from the implementation", placed before the Accepted stamp:
  - Decision 1 builds each Vite environment into a private staging directory under Vite's cache. The re-judged
    text forced `emptyOutDir: false` on the shared output directory instead.
  - Decision 2 step 5 and checklist item 6 narrow the import check to the vendored modules that the handler's
    sources import. The re-judged text checked every vendored top-level module.

  No judge has read either change. I checked both against the code and the evidence, and both are sound:
  - The staging design still never empties a shared or root output directory, which is the judge's M1 concern.
  - The narrowed check avoids a real false failure: duckdb's wheel ships `adbc_driver_duckdb`, which imports a
    package duckdb does not depend on.

  The changes are disclosed rather than silent, and the header places them before acceptance, so I do not treat
  them as a post-acceptance change of substance. **Fix (process)**: run `adr-impl` only after the Accepted stamp,
  and send implementation-driven changes through the judge.
- **Minor · env — there is no committed Accepted ADR text to diff.** The ADR file is untracked, as for ADR-0142 and
  ADR-0143. The freeze check rests on the Date line and the judge and re-judge reports. The Decision, Contracts and
  checklist match the shipped code.

### ✅ Verified correct (keep it)
- **funcd checks**, run by me through the pinned dev shell:
  - `go build ./... && go tool golangci-lint run ./... && go test ./... && go mod verify`: exit 0, lint "0 issues.",
    "all modules verified";
  - `just check-hygiene`: exit 0 ("hygiene: clean");
  - `go test -tags e2e ./pkg/funcd/ -count=1 -timeout 30m`: `ok` in 110.2 s, with no FAIL and no SKIP. That run
    includes `TestScenarioExamplesBuildWithTheTools`, `TestScenarioE2EKVCounterViaContextKV`, the eight redeploy
    tests, the invoke and contract tests and `TestScenarioE2EFunclogCapturesBurst`, all with contracts read from the
    manifests.
- **Releases and publishing.**
  - npm `@funcd-dev/vite-plugin@0.4.0` is published with SLSA provenance. The only other version is the bootstrap
    `0.0.0-bootstrap.0` used to register trusted publishing.
  - PyPI `funcd-bundle` 0.3.0 is published as a wheel and an sdist, and its only requirement is `pyyaml>=6.0.2`.
  - Both release workflows publish the new package next to the shim. release-please versions the plugin through
    `extra-files`, and the Python package is stamped from the tag.
  - CI is green on both merge commits and both release commits. On merged main, funcd-python's CI ran the 10 bundle
    tests with QEMU (`10 passed`, none skipped), and funcd-typescript's CI ran the plugin tests and rebuilt every
    example with no drift.
- **Scenario → test**, every test un-skipped and passing:

  | Scenario | Test | Evidence |
  |---|---|---|
  | vite-builds-one-file-per-function | `plugin.test.ts` "scenario: vite-builds-one-file-per-function" | CI pass; one hyphenated and one plain function, one shared module, one output directory, no chunk, each imports and returns |
  | vite-output-pushes-from-manifest | two tests (stem + generic with a separate `outDir`; `outDir` = root, no copy) | CI pass; funcd's `TestScenarioExamplesBuildWithTheTools` and the TypeScript lanes cover the push itself |
  | vite-missing-manifest-fails | "scenario: vite-missing-manifest-fails" | CI pass (see the Minor on its assertion) |
  | bundle-vendors-locked-deps | `test_scenario_bundle_vendors_locked_deps` | CI and local pass |
  | bundle-cross-platform | `test_scenario_bundle_cross_platform` + `…_import_check` | local pass on an arm64 host (amd64 target), CI pass on amd64 (arm64 target) |
  | bundle-refuses-source-builds | `test_scenario_bundle_refuses_source_builds` | CI and local pass |
  | bundle-includes-workspace-package | `test_scenario_bundle_includes_workspace_package` | CI and local pass |
  | bundle-check-catches-mismatch | `test_scenario_bundle_check_catches_mismatch` | CI and local pass (see the Minor) |
  | bundle-hermetic-post-install | `test_scenario_bundle_hermetic_post_install` | CI and local pass |
  | python-source-push-needs-no-build | the `kv` and `funclog` Python pushes | both lanes PASS |
  | examples-build-with-the-tools | `TestScenarioExamplesBuildWithTheTools` | e2e run above |

  I ran the funcd-bundle suite myself at the v0.3.0 tag on an arm64 host (`10 passed`). I also ran
  `uv run --locked funcd-bundle` in `examples/catalog-quack` (hermetic, with the post-install). The bundle holds
  `_duckdb.cpython-314-aarch64-linux-gnu.so`, `duckdb-ext/`, `handler.py` and `funcdctl.yaml`, and no `funcd_shim`.
- **Contracts first holds.** I compared every deleted schema file with its manifest's contract, ignoring
  descriptions: all 13 TypeScript schemas (26 sides) and both Python schemas (kv-counter, log-burst) match the
  shipped manifests. s3-roundtrip's manifest gained the `granted` output and its `required` entry.
- **Plugin contract.** `funcd(options?): Plugin[]`, `environmentName`, `FuncdPluginOptions` and the defaults match
  the Contracts. `package.json` matches: Apache-2.0, `type: module`, peer `vite ^8.0.0`, `exports` to
  `dist/index.js` + `dist/index.d.ts`, `files: ["dist"]`, public access.
  - Environment names are sanitized to `[A-Za-z0-9_$]` with the `funcd_` prefix, and a collision fails.
  - Each environment builds ESM for `node22` with `noExternal: true`, builtins external, `codeSplitting: false`, no
    minification and the `createRequire` banner, into its own staging directory.
  - The output directory is never emptied, and the manifest is copied only where funcdctl's resolver would not
    already find it.
- **Bundler contract.**
  - The `bundle(project, name, platform, out, *, hermetic, check) -> Path` signature matches.
  - Functions come from `<name>.funcdctl.yaml`, else `funcdctl.yaml`. The handler is the manifest's `main`, else
    funcdctl's default (`<name>.py` / `handler.py`), and a missing handler is named.
  - The export uses `--frozen --no-dev --no-emit-project --no-editable --prune funcd-shim`. Local entries are split
    out, so the registry install runs with `--require-hashes`, `--only-binary :all:`, Python 3.14 and
    `<arch>-manylinux_2_36`.
  - Workspace members are built as pure-Python wheels and refused otherwise.
  - The copied manifest's `main` is rewritten to the handler's name inside the bundle, and the output is replaced.
  - Several platforms write to `<out>/<os>-<arch>/<name>/`, and the last line per bundle is the `funcdctl push … --entry`
    hint.
  - Every container step uses `docker create` + `docker cp` and binds no host path. The hermetic path runs pip in
    `python:3.14-slim-bookworm` with `FUNCD_BUNDLE_DIR` and `PYTHONPATH` set to the bundle.
- **Examples migrated.**
  - funcd-typescript: all nine examples (hello-world included) build with a `vite.config.ts`, and the esbuild, AJV
    and ts-json-schema-generator dev dependencies are gone.
  - funcd-python: kv-counter and log-burst drop the built handlers and gain `main`. catalog-quack builds with
    funcd-bundle (hermetic + `scripts/install_extensions.py`) and drops its `build` group. `.gitignore` drops
    `bundle/`.
  - At the pinned tags, no example except `releve-lakehouse` holds a `build.ts`, `build.py` or `*.schema.json`.
- **funcd side.**
  - `go.mod`/`go.sum` pin funcd-typescript v0.4.0 and funcd-python v0.3.0, and no other tracked file names the new
    tags.
  - `scripts/lanes.yaml` has no `push[].schema`. Each artifact stages its resolvable manifest (`py/counter.funcdctl.yaml`
    beside `py/counter.py`, for example), and the duckdb lane builds with `uv run --locked funcd-bundle` and pushes
    `dist/catalog-quack` with `entry: handler.py`.
  - `exampleContract(t, dir, name) (input, output []byte)` matches the Contracts (stem, else generic, through
    `sdk.LoadManifest` + `ContractSides`, both sides gated by `contract.Check`). `pushExampleFn`, the kv test and
    the redeploy harness (which copies the manifest beside the edited handler) use it.
  - `blueprint.md` (build-pipeline note, companion-repository table) and the ADR-0089 "Superseded in part by"
    back-link are in place.
- **Live lanes.** I read the builder's `just lima-example-all` log and did not rerun it. Every lane reports
  `final status: PASS`, and the run ends with "PASSED: env-echo fn-to-fn duckdb s3 kv workflow funclog egress
  metastore". It used the pinned modules (no `go.work`) and started after both releases. The duckdb lane built
  with funcd-bundle and pushed the bundle directory, and the kv and funclog lanes pushed the Python handler sources.
- **Tracking.** ADR-0144 is at `Reviewing`, the F105 row is at `reviewing`, and the module path is unchanged.

### Definition of Done
12 / 12: the 11 Review-checklist items and the Definition of done all hold. Checklist item 6 holds in the form the
ADR now states (see the gate-order Minor).

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0144 (implementation) → pass, 0/0/6, 5 model-attributed, DoD 12/12. See
docs/reviews/model-scorecard.md.

### Recommendation
Sign off: stamp ADR-0144 `Reviewing → Implemented`, move F105 to `implemented`, and move the board card to Done. The
five `model` Minors are small follow-ups in the language repositories: the exit code, the CI filter, the two test
assertions and the check filter. They can ship in the next patch release of each repository.
