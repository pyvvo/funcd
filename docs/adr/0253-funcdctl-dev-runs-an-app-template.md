# ADR-0253: `funcdctl dev` runs a whole App from its template

- **Status**: Reviewing (2026-10-20), implemented; Accepted (2026-10-20), after two judge rounds, under `adr-batch`
  with acceptance delegated by the decider. The judge made every scenario run on the todo and site-config Apps with its
  full command, kept a Secret an `--apply` document defines, applied the Secrets again on a reload, and narrowed what
  dev skips in a template.
- **Date**: 2026-10-20
- **Deciders**: green-0-rabbit
- **Tags**: dx, cli, dev, app, template
- **Realizes**: [FEAT-0001/F164](../feat/0001-feat-v1.1.md) (#1067, tracker #1062)
- **Supersedes (in part)**: [ADR-0247](0247-contract-file-replaces-funcdctl-yaml.md) Decision 4, for a directory that
  holds `app.yaml` beside `resources/`: it runs in App mode instead of as a folder of resource files.
- **Relates to**: [ADR-0125](0125-funcdctl-dev-local-run.md), [ADR-0126](0126-funcdctl-dev-developer-experience.md),
  [ADR-0128](0128-funcdctl-dev-interpreter-config-and-seedable-writes.md) and
  [ADR-0132](0132-funcdctl-dev-print-env.md) (dev and its flags) · [ADR-0217](0217-app-templates-values-and-rendering.md)
  and [ADR-0218](0218-app-template-images-lock-and-push.md) (templates, `app.lock`) ·
  [ADR-0199](0199-app-resource.md), [ADR-0200](0200-app-revisions.md), [ADR-0212](0212-app-self-heal-and-pause.md)
  and [ADR-0214](0214-app-hooks.md) (the App) · [ADR-0254](0254-funcdctl-dev-config-file.md) (the dev config file)

## Context & Need

`funcdctl dev` runs a folder of resource files from local builds (ADR-0247) and stops on an App
(`cmd/funcdctl/dev_folder.go:132`). It runs Functions only from `file://` builds (`cmd/funcdctl/dev.go:919`), while
each part of an App template names an OCI image through `${{ images.<key> }}`, pinned by `app.lock`
(`internal/app/template/render.go:250`, `internal/app/template/images.go:71`). So an App is developed as loose parts
plus a recipe kept by hand (its Route applied to dev, its migration run once, an `/api` proxy). The App then exists
twice, and nothing notices the two drift apart. Yet anything an author or an agent versions with a lifecycle belongs
in an App (`docs/PROJECT-SUMMARY.md`, the decider's positioning of 2026-10-20).

**Purpose**: `funcdctl dev <template dir>` deploys the App from local builds on dev's embedded platform the way
`funcdctl app deploy` deploys it, then deploys it again on each save. The callers are App authors, people and agents.

The decider chose the approach on 2026-10-20 (#1067). A spike measured it in experiment 0001 (`pyvvo/experiment`,
private, at 62a177b): on the todo and site-config Apps the embedded App reconciler ran every part kind unchanged, 37
checks covering the App's features passed, and an isolated change served in under a second.

## Scenarios

Fixture: copies of the todo and site-config Apps of funcd-functions. `dev-todo` stands for this command, run in the
todo App's copy; its TS builds carry their version in their names, so two of them are named by key:

```bash
TODO_API_TOKEN=dev-token funcdctl dev app --build functions/ts --build functions/py --build web=web --build api=functions/ts/api-1.0.0.mjs --build migrate=functions/ts/migrate-1.0.0.mjs
```

- **scenario: app-dev-runs-template** — *When* `dev-todo`, *Then* `/api` answers from the local builds, the Site serves
  `web/`, and the banner names the App, its current revision, the Route's path and the Site's URL.
- **scenario: app-dev-reload** — *When* a build, a template file or the values file is saved under `dev-todo -f
  values/greeting.yaml`, *Then* the App stamps one new revision and serves the change.
- **scenario: app-dev-hooks-opt-in** — *When* `dev-todo` starts and reloads, *Then* no hook is called. *When*
  `dev-todo --hooks`, *Then* the install calls `preApply` then `postApply`, and each new revision calls them again.
- **scenario: app-dev-failed-revision** — *Given* `dev-todo --hooks --persist` at 1.0.0 with two todos, *When* dev
  restarts on `app-1.1.0.yaml` and the 1.1.0 builds, *Then* the 1.1.0 `preApply` hook gets 403, dev prints the reason
  naming the part, the 1.0.0 App keeps serving and dev keeps watching; once the grant is applied, `funcdctl app retry
  todo` rolls 1.1.0 out.
- **scenario: app-dev-apply-files** — *Given* the same upgrade with `--apply grants.yaml`, *Then* the hook rewrites
  table `todos` and 1.1.0 rolls out with no 403. *When* a document is removed from `grants.yaml`, *Then* dev deletes it.
- **scenario: app-dev-apply-secret** — *When* `dev-todo --apply secret.yaml` runs without `TODO_API_TOKEN`, *Then* the
  App uses the Secret of `secret.yaml`, and dev asks for no variable.
- **scenario: app-dev-refused-apply** — *When* a fragment whose Workflow step binds a store nothing declares is saved
  into `resources/` under `dev-todo`, *Then* dev prints the refusal naming the part, and the last App keeps serving.
- **scenario: app-dev-persist** — *Given* `dev-todo --persist`, *When* dev restarts with the same builds, *Then* no
  revision is stamped, no hook is called and the App's data is kept.
- **scenario: app-dev-site-only** — *When* `funcdctl dev app -f values/staging.yaml --build web=web` runs in the
  site-config App's copy, *Then* its Site serves its generated `config.json`.
- **scenario: app-dev-refusals** — dev stops before the platform starts, naming what to fix, on `dev-todo` without
  `TODO_API_TOKEN`, without the `api=` entry, or without `web=web`; with `-f values/shared.yaml` (`requires`); with
  a values file setting `sql: true` on a build without the SQL engine; with `--apply` of a document in another
  namespace or of an App; and on `funcdctl dev functions/ts --hooks`.
- **scenario: dev-folder-unchanged** — *Given* a folder of resource files, *When* `funcdctl dev` runs it, *Then* it
  runs as ADR-0247 says, and still stops on an App document.

## Scope

**In**: dev on a template directory: builds pushed to a local layout, the render, the apply, the follow of each
revision and the reload; the flags `-f`, `--build`, `--hooks` and `--apply`; the refusals; the banner of an App.

**Out**: the dev config file and dev's cadence defaults (ADR-0254); running an App another requires (refused, a
later ADR); `vite dev` (#1066); several Apps in one dev.

## Constraints & Decision drivers

- One source of truth: dev renders the template as `app deploy` does and changes only what Decision 4 lists.
- The parts run as deployed: OCI artifacts, the App reconciler, its revisions and hooks (option A, the decider).
- Folder mode stays as ADR-0247 says (the decider, 2026-10-19).
- No new dependency; no change outside `cmd/funcdctl`.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. Push the builds to a local OCI layout, render against it, apply the App** | The deploy path, every part kind included; reuses push, render and apply | A push per changed build (1-2 ms in the spike) | **Chosen** (the decider) |
| B. Point each part at its `file://` build, as folder mode | Folder mode's delivery | A Site reads only an OCI artifact (`internal/site/reconcile.go:125`); a required App's pushed Functions could never run | Rejected |
| C. Flatten the template into standalone parts | Folder mode as is | No App reconciler: no revisions, hooks, self-heal or prune | Rejected |
| D. A local funcd and `app deploy` per change | Production exactly | A push and a deploy per change; it stays the release check | Rejected |
| Hooks always on | Fidelity | Each save runs the migration | Rejected: opt-in (the decider) |
| Hooks only when the template changes | Fewer calls | A dev-only rule in the App reconciler | Rejected |
| Site through the frontend's dev server | Hot reload | A dev-only proxy, and funcd still has to serve `config.json` | Rejected: the build folder (the decider) |
| Dev grants its Functions wide rights | No grant file | Hides a grant production needs | Rejected: `--apply` (the decider) |

## Decision

1. **App mode.** `funcdctl dev <dir>` runs an App when `<dir>` holds `app.yaml` beside `resources/` (ADR-0217). Any
   other folder runs as ADR-0247 says, and `-f`, `--build`, `--hooks` or `--apply` on it stop dev.
2. **Builds.** Each `images` key the rendered App uses gets its build from `--build <key>=<path>`, a file with its
   contract file beside it or a bundle directory with its contract file at its root, as `funcdctl push` reads them.
   Otherwise the first `--build <folder>` that holds it by ADR-0247 Decision 6 supplies it: `<key>.mjs`,
   `dist/<key>.mjs`, or a Python entry `<key>`. A Site's build is a folder, named only by `--build <key>=<dir>`, since a
   folder named after the key usually holds the frontend's sources. Dev learns which keys a render uses, and what each
   one serves, by rendering once with a stand-in lock whose digests name the keys.
3. **Push and render.** Dev pushes each build whose fingerprint changed to `<state>/oci`, one layout per repository,
   tagged with the version `app.yaml` names (`0.0.0-dev` for a range), as `funcdctl push` and `push --site` do; a
   Python entry is pushed as a bundle from a copy of its directory. It renders as `app deploy` does (values from `-f`,
   namespace `default`), with `oci-layout://<state>/oci` as the registry and an in-memory lock of the pushes, so an
   image reads `oci-layout://<state>/oci/<repo>:<version>@<digest>`. `app.lock` is neither read nor written. Dev
   skips only dot-entries and its persist dir inside the template directory; any other entry stops it, as
   `template.Load` refuses it (`internal/app/template/load.go:75`), so dev runs no template that `app deploy` refuses.
4. **What dev changes in the App**: the registry and the lock (Decision 3), and `spec.hooks`, dropped unless
   `--hooks`. Nothing else: no replica, group or tag, and no synthesized grant (ADR-0247 Decision 5's writer Role is
   folder mode's). A grant the App needs comes in through `--apply`.
5. **The platform** pulls the parts as a deploy does: App mode runs the oras materializer over `<state>/artifacts`,
   not folder mode's file materializer, and wires the SQL and catalog engines as folder mode does.
6. **Secrets.** Dev creates each Secret the App declares that no `--apply` document defines, each key from the
   environment variable of that name (ADR-0247 Decision 5's rule; ADR-0254 adds sources). A missing key stops dev,
   naming the Secret and the key.
7. **Files beside the App.** Each `--apply <file>` (repeatable) holds resource documents that dev applies before the
   App, again when the file changes, and deletes once they leave the file. Each carries folder mode's
   `managed-by: funcdctl-dev` tag (ADR-0247 Decision 5), so a `--persist` restart prunes one that left the file. A
   document in another namespace, or an App, stops dev, as ADR-0247 Decision 4 says.
8. **Apply and follow.** Dev applies the App as `app deploy` does, keeping the stored `spec.paused` and skipping an
   unchanged spec. It then follows the new revision in the background, polling every 100 ms, and a newer apply cancels
   the older follow, since the App reconciler switches only its latest revision. A failed revision is printed with
   its reason and part, and dev keeps running. A refused apply is printed and the last App keeps serving; at start it
   stops dev.
9. **Reload.** Every 300 ms (`devReloadPoll`, `cmd/funcdctl/dev.go:662`) dev reads the template directory, the `-f`
   and `--apply` files and the `--build` folders and paths. On a change it pushes the changed builds, renders, applies
   the declared Secrets and the App. An unchanged build pushes to the same digest, so a `--persist` restart with the
   same builds stamps nothing. A reload that fails, a missing Secret key included, is printed, and the last App keeps
   serving.
10. **Refused for now**: an App with `spec.requires`, naming the first requirement; a SQL database on a build without
    the SQL engine, as folder mode refuses it.
11. **Output.** The banner names the App, its current revision, each Route's path, each Site's URL, and the gateway,
    control and S3 endpoints; the list of `POST /function/<name>` lines is folder mode's. `--print-env` prints the
    keypair of the App's first function part, found by the stand-in render, without starting the platform; for an App
    with no function part, the keypair derived over the name `dev`.

## Temporary workarounds

- **`requires` is refused.** Exit: an ADR that runs, or waits for, a required App in dev. The todo App's requirement,
  todo-shared, has no parts, so deploying it into dev first would satisfy it.

## Contracts

| Flag | Meaning (App mode only) |
|---|---|
| `-f`, `--values <file>` | a values file; repeatable, merged in order, as `app deploy` |
| `--build <folder>` | a folder each `images` key is looked up in (Decision 2); repeatable |
| `--build <key>=<path>` | the build of one key: a file, a bundle directory, or a Site's folder |
| `--hooks` | keep the App's hooks |
| `--apply <file>` | resource documents applied before the App; repeatable |

`--persist` and `--persist-to` (ADR-0125), `--gport` and `--s3port` (ADR-0126), `--cport` (ADR-0128) and
`--print-env` (ADR-0132) keep their meaning in both modes.

| State dir entry | Holds |
|---|---|
| `oci/<repo>` | the OCI layout of each image dev pushed |
| `artifacts/` | what the materializer pulled |
| `stage/` | a Python entry's copy, removed after its push |
| `process/` | the processes dev starts (ADR-0167), as in folder mode |

| Consumes | Exposes |
|---|---|
| the template directory, the `-f` and `--apply` files, the `--build` folders and paths, the environment | the gateway (Routes and Sites), the control port (`funcdctl --server … app history`, `rollback`, `pause`, `resume`, `retry`, `workflow run`), the S3 frontend |

Each revision prints the lines `app deploy` prints while it waits, then `App <name> deployed in <d>` at start or
`reloaded in <d>` after a save. A failure prints `the new revision did not become current; save a fix, or run
funcdctl app retry`, with the error.

## Implementation plan

- **funcd**, one PR closing #1067 (`Fixes #1067`):
  - `cmd/funcdctl/dev_app.go`, from the spike (experiment 0001's `funcd.patch` on 080e2b39): the template check, the
    build lookup, the stand-in render, the pushes, the render and apply, the follow and the watcher. The spike lacks
    `--apply`, the template snapshot of Decision 3, the catalog engine wiring, the 100 ms follow, `--print-env` and
    the App banner.
  - `cmd/funcdctl/dev.go`: the flags, the App-mode entry in `startDev`, the oras materializer when the instance has an
    artifact dir, and the S3 keypair's identity for an App with no Function.
  - Tests (`-tags dev`, `cmd/funcdctl/dev_app_test.go`): one per scenario, named after it, through `startDev` on
    copies of the two Apps from the pinned funcd-functions module (`internal/testkit/langmod`); the refusals as a
    table test; unit tests for the build lookup and the stand-in render.
  - `funcdctl dev --help` names the App mode; `docs/PROJECT-SUMMARY.md` drops "the App has no dev mode yet".
- **Live check** in the PR: experiment 0001 rerun against the PR's commit, with its `funcd.patch` emptied.
- **Done when** `go build`, `go vet`, `golangci-lint` and `go test` pass with `-tags dev` and without, and the live
  check passes.

## Review checklist

- [ ] A template directory runs in App mode; any other folder runs as ADR-0247 says and refuses the App-mode flags.
- [ ] Builds resolve as Decision 2 says; a Site needs `key=path`; each part runs the pushed digest of its build.
- [ ] The App dev applies differs from `app deploy`'s render only in its registry, its lock and, without `--hooks`,
      its hooks; no grant is synthesized.
- [ ] A failed revision, a refused apply and a failed reload leave dev running and the last App serving; a refused
      apply stops dev only at start.
- [ ] `--apply` documents are applied before the App, again on change, and deleted once removed.
- [ ] A Secret an `--apply` document defines is not created from the environment.
- [ ] A `--persist` restart with the same builds stamps no revision.
- [ ] Each refusal of `app-dev-refusals` names what to fix.
- [ ] One test per scenario, named after it; the live check is recorded.

## Consequences

- **Positive**: one command runs an App as it deploys, its features included, from its one template.
- **Negative**: dev has two modes, and App mode pushes each changed build.
- **Risks accepted**:
  - Production's cadences show through until ADR-0254 sets dev's own: a back-to-back change waits for the drain
    (`runtime.handOutSettle`, 2 s), an upload for the blob poll (`eventing.blobPollInterval`, 15 s), and
    `funcdctl logs` for a log segment (`funclog.segmentMaxAge`, 10 s).
  - A part the admission accepts but cannot run, such as a Route to a missing Function, replaces the working one, as
    in a deploy.
  - `oci/` and `artifacts/` keep every build dev pushed and are never pruned.

## Open questions

- Running an App another requires: a later ADR (Temporary workarounds).

## References

- #1067, the feature issue (tracker #1062), with the decider's choices; the board card "funcdctl dev: run a whole App
  from its directory".
- Experiment 0001 (`pyvvo/experiment`, private): `0001-app-dev-mode` at 62a177b, its report and `funcd.patch`.
- `internal/function/function.go:1684` (a Function switches only when nothing drains, ADR-0143);
  `internal/eventing/blobwatch.go:19` (the blob poll, ADR-0119); `internal/funclog/sink.go:46` (the log segment).
- `cmd/funcdctl/app.go:353` (a template with no lock is resolved in memory); `pkg/funcd/funcd.go:621-627` (the oras
  materializer in process mode); `pkg/funcd/app_template_e2e_test.go:46` (the todo template deployed from local
  builds pushed to an `oci-layout://` directory).
