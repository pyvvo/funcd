# ADR-0324: funcd's and funcdctl's Spec tabs come from the config struct and the command trees

- **Status**: Reviewing (2026-11-13, implementation done); Accepted (2026-11-12)
- **Date**: 2026-11-12
- **Deciders**: green-0-rabbit
- **Tags**: docs, config, cli, codegen
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md) (API contracts & codegen; joined as a further entry, as ADR-0323
  did)
- **Relates to**: [ADR-0323](0323-each-features-spec-tab-is-generated-from-its-described-fields.md) (the Spec tab, the
  doc-tag rule and its list) · [ADR-0061](0061-funcd-daemon-config-file.md) (funcdconfig.yaml) ·
  [ADR-0062](0062-config-single-env-validated-struct.md) (one struct, `env` and `validate` tags) ·
  [ADR-0042](0042-cobra-cli-framework.md) (both CLIs on cobra)

## Context & Need

The decider chose on 2026-11-12 that running funcd is one page of the Developer chapter with a Usage tab and a Spec
tab, as each feature is (ADR-0323), and that funcdctl gets the same two tabs. The Spec tab of funcd lists every key of
`funcdconfig.yaml` and every `funcd` command with its flags; that of funcdctl lists every command with its flags.
Operators read the first; anyone who scripts funcdctl reads the second.

The sources exist. `config.Config` (`internal/platform/config/config.go`) holds 223 JSON-named fields, about 175 of them
leaf keys; 170 have an `env` tag (`caarlos0/env`) and 42 a `validate` tag (`go-playground/validator`), and
`config.Defaults()` returns every default. No field has a description. funcd and funcdctl each build their cobra tree
with `newRootCmd` in their main package. funcdctl ships twice (`.github/workflows/release.yml`): a thin build, whose
`dev` refuses with a rebuild hint, and a `-tags dev` build, `funcdctl-dev`, whose `dev` runs a function locally. Some
flags take their default from where funcdctl runs, when the tree is built: `--server` reads `FUNCD_SERVER`
(`cmd/funcdctl/cli.go:56`), and `pull --platform` is `linux/` and the host's architecture (`cli.go:363`).

## Scenarios

- `scenario: config-spec-generated`: **Given** `config.Config`, **when** `just generate` runs, **then**
  `docs/guide/develop/running-funcd/spec.md` lists `server.listenAddr` with its type, its default `127.0.0.1:8080`, its
  variable `FUNCD_LISTEN_ADDR` and its description.
- `scenario: command-spec-generated`: **Given** funcdctl's command tree, **when** `just generate` runs, **then**
  `docs/guide/develop/tools/funcdctl/spec.md` lists `funcdctl apply` with its usage line, its description and its
  flags, and marks `funcdctl dev` as a command of `funcdctl-dev` only.
- `scenario: page-same-on-every-host`: **Given** `FUNCD_*` variables set in the shell, **when** `just generate` runs,
  **then** both pages are the same bytes as without them, and hold none of their values.
- `scenario: host-default-refused`: **Given** a flag whose default holds a `FUNCD_*` variable's value or the host's OS
  or architecture, **when** the page's test runs, **then** it fails and names the flag.
- `scenario: undescribed-config-key-refused`: **Given** a new config field with no `doc` tag, **when** `go test` runs,
  **then** a test fails and names its key, unless the list of undescribed keys holds it.
- `scenario: stale-command-spec-fails`: **Given** a command whose help changed and whose page was not regenerated,
  **when** the page's test runs without `-update`, **then** it fails and names the page.

## Scope

In: the two Spec pages and their generators; their folders, `meta.json` files and a first Usage page each (what the
tool is for and how to start it); `doc` tags for the config keys, under a list that only shrinks; help texts written
for users; the checks; the agent rule. Out: a full Usage page, written by hand later; the Go library's page
(`pkg/funcd`); funcdctl's plugins, which are other programs.

## Constraints & Decision drivers

- One source per fact: a key's type, default, variable and limits come from the struct and `Defaults()`, a command's
  text and flags from the cobra tree; only a key's description is new, and it lives on the field (ADR-0323).
- Written for users: a description or a help text names no ADR, as ADR-0323 Decision 2 requires of a field's.
- A page is the same on every host and holds nothing of the shell that generated it.
- No new dependency: cobra, `caarlos0/env` and `go-playground/validator` are in `go.mod`.
- The binaries ship no documentation tooling.

## Alternatives considered

1. **cobra's `doc` package.** Rejected: its Markdown gives each command a run of headings and a SEE ALSO list, with no
   table, and importing it adds `go-md2man`, which `go.mod` does not hold.
2. **A hidden `reference` command in each binary.** Rejected: it ships documentation tooling in the binaries; a test in
   the main package reaches the same tree.
3. **Command builders moved to importable packages.** Rejected: a refactor of two CLIs for the sake of a page.
4. **Pages written by hand.** Rejected: nothing keeps them true when a key or a flag changes.
5. **A shrinking list for the help texts that name an ADR.** Rejected: 22 help texts in the two main packages name one,
   and `--help` shows them too; rewriting them once is smaller than a second list.
6. **Unset the `FUNCD_*` variables for the walk, and keep those defaults in the tree.** Rejected: it covers the
   variables but not the host's architecture, and `--help` still differs from one shell to the next.
7. **Do nothing.** Rejected: the Developer chapter's pages for funcd and funcdctl then have no Spec tab, against the
   decider's choice.

## Decision

1. **Pages.** `docs/guide/develop/running-funcd/spec.md` holds the `funcdconfig.yaml` table, then the `funcd` commands;
   `docs/guide/develop/tools/funcdctl/spec.md` holds the funcdctl commands. Each sits beside a hand-written `index.md`,
   in a folder that a `meta.json` lists, so the site shows the two as tabs (ADR-0323). Each starts with
   `reference.Marker`, not `kindref.Marker`, so specgen's cleanup of stale pages (ADR-0323's Contracts) leaves it alone.
2. **The config table.** One row per leaf key: its dotted JSON path (`server.listenAddr`), its type, its default from
   `config.Defaults()`, its limits, its environment variable from the `env` tag, and its `doc` tag.
   - The limits map the `validate` rules `oneof` ("one of …"), `min`, `max`, `required` and `eq` ("must be …"). Any
     other limit, whether a `validate` rule such as `ltefield` or a check in `Validate`, and a default derived when the
     key is empty are written in the key's `doc` tag.
   - A list of structs shows its element's keys as `path[].key` (`auth.credentials[].role`). A map is one row, and its
     variable shows the `key=value,…` form that its `envSeparator` and `envKeyValSeparator` tags give.
   - `TestEveryConfigKeyIsDescribed` holds every key to a `doc` tag that names no ADR, except the keys that
     `internal/reference/testdata/undescribed-config.txt` lists. The list only shrinks, and
     `scripts/check-undescribed.sh` refuses a line added to it, as it does for ADR-0323's list.
3. **The command tables.** A test in each main package builds `newRootCmd`, adds cobra's `help` and `completion`
   commands (`InitDefaultHelpCmd`, `InitDefaultCompletionCmd`), and walks it: every visible command's full name, usage
   line, short and long text, and flags (name, shorthand, type, default, usage), the root's persistent flags once.
   - A flag's default is the same wherever funcdctl runs. Today four defaults come from a variable or from the host
     (`--server`, `--token`, `bench --data-plane` and `pull --platform`); issue #1445 resolves them when the command
     runs instead, and this ADR's implementation follows it.
   - The test sets every `FUNCD_*` variable that either CLI or `config.Config`'s `env` tags name to a value of its own
     before it builds the tree. The walk fails on a default that holds one of those values, or that has a part, split
     at `/`, equal to the host's `runtime.GOOS` or `runtime.GOARCH`.
   - A visible command's short or long text, or a flag's usage, that names an ADR fails the walk. The help texts that
     name one today are rewritten once.
   - funcdctl's test runs under `-tags dev`. The dev build's `dev` command (`cmd/funcdctl/dev.go`) carries the cobra
     annotation `build: dev`, and the page marks it and its subcommands "funcdctl-dev only".
   - With `-update` the test rewrites the page; without it, it fails on a stale page.
4. **Generation and checks.** `just generate` runs the two tests with `-update`, after specgen. `just ci` regenerates
   before its checks, so in CI `scripts/check-generated.sh`, whose glob already covers both pages, fails on a stale
   page; `go test -tags dev` runs funcdctl's test.
5. **Agents.** A rule scoped to `internal/platform/config/**/*.go`, `cmd/funcd/**/*.go` and `cmd/funcdctl/**/*.go` asks
   for a `doc` tag with each new key, and for descriptions and help texts written for users, never an ADR number, as
   `.claude/rules/api-types.md` does for the kinds.

## Design model

None: code generation and tests only.

## Behavior rows

None: help texts change their wording only.

## Temporary workarounds

`internal/reference/testdata/undescribed-config.txt` lists the keys without a description when this ADR is implemented
(about 140, once the `server` keys are described). Exit: the list is empty; the file, its exemption and its CI line are then deleted.

## Contracts

```go
// Package reference renders the Spec pages of funcd and funcdctl (ADR-0324).
package reference

import (
	"github.com/spf13/cobra"

	"github.com/pyvvo/funcd/internal/platform/config"
)

// Marker is the first line of each page's front matter.
const Marker = "# Generated by just generate from the config struct and the command trees: change the code, not this page."

// ConfigKey is one leaf key of funcdconfig.yaml.
type ConfigKey struct {
	Path, Type, Default, Limits, Env, Description string
}

// ConfigKeys lists every leaf key of config.Config in declaration order, its default taken from defaults.
func ConfigKeys(defaults config.Config) []ConfigKey

// Command is one visible command and its flags.
type Command struct {
	Path, Use, Short, Long string
	DevOnly                bool
	Flags                  []Flag
}

// Flag is one flag of a command.
type Flag struct {
	Name, Shorthand, Type, Default, Usage string
}

// Commands lists every visible command under root, depth first, root first. It fails, naming the command and the
// flag, on a text or a flag usage that names an ADR, and on a default that holds the value of a set FUNCD_* variable
// or has a part, split at "/", equal to the host's runtime.GOOS or runtime.GOARCH.
func Commands(root *cobra.Command) ([]Command, error)

// ConfigTable renders keys as one Markdown table.
func ConfigTable(keys []ConfigKey) []byte

// CommandTables renders each command as a heading, its usage line, its text and a table of its flags.
func CommandTables(cmds []Command) []byte

// Page renders a Spec page: front matter that starts with Marker, then the sections in order.
func Page(title, description string, sections ...[]byte) []byte

// Stale returns an error naming path when the file at path is missing or differs from page.
func Stale(path string, page []byte) error
```

| Consumes | Exposes |
| --- | --- |
| `config.Config`'s `json`, `env`, `validate` and `doc` tags; `config.Defaults()` | `docs/guide/develop/running-funcd/spec.md` |
| `newRootCmd` of `cmd/funcd`, and of `cmd/funcdctl` under `-tags dev` | `docs/guide/develop/tools/funcdctl/spec.md` |
| `internal/reference/testdata/undescribed-config.txt` | the list that `scripts/check-undescribed.sh` guards |

## Implementation plan

1. `internal/reference/config.go`, `internal/reference/commands.go`, `internal/reference/page.go` and
   `internal/reference/reference_test.go`: `TestEveryConfigKeyIsDescribed`; `TestScenarioUndescribedConfigKeyRefused`
   on a test struct, through the walker that `ConfigKeys` uses; `TestScenarioStaleCommandSpecFails` and
   `TestScenarioHostDefaultRefused` on test trees; unit tests of the limit mapping, a `path[].key` row, a map row and
   an ADR number in a help text.
2. After #1445 has merged: `cmd/funcd/spec_page_test.go` (`TestScenarioConfigSpecGenerated`, the funcd command
   tables, `TestScenarioPageSameOnEveryHost`) and `cmd/funcdctl/spec_page_test.go` under `-tags dev`
   (`TestScenarioCommandSpecGenerated`, `TestScenarioPageSameOnEveryHost`); the `build: dev` annotation in
   `cmd/funcdctl/dev.go`; the help texts that name an ADR rewritten.
3. `internal/reference/testdata/undescribed-config.txt`; `doc` tags on the `server` keys as the first ones.
4. `justfile`: `generate` runs the two tests with `-update`. `scripts/check-undescribed.sh` checks both lists, and
   `TestScenarioUndescribedListNeverGrows` (`scripts/generated_test.go`) gains a case for the config list. The rule
   file `.claude/rules/config-and-cli.md`.
5. `docs/guide/develop/running-funcd/index.md`, `docs/guide/develop/running-funcd/meta.json`,
   `docs/guide/develop/tools/meta.json`, `docs/guide/develop/tools/funcdctl/index.md`,
   `docs/guide/develop/tools/funcdctl/meta.json`, and their two entries in `docs/guide/develop/meta.json`.
6. `scripts/agent/d just generate`, then `scripts/agent/d just ci`.

Definition of done: `just ci` passes; a second `just generate` changes nothing, with or without `FUNCD_*` variables set;
both pages are committed, and `meta.json` lists their folders; the config list holds only the undescribed keys.

## Review checklist

- [ ] Every leaf key of `config.Config` has a row with its default, its variable and its limits; a list's keys read
  `path[].key`.
- [ ] Every command that `--help` lists in either CLI has a section; `funcdctl dev` is marked as `funcdctl-dev` only.
- [ ] A new undescribed key fails a test; a listed key that has a description fails it too.
- [ ] No `doc` tag, command text or flag usage on either page names an ADR.
- [ ] With `FUNCD_*` variables set, `just generate` writes the same pages, holding none of their values; no default on
  either page holds the host's OS or architecture.
- [ ] A stale page fails its test, and `scripts/check-generated.sh` fails in CI.
- [ ] Neither binary gains a command or a dependency.

## Consequences

- A change to a config key or a flag shows its Spec change in the same PR.
- About 140 config keys become listed debt, shown with an empty description until described.
- `--help` no longer names ADRs; a maintainer finds a command's ADR in the code's comments.

## Open questions

None.

## References

- [ADR-0323](0323-each-features-spec-tab-is-generated-from-its-described-fields.md),
  [ADR-0061](0061-funcd-daemon-config-file.md), [ADR-0062](0062-config-single-env-validated-struct.md),
  [ADR-0042](0042-cobra-cli-framework.md).
- [#1445](https://github.com/pyvvo/funcd/issues/1445): funcdctl resolves its machine-dependent flag defaults when a
  command runs; this ADR's implementation follows it.
- `.github/workflows/release.yml`: the thin and `-tags dev` funcdctl builds.
