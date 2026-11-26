# ADR-0359: Under funcdctl dev, an opt-in runs funcd's own runtimes in containers, from images funcdctl carries (amends ADR-0346)

- **Status**: Implemented (2026-11-26), review passed (docs/reviews/adr-0359-implementation-claude-opus-5-5.md); Reviewing (2026-11-26, implemented); Accepted (2026-11-26), by the decider ("accept"), after three rounds of an independent judge at medium
  effort, every Major and Minor folded
- **Amended in place**: 2026-11-26, by the decider ("A, we should just amend and build it quickly"): a link to a
  directory the engine can see is bind-mounted read-only, not copied (Decision 5)
- **Date**: 2026-11-26
- **Deciders**: green-0-rabbit
- **Tags**: dev, runtime, functions, jobs, cli
- **Realizes**: [FEAT-0012/F276](../feat/0012-feat-infrastack.md) (funcd's runtimes in containers under dev)
- **Supersedes (in part)**, with a back-link at acceptance:
  [ADR-0346](0346-under-funcdctl-dev-a-worker-on-an-addons-runtime-runs-on-devs-container-engine.md): its Scope's Out
  item "funcd's own runtimes on the engine"; Decision 2's sentence "No other Function or Job worker in process mode
  sets `Image`"; and its Review checklist's first two items, which now hold without the opt-in only, since under it a
  worker on funcd's own runtimes sets `Image` and runs on the engine (Decision 1)
- **Relates to**: [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md) (the embedded images) ·
  [ADR-0128](0128-funcdctl-dev-interpreter-config-and-seedable-writes.md) (dev's interpreters) ·
  [ADR-0145](0145-multi-arch-function-bundles-and-arch-aware-placement.md) (a worker's platform) ·
  [ADR-0173](0173-container-execution-runs-solo.md) (a container runs each Function solo) ·
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

Measured on 2026-11-26 on colima's Docker, five saves each, with a folder under the home directory whose `node_modules`
holds express and `@aws-sdk/client-s3`, 23 MiB in 3,968 files: a save was served in a median 1.13 s (0.96 to 1.24 s)
when each revision's volume took a copy of it, and in 0.89 s (0.82 to 0.92 s) when the worker bound it (amended
2026-11-26).

## Scenarios

- `scenario: dev-containers` — *Given* `funcdctl dev --containers` on Docker or nerdctl and a folder with a Function on
  `nodejs22`, one that declares a pool worker, one on `python314` and a Job on `nodejs22`, *When* dev starts and a run
  of the Job is created, *Then* each Function answers from a Linux container of its own and the run succeeds in one.
- `scenario: dev-containers-no-host-runtime` — *Given* the folder of `dev-containers` and a PATH with no node and no
  Python of 3.12 or later, *Then* `funcdctl dev --containers` serves its Functions, and `funcdctl dev` refuses to
  start, as it does today.
- `scenario: dev-containers-key` — *Given* `dev.containers: true` in `funcdctl.config.yaml`, *Then* `funcdctl dev`
  runs as with the flag, and `funcdctl dev --containers=false` runs the workers on the process driver.
- `scenario: dev-containers-save` — *When* a Function's source file changes, *Then* its new answer is served from a new
  container.
- `scenario: dev-containers-load-once` — *Given* an engine without funcd's images, *When* dev starts, *Then* it loads
  both before it serves; *when* dev starts again, *then* it loads neither.
- `scenario: dev-containers-linked-build` — *Given* a Python Function in folder mode and a Node Function beside a
  `node_modules`, *Then* each worker reads its sources and its modules.
- `scenario: dev-containers-refused` — *Given* `--containers` and neither engine, or a funcdctl that carries the
  placeholders instead of images, *Then* dev refuses to start, saying why.

## Scope

**In**: Decisions 1 to 7. **Out**: installing a project's dependencies (npm, pip), which dev leaves to the developer as
today; native code built for the host's OS, which does not load in a Linux container; pooling in a container, still a
Backlog card (ADR-0173 Decision 4); the opt-in as a default; a server, unchanged. **Unchanged** (ADR-0346): a worker on
an Addon's runtime, and the engine workers of Lake and SQL, which keep their own runtimes.

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
| Load the images at every start | no reference to keep | 0.53 s on every start | Rejected |
| Tag the loaded image by its tar's sha256 at start | no build step | hashes 74 MiB at each start, and tags after the load, racing another session's load | Rejected |
| Read the images through `embedimg.Tar` | no new function | it embeds all four images, DuckDB's and rqlite's too | Rejected |

## Decision

1. **The opt-in.** `funcdctl dev --containers`, or `dev.containers: true` in `funcdctl.config.yaml`, where a flag wins
   (ADR-0254 Decision 3), default false. Under it, the worker of each Function and each Job run on `nodejs22` or
   `python314` runs on dev's container engine (ADR-0334 Decision 1, which `dev.addons.runtime` steers) as ADR-0346 runs
   a worker on an Addon's runtime: ADR-0346's Decisions 1 to 8 apply, its gate included, with Decision 4's image in
   place of a Revision's runtime image. A Function that declares `spec.pooling.worker` runs solo, in a container of its
   own, as under container execution (ADR-0173 Decision 2), since the images carry no pool host. A Function or a Job on
   another Node or Python runtime name, which no carried image serves, fails `RuntimeUnavailable` naming its runtime
   (ADR-0149). Without the opt-in, nothing changes.
2. **No runtime on the host.** Under the opt-in, dev looks up no interpreter and extracts no shim, and the reconciler
   needs no shim command, since every worker of those runtimes has an image. With neither engine, dev refuses to start,
   naming what it tried (ADR-0334 Decision 1): the developer asked for containers, so dev does not fall back to the
   host's interpreters.
3. **The images funcdctl carries.** The `-tags dev` funcdctl embeds the `nodejs22` and `python314` images that the
   server embeds (ADR-0054), for Linux on funcdctl's own architecture. It reads them through `embedimg.WorkerTar`, which
   reads only those two, each from a `go:embed` of its own, so funcdctl carries no DuckDB or rqlite image; the thin
   funcdctl carries none. Dev refuses to start, saying why, when funcdctl carries the committed placeholders instead of
   images (a build that skipped `just build-worker-images`), or when the engine's platform (ADR-0346's
   `ImagePlatform`) is not the images'.
4. **Loading.** `just build-worker-images` gives each image a second reference,
   `funcd/runtime-<runtime>:dev-<12 hex digits of its image ID>`, saves both of its references in its tar, and writes
   that one beside the tar. At start, under the opt-in, dev loads a tar only when the engine lacks that reference, and a
   worker's spec names it. A start with the images present loads nothing, and two funcdctl versions on one engine never
   run each other's image.
5. **Builds in a container.** A worker's bundle is copied into its volume (ADR-0346 Decision 4) with each symbolic link
   resolved as the OS resolves it, from the link's own directory. A link that resolves inside the bundle stays a link.
   One that resolves outside it to a directory the engine can see is bind-mounted read-only at the link's path, and the
   copy writes an empty directory there as its mount point (amended 2026-11-26): an engine on the host's network sees
   every path, and one in a VM the user's home directory, which colima and Lima share at the same path (ADR-0346
   Decision 5's classification). Any other link that resolves outside the bundle is replaced by a copy of what it names,
   a file or a directory; a link cycle fails the copy, naming the link. This holds for every container worker of dev,
   one on an Addon's runtime too. Dev's folder mode delivers a Python build and a folder's `node_modules` as absolute
   links (`cmd/funcdctl/dev.go:787`, `:1607`), which name nothing inside a container.
6. **Platform and logs.** Such a worker runs on the engine's platform (ADR-0346 Decision 2, ADR-0145 Decision 6). Native
   code built for the host's OS does not load there, and dev neither checks it nor rebuilds it. Its records arrive as
   stdout lines, without their level or fields (ADR-0346 Decision 4).
7. **The release.** `release.yml` runs `scripts/build-worker-images.sh` on a runner of each architecture,
   `ubuntu-latest` and `ubuntu-24.04-arm`, as its SQL engine job runs `scripts/build-sql-engine.sh`, and each
   target's fat build embeds the images of its architecture. `just build-worker-images` runs the same script, and
   `just build-runtime-images` runs it, then builds the DuckDB and rqlite images as today.

## Design model

None. The opt-in decides once, at start, where these workers run; the load is a check, then a load, before the
platform starts and before any worker; and the workers run on ADR-0346's driver, which adds no lock, timer or stop
path.

## Behavior rows

- **funcdctl-dev.md**: a new row "funcd's runtimes in containers" (Decisions 1 to 4); "Leftover dev workers" covers
  them as it covers ADR-0346's.

## Temporary workarounds

None.

## Contracts

```go
// WorkerTar returns the embedded tar of a worker runtime's image, "nodejs22" or "python314", and the dev reference
// the image carries; ok is false for another runtime, or when funcdctl carries the placeholder
// (internal/runtime/embedimg).
func WorkerTar(runtime string) (r io.Reader, ref string, ok bool)

// Engine gains, in internal/provider/docker and nerdctl's through the same Engine:
HasImage(ctx context.Context, ref string) (bool, error)
Load(ctx context.Context, tar io.Reader) error

// function.Deps gains the image of each of funcd's runtimes whose workers run in containers (internal/function).
OwnImages map[v1.RuntimeName]string

// pkg/funcd
func WithOwnRuntimeImages(images map[v1.RuntimeName]string) Option

// Mount gains, in internal/provider/docker, a host directory mounted at Target in place of a volume; a worker's
// RunSpec.Mounts carries each bind, read-only, after its bundle's volume (Decision 5, amended 2026-11-26).
type Mount struct {
	Volume, Target string
	ReadOnly       bool
	Bind           string // new
}
```

| Key | Type | Default |
|---|---|---|
| `dev.containers` (flag `--containers`) | bool | false |

| Consumes | Exposes |
|---|---|
| ADR-0334's engine; the embedded tars and their references; ADR-0346's worker driver | `--containers`, `dev.containers`; the images `funcd/runtime-<runtime>:dev-<digits>` on the engine |

## Implementation plan

1. `internal/runtime/embedimg`: one `go:embed` per image, and a reference file beside each worker image's tar; `Tar`
   reads all four, `WorkerTar` the two. The committed placeholders carry an empty reference. The server's containerd
   import takes the imported record named the reference it looked up, since a worker image's tar now names it twice
   (`internal/runtime/containerd/containerd_linux.go:1313-1330`).
2. `scripts/build-worker-images.sh ARCH` builds `nodejs22` and `python314`, tags each with its dev reference, saves
   both references and writes the reference file. In the `justfile`, `build-worker-images` runs it and
   `build-runtime-images` runs it first, and `embedimg-pin` and `embedimg-unpin` also cover the reference files.
3. `internal/provider/docker`: `HasImage` and `Load` on the Engine API (`/images/{name}/json`, `/images/load`);
   `internal/provider/nerdctl`: `image inspect` and `load`.
4. `internal/function`: `Deps.OwnImages`; the gate, `addonImage` and `workerPlatform` take an own runtime that has an
   image, and under the opt-in the gate fails one that has none `RuntimeUnavailable` (`function.go:1377`, where own
   runtimes skip it today); `NewReconciler` needs no shim command when every own runtime has an image
   (`function.go:453`). `pkg/funcd`: `WithOwnRuntimeImages`.
5. `internal/provider/docker/workers.go`: the bundle copy resolves links as Decision 5 says and returns its binds, which
   `Start` mounts; the Engine API takes a `Bind` as a bind, and nerdctl as `--mount type=bind,…,readonly` (amended
   2026-11-26).
6. `cmd/funcdctl`: `--containers` and `dev.containers` through ADR-0254's loader; under them, no interpreter lookup and
   no shim, Decision 4's load before the platform starts, and Decisions 2 and 3's refusals.
7. `.github/workflows/release.yml`: a job per architecture runs `scripts/build-worker-images.sh` and uploads the tars
   and their references; each fat build downloads its architecture's.
8. Docs: the guide's dev page and the behavior row. Tests: one `TestScenario…` per scenario; the real-engine ones need
   the images that `just build-worker-images` writes and skip without them or without an engine, as ADR-0346's skip
   with no engine.
9. Done when `just ci-full` passes and the real-engine scenarios pass on colima's Docker and on its containerd.

## Review checklist

- [ ] Without the opt-in, nothing changes: funcd's runtimes run on the process driver, and dev needs their interpreters.
- [ ] Under it, every worker on `nodejs22` or `python314`, a Job's run included, sets `Image` and runs on the engine; a
      pooled Function runs solo; engine workers do not change.
- [ ] Dev loads an image only when the engine lacks its dev reference, and a worker names that reference.
- [ ] The fat funcdctl carries the two images and no DuckDB or rqlite image; the thin funcdctl carries none.
- [ ] A link that resolves outside the bundle to a directory the engine can see is bind-mounted read-only, another one
      outside is copied as what it names, one inside stays a link, and a cycle fails the copy (amended 2026-11-26).
- [ ] With neither engine, an engine on another platform or the placeholders, `--containers` refuses to start and says
      why.

## Consequences

- **(+)** A developer runs Functions and Jobs on funcd's runtimes with no Node or Python installed, on the images that a
  server runs.
- **(−)** The fat funcdctl grows by the two images, 74.3 MiB, for each of its users, opted in or not.
- **(−)** A start takes about a second more than on the process driver and a save a fraction of a second more
  (experiment 0028); the first start on an engine also loads the images.
- **(−)** Records lose their level and fields (ADR-0346's cost), a pooled Function takes a container of its own, and a
  build with native code for the host's OS fails in the container.
- **(+)** A save binds a folder's `node_modules` instead of copying it: 0.89 s against 1.13 s with 23 MiB of modules
  (Context; amended 2026-11-26).
- **(−)** A bound directory is read-only and live, as a process worker's links are: every revision's worker reads its
  files as they are now, a link inside it that leaves it names nothing in the container, as an npm workspace's does,
  and its files keep their host owners and modes (amended 2026-11-26).
- **(−)** On an engine in a VM, as on macOS, a folder outside the home directory is still copied at each save, its
  `node_modules` included (amended 2026-11-26).
- **(−)** A load also moves the engine's `funcd/runtime-<runtime>:latest`, and each funcdctl version leaves its two
  images on the engine until the developer removes them.

## Open questions

None.

## References

- FEAT-0012 F276 and its issue [#1585](https://github.com/pyvvo/funcd/issues/1585); the decider's answers of
  2026-11-26: an opt-in, and the images embedded in funcdctl
- Experiment 0028, `pyvvo/experiment` (private), folder `0028-dev-own-runtimes` at cb7b9b18 (pyvvo/experiment#43)
- ADR-0050, ADR-0054, ADR-0128, ADR-0145, ADR-0149, ADR-0173, ADR-0254, ADR-0334, ADR-0344, ADR-0346
- `cmd/funcdctl/dev.go` (`devShimOptions`, `linkSources`), `internal/function/function.go` (`NewReconciler`,
  `workerSpec`), `internal/function/addonruntime.go` (`addonImage`, `workerPlatform`),
  `internal/function/poolaccess.go` (`pooled`), `internal/provider/docker/workers.go` (`tarTree`),
  `internal/runtime/embedimg/embedimg.go`, `internal/runtime/containerd/containerd_linux.go` (the embedded import),
  `justfile` (`build-runtime-images`, `embedimg-pin`), `.github/workflows/release.yml` (`build-sql-engine`)
