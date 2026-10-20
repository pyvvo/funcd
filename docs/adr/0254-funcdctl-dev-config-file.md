# ADR-0254: `funcdctl.config.yaml` holds what `funcdctl dev` is run with

- **Status**: Accepted (2026-10-20), after two judge rounds, under `adr-batch` with acceptance delegated by the
  decider; Decision 4's defaults (`250ms`, `1s`, `1s`) are the drafting agent's choice, brought back to the decider.
  The judge added the per-mode rule for a Secret no document defines, a missing `envFile` read as empty, the
  `envFile` as a reload input in both modes and the corrected reason for the drain default.
- **Date**: 2026-10-20
- **Deciders**: green-0-rabbit
- **Tags**: dx, cli, dev, config, secrets
- **Realizes**: [FEAT-0001/F165](../feat/0001-feat-v1.1.md) (#1110, tracker #1062)
- **Supersedes (in part)**: [ADR-0247](0247-contract-file-replaces-funcdctl-yaml.md) Decision 5's Secret rule, which
  read a declared Secret's values from the shell only (Decision 5 here adds `envFile` and `env`, in both modes), and
  its Decision 4 default `[dir]`, which `dev.dir` now gives when the file sets it.
- **Relates to**: [ADR-0253](0253-funcdctl-dev-runs-an-app-template.md) (App mode and its flags) ·
  [ADR-0247](0247-contract-file-replaces-funcdctl-yaml.md) (folder mode; Decision 11 refuses `funcdctl.yaml`) ·
  [ADR-0125](0125-funcdctl-dev-local-run.md), [ADR-0126](0126-funcdctl-dev-developer-experience.md) and
  [ADR-0128](0128-funcdctl-dev-interpreter-config-and-seedable-writes.md) (dev's flags) ·
  [ADR-0143](0143-redeploy-by-revision-switch.md) and [ADR-0163](0163-retry-times-in-config.md) (the drain and its
  keys) · [ADR-0119](0119-object-store-eventsource.md) (the blob poll)

## Context & Need

Experiment 0001 (`pyvvo/experiment`, private) ran the todo App in dev with a command like this one, seven flags long:

```bash
TODO_API_TOKEN=dev-token funcdctl dev app --hooks --persist --build functions/ts --build functions/py --build web=web --build api=functions/ts/api-1.0.0.mjs --build migrate=functions/ts/migrate-1.0.0.mjs
```

People copy it into a recipe, and agents have to remember it. The same run showed dev inheriting production's
cadences: a change saved right after another waits for the 2 s drain (`runtime.handOutSettle`), an upload waits for
the 15 s blob poll (`eventing.blobPollInterval`), and `funcdctl logs` for the 10 s log segment
(`funclog.segmentMaxAge`). The decider asked on 2026-10-20 for a dev config file, like docker compose's, that also
carries those cadences, environment values and secret values.

**Purpose**: one committed file says how a project runs in `funcdctl dev`, so `funcdctl dev` alone starts it. The
callers are developers and agents in a project's directory.

## Scenarios

In a copy of the todo App (ADR-0253's fixture), with this file at its root and no `.env`:

```yaml
dev:
  dir: app
  values:
    - values/greeting.yaml
  build:
    - functions/ts
    - functions/py
    - web=web
    - api=functions/ts/api-1.0.0.mjs
    - migrate=functions/ts/migrate-1.0.0.mjs
  hooks: true
  persist: true
  env:
    TODO_API_TOKEN: dev-token
  envFile: .env
```

- **scenario: dev-config-runs** — *When* `funcdctl dev` runs with no argument, *Then* it runs as ADR-0253's
  `dev-todo -f values/greeting.yaml --hooks --persist` does, and the missing `.env` reads as empty.
- **scenario: dev-config-flag-wins** — *When* `funcdctl dev --hooks=false -f values/backup.yaml`, *Then* no hook is
  called, the greeting is the template's default and the App has its backup schedule: each flag replaced its key.
- **scenario: dev-config-secret-sources** — *Given* `TODO_API_TOKEN` in the shell, in `.env` and under `env`, *Then*
  the Secret `todo-api-keys` takes the shell's value; without it, `.env`'s; without both, `env`'s; with none, dev stops
  before the platform starts, naming the key and the three places.
- **scenario: dev-config-cadences** — *Given* no `platform` section, *Then* an upload reaches its Sensor within about a
  second, a change saved right after another is not held for 2 s, and `funcdctl logs` shows a line within about a
  second; a folder of resource files with a blob EventSource gets the same poll. *Given*
  `dev.platform.eventing.blobPollInterval: 5s`, *Then* the upload waits for that poll.
- **scenario: dev-config-reload** — *When* the file's `build`, `values`, `apply` or `env`, or the contents of its
  `envFile`, change, *Then* dev reloads as for a saved build; a change that does not decode is printed and dev runs on.
  *When* another key changes, *Then* dev prints that a restart applies it and runs on as it was.
- **scenario: dev-config-strict** — *Then* dev stops, naming the key, on a root key other than `dev`, an unknown key
  under it, a `platform` key other than Decision 4's, a malformed `envFile` line, and `values`, `build`, `hooks` or
  `apply` when `dev.dir` is not a template.
- **scenario: dev-config-absent** — *Given* no file, *Then* dev runs from its flags as ADR-0247 and ADR-0253 say, with
  Decision 4's defaults.

## Scope

**In**: the file's name, place, keys and precedence; its `platform` section; the sources of a declared Secret's
values; what a change to the file does; dev's own cadence defaults.

**Out**: any other command reading the file (`--server`, `--token`); a flag that names another file; secrets
fetched from a vault; dev's modes themselves (ADR-0247, ADR-0253).

## Constraints & Decision drivers

- The file is committed, so no real secret value belongs in it (the decider).
- Its name is not `funcdctl.yaml` or `<name>.funcdctl.yaml`, which ADR-0247 Decision 11 refuses everywhere.
- The `platform` keys keep `FuncdConfig`'s names and syntax (`internal/platform/config/config.go:240`, `:288`,
  `:360`).
- No new dependency: YAML through `go.yaml.in/yaml/v3`, as `app.yaml` is read.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. `funcdctl.config.yaml`: the flags as keys, a `platform` section, `env` and `envFile`** | One file, compose's habits, the daemon's names | A second way to set a flag | **Chosen** (the decider) |
| B. Name it `funcdctl.yaml` | The old name | ADR-0247 refuses it; an old function manifest would be misread | Rejected |
| C. Flags only, as today | Nothing new | Long commands and remembered steps | Rejected |
| D. A `FuncdConfig` document for the cadences | The daemon's file | It brings server, storage and auth settings dev owns; folder mode skips it | Rejected |
| E. `env` before `envFile` (compose's order) | compose's habit | The committed fake value beats a developer's real one | Rejected (the decider) |

## Decision

1. **The file** is `funcdctl.config.yaml` in the directory dev starts in, beside the template rather than inside it
   (a template holds only its own files, ADR-0217). It is a client file with no `apiVersion`, decoded strictly as
   `app.yaml` is: its root holds only `dev`, and an unknown key is refused. Its paths are relative to its directory.
   Folder mode skips it, as it skips any document that is not funcd's (ADR-0247 Decision 4).
2. **`dev` keys are dev's flags**, in camelCase: `dir`, `values`, `build`, `hooks`, `apply`, `persist`, `persistTo`,
   `gport`, `s3port`, `cport`, with the flags' meanings (ADR-0125, ADR-0126, ADR-0128, ADR-0253). `--print-env` has no
   key. `values`, `build`, `hooks` and `apply` need a template `dir`, as their flags do.
3. **A flag wins**: a flag given on the command line replaces the file's key, and a repeatable flag the whole list. A
   directory given as the argument replaces `dev.dir`; when it is not a template, the file's App-only keys are
   ignored.
4. **`dev.platform`** takes three `FuncdConfig` keys, in its syntax: `runtime.handOutSettle`,
   `eventing.blobPollInterval` and `funclog.segmentMaxAge`. Dev passes them to `funcd.WithPacing`,
   `funcd.WithBlobPollInterval` and `funcd.WithFunclog`. Any other key is refused. **Dev's defaults**, in both modes,
   are `250ms`, `1s` and `1s`, where production's are 2 s, 15 s and 10 s. `handOutSettle` covers the gap between
   resolving an upstream and calling it (ADR-0143 Decision 6), and calls in flight are counted apart, so a short
   settle drops none; ADR-0163's own scenario runs `200ms`. A list or a flush every second costs nothing on one
   machine. The live check measures all three.
5. **A declared Secret's value.** For a Secret declared and defined by no document (in folder mode, by no document of
   the folder; in App mode, by no `--apply` document), dev reads each key from the shell's environment, else
   `envFile`, else `env`. With none, dev stops before the platform starts, naming the key and the three places. A
   missing `envFile` reads as empty. `env` and `envFile` fill only these Secrets, never dev's own variables such as
   `FUNCD_PYTHON`. `env` is committed and holds fake dev values only; a real value goes in the shell or in `envFile`,
   which the project keeps out of git. `envFile` holds `KEY=value` lines, `#` comments and blank lines, with one pair
   of double quotes around a value removed; any other line is malformed.
6. **A change to the file.** The file and its `envFile`'s contents are inputs of dev's reload, in folder mode
   (ADR-0247 Decision 8) and in App mode (ADR-0253 Decision 9). A change to `build`, `values`, `apply`, `env` or the
   `envFile`'s contents reloads as a saved input does. A change to another key prints that a restart applies it, and
   dev runs on as it was. A file that fails to decode at a reload is printed, and dev keeps its last good one.

## Temporary workarounds

None.

## Contracts

| Key | Type | Default |
|---|---|---|
| `dev.dir` | path | `.` |
| `dev.values`, `dev.build`, `dev.apply` | list of strings, the flags' values | empty |
| `dev.hooks`, `dev.persist` | bool | `false` |
| `dev.persistTo` | path | `.funcd-dev` |
| `dev.gport`, `dev.s3port`, `dev.cport` | int | `0`, a free port |
| `dev.env` | map of name to string | empty |
| `dev.envFile` | path | none |
| `dev.platform.runtime.handOutSettle` | duration | `250ms` |
| `dev.platform.eventing.blobPollInterval` | duration | `1s` |
| `dev.platform.funclog.segmentMaxAge` | duration | `1s` |

| Consumes | Exposes |
|---|---|
| `funcdctl.config.yaml`, its `envFile`, the shell's environment | the flags' effect; the three cadences on dev's platform; the Secret values |

Messages, each naming the file it reads:

- `funcdctl.config.yaml: unknown key <key>`, for a root key or a key under `dev`;
- `funcdctl.config.yaml: platform takes runtime.handOutSettle, eventing.blobPollInterval and funclog.segmentMaxAge,
  not <key>`;
- `<envFile>:<line>: not KEY=value`;
- `<App <app> | Function <fn> of <file>> declares Secret <secret> with key <KEY>; set it in the shell[, in
  <envFile>] or under dev.env in funcdctl.config.yaml`;
- `funcdctl.config.yaml: <key> changed; restart funcdctl dev to apply it`.

## Implementation plan

- **funcd**, one PR closing #1110 (`Fixes #1110`), after ADR-0253's:
  - `cmd/funcdctl/devconfig.go`: load and validate the file and its `envFile`, and merge them under the flags
    (cobra's `Changed` tells a given flag).
  - `cmd/funcdctl/dev.go` and `dev_app.go`: the merged config; the cadences in the platform's options; one secret
    lookup that both modes call; the file and the `envFile` among the reload's inputs.
  - Tests (`-tags dev`, `cmd/funcdctl/devconfig_test.go`): one per scenario, named after it, on the todo App's copy;
    table tests for the strict decode, the precedence and the `envFile` format.
  - `funcdctl dev --help` names the file.
- **Live check**, recorded in the PR: experiment 0001 rerun from a `funcdctl.config.yaml`, showing the upload, logs
  and back-to-back timings under the defaults.
- **Done when** `go build`, `go vet`, `golangci-lint` and `go test` pass with `-tags dev` and without, and the live
  check is recorded.

## Review checklist

- [ ] The file is read from the directory dev starts in, strictly, with paths relative to it.
- [ ] Each key acts as its flag; a given flag replaces the key; App-only keys need a template `dir`.
- [ ] `platform` accepts only the three keys, and dev's defaults are `250ms`, `1s` and `1s` in both modes.
- [ ] A declared Secret no document defines takes each key from the shell, then `envFile`, then `env`; a missing one
      names the three places; a missing `envFile` reads as empty.
- [ ] A change to the file or to its `envFile` reloads, asks for a restart, or is printed, as Decision 6 says.
- [ ] One test per scenario, named after it; the live check is recorded.

## Consequences

- **Positive**: `funcdctl dev` alone runs a project; an agent reads one file instead of a remembered command.
- **Negative**: two ways to set a flag; dev's cadences differ from production's by default.
- **Risks accepted**: a developer may commit a real secret under `env`; the docs and the message say where real
  values go, and nothing enforces it.

## Open questions

- Whether other commands read the file (`--server`, `--token`): a later ADR if asked.

## References

- #1110, the feature issue (tracker #1062); the decider's answers of 2026-10-20 on #1067.
- Experiment 0001 (`pyvvo/experiment`, private, 62a177b): the command above and the three cadences.
- `pkg/funcd/options.go:879` (`WithPacing`), `:352` (`WithBlobPollInterval`), `:119` (`WithFunclog`);
  `internal/platform/config/config.go:240`, `:288`, `:360` (the three keys); `cmd/funcdctl/dev.go:297` (folder mode's
  declared Secrets).
- docker compose's `environment` and `env_file` (the model for `env` and `envFile`).
