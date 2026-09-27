# ADR-0141: Repository split — funcd moves to `pyvvo` and pins the language repos as Go modules

- **Status**: Reviewing (2026-09-28)
- **Date**: 2026-09-28 (judged 2026-09-28 — right decision; folded 1 Blocker (`tests/e2e` may not import
  `internal/`, so it gets its own shim helper) + 4 Majors (an explicit *Refines* line for the frozen ADRs whose
  paths move; the renamed depguard prefixes proven by `tests/lint-fixtures`; the bump scenario gets the
  `check-hygiene` version gate and a live drill; `.examples/` renamed `.modcopy/`) + minors. **Accepted
  2026-09-28** under `adr-batch`, with acceptance delegated by the decider. **Reviewing 2026-09-28** — implemented by
  `adr-impl`; the move to `pyvvo/funcd` follows the Implementation plan's step 3.)
- **Deciders**: green-0-rabbit
- **Tags**: repo, module-path, shim, examples, e2e, history, ci
- **Realizes**: [FEAT-0000/F01](../feat/0000-feat-v1.md) (the repo skeleton this re-shapes)
- **Supersedes (in part)**: [ADR-0001](0001-project-setup-and-structure.md) Decision 1 — the module path
  (`github.com/green-0-rabbit/funcd` → `github.com/pyvvo/funcd`). The rest of ADR-0001 stands.
- **Refines (location only, no frozen text changes)**: [ADR-0036](0036-daemon-execution-wiring.md)
  Decision 1 (the embed package moves from `shim/nodejs` to the language module; embed + extract on boot
  unchanged), [ADR-0032](0032-curated-runtime-images-container-execution.md) /
  [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md) (the images copy the shim
  from the pinned module), [ADR-0040](0040-benchmark-sustainability-harness.md) (`funcd bench` runs the
  embedded shims), [ADR-0030](0030-function-execution-runtime-shim-node.md) /
  [ADR-0037](0037-typescript-hono-runtime-shim.md) / [ADR-0044](0044-worker-pooling-threads.md) /
  [ADR-0049](0049-python-runtime-shim.md) / [ADR-0050](0050-python-worker-pooling-subinterpreters.md)
  (the shim sources live in the language repos)
- **Relates to**: [ADR-0025](0025-testing-strategy-and-e2e-harness.md) (the `tests/e2e` import boundary),
  [ADR-0077](0077-declarative-e2e-venom.md) (the lane registry), [ADR-0026](0026-packaging-and-release.md)
  (version stamp from `git describe` — unchanged), [ADR-0078](0078-rename-funcdcli-to-funcdctl.md)
  (precedent: frozen text keeps the old name, this ADR is the forward pointer),
  [ADR-0125](0125-funcdctl-dev-local-run.md) (frozen text redacted once, Decision 9)

## Context & Need

funcd is one private repository holding the Go platform, both language shims (`shim/nodejs`,
`shim/python`) and their examples (`examples/js`, `examples/python`). The shims and examples now also live
in two public repos, `pyvvo/funcd-typescript` and `pyvvo/funcd-python`, which release them (tags `vX.Y.Z`,
npm `@funcd-dev/shim`, PyPI `funcd-shim`; v0.2.0 on 2026-09-27). Two copies exist and will drift.

This ADR decides how funcd consumes those repos, and how funcd itself moves to the `pyvvo` org and goes
public. Purpose: funcd builds, tests and ships exactly the shim and examples of a pinned language release,
and a shim change reaches funcd only through a reviewed version bump. Callers: the `funcd` and `funcdctl`
builds (embedded shims), `funcd bench`, the runtime-image build, the Go e2e tests, the Lima lanes, the dev
recipes, and developers changing a shim and funcd together.

Going public has a precondition. The history holds two synthetic bank-statement PDFs, a real bank's name
(37 content lines, 4 commit messages) and the local username (once). A force-push does not erase them:
GitHub keeps `refs/pull/*` and cached views of the old commits. So the public repo is created fresh from a
rewritten history.

## Scenarios

- `scenario: module-path-moved` — Given the migrated repo, when an external module runs
  `go get github.com/pyvvo/funcd/pkg/sdk@main`, then it builds; and no live file names the old module path
  (frozen ADRs, `docs/reviews/` and `docs/legacy/` keep it as history).
- `scenario: shim-from-pinned-module` — Given `go.mod` pins both language modules, when funcd is built,
  then the daemon extracts byte-identical copies of the pinned `shim.mjs` and `funcd_shim` sources, and
  funcd holds no shim source.
- `scenario: bench-uses-embedded-shims` — Given no `--shim` / `--pool-shim` flag, when `funcd bench`
  runs, then it runs the embedded shims; an explicit path still overrides.
- `scenario: runtime-image-from-pinned-shim` — Given the pins, when `just build-runtime-images` runs, then
  the nodejs22 and python314 images carry the pinned shim files.
- `scenario: tests-read-pinned-examples` — Given the Go e2e tests that deploy example functions, when
  `just test-e2e` runs, then they deploy the committed bundles, schemas and manifests of the pinned module
  and build nothing.
- `scenario: lane-reads-committed-build` — Given a lane whose example lives in a language repo, when
  `just lima-example <name>` runs, then it stages the committed files of the pinned module, runs no JS
  build, and its Venom suite passes as before.
- `scenario: duckdb-lane-builds-bundle` — Given the duckdb lane, when it runs, then it builds the
  catalog-quack bundle in a writable copy of the pinned example; the module cache is untouched.
- `scenario: local-override-go-work` — Given a sibling clone with an unreleased shim change and a
  gitignored `go.work` that uses it, when the developer builds, tests or runs a lane, then funcd uses the
  sibling's files; deleting `go.work` restores the pin.
- `scenario: bump-language-release` — Given a new language tag, when a developer runs
  `go get <module>@<tag>` and commits `go.mod`/`go.sum`, then builds, images, tests and lanes use it and
  nothing else changes.
- `scenario: history-scrubbed` — Given the public `pyvvo/funcd`, when every reachable commit, tree, blob
  and message is scanned, then no PDF, bank name, local username, home path or personal email appears;
  authors and dates are preserved.
- `scenario: merge-rules-enforced` — Given a PR to `pyvvo/funcd` main, when it is merged, then it lands
  through the merge queue as one squash commit with a checked Conventional title and green required checks;
  a direct merge is refused.
- `scenario: old-repo-archived` — Given the move is done, then `green-0-rabbit/funcd` is private and
  archived with its PRs, and the planning board lives in the pyvvo org with every item and Status.

## Scope

**In:**
- funcd's module path and every live reference to it (Go imports, the nested `bench/*` modules, lint
  config, the build stamp, CI, docs, agent instructions).
- Consuming the two language repos as Go modules pinned by tag: embedded shims, `funcd bench`, runtime
  images, Go tests, Lima lanes, dev recipes, the local `go.work` override.
- Deleting funcd's copies (`shim/`, `examples/js/`, `examples/python/`) and the recipes that built them.
  `examples/funcdconfig.yaml` stays: it is the daemon's example config.
- The one funcd-typescript change this needs: embed and export `pool.mjs`.
- The one-time move: rewritten history, a fresh public `pyvvo/funcd` with the language repos' merge rules
  and release-please, the archived old repo, the board.

**Out:**
- The shim contract spec and a cross-repo conformance suite (follow-up ADR).
- The language repos' own tooling (CI, release-please, npm/PyPI publishing): built and recorded there.
- The layout of `pyvvo/funcd-functions`.
- Binary release artifacts: ADR-0026's build and stamp are unchanged.
- Providers: `images/runtime/duckdb` and its shim stay in funcd.
- A vanity import path.

## Constraints & Decision drivers

- **D1 One source of truth.** Shim and example sources live only in the language repos.
- **D2 Reproducible pin.** A funcd build is a function of `go.mod`/`go.sum`: release tags only, no fetch
  outside the Go module system.
- **D3 No foreign toolchain in `just ci`.** The fast lane needs no node, yarn or uv; it reads committed
  outputs.
- **D4 The module cache is read-only.** Nothing writes under a module dir; writers work on a copy.
- **D5 Privacy.** No machine detail and no real financial name in any public object (CLAUDE.md absolute
  rule).
- **D6 Frozen stays frozen**, except the one privacy redaction this ADR sanctions (Decision 9).
- **D7 The language repos' merge rules**: squash only, linear history, merge queue without bypass,
  Conventional titles checked at merge time.
- **D8 Licenses.** Both language modules are Apache-2.0, by the same authors.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| Keep copies in funcd | nothing to do | two sources of truth that drift | rejected (D1) |
| Git submodules | pins a commit | extra clone step; Go module zips skip submodules, so a `go get` of funcd would lose the shim | rejected (D2) |
| Fetch the npm/PyPI packages or release assets at build time | reuses the published artifacts | the npm package ships types and the contract builder, not `shim.mjs`; adds node/uv and a second pin file | rejected (D2, D3) |
| **Go modules pinned by tag** | one pin in `go.mod`; `go:embed` keeps working; `go.work` gives the local override; the module zip carries the committed builds | the examples ride in the module download (2.6 MB + 0.8 MB), never in the binary | **chosen** |
| Read `pool.mjs` from the module dir at run time | no funcd-typescript change | a built binary would need a Go toolchain and module cache | rejected; embed it like `Shim` (ADR-0036) |
| Transfer the repo, rewrite in place, ask GitHub Support to purge | keeps PRs and URL redirects | old objects stay reachable through `refs/pull/*` and caches until Support acts | rejected (D5); PRs stay in the private archive |
| Publish one squashed snapshot | only the tip needs scrubbing | loses ~480 commits of blame, bisect and ADR history | rejected |
| Stay private | no rewrite | the decider chose public for all pyvvo repos | rejected |

## Decision

1. **Module path** `github.com/pyvvo/funcd`, in repo `pyvvo/funcd`; the product name stays funcd. Frozen
   ADRs, `docs/reviews/` and `docs/legacy/` keep the old path as history (ADR-0078 precedent).
2. **Pins.** funcd's `go.mod` requires `github.com/pyvvo/funcd-typescript` and `github.com/pyvvo/funcd-python`
   at release tags, never pseudo-versions. A bump is one `go get <module>@<tag>` commit.
3. **Embedded shims.** `cmd/funcd` and `cmd/funcdctl` import `github.com/pyvvo/funcd-typescript/shim`
   (`Shim`, `Pool`) and `github.com/pyvvo/funcd-python/shim` (`Extract`). funcd-typescript adds `Pool` in a
   `feat` release, and funcd pins that release.
4. **`funcd bench`.** `--shim` and `--pool-shim` default to empty, which extracts the embedded shim to a
   temp dir; a path overrides.
5. **Runtime images.** `build-runtime-images` passes each pinned `shim/` dir as the BuildKit named context
   `shim`; the nodejs22 and python314 Dockerfiles `COPY --from=shim`. The duckdb image is unchanged.
6. **Examples.** The language repos commit their example build outputs, and their CI fails on a stale
   committed build. funcd finds a module root with `go mod download <module>` then
   `go list -m -f '{{.Dir}}' <module>`, reads from it, and never builds or writes there. Writers (the
   catalog-quack bundle, `funcdctl dev`, the releve seeding) run on a writable copy under the gitignored
   `.modcopy/`. Only `go.mod`/`go.sum` name a language-module version.
7. **Lanes.** A `scripts/lanes.yaml` lane names its `module`; `dir` and plain `stage` entries resolve
   against that module's root; a `from` entry may name its own `module`; `copy: true` works on a writable
   copy. The JS lanes lose their `build` steps.
8. **Local override.** A gitignored `go.work` (`use . ../funcd-typescript`) replaces a pin for builds,
   tests, images and lanes, since all of them resolve through `go list -m`.
9. **The move.** Freeze `green-0-rabbit/funcd` (no open PR, no new commit). Rewrite a mirror with
   git-filter-repo: drop every `*.pdf`, replace the bank names with "Bank" in blobs and messages, strip the
   local home-path prefix, and rewrite `(#NN)` to `(funcd#NN)` in messages (a PR of the archived repo,
   written like the language repos already do). The rules file and the scan's pattern list stay outside
   the repo; the review records hit counts only. Push `main` only to a new private `pyvvo/funcd`, verify,
   then make it public; unmerged branches stay only in the archive. This rewrite is the one sanctioned edit
   of frozen ADR and review text (ADR-0125, one review doc); it removes privacy data and changes no
   decision.
10. **Repo rules.** `pyvvo/funcd` gets the language repos' pattern: squash only (PR title and body), linear
    history, a merge queue with no bypass actor, required checks `ci`, `e2e` and `pr-title`; `pr-title.yml`
    lints the PR title and, on `merge_group`, every commit message about to land. release-please
    (`simple`, tags `vX.Y.Z`, the `funcd-release` App token) starts from a `v0.1.0` baseline tag and release
    on the migrated tip, and lands in the first PR after it, so its first run does not propose one release
    spanning the whole history. Its tags feed ADR-0026's `git describe` stamp. They version the code (0.x
    while the API is `v1alpha1`) and are unrelated to the feature-version labels (V1, v1.1).
11. **Archive and board.** Archive `green-0-rabbit/funcd`, still private. Copy the planning board into the
    pyvvo org with its draft issues (GitHub copies draft issues with their field values), point
    `project-management/driver.py` and CLAUDE.md at the copy, and close the old board.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| A language release is tested against funcd only when funcd bumps to it | the language repos' CI cannot run funcd's e2e | the shim-contract ADR ships a conformance suite that runs in each language repo's CI |
| The catalog-quack bundle is built at lane time | it holds a native per-arch closure, too heavy to commit per arch | funcd-python publishes per-arch bundles (release assets or committed files) |

## Contracts

### Language modules

```go
// github.com/pyvvo/funcd-typescript/shim — package nodejs
var Shim []byte // shim.mjs, the single-tenant Node shim (ADR-0030/0037)
var Pool []byte // pool.mjs, the worker_threads pool shim (ADR-0044) — new here

// github.com/pyvvo/funcd-python/shim — package python, unchanged (ADR-0049/0050)
func Extract(dir string) (shimEntry, poolEntry string, err error)
```

### funcd `go.mod`

```text
module github.com/pyvvo/funcd

require (
	github.com/pyvvo/funcd-python v0.2.0
	github.com/pyvvo/funcd-typescript v0.3.0 // the first release exporting Pool
)
```

### `internal/testkit/langmod` (new)

```go
// Package langmod locates the language repos funcd pins as Go modules (ADR-0141).
package langmod

const (
	TypeScript = "github.com/pyvvo/funcd-typescript"
	Python     = "github.com/pyvvo/funcd-python"
)

// Dir returns module mod's root as the go command resolves it (module cache or go.work). Read-only.
func Dir(t testing.TB, mod string) string

// NodeShim and PoolShim write the embedded Node shims into t.TempDir() and return their paths.
func NodeShim(t testing.TB) string
func PoolShim(t testing.TB) string
```

`scripts/moddir.sh <module>` prints the same root for recipes and `scripts/lane.py`. `tests/e2e` may not
import `internal/` (ADR-0025's `e2e-boundary` rule), so it writes `nodejs.Shim` from
`github.com/pyvvo/funcd-typescript/shim` through a package-local helper instead of `langmod`.

### Runtime images

```dockerfile
# images/runtime/nodejs22/Dockerfile
COPY --from=shim shim.mjs /opt/funcd/shim.mjs
# images/runtime/python314/Dockerfile, py stage
COPY --from=shim src/funcd_shim /opt/funcd/funcd_shim
```

`build-runtime-images` builds each image with `--build-context shim="$(scripts/moddir.sh <module>)/shim"`.

### Lane registry

New keys `module` and `copy`; `dir`, `stage`, `from`, `to` and `build` are ADR-0077's. Excerpt:

```yaml
kv:
  module: github.com/pyvvo/funcd-typescript
  dir: examples/kv-counter
  stage:
    - counter.mjs
    - from: examples/kv-counter/counter.py
      module: github.com/pyvvo/funcd-python
      to: py/counter.py
duckdb:
  module: github.com/pyvvo/funcd-python
  dir: examples/catalog-quack
  copy: true
  build:
    - uv run --group build python build.py
```

| Key | Meaning |
|---|---|
| `module` | the module whose root anchors `dir` and plain `stage` entries; absent means funcd's root |
| `from` + `module` | a file under that module's root; `from` alone stays funcd-root-relative |
| `copy` | copy `dir` into `.modcopy/` first; `build` and `stage` then use the copy |
| `build` | runs with the lane's resolved `dir` as its working directory |

### Dependencies & I/O

| | Item | Notes |
|---|---|---|
| Consumes | module `github.com/pyvvo/funcd-typescript` (Apache-2.0) | `shim.Shim`, `shim.Pool`, `shim/shim.mjs`, `examples/*` |
| Consumes | module `github.com/pyvvo/funcd-python` (Apache-2.0) | `shim.Extract`, `shim/src/funcd_shim`, `examples/*` |
| Consumes | the `go` command in tests, lanes and recipes | `go mod download` + `go list -m -f '{{.Dir}}'` |
| Consumes | Docker BuildKit | `docker build --build-context` |
| Exposes | module `github.com/pyvvo/funcd` | import path of `pkg/funcd`, `pkg/sdk` |
| Exposes | `.modcopy/` (gitignored) | writable example copies |
| Exposes | repo `pyvvo/funcd` | ruleset + merge queue, `pr-title`, release-please |

## Implementation plan

The order matters: the language release first, then the code, then the move.

1. **funcd-typescript**: `shim/embed.go` gains `//go:embed pool.mjs` and `var Pool []byte`, with a test that
   both are non-empty; merge through its queue as a `feat`, then merge the release PR (`v0.3.0`).
2. **One funcd PR on `green-0-rabbit/funcd`:**
   - `go mod edit -module github.com/pyvvo/funcd`; rewrite the path in every live file (Go, `bench/*/go.mod`,
     the `.golangci.yml` depguard prefixes, `scripts/build.sh`, `.github/`, `configs/systemd/`, live docs,
     `.claude/`); `gofmt`.
   - `go get github.com/pyvvo/funcd-typescript@v0.3.0 github.com/pyvvo/funcd-python@v0.2.0`; swap the
     imports in `cmd/funcd/{main,bench,main_test}.go`, `cmd/funcdctl/dev.go`,
     `internal/testkit/bench/pypool_test.go`.
   - Add `internal/testkit/langmod` and `scripts/moddir.sh`; replace every repo path to
     `shim/nodejs/{shim,pool}.mjs` and every in-place example build in tests (`pkg/funcd/*_e2e_test.go`,
     `tests/chaos`, `internal/function`, `internal/artifact`, `internal/testkit/bench`; `tests/e2e` through
     its own helper); `docs/demo/server/main.go` extracts the embedded shim.
   - `cmd/funcd/bench.go` flag defaults; the two Dockerfiles, `build-runtime-images`, and the Dockerfile
     assertion in `internal/function/shim_test.go`.
   - `scripts/lane.py` (`module`, `from.module`, `copy`, build working dir), `scripts/lanes.yaml`, and the
     paths named in `e2e/duckdb.venom.yml` and `e2e/README.md`.
   - `justfile`: drop `build-shim` and `check-shim-python`; re-point `lima-example`, `example-fn-to-fn`,
     `example-kv`, `dev-example`, `gen-releve`, `seed-releves`, `run-releve`, `demo`; `scripts/demo/*`.
   - Delete `shim/`, `examples/js/`, `examples/python/`; clean `.gitignore`; ignore `.modcopy/`, `go.work`,
     `go.work.sum`.
   - CI: `ci.yml` gains `merge_group`, drops the `shim/**` filter and moves to `dorny/paths-filter@v4` (the
     version the language repos run on `merge_group`); add `pr-title.yml`. `just check-hygiene` fails when a
     tracked file other than `go.mod`/`go.sum` (and frozen docs) names a language-module version or a
     module-cache path.
   - Live docs: `blueprint.md` (Repository structure), `README.md`, `docs/install.md`,
     `.github/copilot-instructions.md`, `.claude/CLAUDE.md` (identity, multi-repo workflow), the skills that
     name the module or the shim paths (incl. `bench-overview`).
   - Replace the bank names still in live files and the home path in the one review doc, so the rewrite in
     step 3 only changes history and ADR-0125.
   - Green: `go build ./...`, `go tool golangci-lint run ./...`, `go test ./...`, `go mod verify`,
     `just ci-full` (incl. `tests/lint-fixtures`, which proves the renamed depguard prefixes still fire),
     `just lima-example-all`; CI green on the PR; squash-merge.
3. **The move** (Decisions 9–11): freeze; mirror; filter-repo; scan (zero hits); `just ci` on the rewritten
   tip; create the private `pyvvo/funcd`; push `main`, tag and release `v0.1.0`; CI green on `main`; scan a
   fresh clone; go public; repo settings and ruleset; archive the old repo; copy the board.
4. **First PRs on `pyvvo/funcd`, through the queue:** release-please (`release-please.yml`, config,
   manifest `0.1.0`, `version.txt`) and the board pointers (`driver.py`, CLAUDE.md); then the review gate,
   with the `docs/PROJECT-SUMMARY.md` row.

### Test plan

| Test | Scenario |
|---|---|
| `TestScenarioShimFromPinnedModule` (`cmd/funcd`): the extracted shims equal the files under `langmod.Dir` | shim-from-pinned-module |
| `TestScenarioBenchUsesEmbeddedShims` (`cmd/funcd`): empty flags resolve to extracted embedded shims | bench-uses-embedded-shims |
| `TestPinsAreReleaseTags` (`langmod`): both requirements are semver tags, not pseudo-versions; the `check-hygiene` version gate; the v0.2.0 → v0.3.0 bump as the live drill | bump-language-release |
| `TestScenarioLaneStagesResolve` (`langmod`): every `lanes.yaml` `dir`/`stage`/`from` path exists in its module | lane-reads-committed-build (static half) |
| `TestScenarioCuratedImageBuilds` (updated): the nodejs22 Dockerfile copies from the `shim` context | runtime-image-from-pinned-shim |
| the existing e2e suites on `langmod` (`just test-e2e`) | tests-read-pinned-examples |
| `just lima-example-all` | lane-reads-committed-build, duckdb-lane-builds-bundle, runtime-image-from-pinned-shim |
| manual, recorded in the review: `TestScenarioShimFromPinnedModule` under a `go.work` with a sibling clone whose shim differs | local-override-go-work |
| the scan on the rewritten mirror and on a fresh clone of `pyvvo/funcd` | history-scrubbed |
| a PR merged through the queue; a direct merge refused | merge-rules-enforced |
| `gh api`: old repo archived and private; board item count and Statuses equal the source | old-repo-archived |
| an external scratch module `go get github.com/pyvvo/funcd/pkg/sdk@main`; the grep gate | module-path-moved |

**Definition of done:** the four sub-checks, `just ci-full` (with `tests/lint-fixtures`) and every Lima
lane green; CI green on `pyvvo/funcd` main; the grep gate leaves the old path only in frozen ADRs,
`docs/reviews/`, `docs/legacy/` and this ADR; the history scan has zero hits; the old repo is archived; the
board is moved.

## Review checklist

- [ ] `go.mod` declares `github.com/pyvvo/funcd` and pins both language modules to release tags.
- [ ] funcd has no `shim/`, `examples/js/` or `examples/python/`; `examples/funcdconfig.yaml` remains.
- [ ] `cmd/funcd` and `cmd/funcdctl` embed the shims from the language modules; `funcd bench` defaults to
      them.
- [ ] No test, lane or recipe writes under a module dir; writers use `.modcopy/`.
- [ ] `tests/e2e` imports no `internal/` package; its shim helper uses the language module directly.
- [ ] Both Dockerfiles copy from the `shim` named context; the duckdb image is untouched.
- [ ] `lanes.yaml` lanes name their `module`; only duckdb uses `copy: true`; `lane.py` resolves roots
      through `go list -m`.
- [ ] Grep gate clean; no username, home path or bank name in any changed file or public object.
- [ ] `pyvvo/funcd` is public, squash-only, linear, merge queue with no bypass, required checks `ci`, `e2e`,
      `pr-title`, release-please from `v0.1.0`.
- [ ] `green-0-rabbit/funcd` is archived and private; the board copy has every item with its Status.
- [ ] ADR-0001's Status line carries the partial-supersession back-link.

## Consequences

- **Positive:** one source per shim and example; `just ci` needs no node or uv; shim changes reach funcd as
  reviewed bumps; funcd is public under enforced merge rules.
- **Negative:** a shim fix takes two PRs (the language release, then the funcd bump); pre-move PR links name a
  private archive; every commit hash changes.
- **Risks accepted:** a bad language tag surfaces only at bump time (workaround 1); a cold module cache needs
  the network, as `go build` already does; once public, secret scanning flags the two forged key IDs in
  `e2e/s3.venom.yml` (test fixtures, dismissed as such).

## Open questions

- The shim contract's form and its conformance suite: a follow-up ADR.
- A vanity import path: only with a domain, through a superseding ADR.
- The `pyvvo/funcd-functions` layout: its own ADR when real functions land.
- Release binaries on the release-please tags: an ADR-0026 successor, if wanted.

## References

- Go Modules Reference — `go list -m`, `go mod download`, workspaces, module zip contents
  (<https://go.dev/ref/mod>).
- Docker build `--build-context` (named contexts): <https://docs.docker.com/reference/cli/docker/buildx/build/>.
- GitHub: copying a project, "draft issues and associated field values" are copied (checked 2026-09-28):
  <https://docs.github.com/en/issues/planning-and-tracking-with-projects/creating-projects/copying-an-existing-project>.
- GitHub: merge queue and rulesets; `refs/pull/*` are read-only.
- git-filter-repo: <https://github.com/newren/git-filter-repo>.
- Prior art: wasmCloud keeps one repo per language SDK (checked 2026-09-27).
