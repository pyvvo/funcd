## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0359 implementation, model: claude-opus-5-5)

This independent review covers branch `docs/adr-0359-dev-containers`: b507483e, which implements ADR-0359, and 152aada9,
a defect found on the way and fixed in the same branch with its own regression test. The work was not edited. The
reverted run used `go test -overlay`, and `git stash` was not used.

### Blocker

None.

### Major

None.

### Minor

1. **The behavior row is in a different file from the one the ADR names** · here · model. The ADR's *Behavior rows*
   names `docs/behavior/funcdctl-dev.md` for a new row "funcd's runtimes in containers". The row was added to
   `docs/behavior/workers.md:39` as "Under `funcdctl dev --containers`", and `funcdctl-dev.md:57` only gains "unless
   `--containers`". The row's content is right and cites its tests. Either move it or add a short pointer row in
   `funcdctl-dev.md`. "Leftover dev workers" (`funcdctl-dev.md:74`) covers these containers as its text stands.

### Routed (not this work's)

None.

### The fix on the way: node_modules linked by its absolute path (152aada9)

- **Reproduced, and the test fails without the fix.** `TestDeliverBuildLinksNodeModulesOfARelativeFolder`
  (`cmd/funcdctl/dev_phase2_test.go:335`) was run with `go test -overlay`, with `cmd/funcdctl/dev.go` replaced by its
  copy at `152aada9^` (only that hunk differs). It fails with `too many levels of symbolic links` on
  `<bundle>/node_modules/greet/index.mjs`. With the fix it passes, under `-race`, and the five `TestIssue1053_*` kept-bundle
  tests next to it also pass.
- **Design check: `bug`.** ADR-0125 line 126 says that deps come from the author's local `node_modules`/venv. When the
  folder is given as `.`, the old code linked `node_modules` to the relative path `node_modules`, so the link pointed at
  itself and the author's modules could not be read. No ADR accepts that, and no sound design would want it.
- **The cause is fixed, not the symptom, and nothing else changed.** The link target is made absolute
  (`filepath.Abs`), and a stale symbolic link in a kept bundle is removed before the new link is made. A regular
  directory is not removed, which matches #1053's rule that a copy never writes through into the source. The diff is
  11 lines in `deliverBuild` and the test. The test does not call `t.Parallel()` because it uses `t.Chdir`, which is
  correct.

### ✅ Verified correct (keep it)

- **Build, vet and lint.** `go build ./...` and `go build -tags dev ./...` passed. `go vet` passed on the touched
  packages, on `-tags dev ./cmd/funcdctl/`, and with `GOOS=linux` on `internal/runtime/containerd`. golangci-lint
  reported `0 issues` on `internal/function`, `internal/provider/...`, `internal/runtime/embedimg`,
  `internal/runtime/containerd`, `pkg/funcd` and `internal/testkit/fakenerdctl`, and `0 issues` with `--build-tags dev`
  on `cmd/funcdctl`.
- **Tests of the touched packages.** `go test -race -count=1` passed (`ok`) on `internal/function`,
  `internal/provider/docker`, `internal/provider/nerdctl`, `internal/runtime/embedimg`, `internal/runtime/containerd` and
  `internal/testkit/fakenerdctl`.
- **Every Scenario has a named test that is not skipped and passes on the real engine.** The run on colima's Docker was
  `go test -tags dev -race -count=1 ./cmd/funcdctl/ -run 'TestScenarioDevContainers|TestScenarioDevAddonWorker' -v`,
  with the real worker images embedded:
  - `TestScenarioDevContainers` passed in 12.9 s (Node, pooled Node and Python Functions, each in its own container, and
    a `nodejs22` Job run that succeeded).
  - `…NoHostRuntime` passed in 1.5 s, `…Save` in 1.9 s, `…LoadOnce` in 7.8 s (2 loads, then 0),
    `…LinkedBuild` in 1.7 s, `…Key` and `…Refused` (no engine, another platform, placeholders).
  - ADR-0346's `TestScenarioDevAddonWorker*` all still pass. Only `…Linux` was skipped, as on main.
- **containerd.** The author's hand run, ADR-0359 plan step 9, was rerun here on the colima containerd profile with a
  funcdctl built from HEAD. First, the two dev references were removed from the engine. Run 1 then loaded both images
  (`nerdctl load`, 0.71 s and 0.89 s). On a PATH with no node and no Python, run 1 served `hello`, `pyhello`, `pooled`
  (solo), `linked` (the folder's `node_modules`) and `pysib` (a Python sibling module) from Linux containers. Run 2
  served the same five Functions and loaded nothing. That VM was stopped after the run. A Job run and a save were run
  only on Docker. They go through the same `workers.go` driver.
- **Review checklist.**
  1. Without the opt-in, nothing changes. `TestProcessModeSpecsSetNoImage` passes. `ownImage` returns `""` when
     `OwnImages` is nil, and the shim lookup in `devPlatformOptions` is unchanged.
  2. Under the opt-in, each own-runtime worker sets `Image`, a Job run included, and a pooled Function runs solo
     (`pooled` returns false when `ownImage` is set). This is shown by `TestDevOwnRuntimeWorker`, which also covers
     `nodejs20` failing `RuntimeUnavailable` with the ADR-0359 cause, and by the real-engine runs.
  3. Dev loads only what the engine lacks. `loadOwnImages` uses `HasImage`, then `Load`, as shown by
     `TestLoadOwnImagesOnce` and the real load-once runs on both engines. A worker's spec names the dev reference.
  4. The fat funcdctl carries two images and the thin one carries none. `go tool nm` on the `-tags dev` binary lists
     `embedimg.nodejs22Tar`/`python314Tar` and their refs, and no `duckdbTar`/`rqliteTar`. The thin binary has no
     `embedimg.` symbol. The separate `go:embed` per image is what makes this dead-code elimination work.
  5. Links are handled as Decision 5 says. `tarTree` keeps a link that resolves inside the bundle (relative to its own
     directory) and copies one that resolves outside as a file or a directory. A cycle and a dangling link each fail
     `Invalid` and name the link (`TestTarTreeLinks`).
  6. The refusals name their cause: no engine (the docker.sock search reason), another platform ("the engine runs
     linux/s390x…") and the placeholders ("built without just build-worker-images").
- **Contracts match the ADR.** `embedimg.WorkerTar(runtime) (io.Reader, string, bool)`, `Engine.HasImage` and
  `Engine.Load` on the Docker Engine API (`/images/{ref}/json`, `POST /images/load`, with its stream's error checked) and
  on nerdctl (`image inspect`, `load`), `function.Deps.OwnImages`, `funcd.WithOwnRuntimeImages`, and the
  `dev.containers`/`--containers` key, where the flag wins (ADR-0254). The containerd import now takes the record of the
  name that was looked up (`importedFor`, `TestImportedFor`), as plan step 1 requires.
- **Code is reused, not duplicated.** `Pull` now goes through `HasImage` and a shared `streamError`. The fake nerdctl
  gains `image inspect` and `load` and does not add a second fake. `scripts/build-worker-images.sh` is the only build
  path for the worker images: `just build-worker-images`, `just build-runtime-images` and `release.yml` all call it, and
  the python314 fastjsonschema check moved into it unchanged. Its release job follows the shape of `build-sql-engine`,
  with one runner per architecture. Actions are pinned by SHA.
- **Tracking.** ADR-0359 differs from its accepted text (82de9425) only in the `Accepted → Reviewing` status bump. The
  F276 row was at `reviewing`. ADR-0346 carries the `Superseded in part by` back-link. The blueprint's funcdctl line
  names `--containers`. The embedded tars and `.ref` files stay `skip-worktree`, and the committed `.ref` placeholders
  are empty.

### Definition of Done

13/13 items hold: the 6 ADR Review-checklist items, plus the generic items for scenarios, real behavior, contracts,
tree, conventions, dependencies (none added) and tracking. `just ci-full` is the PR's gate (`scripts/agent/gate.sh`),
which runs once later. It was not run here.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0359 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 13/13. See
docs/reviews/model-scorecard.md.

### Recommendation

Stamp `Implemented`. The behavior-row Minor can ride the PR or a follow-up. The PR that implements the ADR closes
feature issue #1585 (`Fixes #1585`).
