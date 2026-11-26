# ADR-0359: Under funcdctl dev, an opt-in runs funcd's own runtimes in containers, from images funcdctl carries (amends ADR-0346)

- **Status**: Proposed (2026-11-26)
- **Date**: 2026-11-26
- **Deciders**: green-0-rabbit
- **Tags**: dev, runtime, functions, jobs, cli
- **Realizes**: [FEAT-0012/F276](../feat/0012-feat-infrastack.md) (funcd's runtimes in containers under dev)
- **Supersedes (in part)**, with a back-link at acceptance:
  [ADR-0346](0346-under-funcdctl-dev-a-worker-on-an-addons-runtime-runs-on-devs-container-engine.md): its Scope's Out
  item "funcd's own runtimes on the engine", and Decision 2's sentence "No other Function or Job worker in process mode
  sets `Image`", since under the opt-in a worker on funcd's own runtimes does (Decision 1)
- **Relates to**: [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md) (the embedded images) ·
  [ADR-0128](0128-funcdctl-dev-interpreter-config-and-seedable-writes.md) (dev's interpreters) ·
  [ADR-0145](0145-multi-arch-function-bundles-and-arch-aware-placement.md) (a worker's platform) ·
  [ADR-0254](0254-funcdctl-dev-config-file.md) (a key added to its file) ·
  [ADR-0334](0334-funcdctl-dev-runs-add-on-images.md) (dev's container engine)

## Context & Need

Under `funcdctl dev` every worker of funcd's own runtimes, `nodejs22` and `python314`, runs on the process driver from
the host's interpreters: dev needs node for a Node Function and a Python of 3.12 or later for a Python one, and refuses
to start without them (`cmd/funcdctl/dev.go:1704-1754`). On 2026-11-26 the decider asked that these workers can run in
containers, so that a developer's machine needs no Node or Python: as an opt-in, off by default, from images that
funcdctl embeds. ADR-0346 already runs a worker on an Addon's runtime on dev's container engine, and left funcd's own
runtimes out of its Scope.

Experiment 0028 ran this with a patch on funcd 91b294e3, on colima's Docker: a Function on each runtime and a Job on
`nodejs22` ran in Linux containers with no node and no Python on the PATH. A start took 1.49 s, against 0.42 s on the
process driver, and 0.53 s of it reloaded images that the engine already held. A save was served in 0.85 s on Node and
1.07 s on Python, against 0.62 s and 0.61 s. The two image tars are 74.3 MiB. Callers: a developer who runs dev, and
dev itself.

## Scenarios

- `scenario: dev-containers` — *Given* `funcdctl dev --containers` on Docker or nerdctl and a folder with a Function on
  `nodejs22`, a pooled one, one on `python314` and a Job on `nodejs22`, *When* dev starts and a run of the Job is
  created, *Then* the three Functions answer from Linux containers and the run succeeds in one.
- `scenario: dev-containers-no-host-runtime` — *Given* a PATH with no node and no Python of 3.12 or later, *Then*
  `funcdctl dev --containers` serves both Functions, and `funcdctl dev` refuses to start, as it does today.
- `scenario: dev-containers-key` — *Given* `dev.containers: true` in `funcdctl.config.yaml`, *Then* `funcdctl dev`
  runs as with the flag, and `funcdctl dev --containers=false` runs the workers on the process driver.
- `scenario: dev-containers-save` — *When* a Function's source file changes, *Then* its new answer is served from a new
  container.
- `scenario: dev-containers-load-once` — *Given* an engine without funcd's images, *When* dev starts, *Then* it loads
  both before it serves; *when* dev starts again, *then* it loads neither.
- `scenario: dev-containers-linked-build` — *Given* a Python Function in folder mode and a Node Function beside a
  `node_modules`, *Then* each worker reads its sources and its modules.
- `scenario: dev-containers-no-engine` — *Given* `--containers` and neither engine, *Then* dev refuses to start,
  naming what it tried.

## Scope

**In**: Decisions 1 to 7. **Out**: installing a project's dependencies (npm, pip), which dev leaves to the developer as
today; native code built for the host's OS, which does not load in a Linux container; the opt-in as a default; a
server, unchanged. **Unchanged** (ADR-0346): a worker on an Addon's runtime, and the engine workers of Lake and SQL,
which keep their own runtimes.

## Constraints & Decision drivers

- The decider, 2026-11-26: an opt-in, off by default, and the images embedded in funcdctl, not pulled or built.
- Reuse ADR-0346's container worker as written: its shape, pair, driver, network, local API, names and reap.
- Measured in experiment 0028 (see Context): the reload at every start and the 74.3 MiB of images decide Decisions 3
  and 4.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| Pull the images from a registry at first use | a smaller funcdctl | network at the first start, and a registry kept in step with each funcdctl | Rejected (decider: embed) |
| Build the images on the engine from funcd's Dockerfiles | nothing to carry | a slow first start, and network for the base images | Rejected |
| On by default | no flag to learn | every dev user would need Docker or nerdctl | Rejected (decider: opt-in) |
| Load the images at every start | no tag and no check | 0.53 s on every start | Rejected |
| Read the images through `embedimg.Tar` | no new function | it embeds all four images, DuckDB's and rqlite's too | Rejected |

## Decision

1. **The opt-in.** `funcdctl dev --containers`, or `dev.containers: true` in `funcdctl.config.yaml`, where a flag wins
   (ADR-0254 Decision 3), default false. Under it, the worker of each Function, pooled Function and Job run on
   `nodejs22` or `python314` runs on dev's container engine (ADR-0334 Decision 1, which `dev.addons.runtime` steers) as
   ADR-0346 runs a worker on an Addon's runtime: ADR-0346's Decisions 1 to 8 apply, its gate included, with Decision
   4's image in place of a Revision's runtime image. Without the opt-in, nothing changes.
2. **No runtime on the host.** Under the opt-in, dev looks up no interpreter and extracts no shim, and the reconciler
   needs no shim command, since every worker of those runtimes has an image. With neither engine, dev refuses to start,
   naming what it tried (ADR-0334 Decision 1): the developer asked for containers, so dev does not fall back to the
   host's interpreters.
3. **The images funcdctl carries.** The `-tags dev` funcdctl embeds the `nodejs22` and `python314` images that `just
   build-runtime-images` builds for the server (ADR-0054), for Linux on funcdctl's own architecture. It reads them
   through `embedimg.WorkerTar`, which reads only those two, each from a `go:embed` of its own, so funcdctl carries no
   DuckDB or rqlite image. The thin funcdctl carries none. When the engine's platform (ADR-0346's `ImagePlatform`) is
   not the images', dev refuses to start, naming both.
4. **Loading.** At start, dev names each image `funcd/runtime-<runtime>:dev-<12 hex digits>`, the first digits of the
   sha256 of its embedded tar. When the engine lacks that reference, dev loads the tar and gives the image it loaded
   that reference; when the engine holds it, dev loads nothing. A worker's spec names that reference. Two funcdctl
   versions on one engine therefore never run each other's image.
5. **Builds in a container.** A worker's bundle is copied into its volume (ADR-0346 Decision 4) with each symbolic
   link that points outside the bundle replaced by what it names, a file or a directory. Dev's folder mode delivers a
   Python build and a folder's `node_modules` as absolute links (`cmd/funcdctl/dev.go:787`, `:1607`), which name
   nothing inside a container. A link that stays inside the bundle is copied as a link.
6. **Platform and logs.** Such a worker runs on the engine's platform (ADR-0346 Decision 2, ADR-0145 Decision 6). Native
   code built for the host's OS does not load there, and dev neither checks it nor rebuilds it. Its records arrive as
   stdout lines, without their level or fields (ADR-0346 Decision 4).
7. **The release.** `release.yml` builds the two images on a runner of each architecture, `ubuntu-latest` and
   `ubuntu-24.04-arm`, as its SQL engine job does, and each target's fat build embeds the images of its architecture.

## Design model

None. The opt-in decides once, at start, where these workers run; the load is a check, then a load, before the
platform starts and before any worker; and the workers run on ADR-0346's driver, which adds no lock, timer or stop
path.

## Behavior rows

- **funcdctl-dev.md**: a new row "funcd's runtimes in containers" (Decisions 1, 2 and 4); "Leftover dev workers"
  covers them as it covers ADR-0346's.

## Temporary workarounds

None.

## Contracts

```go
// WorkerTar returns the embedded image tar of a worker runtime, "nodejs22" or "python314", and reads no other
// (internal/runtime/embedimg).
func WorkerTar(runtime string) (r io.Reader, ok bool)

// Engine gains, in internal/provider/docker and nerdctl's through the same Engine:
HasImage(ctx context.Context, ref string) (bool, error)
Load(ctx context.Context, tar io.Reader) (id string, err error) // the ID of the image the tar holds
Tag(ctx context.Context, id, ref string) error

// function.Deps gains the image of each of funcd's runtimes whose workers run in containers (internal/function).
OwnImages map[v1.RuntimeName]string

// pkg/funcd
func WithOwnRuntimeImages(images map[v1.RuntimeName]string) Option
```

| Key | Type | Default |
|---|---|---|
| `dev.containers` (flag `--containers`) | bool | false |

| Consumes | Exposes |
|---|---|
| ADR-0334's engine; the embedded tars; ADR-0346's worker driver | `--containers`, `dev.containers`; the images `funcd/runtime-<runtime>:dev-<digits>` on the engine |

## Implementation plan

1. `internal/runtime/embedimg`: one `go:embed` per image; `Tar` reads all four, `WorkerTar` the two.
2. `internal/provider/docker`: `HasImage`, `Load` and `Tag` on the Engine API (`/images/{name}/json`, `/images/load`,
   `/images/{name}/tag`); `internal/provider/nerdctl`: `image inspect`, `load` and `tag`. `Load` reads the image's ID
   from the tar's `manifest.json` as the tar streams to the engine.
3. `internal/function`: `Deps.OwnImages`; the gate and `addonImage` take an own runtime that has an image, in
   `workerSpec` and `pool.go`; `NewReconciler` needs no shim command when every own runtime has an image
   (`function.go:453`). `pkg/funcd`: `WithOwnRuntimeImages`.
4. `internal/provider/docker/workers.go`: the bundle copy replaces each link that leaves the bundle (Decision 5).
5. `cmd/funcdctl`: `--containers` and `dev.containers` through ADR-0254's loader; under them, no interpreter lookup,
   Decision 4's load before the platform starts, and the refusal with neither engine.
6. `.github/workflows/release.yml`: a job per architecture builds the two images and uploads them; each fat build
   downloads its architecture's.
7. Docs: the guide's dev page and the behavior row. Tests: one `TestScenario…` per scenario; the real-engine ones need
   the images that `just build-runtime-images` writes and skip without them or without an engine, as ADR-0346's skip
   with no engine.
8. Done when `just ci-full` passes and the real-engine scenarios pass on colima's Docker and on its containerd.

## Review checklist

- [ ] Without the opt-in, nothing changes: funcd's runtimes run on the process driver, and dev needs their interpreters.
- [ ] Under it, every worker on `nodejs22` or `python314`, pooled or not and a Job's run, sets `Image` and runs on the
      engine; engine workers do not.
- [ ] Dev loads an image only when the engine lacks its reference, and a worker names that reference.
- [ ] The fat funcdctl carries the two images and no DuckDB or rqlite image; the thin funcdctl carries none.
- [ ] A link that leaves the bundle is copied as what it names; one inside the bundle stays a link.
- [ ] With neither engine, or an engine on another platform than the images', `--containers` refuses to start and
      says why.

## Consequences

- **(+)** A developer runs Functions and Jobs on funcd's runtimes with no Node or Python installed, on the images that a
  server runs.
- **(−)** The fat funcdctl grows by the two images, 74.3 MiB, for each of its users, opted in or not.
- **(−)** A start takes about a second more than on the process driver and a save a fraction of a second more
  (experiment 0028); the first start on an engine also loads the images.
- **(−)** Records lose their level and fields (ADR-0346's cost), and a build with native code for the host's OS fails
  in the container.
- **(−)** A large `node_modules` is copied into the volume of each new revision.

## Open questions

None.

## References

- FEAT-0012 F276 and its issue [#1585](https://github.com/pyvvo/funcd/issues/1585); the decider's answers of 2026-11-26: an opt-in, and the images embedded in funcdctl
- Experiment 0028, `pyvvo/experiment` (private), folder `0028-dev-own-runtimes` at 223f0387 (pyvvo/experiment#43)
- ADR-0054, ADR-0128, ADR-0145, ADR-0254, ADR-0334, ADR-0344, ADR-0346
- `cmd/funcdctl/dev.go` (`devShimOptions`, `linkSources`), `internal/function/function.go` (`NewReconciler`,
  `workerSpec`), `internal/function/addonruntime.go` (`addonImage`), `internal/provider/docker/workers.go` (`tarTree`),
  `internal/runtime/embedimg/embedimg.go`, `.github/workflows/release.yml` (`build-sql-engine`)
