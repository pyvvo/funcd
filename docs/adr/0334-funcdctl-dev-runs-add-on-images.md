# ADR-0334: funcdctl dev runs add-on engines on the local Docker or containerd

- **Status**: Proposed
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Tags**: addons, funcdctl, dev, docker
- **Realizes**: [FEAT-0018/F260](../feat/0018-feat-addons.md) (add-ons under `funcdctl dev`)
- **Relates to**: [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) (the Addon kind),
  [ADR-0333](0333-an-add-on-objects-data-in-funcds-backups.md) (engine files),
  [ADR-0237](0237-sql-in-funcdctl-dev.md) (dev's own engine for a built-in add-on),
  [ADR-0254](0254-funcdctl-dev-config-file.md) (`funcdctl.config.yaml`),
  [ADR-0277](0277-a-containerd-worker-gets-one-stop-grace.md) (the stop grace)

## Context & Need

`funcdctl dev` runs funcd on a laptop with process workers, and it runs the Lake and SQL engines it bundles with its
own runtime (ADR-0237). An add-on's engine is an image built elsewhere (ADR-0332), which a process runtime cannot run.
This ADR lets dev run it on the container engine the developer already has, so an App that uses an add-on runs under
dev as on a server. Its callers: the add-on reconciler, through the add-on runtime port, and the developer.

## Scenarios

- `scenario: dev-addon-docker`. *Given* Docker's socket and the Addon `rauthy`, *When* the developer applies the
  IdentityProvider `login` under `funcdctl dev`, *Then* it turns `Ready` and `GET /auth/v1/ping` on
  `login.localhost:<gport>` answers 200.
- `scenario: dev-addon-runtime-choice`. *Given* both Docker and nerdctl, *Then* dev uses Docker; *Given*
  `addons.runtime: containerd` in `funcdctl.config.yaml`, *Then* it uses nerdctl; *Given* neither, *Then* the object
  stays `Pending`, `RuntimeUnavailable`, naming what dev looked for.
- `scenario: dev-addon-data`. *When* dev stops, *Then* the object's volume is removed; with `--persist` it stays, and
  the next run starts on it.
- `scenario: dev-addon-register`. *Given* an Addon in the `apply` list, *When* dev starts, *Then* the Addon is
  registered; *When* the developer applies one with `funcdctl --token funcd-dev-admin-token`, *Then* it is too.
- `scenario: dev-addon-backup`. *When* a Backup and an in-place Restore of `login` run under dev, *Then* they behave as
  ADR-0333's scenarios say.

## Scope

In: the Docker and nerdctl drivers of the add-on runtime under dev, their volumes, the engine files, how an Addon is
registered under dev. Out: a remote Docker host, Docker Desktop's VM file sharing, and dev on Windows.

## Constraints & Decision drivers

- One port: the add-on reconciler sees the same `provider.Runtime` under dev as on a server (ADR-0087).
- No new daemon on the laptop: dev uses the Docker or containerd the developer runs (colima on macOS).
- dev keeps its data only with `--persist` (ADR-0131).

## Alternatives considered

- **Embed a container runtime in funcdctl**. Rejected: macOS has no namespaces, so it would still need a VM.
- **Run add-ons only on a server**. Rejected: FEAT-0018's exit criterion runs the App under dev.
- **Docker only**. Cheaper, but a colima user on its containerd runtime has no Docker socket; the decider asked for
  both (card 6).
- **Make dev's developer token an admin**. Fewer tokens, but every dev session would then hide the refusals a
  developer meets on a server.

## Decision

1. **Two drivers.** Under dev, the add-on runtime is the Docker driver (the Engine API over a unix socket, from
   `FUNCD_DEV_DOCKER_HOST`, `DOCKER_HOST`, colima's default socket or `/var/run/docker.sock`, the first that answers)
   or the nerdctl driver (the `nerdctl` CLI on `PATH`). Docker comes first; `addons.runtime: docker | containerd` in
   `funcdctl.config.yaml` forces one. Dev's add-on mode is `containerd`, so an Addon's default `runtimes` runs.
2. **The engine** is one container per object, `funcd-dev-addon-<ns>-<name>`, its port published on `127.0.0.1` at a
   free port, labelled with its spec's hash, and replaced on a change with SIGTERM and ADR-0277's default grace
   (10 s), then removed. `status.address` is the published address.
3. **The storage** is a named volume of the same name at `storage.path`, which the engine image's directory and owner
   fill when it is new. dev removes it at its stop unless `--persist`.
4. **The engine files** (ADR-0333): Docker's archive API reads the newest file and writes the copy; a helper
   container (`busybox`, pinned by digest) empties `restore.path`. nerdctl does the same with `nerdctl cp` and the
   same helper.
5. **Registering under dev.** dev serves a second built-in token, `funcd-dev-admin-token`, with the admin role, on
   loopback only as its developer token is; dev applies the Addons of its `apply` list with it. Under dev,
   `addons.registries` allows every registry.

## Design model

None: the drivers implement ADR-0087's port with no new state; the restore's states are ADR-0333's spec, which holds
for either runtime, and the replacement is the SIGTERM stop that spec takes as its environment.

## Behavior rows

- **funcdctl-dev.md**: "An add-on object under dev" (Docker first, the config key, `RuntimeUnavailable` naming the
  sockets tried, the published port), `decided`; "dev stops" (an add-on's volume removed unless `--persist`),
  `decided`; "Registering an Addon under dev" (the admin token, the `apply` list, any registry), `decided`.

## Temporary workarounds

None.

## Contracts

```go
package docker // internal/provider/docker: the Docker driver of provider.Runtime under dev

func Socket() (string, error)
func New(socket string, persist bool, logger *slog.Logger) *Runtime

func (r *Runtime) Converge(ctx context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error)
func (r *Runtime) Teardown(ctx context.Context, ref provider.ProviderRef) error
func (r *Runtime) Reclaim(ctx context.Context, ref provider.ProviderRef) error // the volume, unless persist
// the AddonFiles of ADR-0333, through the archive API
```

```go
package nerdctl // internal/provider/nerdctl: the same, through the nerdctl CLI

func Find() (string, error)
func New(bin string, persist bool, logger *slog.Logger) *Runtime
```

```yaml
# funcdctl.config.yaml
addons:
  runtime: docker
```

| Consumes | Exposes |
|---|---|
| a Docker socket or `nerdctl` on `PATH`; `addons.runtime` | containers and volumes named `funcd-dev-addon-<ns>-<name>` |
| `--persist` | the token `funcd-dev-admin-token` (admin, loopback) |

## Implementation plan

- `internal/provider/docker/docker.go` and `internal/provider/nerdctl/nerdctl.go` (one file each), `cmd/funcdctl`
  (the choice, the config key, the admin token, the `apply` order: Addons first), `pkg/funcd/options.go`
  (`WithAddonRuntime`), from experiment 0025's spike (the Docker driver ran there).
- Tests: each Scenario named after it in `cmd/funcdctl`, against Docker when a socket answers and skipped with the
  reason otherwise; the nerdctl driver's tests run in the Linux VM (`just test-linux`) where nerdctl is installed.
- Done: `just ci` and `just check-behavior` green; the Scenarios pass on colima's Docker and its containerd runtime.

## Review checklist

- [ ] Docker is tried first; the config key forces a driver; neither gives `RuntimeUnavailable` naming what was tried.
- [ ] A replaced engine gets SIGTERM and the grace before removal.
- [ ] The volume goes at dev's stop unless `--persist`.
- [ ] The admin token answers on loopback only.

## Consequences

- An App with an add-on runs under dev on colima, Docker Desktop or Linux.
- A developer has an admin token under dev, so `funcdctl` can register an Addon there.
- rauthy accepts http callbacks on `localhost` and loopback addresses only, so a host that signs users in under dev is
  `localhost` or `127.0.0.1` (experiment 0025).

## Open questions

None.

## References

- Feature issue [#1500](https://github.com/pyvvo/funcd/issues/1500) (FEAT-0018 F260).
- Experiment 0025 (pyvvo/experiment at a0643b3c): every check ran on the Docker driver.
- The decider's card 6 (2026-11-19).
