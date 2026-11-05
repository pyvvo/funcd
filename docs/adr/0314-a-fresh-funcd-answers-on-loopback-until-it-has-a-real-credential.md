# ADR-0314: A fresh funcd answers on loopback until it has a real credential

- **Status**: Implemented (2026-11-05), review passed with three minors, folded in ([report](../reviews/adr-0314-implementation-claude-opus-5-5.md)); Reviewing (2026-11-05); Accepted (2026-11-05), the decider delegated acceptance to the session driving this work ("I'll let you accept
  the proposed ADR"), with Decisions 3 and 6 confirmed; judged once: no Blocker, one Major (the list below), folded in
- **Date**: 2026-11-05
- **Deciders**: green-0-rabbit
- **Tags**: auth, config, daemon, defaults
- **Realizes**: [FEAT-0001/F228](../feat/0001-feat-v1.1.md) (a fresh funcd answers on its own machine until it is configured)
- **Answers**: the decider's choice of 2026-11-05: local until configured, over a token generated at install
- **Supersedes (in part)**, with back-links at acceptance (lines at main 5b27e74a; "with this rule": on loopback or with a real credential):
  - [ADR-0061](0061-funcd-daemon-config-file.md): scenario `zero-config-defaults` (lines 46-48) and the
    `server.listenAddr` default of its Contracts (line 159): `127.0.0.1:8080`. Scenarios `file-sets-addresses` (lines 49-51)
    and `partial-file-fills-rest` (lines 54-55), and Decision 3's "a partial file is valid" (line 123): with this rule.
  - [ADR-0062](0062-config-single-env-validated-struct.md): scenario `zero-config-defaults` (lines 40-41): the same default.
    Constraints' "a malformed bind fails at listen time" (lines 75-76): with the built-in token, this rule refuses it first.
  - [ADR-0171](0171-static-credential-list.md): Decision 4's "today's path is unchanged" (line 137) and the built-in half
    of scenario `legacy-token-is-developer` (lines 58-60): the built-in token works only on loopback, and warns in each
    form. Decision 7's "`WithDevAuth` does not change" (lines 156-157): `New` refuses `DevToken` off loopback.
  - [ADR-0028](0028-platform-control-plane-wiring.md) Decision 1's "unless the operator supplies `WithDevAuth`/credentials"
    (line 108): with this rule.
  - [ADR-0113](0113-edge-authn-pep.md) Decision 6 (lines 137-142): `WithEdgeAuth` with the built-in token needs a data plane on loopback.
- **Keeps**: ADR-0028 `Production()`'s own bind and its missing credential (lines 105-108): the daemon always overrides
  that bind (`cmd/funcd/main.go:408`) · ADR-0033's `defaultDataPlaneAddr` · ADR-0171 Decision 3, which refuses the
  built-in token in a token file
- **Relates to**: [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md) (`runs-out-of-the-box`) ·
  [ADR-0207](0207-pre-upgrade-snapshot-and-safe-mode.md) (Decision 3, what counts as a start) ·
  [ADR-0313](0313-one-place-says-what-the-platform-does-and-who-decided-it.md) (the rows below)

## Context & Need

funcd works with no config. `server.listenAddr` has a default (ADR-0061, ADR-0062), and with neither `auth.token` nor
`auth.credentials` the daemon uses the built-in token `funcd.DevToken` (ADR-0171 Decision 4). `funcd install` starts its unit
with no config (ADR-0054). Three forms give that token: `auth.token` empty, `auth.token: funcd-dev-token` and
`FUNCD_TOKEN=funcd-dev-token`.

**Purpose.** The built-in token is a local convenience: `funcdctl`, with the documented token, and a library user work at
once on the machine funcd runs on. This ADR ties it to that machine. A fresh funcd answers on loopback. An operator who wants
to reach it from another machine sets an address and a credential of their own, and a start that sets only the address
says what is missing. Callers: an operator after `funcd install`; a library user of `funcd.New`.

## Scenarios

- `scenario: fresh-config-binds-loopback`: Given no file and no `FUNCD_*` env, When the config loads, Then
  `server.listenAddr` is `127.0.0.1:8080`.
- `scenario: built-in-token-off-loopback-refused`: Given `server.listenAddr` `0.0.0.0:8080`, `:8080`, `[::]:8080`,
  `192.0.2.1:8080` or `example.test:8080`, with `auth.token` empty, `auth.token: funcd-dev-token` or
  `FUNCD_TOKEN=funcd-dev-token`, When funcd starts, Then it exits 1 with `fault.Invalid` naming `server.listenAddr` and
  what to set, no token value in the error, and its data directory stays absent: no `safemode.json`, no `store/`.
- `scenario: built-in-token-on-loopback-warns`: Given `127.0.0.1:8080`, `127.1.2.3:0`, `[::1]:8080` or `localhost:8080`
  with each form above, When the rule runs, Then it passes; and a funcd started on `127.0.0.1:0` in each form logs the
  warning once and answers the built-in token with 200.
- `scenario: real-credential-binds-anywhere`: Given `server.listenAddr: 0.0.0.0:0` and `auth.token: <own>` or
  `auth.credentials`, When funcd starts, Then it answers that credential with 200 and the built-in token with 401, with
  no warning about the token.
- `scenario: library-follows-the-rule`: Given `New(Production(), <drivers>, WithDevAuth(DevToken, "default"))`,
  `New(InMemory(), WithListenAddr("0.0.0.0:0"))` or `New(InMemory(), WithListenAddr(""))`, Then `New` returns
  `fault.Invalid` naming `WithListenAddr`, no token value in it; `New(InMemory())` and
  `New(InMemory(), WithDevAuth("own", "default"), WithListenAddr("0.0.0.0:0"))` start.
- `scenario: edge-auth-follows-the-rule`: Given `server.auth.edge: true`, the built-in token in effect, `server.listenAddr`
  on loopback and `server.dataPlaneAddr: 0.0.0.0:0`, When funcd starts, Then it is refused as above, naming
  `server.dataPlaneAddr`; with `server.auth.edge: false` the same config starts. Given `InMemory()`, `WithEdgeAuth()` and
  `WithDataPlaneAddr("0.0.0.0:0")`, `New` refuses it naming `WithDataPlaneAddr`.

## Scope

**In**: the default; the rule on the built-in token, in the daemon and the library; the warning; the docs on reaching a
node from another machine. **Out**: `Production()`'s own bind and `defaultDataPlaneAddr`; `configs/systemd/funcd.service`
and `funcdctl dev`, which need no change; TLS; token rotation. The unit `funcd install` writes sets
`FUNCD_RUNTIME=containerd`: ADR-0054 and `docs/install.md` step 2 already say it runs that runtime, so it needs no decision
and lands in the same PR as its own commit, test first.

## Constraints & Decision drivers

- `funcd install` runs `enable --now` with no config (ADR-0054), so the default and the rule go together: with the rule
  alone, each fresh install would refuse its own start.
- ADR-0062: errors name the key. ADR-0171 Decision 8: no token value in any log or error. No new key, state or dependency.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| **A loopback default, and the built-in token only on loopback** | works at once on the machine, with no file to keep; no new key; the refusal says what to set | another machine reaches a node only after two settings; a config that sets only the address is refused | **chosen** (decider, 2026-11-05) |
| A token generated at install or at the first start, kept in the data dir (k3s, faasd) | each node has its own token from the start; a node is reachable from elsewhere with no config step | it needs persistence and a reset, the costs for which ADR-0171 line 108 rejected a generated admin token; `funcdctl` must read the file or an operator copy it; it must survive a restore and a moved data dir; k3s backs its token up with the datastore | rejected: more state for what two settings do |
| Bind every address, and answer others with an error that says how to configure it (Redis) | a client on another machine reads why | a check per connection on two listeners, for what a bind does | rejected |
| Refuse off loopback, keep `0.0.0.0:8080` as the default | one rule, no change of default | a fresh install refuses its own start, and the unit retries it with no config to change | rejected |
| Only the default changes, with no refusal | no refused start | a config that sets an address keeps the built-in token with one warning | rejected: the token stays tied to no address |
| A warning only (today) | no change | the same, and two of the three forms do not warn | rejected |
| The rule in `devAuthOption` | beside the warning | it runs after the count (ADR-0207), so refused starts lead to a held start and then exit 70, about 12 s after a one-line edit | rejected |
| The rule in `(Config).Validate` | every verb sees it | `funcd restore` and `funcd upgrade` load the same config and read neither address; the leaf cannot name `funcd.DevToken` | rejected |
| The rule on `server.listenAddr` alone | one key | with edge auth on, the data plane reads the same token | rejected |
| The rule on both addresses always | one sentence | it refuses a data plane that reads no credential | rejected |

What others do, read 2026-11-05:

| Platform | With no configuration | From another machine |
|---|---|---|
| Redis | since 3.2.0, protected mode: with the default config and no password, it replies only on loopback, and elsewhere with an error that explains how to configure it | set a password or a bind |
| etcd | `--listen-client-urls` defaults to `http://localhost:2379` | set the URLs |
| Consul | `client_addr` defaults to `127.0.0.1`, "allowing only loopback connections" | set `client_addr` |
| Docker | "Docker runs through a non-networked UNIX socket" | SSH, or TLS on TCP |
| k3s | with no token, the first server generates one and writes `/var/lib/rancher/k3s/server/token`; it also encrypts the bootstrap data, so it is backed up with the datastore; `k3s token rotate` replaces it | the token |
| faasd | `faasd up` generates a password once, in `/var/lib/faasd/secrets/basic-auth-password` | the password |
| Grafana | an empty `http_addr` binds all interfaces; `admin_password` defaults to `admin`, set once on first run | a login |

## Decision

1. **The default.** `server.listenAddr` defaults to `127.0.0.1:8080`. `server.dataPlaneAddr` keeps `127.0.0.1:0`.
2. **The built-in token works only on loopback.** With `auth.credentials` absent, the built-in token is in effect when
   `auth.token` (`FUNCD_TOKEN`) is empty or, after `parseToken`'s trim, equals `funcd.DevToken`. A start with the token
   in effect and `server.listenAddr` off loopback is refused with `fault.Invalid`, which names the key and what to set
   and never a token. Loopback means `localhost` or an IP with `IsLoopback`: `0.0.0.0`, `::`, an empty host, a LAN address, any
   other hostname and an address that does not split into host and port are not. A real `auth.token` or `auth.credentials` starts on
   any address, as today.
3. **The refusal comes first.** `serve` applies the rule right after `config.Load`, before it creates the data
   directory, so the refused start does not count (ADR-0207 Decision 3), as a config refused at load does not: a config
   with only an address set never leads to safe mode. The unit retries it as it does any failed start.
4. **One warning.** On loopback, each of the three forms logs the same warning at start, which names the rule and no
   token (Contracts). `auth.token` no longer starts in silence with the built-in value.
5. **The library follows the rule.** `(*config).validate` applies it through the same helper, after resolving an empty
   address to the default `New` would bind: when the credential store accepts `DevToken` and the control-plane address
   is off loopback, `New` returns `fault.Invalid`, naming `WithListenAddr`. `WithCredentials` cannot hold `DevToken`
   (ADR-0171 Decision 7), and `WithDevAuth` with another token starts on any address.
6. **The edge follows the rule, when it reads the token.** `server.auth.edge` (`WithEdgeAuth`) is opt-in and off by default.
   With it on, the edge reads the same credential store (`pkg/funcd/funcd.go:869`), so the rule also applies to
   `server.dataPlaneAddr` (`WithDataPlaneAddr`, with an empty address resolved to its default). With it off, the data
   plane reads no credential (an `authenticated` stance answers 401), so any address starts.

## Design model

None: one read of the config at a start, with no lifecycle, no second actor and no timer. Decisions 2, 5 and 6 are two callers
of one pure helper, and one table test runs every address form through it (Implementation plan, steps 1 to 3).

## Behavior rows

Added (ADR-0313), with `just behavior-values` regenerating the `server.listenAddr` value in Calls:

| Chapter | Where | What happens | Time | You see | Status | Source |
|---|---|---|---|---|---|---|
| Platform lifecycle | The built-in dev token off loopback | Refused before the count and before anything opens, so it never leads to safe mode; the unit retries it as any failed start | 2 s (the unit) | `the built-in dev token works only on loopback`, naming the key | decided | Decisions 2 and 3, `TestScenarioBuiltInTokenOffLoopbackRefused` |
| Platform lifecycle | `funcd install` | Writes one unit, which sets the containerd runtime, and starts it with no config | none | `installed and started funcd.service` | decided | ADR-0054 scenario `runs-out-of-the-box`, `TestInstallUnitRunsContainerd` |
| Security | The built-in dev token on loopback | Works in each of its forms, with one warning at the start | none | `using the built-in dev token` | decided | Decisions 2 and 4, `TestScenarioBuiltInTokenOnLoopbackWarns` |
| Security | A real token or `auth.credentials` | Starts on any address; the built-in dev token then gets 401 | none | 401 `invalid credential` | decided | Decision 2, `TestScenarioRealCredentialBindsAnywhere` |
| Security | `funcd.New` with `DevToken` off loopback, or edge auth with it and a data plane off loopback | Refused, as the daemon refuses a start | none | `is off loopback` | decided | Decisions 5 and 6, `TestScenarioLibraryFollowsTheRule`, `TestScenarioEdgeAuthFollowsTheRule` |
| Calls | A fresh funcd | The API answers on loopback, the data plane on loopback at an ephemeral port | none | connection refused at another address | decided | Decision 1, `TestScenarioFreshConfigBindsLoopback` |
| Calls | An address with no port, with the built-in dev token | This refusal, not a bind error | none | `is off loopback` | accepted | Consequences, `TestIsLoopbackAddr` |
| Calls | An explicit empty `server.listenAddr` with a real credential | It reaches the `Production()` default, `0.0.0.0:8080`, as before | none | `platform starting` | accepted | Consequences, `TestScenarioRealCredentialBindsAnywhere` |

Changed: Platform lifecycle's machine edge `[*] --> Exited` names this refusal, and its row *A start that fails* says it
does not count. Removed: none.

## Temporary workarounds

None.

## Contracts

```go
// internal/platform/config: the one helper. A leaf both cmd/funcd and pkg/funcd already import.
// IsLoopbackAddr reports whether addr, a host:port to bind, stays on this machine (ADR-0314 Decision 2).
func IsLoopbackAddr(addr string) bool

// cmd/funcd/credentials.go
func builtInTokenInEffect(cfg config.Config) bool // auth.credentials absent; auth.token empty or, trimmed, funcd.DevToken
func checkBuiltInTokenBind(cfg config.Config) error // fault.Invalid naming the first key off loopback; serve calls it first

// pkg/funcd/funcd.go, in (*config).validate after the credentials case: asks c.credentials.Lookup(ctx, DevToken).
```

The refusal, the same with `server.dataPlaneAddr` and "server.auth.edge is on", then the library's, then the warning:

```
config key "server.listenAddr (FUNCD_LISTEN_ADDR)" is "0.0.0.0:8080", off loopback, and the built-in dev token works only
on loopback: set auth.credentials, or auth.token (FUNCD_TOKEN) to a token of your own, or use a loopback address

WithListenAddr "0.0.0.0:0" is off loopback, and the built-in DevToken works only on loopback: pass WithCredentials,
WithDevAuth with a token of your own, or a loopback address

funcd: using the built-in dev token, which works only on loopback: to reach funcd from another machine, set
server.listenAddr and auth.credentials
```

| Dependencies & I/O | |
|---|---|
| Consumes | `server.listenAddr`, `server.dataPlaneAddr`, `server.auth.edge`, `auth.token` / `FUNCD_TOKEN`, `auth.credentials` |
| Exposes | `config.IsLoopbackAddr`; `fault.Invalid` at a start and from `New`; no new key, flag or status reason |

## Implementation plan

1. `internal/platform/config`: the default, `IsLoopbackAddr` and `TestIsLoopbackAddr` (every form of Decision 2, a
   malformed address; `::ffff:127.0.0.1` passes, `::1%lo0` does not); `TestScenarioZeroConfigDefaults` follows;
   `TestScenarioFreshConfigBindsLoopback`. `examples/funcdconfig.yaml` shows the default and the rule.
2. `cmd/funcd`: the two functions; `serve` calls the check right after `config.Load`; `devAuthOption` warns on
   `builtInTokenInEffect`. `credentials_test.go`: `TestScenarioBuiltInTokenOffLoopbackRefused` through `serve`,
   `TestScenarioBuiltInTokenOnLoopbackWarns`, `TestScenarioRealCredentialBindsAnywhere` and
   `TestScenarioEdgeAuthFollowsTheRule`; `TestScenarioLegacyTokenIsDeveloper` gains the two other forms.
3. `pkg/funcd`: the rule in `validate`; `TestScenarioLibraryFollowsTheRule`, with its edge rows; the doc comments of
   `DevToken`, `InMemory`, `Production`, `WithListenAddr`, `WithDataPlaneAddr`, `WithDevAuth` and `WithEdgeAuth`.
   `TestScenarioDevRunsResources` (`-tags dev`) stands for `funcdctl dev`.
4. `docs/install.md`: what a fresh install answers on; step 4's `funcdctl` command takes `--token`; a section on reaching
   a node from another machine, with `server.listenAddr`, `auth.credentials`, `server.dataPlaneAddr` and `server.auth.edge`;
   an upgrade note from the release with this ADR. `docs/PROJECT-SUMMARY.md` follows in the release wave's last commit.
5. The rows above, then `scripts/agent/d just behavior-values` and `just check-behavior`.
6. Item of its own, conforming to ADR-0054: `cmd/funcd/install.go`'s unit sets `Environment=FUNCD_RUNTIME=containerd`.
   `TestInstallUnitRunsContainerd` reads each `Environment=` line of the rendered unit, sets it and loads the config:
   `runtime.mode` is `containerd`. Test first, with a revert check. `docs/install.md` step 2: the variable wins over
   `runtime.mode` in a config file.
7. Lanes: every lane config, here and in the pinned modules, sets `127.0.0.1:8080` and `127.0.0.1:8081` (read 2026-11-05),
   so none changes. `scripts/lima.yaml` and `e2e/app-todo.venom.yml:1218-1220` run `funcd install` and disable the unit at once. By
   reading, a containerd start with no worker leaves files under `/var/lib/funcd`, apart from a lane's `/var/lib/funcd/data`,
   and nothing `lane-leakcheck.sh` counts.

**Done when** each Scenario's test passes and each fails with its rule reverted; the touched packages' tests, `go vet`, lint and
`just check-behavior` pass; the release wave's gate and its one lane pass are green; `TestInstallUnitRunsContainerd` pins the unit's
environment; and in `funcd-linux-tests`, as root: with the unit's environment and no config the binary takes the containerd path,
leaving no unit or laid-down runtime behind; with no config it binds loopback; and with an address and a token of its own the
VM's other address answers that token with 200 and the built-in token with 401.

## Review checklist

- [ ] Each Scenario has its named test, passing; each fails when its rule is reverted.
- [ ] `serve` applies the rule before the data directory exists; a refused start leaves no `safemode.json`.
- [ ] The daemon and `validate` read one `IsLoopbackAddr`; `validate` resolves an empty address first.
- [ ] Each of the three forms warns on loopback; no refusal or log line holds a token value.
- [ ] `InMemory()`, `funcdctl dev`, `docs/demo/server`, `internal/testkit/bench`, `Production()`'s bind, `defaultDataPlaneAddr`
      and `configs/systemd/funcd.service` are unchanged; the `funcd-linux-tests` run of *Done when* happened, and its result is reported.
- [ ] The rows match this ADR, and `just check-behavior` passes.
- [ ] Changed files carry no local username or absolute path of the dev machine.

## Consequences

- (+) A fresh install works on its own machine with no config, and needs no new state.
- (−) A node is not reachable from another machine until the operator sets an address and a credential, for any connection
  from elsewhere. `docs/install.md` says how, and the refusal names both.
- (−) A config that sets only the address does not start. With no deployed node (decider, 2026-10-29), a note under Upgrade
  is enough: a node with no `server.listenAddr` moves to loopback, one with an address and the built-in token is refused.
- (−) The check reads `localhost` by name and does not resolve it; an address that does not parse is refused with this rule's
  message, not at listen. An explicit `server.listenAddr: ""` with a real credential still reaches `Production()`'s default.

## Open questions

None.

## References

- [FEAT-0001/F228](../feat/0001-feat-v1.1.md), which takes no feature issue, like ADR-0294 to ADR-0297; ADR-0272 Decision 7.
- Read 2026-11-05: [Redis](https://redis.io/docs/latest/operate/oss_and_stack/management/security/),
  [etcd](https://etcd.io/docs/v3.6/op-guide/configuration/), [Docker](https://docs.docker.com/engine/security/protect-access/),
  [Consul](https://developer.hashicorp.com/consul/docs/reference/agent/configuration-file/general), [k3s](https://docs.k3s.io/cli/token),
  [faasd](https://github.com/openfaas/faasd/blob/master/cmd/up.go), [Grafana](https://grafana.com/docs/grafana/latest/setup-grafana/configure-grafana/).
