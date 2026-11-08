# ADR-0319: funcdctl runs kubectl-style plugins

- **Status**: Accepted (2026-11-08), after a judge by the drafting session (not independent) whose one Blocker, one Major and seven Minors were folded, and a rerun of experiment 0017 on the final rules
- **Date**: 2026-11-08
- **Deciders**: green-0-rabbit
- **Tags**: funcdctl, cli, apps
- **Realizes**: [FEAT-0010/F233](../feat/0010-feat-apps.md) (an App's own CLI runs as a funcdctl plugin)
- **Supersedes (in part)**: [ADR-0042](0042-cobra-cli-framework.md) — `scenario: cli-unknown-command`: an unknown verb is still reported and exits non-zero, unless `funcdctl-<verb>` is a plugin on PATH
- **Relates to**: [ADR-0024](0024-funcdcli-and-sdk.md) (funcdctl over the control-plane API), [ADR-0026](0026-packaging-and-release.md) (the version stamp and the release targets)

## Context & Need

funcdctl knows the platform's kinds, not the Apps built on them. An App can serve an API of its own and want a CLI for
it, versioned with that API: the first one planned is `funcdctl-data`, the CLI of a data studio App. On v0.39.3
funcdctl cannot run it: `funcdctl data` fails with `unknown command "data" for "funcdctl"`, and nothing hands such a
CLI the funcdctl, the server and the token the user works with. A project that keeps one funcdctl binary per version
(such as `funcdctl-v0.39.3`) also needs the App's CLI to call that same binary back.

**Purpose.** `funcdctl <name>` runs the App's CLI as a funcdctl plugin, the way kubectl runs `kubectl-<name>`. A
developer, a script or an agent types one command, and the plugin calls the same funcdctl back with the same server and
token. A funcdctl plugin is an executable on PATH: it is neither an invoke plugin of the call chain (ADR-0302) nor a
runtime plugin for Jobs (FEAT-0012 F139). Experiment 0017 built it first and checked every behavior below, on macOS
and on a Linux lane VM against a real funcd.

## Scenarios

- `scenario: plugin-runs-for-a-word-that-is-no-command`: Given `funcdctl-hello` and `funcdctl-hello-world` on PATH, When `funcdctl hello world` runs, Then `funcdctl-hello` runs with the one argument `world`.
- `scenario: a-word-with-no-plugin-is-an-unknown-command`: Given no `funcdctl-nosuch` on PATH, When `funcdctl nosuch` runs, Then it fails as before plugins: `unknown command "nosuch" for "funcdctl"`, exit 1.
- `scenario: built-in-commands-win`: Given plugins named `get`, `help`, `completion`, `plugin` and `__complete` on PATH, When each of those commands runs, Then funcdctl's own command runs and no plugin does.
- `scenario: only-a-plain-word-names-a-plugin`: Given a versioned copy `funcdctl-v0.39.3` and a `funcdctl-Hello` on PATH, When `funcdctl v0.39.3 get` or `funcdctl Hello` runs, Then nothing runs and each is an unknown command, and `funcdctl plugin list` lists neither.
- `scenario: nothing-runs-from-the-working-directory`: Given a `funcdctl-x/y` under the working directory and a `funcdctl-hello` found only through the relative PATH entry `rel`, When `funcdctl x/y` or `funcdctl hello` runs, Then nothing runs and each is an unknown command.
- `scenario: flags-before-the-name-are-funcdctls`: Given `funcdctl --server http://a.example:9 --token T hello env`, Then the plugin is handed that server and a token. Given `funcdctl hello --server x -h -- "two words"`, Then the plugin gets those five words unchanged. Given `funcdctl --bogus hello`, Then funcdctl fails with `unknown flag: --bogus`.
- `scenario: the-plugin-is-handed-what-funcdctl-resolved`: When a plugin runs, Then `FUNCDCTL` names the running funcdctl, a versioned copy's own path included, `FUNCDCTL_VERSION` its version stamp, `FUNCD_SERVER` the server funcdctl resolved, its default included, and `FUNCD_TOKEN` is set only when a token resolved: `--token ""` clears an inherited one.
- `scenario: the-plugin-calls-back-with-the-resolved-token`: Given a control plane with a developer token, When `funcdctl --server <it> --token <the token> hello call get functions -n <ns>` runs, Then the plugin's `$FUNCDCTL get functions` lists, and with a wrong token it is refused with `invalid credential`.
- `scenario: the-plugin-replaces-funcdctl`: When a plugin runs, Then it has funcdctl's PID, funcdctl exits with the plugin's code, stdin, stdout and stderr are the plugin's, and a SIGTERM or a SIGINT sent to that PID reaches the plugin. An executable that cannot be executed, such as a script with no interpreter line, fails as funcdctl's own error, exit 1.
- `scenario: plugin-list`: Given two PATH entries that both hold `funcdctl-hello`, a `funcdctl-get`, a file that is not executable and a relative entry, When `funcdctl plugin list` runs, Then it prints each plugin's name and path in PATH order, warns that `funcdctl-get` and the second `funcdctl-hello` never run, lists nothing else, and exits 0. With no plugin it prints `no plugins on PATH`.

## Scope

- **In**: the lookup in funcdctl's `main`, the four variables a plugin is handed, the exec, `funcdctl plugin list`, a hello-world plugin that is also the tests' fixture, the tests and the `funcdctl-plugin` lane.
- **Out**: an install command or a plugin registry; a plugin that overrides or extends a built-in command (kubectl's `create` plugins included); completion of plugin names and of their subcommands; `funcdctl help <plugin>`; in-process plugins; kubectl's longest dashed match; Windows, which funcd does not ship and which has no exec; what a plugin needs beyond funcdctl's server and token, such as its App's own API address or `funcdctl dev`'s endpoints. `funcdctl dev --print-env` stays as it is: the dev S3 credentials, from the `-tags dev` build only (decider, 2026-11-08).

## Constraints & Decision drivers

- funcd stays generic: funcdctl learns no App's command.
- One resolution: a plugin gets funcdctl's server and token as funcdctl resolved them, and never repeats funcdctl's flag and environment rules (`--server` over `$FUNCD_SERVER` over `http://localhost:8080`, `--token` over `$FUNCD_TOKEN`).
- The same funcdctl back: a plugin can call the binary that ran it, a versioned copy included.
- A plugin's exit code, stdio and signals are its own, so a script, CI or a process supervisor sees the plugin.
- Nothing runs from the working directory by accident.
- No new dependency: cobra, pflag, `os/exec.LookPath` and `syscall.Exec`. funcd ships Linux and macOS only, where exec exists.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| No lookup: the App ships a standalone CLI | No change to funcd | Each CLI repeats funcdctl's resolution of the server and the token, `--token ""` included, cannot know which funcdctl binary the user runs, and `funcdctl <name>` stays an unknown command | Rejected: the request asks for `funcdctl <name>` and the same funcdctl back |
| Run the plugin as a child process | Works on every OS | funcdctl stays in the middle: it forwards each signal by hand, and a `kill` of funcdctl's PID stops funcdctl and leaves the plugin running | Rejected: the exec gives all of it on the only OSes funcd ships |
| kubectl's rules as they stand | One difference less from kubectl | kubectl refuses any flag before a plugin's name (`flags cannot be placed before plugin name`), so a plugin could not be pointed at a server; its longest dashed match adds lookups no plugin needs | Rejected: the request asks for funcdctl's flags before the name and the first word only |
| Any file name, as kubectl takes | No name rule | A versioned copy `funcdctl-v0.39.3` on PATH lists as the plugin `v0.39.3`, and `funcdctl v0.39.3 …` runs another funcdctl (experiment 0017) | Rejected by the decider, 2026-11-08 |
| In-process plugins: Go's `plugin` package, or an RPC protocol | No exec | Ties a plugin to funcdctl's build and Go version, and a CLI written in another language could not be one | Rejected: out of the request |

## Decision

1. **The lookup.** Before cobra runs, funcdctl parses its own persistent flags up to the first word, with a new flag set that holds them, interspersed parsing off and errors left to cobra, so cobra's own parse is unchanged. A plugin runs when that word is a plain word (a lowercase letter, then lowercase letters, digits and dashes), names none of funcdctl's commands, and `exec.LookPath` finds an executable `funcdctl-<word>`. Every word after it goes to the plugin unchanged, and the plugin handles its own subcommands. Otherwise cobra runs the command line as before, with its errors: a word with no plugin stays an unknown command.
2. **Built-in commands win.** funcdctl's commands are its root's subcommands with their aliases, and cobra's `help` and `completion`, which cobra adds only when it runs. A plugin of one of those names never runs. cobra's hidden `__complete` commands are no plain words, so no plugin takes them either.
3. **Flags before the name are funcdctl's.** They set what the plugin is handed (Decision 4). An unknown flag there fails as funcdctl's own error, and `-h` or `--help` there is funcdctl's help. ADR-0042's persistent flags still work before or after one of funcdctl's own verbs; after a plugin's name they are the plugin's words.
4. **The environment.** The plugin inherits funcdctl's environment with `FUNCDCTL`, `FUNCDCTL_VERSION`, `FUNCD_SERVER` and `FUNCD_TOKEN` set, as Contracts lists. `FUNCD_SERVER` is always set. `FUNCD_TOKEN` is set only when a token resolved, so `--token ""` clears an inherited one.
5. **The plugin replaces funcdctl's process** (`syscall.Exec`, with the plugin's path as argv[0]). It keeps funcdctl's PID, and the exit code, the stdio and the signals are its own. When the exec fails, funcdctl prints the error and exits 1.
6. **Nothing runs from the working directory.** A word with a slash is no plain word, so the lookup never tries it as a path, and a plugin found only through a relative PATH entry does not run, because `exec.LookPath` refuses it (`exec.ErrDot`).
7. **`funcdctl plugin list`** prints a NAME and PATH table of each plain-word `funcdctl-<name>` executable in the absolute PATH entries, in PATH order. It writes one warning to stderr for each one that never runs: a built-in command takes its name, or an earlier PATH entry holds one of that name. With none it prints `no plugins on PATH`. It exits 0.

## Design model

None: one lookup and one exec per command line, with no lifecycle, no concurrent actors, no timer and no retry (ADR-0272).

## Behavior rows

A new chapter, `docs/behavior/funcdctl.md`, with a row in the index's chapter table. **New**, each `decided` and citing its Decision and test: `A word that is no command`, `A plugin named like a built-in command`, `A plugin name that is no plain word`, `Flags before a plugin's name`, `What a plugin is handed`, `A plugin's process`, `funcdctl plugin list`.

## Temporary workarounds

None.

## Contracts

```go
// cmd/funcdctl/plugin.go
const pluginPrefix = "funcdctl-"

type pluginCall struct {
	path string
	argv []string
	env  []string
}

func findPlugin(root *cobra.Command, args, environ []string) (pluginCall, bool, error)
func pluginEnv(environ []string, exe, server, token string) []string
func runPlugin(c pluginCall) error
func plainWord(s string) bool
func isBuiltin(root *cobra.Command, name string) bool
func (a *cli) pluginCmd() *cobra.Command
func listPlugins(root *cobra.Command, dirs []string, out, errOut io.Writer) error

// cmd/funcdctl/main.go
root := newRootCmd(os.Stdout)
call, ok, err := findPlugin(root, os.Args[1:], os.Environ())
if err == nil && ok {
	err = runPlugin(call)
}
if err == nil {
	err = root.Execute()
}
```

`findPlugin` returns an error only when a plugin was found and `os.Executable` fails. `runPlugin` returns only when the exec fails.

| Variable | Value |
|---|---|
| `FUNCDCTL` | the running funcdctl's path, from `os.Executable` |
| `FUNCDCTL_VERSION` | `internal/platform/version.Version`, where the release stamps it (an import funcdctl did not have): `dev` for a build with none |
| `FUNCD_SERVER` | `--server`, else `$FUNCD_SERVER`, else `http://localhost:8080` |
| `FUNCD_TOKEN` | `--token`, else `$FUNCD_TOKEN`; absent when that is empty |

| | Consumes | Exposes |
|---|---|---|
| funcdctl | `PATH`, `$FUNCD_SERVER`, `$FUNCD_TOKEN`, its `--server`, `--token` and `-h` flags | the plugin's process, the four variables, `funcdctl plugin list` (NAME and PATH on stdout, warnings on stderr) |

Exit codes: the plugin's own; 1 for an unknown command, an unknown flag before the name, or a failed exec.

## Implementation plan

1. `cmd/funcdctl/plugin.go` (new): the Contracts. `main.go`: the lookup before `Execute`. `cli.go`: `a.pluginCmd()` among the root's commands.
2. `cmd/funcdctl/testdata/plugins/funcdctl-hello`: the hello-world plugin and the fixture of the tests and the lane, in POSIX sh. It says whether `FUNCD_TOKEN` is set and never prints its value.
3. `cmd/funcdctl/plugin_test.go`: a `TestMain` that runs funcdctl's `main` when an environment variable asks, so each test starts funcdctl as a process of its own, and one test per scenario.
4. The lane: `scripts/lanes.yaml` `funcdctl-plugin`, `e2e/funcdctl-plugin.venom.yml`, `e2e/fixtures/funcdctl-plugin-funcdconfig.yaml` with one developer credential, whose token the suite reads inside the VM, and the row in `e2e/README.md`.
5. Docs: `docs/behavior/funcdctl.md` and its index row; F233 in FEAT-0010.

**Test plan**:

| Scenario | Test |
|---|---|
| `plugin-runs-for-a-word-that-is-no-command` | `TestPlugin_RunsTheExecutableNamedByTheFirstWord`; lane `a word that is no command runs funcdctl-word from PATH` |
| `a-word-with-no-plugin-is-an-unknown-command` | `TestPlugin_AWordWithNoPluginIsAnUnknownCommand`; the same lane case |
| `built-in-commands-win` | `TestPlugin_BuiltInCommandsWin`; lane `a built-in command wins over a plugin of its name` |
| `only-a-plain-word-names-a-plugin` | `TestPlugin_OnlyAPlainWordNamesAPlugin`; lane `only a plain word names a plugin …` and `funcdctl plugin list …`, with a staged `funcdctl-v0.39.3` |
| `nothing-runs-from-the-working-directory` | `TestPlugin_NothingRunsFromTheWorkingDirectory` |
| `flags-before-the-name-are-funcdctls` | `TestPlugin_FlagsBeforeTheNameAreFuncdctls`; lane `flags before the name are funcdctl's …` |
| `the-plugin-is-handed-what-funcdctl-resolved` | `TestPlugin_EnvNamesTheCallingFuncdctlAndWhatItResolved`; lane `the plugin is handed …` |
| `the-plugin-calls-back-with-the-resolved-token` | `TestPlugin_CallsBackThroughFuncdctlWithTheResolvedToken`, on a real control plane over httptest; the same lane case, on the lane's funcd |
| `the-plugin-replaces-funcdctl` | `TestPlugin_ReplacesTheFuncdctlProcess`, with the failed exec; lane `the plugin replaces funcdctl's process …` |
| `plugin-list` | `TestPluginList_NamesEachPluginAndWarnsAboutTheOnesThatNeverRun`; lane `funcdctl plugin list …` |

**Done**: every test passes under `-race` on the default and the `-tags dev` builds; reverting each Decision's key line fails a test; the `funcdctl-plugin` lane passes; `scripts/agent/gate.sh` passes.

## Review checklist

- [ ] `main` resolves a plugin before `Execute`, and only `runPlugin` execs.
- [ ] A word that is no plain word, names a built-in command (cobra's included) or is found only through a relative PATH entry never runs a plugin.
- [ ] The flags before the name are parsed by funcdctl's own persistent flag set, which stops at the first word.
- [ ] The four variables match the Contracts table, and an empty token removes `FUNCD_TOKEN`.
- [ ] `funcdctl plugin list` and the lookup agree on what a plugin is, on a filesystem that keeps case.
- [ ] No test or lane step prints a token.
- [ ] Every scenario has its test, and the lane passes.

## Consequences

- An App ships its CLI with no change to funcd, and the CLI calls the same funcdctl back with the user's server and token.
- funcdctl's first word now also names plugins: a later built-in command shadows a plugin of the same name, and `funcdctl plugin list` reports it.
- Every plugin the user runs is handed their token in `FUNCD_TOKEN`, as their shell holds it: a plugin is trusted as funcdctl is.
- PATH is trusted, as kubectl and git trust it: any plain-word `funcdctl-<name>` executable of an absolute PATH entry runs when its name is typed.
- Each plugin call pays funcdctl's start before the plugin's own (experiment 0017's hop measures), small next to a network call.
- On a filesystem that ignores case, as macOS's default does, `funcdctl hello` also runs a file named `funcdctl-Hello`, which `funcdctl plugin list` does not show (experiment 0017).
- A funcdctl built for Windows would fail at the exec; funcd ships none.

## Open questions

- Completion of plugin names and of their subcommands (`funcdctl da<TAB>`, `funcdctl data <TAB>`): a later ADR, when a plugin asks for it.
- `funcdctl dev`'s endpoints for a plugin, when they are not fixed with `--cport` and `--gport`: a `funcdctl dev` ADR, if a plugin needs them.

## References

- The feature issue, #1421, under the Apps tracker #871.
- Experiment 0017, pyvvo/experiment#30 (private), at ec1f3d3: every behavior above, the plain-word names included, checked on macOS and on a Linux lane VM against a real funcd, and the hop's cost. Its first run, at 7391b67, took any file name.
- kubectl: [Extend kubectl with plugins](https://kubernetes.io/docs/tasks/extend-kubectl/kubectl-plugins/), and its lookup in `staging/src/k8s.io/kubectl/pkg/cmd/plugin.go` and `cmd.go`.
- Go: [os/exec, executables in the current directory](https://pkg.go.dev/os/exec#hdr-Executables_in_the_current_directory).
- funcd: the root's `--server` and `--token` flags in `cmd/funcdctl/cli.go`; the stamp in `internal/platform/version/version.go`; the release targets in `.github/workflows/release.yml`.
