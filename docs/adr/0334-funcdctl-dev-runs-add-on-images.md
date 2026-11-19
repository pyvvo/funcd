# ADR-0334: funcdctl dev runs add-on engines on the local Docker or containerd

- **Status**: Accepted (2026-11-19), by the decider's standing go-ahead for FEAT-0018 ("go ahead, don't wait for
  me", "until we ship entirely the new feature"), after two rounds of an independent judge (a subagent), every
  Blocker and Major folded
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Tags**: addons, funcdctl, dev, docker, auth
- **Realizes**: [FEAT-0018/F260](../feat/0018-feat-addons.md) (add-ons under `funcdctl dev`)
- **Supersedes (in part)**, with back-links at acceptance: [ADR-0171](0171-static-credential-list.md) Decisions 3 and 7
  and [ADR-0314](0314-a-fresh-funcd-answers-on-loopback-until-it-has-a-real-credential.md) Decisions 2, 5 and 6: each
  refusal of `funcd.DevToken` also covers `funcd.DevAdminToken`, and `auth.token` refuses it (Decision 5).
- **Relates to**: [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) (the Addon kind),
  [ADR-0333](0333-an-add-on-objects-data-in-funcds-backups.md) (engine files, `deletion`), [ADR-0237](0237-sql-in-funcdctl-dev.md)
  and [ADR-0167](0167-process-worker-crash-recovery.md) (dev's engines, the reap), [ADR-0125](0125-funcdctl-dev-local-run.md)
  (`--persist`), [ADR-0231](0231-durable-storage-for-add-on-engines.md) and
  [ADR-0288](0288-a-boot-reclaim-quarantines-data-no-object-names.md) (storage, quarantine),
  [ADR-0269](0269-a-provider-engine-reaches-only-its-one-internal-endpoint.md) (the fence),
  [ADR-0254](0254-funcdctl-dev-config-file.md) (the config file), [ADR-0277](0277-a-containerd-worker-gets-one-stop-grace.md) (the grace)

## Context & Need

`funcdctl dev` runs funcd on a laptop with process workers, and it runs the Lake and SQL engines it bundles with its
own runtime (ADR-0237). An add-on's engine is an image built elsewhere (ADR-0332), which a process runtime cannot run.
This ADR lets dev run it on the container engine the developer already has, so an App that uses an add-on runs under
dev as on a server. Its callers: the add-on reconciler, through the add-on runtime port; the backup service, through
ADR-0333's `AddonFiles`; and the developer, who registers Addons with dev's admin token.

## Scenarios

- `scenario: dev-addon-docker`. *Given* Docker's socket and the Addon `rauthy`, *When* the developer applies the
  IdentityProvider `login` under `funcdctl dev`, *Then* it turns `Ready` and `GET /auth/v1/ping` on
  `login.localhost:<gport>` answers 200.
- `scenario: dev-addon-runtime-choice`. *Given* both Docker and nerdctl, *Then* dev uses Docker; *Given*
  `dev.addons.runtime: containerd`, *Then* nerdctl; *Given* neither, *Then* the object stays `Pending`,
  `RuntimeUnavailable`, naming what dev looked for.
- `scenario: dev-addon-data`. *When* dev stops, *Then* each engine gets SIGTERM and the grace, and its container and
  volume are removed; with `--persist` the volume stays, and the next run starts on it.
- `scenario: dev-addon-crash-reaped`. *Given* a dev run killed with SIGKILL while `login`'s engine runs, *When* dev
  starts again in that project, *Then* the old container is gone before anything converges; without `--persist` its
  volume is gone too and `login` starts empty; with `--persist` the new engine starts on its data.
- `scenario: dev-addon-two-projects`. *Given* two projects that each run `login` under dev at once, *Then* each has
  its own container and volume, and stopping one leaves the other serving.
- `scenario: dev-addon-deletion`. *Given* `--persist` and `login` with `deletion: delete`, *When* it is deleted,
  *Then* its volume is removed and a new `login` starts empty; with `retain`, the volume moves into the quarantine.
- `scenario: dev-addon-register`. *Given* an Addon in App mode's `apply` list or in the folder, *When* dev starts,
  *Then* it is registered, with no namespace, before any object of its kind; *When* the developer applies one with
  `funcdctl --token funcd-dev-admin-token`, *Then* it is too; with dev's developer token, it gets 403.
- `scenario: dev-admin-token-loopback-only`. *Given* a daemon with `auth.token: <own>`, *Then* a call with
  `funcd-dev-admin-token` gets 401; *Given* `WithDevAdminToken()` with `WithListenAddr("0.0.0.0:0")`, or with
  `WithEdgeAuth()` and `WithDataPlaneAddr("0.0.0.0:0")`, *Then* `New` fails `Invalid` naming that address option;
  *Given* the token in `WithCredentials`, a token file or `auth.token`, *Then* each is refused, with no token in the
  error.
- `scenario: dev-addon-backup`. *When* a Backup and an in-place Restore of `login` run under dev, *Then* they behave
  as ADR-0333's `addon-backup-member` and `addon-restore-in-place` say.

## Scope

In: the Docker and nerdctl drivers of the add-on runtime under dev, their containers and volumes, the session's key,
dev's stop and reap of them, the engine files, and registering an Addon under dev with its token. Out: a remote
Docker host, Docker Desktop's VM file sharing, dev on Windows, and a server's isolation of an engine (Decision 2).

## Constraints & Decision drivers

- One port: the add-on reconciler sees the same `provider.Runtime` and `provider.Volumes` under dev as on a server
  (ADR-0087, ADR-0231).
- No new daemon on the laptop: dev uses the Docker or containerd the developer runs (colima on macOS).
- dev keeps its data only with `--persist`, under the project's persist root (ADR-0125 Decision 7).
- Crash-only: what a killed dev leaves, its next start removes before anything else (ADR-0167).
- A published token works only on loopback, and no credential list holds one (ADR-0314, ADR-0171).

## Alternatives considered

- **Embed a container runtime in funcdctl**. Rejected: macOS has no namespaces, so it would still need a VM.
- **Run add-ons only on a server**. Rejected: FEAT-0018's exit criterion runs the App under dev.
- **Docker only**. Cheaper, but a colima user on its containerd runtime has no Docker socket; the decider asked for
  both (card 6).
- **A Docker driver of `runtime.Runtime` under `provider.NewRuntime`**: it reuses Converge's fingerprint, probe and
  storage rules, but its host-path mounts and netns address fit neither a named volume nor a port published from a
  VM. Rejected: the drivers implement `provider.Runtime` whole, as `internal/sql/devengine` does.
- **Make dev's developer token an admin**. Fewer tokens, but every dev session would then hide the refusals a
  developer meets on a server.
- **A token dev makes for each session and prints in its banner**. Nothing fixed is published, but each script reads
  it anew at every run. Rejected: the fixed `DevToken`, which works only on loopback, is the precedent (ADR-0314).

## Decision

1. **Two drivers.** Under dev, the add-on runtime is the Docker driver, the Engine API on the first unix socket that
   answers among `FUNCD_DEV_DOCKER_HOST`, `DOCKER_HOST`, the active Docker context's endpoint (`DOCKER_CONTEXT`, else
   `currentContext` in `~/.docker/config.json`), colima's `~/.colima/default/docker.sock`, Docker Desktop's
   `~/.docker/run/docker.sock` and `/var/run/docker.sock`; or the nerdctl driver, the `nerdctl` CLI on `PATH`. Docker
   comes first; `dev.addons.runtime: docker | containerd` in `funcdctl.config.yaml`, a key with no flag as `dev.watch`
   is (ADR-0255 Decision 1), forces one, and another value is refused at start. With neither, an object stays
   `Pending`, `RuntimeUnavailable` (ADR-0149), naming what dev looked for and, on macOS, `colima nerdctl install`.
   Dev passes the driver as `WithAddonRuntime(r, "containerd")`, so an Addon's default `runtimes` runs.
2. **The engine** is one container per object, labelled with its spec's hash and replaced when that changes, with no
   restart policy and the stop signal SIGTERM; each stop gives it ADR-0277's default grace (10 s), then removes it.
   This is the environment of ADR-0333's spec ([`restore-start.fizz`](../design/addon/restore-start.fizz)): an
   engine starts from the current spec only. It runs as its image's user, its port published on `127.0.0.1` at a
   free port that `status.address` holds. Dev does not honor the rest of a server's engine: it applies no ADR-0269
   fence, as for dev's Lake engine (ADR-0269 Consequences); that port serves every path, `privatePaths` included, to
   any local process; dev does not enforce `resources` and logs so once per object, as a runtime without
   `LimitEnforcer` does (ADR-0291 Decision 1); and the Docker driver sends no pull credentials, so a private image is
   pulled first with the developer's own `docker pull` (nerdctl pulls with the developer's own login).
3. **The storage** is a named volume at `storage.path`, named as the engine's container (Decision 6) and mounted with no
   copy-up (Docker's `NoCopy`, nerdctl's `volume-nocopy`), so no file of the image shows through it; the helper
   (Decision 4) sets a new one's root to `storage.uid:gid`, mode 0700, as ADR-0231 Decision 2 sets the leaf. The
   driver implements `provider.Volumes` as the SQL dev engine does: `Reclaim` removes the volume, `--persist` or not,
   and fails `Conflict` while its container exists; `List` names the session key's object volumes; `Quarantine` keeps
   each entry (ADR-0288, which ADR-0333 applies to `retain`) as a volume of its own, labelled with the key, the ref
   and its since, holding `data/` and its `state`, filled by a copy move through the helper (ADR-0288 Decision 3).
   ADR-0333's return by hand copies an entry into the object's volume through the same helper, with dev stopped.
4. **The engine files** (ADR-0333's `AddonFiles`) are reached only through a helper container (`busybox`, pinned by
   digest) that mounts the volume at `/d`. `Newest` lists `snapshot.path` twice, `addonSnapshotQuiet` apart, and
   streams only the newest file of ADR-0333 Decision 2 whose size and modification time match in both, so no VM
   clock is compared with the host's. `Put` writes the copy owned by `uid:gid`; `Clear` empties `restore.path`.
   Docker reads and writes with the archive API on a helper container it creates and never starts, and runs the
   helper to list and to clear; nerdctl runs `nerdctl run --rm -i -v <volume>:/d <helper> tar …` (or `stat`, `rm`),
   since `nerdctl cp` needs a running container and lacks `--archive`.
5. **Registering under dev.**
   - `funcd.DevAdminToken` (`funcd-dev-admin-token`) is fixed and documented as `funcd.DevToken` is.
     `WithDevAdminToken()`, which only `funcdctl dev` passes, makes the credential store answer it as an admin,
     whichever option built the store; `WithDevAuth` and `InMemory()` never add it.
   - `New` refuses a store that accepts it off loopback, for the control plane and, with `WithEdgeAuth`, the edge
     (`checkDevTokenBind`, ADR-0314 Decisions 5 and 6). `WithCredentials` refuses it (ADR-0171 Decision 7); the
     daemon refuses it in a token file (ADR-0171 Decision 3) and in `auth.token`, before anything opens. No error
     holds a token.
   - Dev applies Addons first, with it: App mode's `apply` list and folder mode's Addon documents, which dev's reader
     leaves with no namespace or resource group, as a cluster-scoped kind. `pruneStale` at a `--persist` start and a
     reload's prune delete Addons and add-on objects with it, each object before its Addon.
   - `WithAnyAddonRegistry()` allows every registry: dev reads no `funcdconfig.yaml`, so no `addons.registries`
     (ADR-0332 Decision 6), and it runs the images its developer names.
6. **The session's key** is the first 16 hex digits of the sha256 of the state dir's absolute path (`devStateDir`:
   `<persist-to>/process` under `--persist`, ADR-0125 Decision 7, else the project's dir in the user cache dir). It
   is in each container's and volume's name, `funcd-dev-addon-<key>-<ns>-<name>`, and in its label `funcd/dev-key`.
   One session holds a state dir at a time (ADR-0167 Decision 3). Converge, the stop and the reap touch only the
   key's containers and volumes, so two projects or two persist roots never share an engine or its data.
7. **Dev's stop and the reap.** `StopAll`, in dev's cleanup once the platform has stopped (as the SQL dev engine's),
   stops every engine of the key at once with SIGTERM and the grace, removes the containers, then the volumes unless
   `--persist`. `New` reaps the same way before anything converges: every leftover container of the key, helpers
   included, and, when the run has no `--persist`, every volume of the key, quarantine entries included, as ADR-0237's
   `dev-crashed-engine-reaped` reaps an engine (ADR-0167 Decision 5).
   Dev builds the driver once `process.Open` holds the session's registry, so a second session is refused first.

## Design model

None: the drivers implement ADR-0087's port, and one session alone holds its key (Decision 6), so no two actors share
a container or a volume; a stop cut by a kill leaves only what the next start's reap removes with the same steps, as
ADR-0167's reap does. The mechanisms it reuses keep their specs: ADR-0333's
[`restore-start.fizz`](../design/addon/restore-start.fizz), with Decision 2 as its environment, and ADR-0288's
[`quarantine.fizz`](../design/storage/quarantine.fizz).

## Behavior rows

- **funcdctl-dev.md**, added: "The add-on runtime under dev" (Docker first, `dev.addons.runtime`, `RuntimeUnavailable`
  naming what dev tried, an engine and a volume per object and project), `decided`; "An add-on object under dev",
  beside "No isolation" (no ADR-0269 fence, every path on its loopback port, `resources` logged as not enforced, a
  private image pulled first), `accepted`; "Registering an Addon under dev" (the admin token, Addons applied first and
  pruned with it, any registry), `decided`. Changed: "A stop" (then each add-on engine's SIGTERM and grace, all at
  once, its container removed, its volume too unless `--persist`) and "Dev is killed" (add-on engines run on until
  the next start removes them, and their volumes without `--persist`).
- **security.md**, added: "The dev admin token" (only `funcdctl dev` answers it, on loopback; `New` off loopback,
  `WithCredentials`, a token file and `auth.token` refuse it), `decided`. Changed: "A real token or
  `auth.credentials`" (the dev admin token gets 401 too) and "`funcd.New` with `DevToken` off loopback" (or
  `DevAdminToken`).

## Temporary workarounds

None.

## Contracts

```go
package funcd // pkg/funcd

const DevAdminToken = "funcd-dev-admin-token" // dev's admin token, fixed and documented as DevToken is (Decision 5)

func WithDevAdminToken() Option // the store also answers DevAdminToken as an admin; New refuses it off loopback
// WithAddonRuntime runs add-on engines on r for these modes; r is also provider.Volumes and workload.AddonFiles.
func WithAddonRuntime(r provider.Runtime, modes ...string) Option
func WithAnyAddonRegistry() Option // ADR-0332 Decision 6's image rule allows every registry
```

```go
package docker // internal/provider/docker: the Docker driver of provider.Runtime under funcdctl dev

// Socket returns the first socket of Decision 1 that answers GET /_ping; Unavailable names each one tried.
func Socket(ctx context.Context) (string, error)
// New reaps key's leftover containers, and their volumes unless persist, before it returns (Decision 7).
func New(ctx context.Context, socket, key string, persist bool, logger *slog.Logger) (*Runtime, error)
func (r *Runtime) Converge(ctx context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error)
func (r *Runtime) Teardown(ctx context.Context, ref provider.ProviderRef) error // the container; the volume stays
func (r *Runtime) Reclaim(ctx context.Context, ref provider.ProviderRef) error  // Conflict while its container exists
func (r *Runtime) List(ctx context.Context) ([]provider.ProviderRef, error)     // the key's object volumes, sorted
func (r *Runtime) Quarantine() quarantine.Store
func (r *Runtime) StopAll()
// and ADR-0333's workload.AddonFiles (Newest, Put, Clear), through the helper (Decision 4)
```

```go
package nerdctl // internal/provider/nerdctl: the same methods, through the nerdctl CLI

func Find() (string, error) // nerdctl on PATH; Unavailable names `colima nerdctl install` on macOS
func New(ctx context.Context, bin, key string, persist bool, logger *slog.Logger) (*Runtime, error)
```

```yaml
# funcdctl.config.yaml
dev:
  addons:
    runtime: docker
```

| Consumes | Exposes |
|---|---|
| `FUNCD_DEV_DOCKER_HOST`, `DOCKER_HOST`, the Docker context, the sockets of Decision 1, or `nerdctl` on `PATH` | containers and volumes `funcd-dev-addon-<key>-<ns>-<name>`, label `funcd/dev-key` |
| `dev.addons.runtime`, `--persist`, `--persist-to`, the session's state dir | `funcd.DevAdminToken` (admin, loopback only) and the three options |

## Implementation plan

- `internal/provider/docker/docker.go`, `internal/provider/nerdctl/nerdctl.go`; `cmd/funcdctl` (the choice, the key,
  the options, Addons first with the admin token in `applyOrdered` and both prunes, cluster-scoped kinds in
  `devDocuments`); `pkg/funcd` (the constant, the options, `checkDevTokenBind` and `checkCredential` for both
  tokens); `cmd/funcd/credentials.go` (the token-file and `auth.token` refusals). The work starts from experiment
  0025's spike, where the Docker driver ran. No new dependency: the Engine API over `net/http`, and nerdctl as a CLI.
- Tests: each Scenario named after it, the add-on ones in `cmd/funcdctl` against Docker when a socket answers and
  skipped with the reason otherwise, `dev-admin-token-loopback-only` in `pkg/funcd` and `cmd/funcd` with no Docker.
  Unit tests: the nerdctl driver with a fake `nerdctl` on `PATH` that records each command (the labels, the stop's
  signal and grace, each helper `tar`, `stat` and `rm`); the socket order, the key and the quiet rule.
- Real platform: the Scenarios on colima's Docker; on colima's containerd runtime (`colima start --runtime
  containerd`, `colima nerdctl install`), `dev-addon-docker`, `dev-addon-data` and `dev-addon-backup` with
  `dev.addons.runtime: containerd`, run by hand and recorded in the implementation PR.
- Done: `just ci` and `just check-behavior` green, and both runs above.

## Review checklist

- [ ] Docker is tried first, over Decision 1's sockets in order; `dev.addons.runtime` forces a driver; neither gives
      `RuntimeUnavailable` naming what was tried.
- [ ] An engine has no restart policy, the stop signal SIGTERM, the grace before removal, and its image's user.
- [ ] Every container and volume carries the key; Converge, `StopAll` and the reap touch no other, stop engines at
      once, and keep volumes only with `--persist`.
- [ ] `Reclaim` removes the volume under `--persist` too, `Conflict` while its container exists; `List` and
      `Quarantine` follow Decision 3.
- [ ] Files are reached only through the helper; a new volume's root is `storage.uid:gid`, mode 0700; `Newest`
      streams one file, judged quiet over two listings.
- [ ] Only `WithDevAdminToken` adds `DevAdminToken`; `New` off loopback, `WithCredentials`, a token file and
      `auth.token` refuse it, with no token in any error.

## Consequences

- The key follows the state dir's path: a project or persist root that moves gets a new key, and the old key's
  volumes stay until removed by hand (`docker volume rm`).
- Under dev, a quarantine entry is a labelled volume, and it comes back by hand, as ADR-0333 returns an add-on object's
  storage on a server: stop dev, copy the entry into the object's volume with the helper, start dev.
- An App with an add-on runs under dev on colima (Docker or containerd), Docker Desktop or Linux.
- A developer has an admin token under dev, so `funcdctl` can register an Addon there; no daemon answers it.
- rauthy accepts http callbacks on `localhost` and loopback addresses only, so a host that signs users in under dev is
  `localhost` or `127.0.0.1` (experiment 0025).

## Open questions

- **An automated containerd lane for the nerdctl driver** (containerd turned on in `funcd-linux-tests`, its tests run
  as root as `just test-volumes-root` runs its own): a follow-up issue filed with the implementation, whose PR records
  the manual check until then.

## References

- Feature issue [#1500](https://github.com/pyvvo/funcd/issues/1500) (FEAT-0018 F260).
- Experiment 0025 (pyvvo/experiment at a0643b3c, folder `0025-addons-rauthy`): every check ran on the Docker driver,
  and its spike is where the plan starts.
- The decider's card 6 (2026-11-19).
- Read 2026-11-19: [nerdctl's command reference](https://github.com/containerd/nerdctl/blob/main/docs/command-reference.md)
  (`cp`, `run --restart`), [colima's README](https://github.com/abiosoft/colima) (`--runtime containerd`, `nerdctl
  install`), [Docker Desktop for Mac's permissions](https://docs.docker.com/desktop/setup/install/mac-permission-requirements/)
  (`~/.docker/run/docker.sock`; `/var/run/docker.sock` only with its setting on).
