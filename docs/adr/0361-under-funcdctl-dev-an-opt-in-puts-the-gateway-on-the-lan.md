# ADR-0361: Under funcdctl dev, an opt-in puts the gateway on the LAN, and every other listener stays on loopback

- **Status**: Implemented (2026-11-27), review passed in its first round ([report](../reviews/adr-0361-implementation-claude-opus-5-5.md)); Reviewing (2026-11-27, implemented); Accepted (2026-11-27), by the decider ("accept with --lan", relayed word for word by the DR session, to
  which the decider gave the decision), after three rounds of an independent judge at medium effort, every finding folded
- **Amended in place**: 2026-11-27, by the decider on [#1601](https://github.com/pyvvo/funcd/issues/1601): the banner
  prints each URL once, on the default route's LAN address, and one note names the other addresses (Decision 3)
- **Date**: 2026-11-27
- **Deciders**: green-0-rabbit
- **Tags**: dev, cli, edge, security
- **Realizes**: [FEAT-0012/F278](../feat/0012-feat-infrastack.md) (dev's gateway on the LAN)
- **Supersedes (in part)**, with a back-link: [ADR-0125](0125-funcdctl-dev-local-run.md) Decision 1's
  "serves on localhost", which now holds without the opt-in only: with it, the gateway also serves on the LAN
- **Relates to**: [ADR-0254](0254-funcdctl-dev-config-file.md) (a key added to its file) ·
  [ADR-0253](0253-funcdctl-dev-runs-an-app-template.md) (the App's banner, Decision 11) ·
  [ADR-0255](0255-funcdctl-dev-runs-the-dev-loop.md) (the live reload stream) ·
  [ADR-0314](0314-a-fresh-funcd-answers-on-loopback-until-it-has-a-real-credential.md) (the built-in tokens on
  loopback) · [ADR-0110](0110-route-v2-declarative-edge-exposure.md) and [ADR-0240](0240-namespace-hosts.md) (the
  hosts a rule answers on) ·
  [ADR-0360](0360-a-function-that-serves-http-takes-websocket-connections.md) (WebSocket)

## Context & Need

`funcdctl dev` binds its gateway, where Functions, Routes and Sites answer, to 127.0.0.1: `funcd.InMemory()` sets the
data plane to `127.0.0.1:0`, and `--gport` replaces it with `127.0.0.1:<gport>` (`cmd/funcdctl/dev.go:1210-1212`). No
flag changes the address. On 2026-11-27 a dev App with a TV page and a phone remote, linked by a WebSocket
(ADR-0360), needed a phone on the same Wi-Fi to reach the gateway, so it ran a TCP forwarder of its own from the
machine's LAN address to `127.0.0.1:8795`. The forwarder works, WebSocket included, but every App that wants a phone
would need one.

The decider asked the same day for Vite's model: `vite --host`, or `server.host: true`, listens on every address, and
the banner prints `Local: http://localhost:5180/` and one `Network:` line per IPv4 address found at start.

Purpose: on an opt-in, a phone or another machine on the developer's network opens a dev App through the gateway,
while dev's control plane and S3 frontend stay reachable from the developer's machine only. Callers: the developer
who runs dev, and the devices on that network.

## Scenarios

A *LAN address* is an IPv4 address of one of the machine's interfaces that is up, other than a loopback address.

- `dev-lan-off` — *Given* no `--lan` and no `dev.lan`, *when* a client calls a Function at the gateway's port on a
  LAN address, *then* the connection is refused, the same call on 127.0.0.1 answers, and the banner names 127.0.0.1 on
  its gateway lines, with no note of other addresses and no LAN warning (amended 2026-11-27).
- `dev-lan-every-address` — *Given* `funcdctl dev --lan` on the http-api example (Function `devices-api`), *when* a
  client calls `/function/devices-api/devices` on a LAN address, and two WebSockets open at
  `/function/devices-api/relay` there, *then* the call answers as on 127.0.0.1, and a message sent on one socket
  reaches the other.
- `dev-lan-loopback-kept` — *Given* `--lan`, *when* a client dials the control plane's port and the S3 frontend's
  port on a LAN address, *then* both connections are refused, and both services answer on 127.0.0.1.
- `dev-lan-one-address` — *Given* `--lan=<a LAN address>`, *then* the gateway answers on that address, a call on
  127.0.0.1 is refused, and the banner's title and gateway lines name that address, with the LAN warning and no note
  of other addresses (amended 2026-11-27).
- `dev-lan-banner` — *Given* `dev.lan: true` in `funcdctl.config.yaml`, an App with a Route `/api` that names no host,
  a Route on host `login.localhost` and a Site `web`, and two LAN addresses at start, the default route's first, *when*
  dev prints its banner, *then* the gateway's line, the `/api` line and the Site's line each name the first address
  once, the `login.localhost` line names its host, the control and s3 lines name 127.0.0.1, a note names 127.0.0.1
  and the second address, and the LAN warning is printed (amended 2026-11-27).
- `dev-lan-refused` — *When* dev starts with no `funcdctl.config.yaml` and `--lan=tv.local`, or `--lan 192.168.1.20`
  (a space) with or without a directory after it, or with a `funcdctl.config.yaml` that sets `dev.lan: 3`, *then* it
  refuses to start: `tv.local` and `3` are named with what the option takes, and the space form is told to write
  `--lan=192.168.1.20`.

## Scope

- **In**: the flag `--lan` and the key `dev.lan`, in folder and App mode; the gateway's listen address; the banner's
  LAN URLs, its note of the other addresses (amended 2026-11-27) and its LAN warning; their checks; the Spec page, the
  guide, the behavior rows and the tests.
- **Out**:
  - every other listener: the control plane with its built-in tokens (ADR-0314 Decision 2), the S3 frontend with
    dev's fixed keypair, the live reload stream (ADR-0255 Decision 5) and a dev container worker's local API (ADR-0346
    Decision 6) stay on 127.0.0.1. A Site page opened from the LAN therefore does not reload by itself: its tag names
    `http://127.0.0.1:<port>/events`.
  - an authenticator for dev's gateway, or a LAN token;
  - a host name as the value, and HTTPS on the LAN: a page from a LAN address is not a secure context in a browser;
  - the daemon, whose `server.dataPlaneAddr` already sets the address.

## Constraints & Decision drivers

- The decider, 2026-11-27: Vite's model, off by default; given alone, every address; given an address, that one; no
  address hardcoded; only the gateway on the LAN, since dev has no edge authenticator, and the banner warns that the
  gateway is reachable from the LAN without authentication; a network line beside each localhost URL the banner
  prints today, the gateway, each Route and each Site; the tests of `dev-lan-every-address` and `dev-lan-off`. Amended
  the same day on #1601: each of those URLs once, on the default route's LAN address, and one note that names the
  others (amended 2026-11-27).
- funcd's words, grepped 2026-11-27: `host` is the host that a rule or an address answers on (`Route.spec.host`,
  `ingress.host`, `status.host`), and dev's banner prints such hosts (`http://login.localhost:<gport>/`); a listen
  address is an `…Addr` (`server.listenAddr`, `server.dataPlaneAddr`, `s3gateway.listenAddr`).
- pflag v1.1.0 (`parseLongArg`) reads an optional value only after `=`: with `NoOptDefVal` set, `--lan 192.168.1.20`
  leaves the address as dev's directory argument.
- `funcd.New` refuses the built-in tokens with a control plane off loopback, and with a data plane off loopback only
  when edge auth is on (`checkDevTokenBind`; ADR-0314 Decisions 5 and 6). Dev sets no `WithEdgeAuth`.
- ADR-0110 Decision 1 and ADR-0240 Decision 3: a rule with a host answers only on that exact host, and in `default` a
  rule with no host answers on any host that no namespace reserves.
- ADR-0254 Decisions 2, 3 and 6: a key per flag, in camelCase; a flag replaces its key; a changed key other than
  `build`, `values`, `apply` or `env` prints that a restart applies it.
- No new dependency: `net` and `net/netip`.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. `--lan[=<IP>]` and `dev.lan`; the gateway alone leaves loopback** | Vite's model; one listener; no other listener moves | not Vite's name | **Chosen** |
| B. Vite's names, `--host` and `dev.host` | what a Vite user types | in funcd a `host` is what a rule answers on, and the same banner prints Route hosts; an App's values often set one | Rejected: the word is taken |
| C. `--gport` takes `<address>:<port>` | one flag | `gport` is an integer key that files already set | Rejected |
| D. A forwarder in each App, as today | no funcd change | each App that wants a phone writes one | Rejected: the need |
| E. Every listener on the LAN | one switch | the control plane takes the built-in tokens, and the S3 frontend dev's fixed keypair | Rejected by the decider |
| F. Bind each LAN address found at start | the listeners match the banner | an address that comes later, after a Wi-Fi change or a VPN, gets none | Rejected: every address, as Vite |
| G. A host name as the value, as Vite takes | `--lan=mymac.local` | a name needs a rule for which of its addresses to bind and print | Rejected: an IP address |
| H. `--lan <IP>` with a space, as Vite's parser reads it | the decider's literal form | funcdctl's flags are pflag's; it would take a parser of its own before cobra | Rejected: `=`, and a refusal that names it |

## Decision

1. **The opt-in.** `--lan` and its key `dev.lan`, off by default, in both modes. `--lan` alone and `dev.lan: true`
   put the gateway on every address of the machine; `--lan=<IP address>` and `dev.lan: <IP address>`, IPv4 or IPv6,
   on that address only; `--lan=false` and `dev.lan: false` are the default, both read as `""`. Any other value is
   refused before anything starts, naming it. The flag is checked before dev reads `funcdctl.config.yaml`, so with or
   without one, and before its arguments are counted: when `--lan` is given with no value and the first argument
   reads as an IP address and is no directory, dev refuses to start and names `--lan=<that address>`, whether or not
   a directory follows. A flag replaces the key (ADR-0254 Decision 3), and a change of `dev.lan` while dev runs prints
   that a restart applies it (Decision 6). `--gport` keeps its meaning, so a fixed `--gport` gives a phone the same
   URL at each start.
2. **Only the gateway leaves loopback** (supersedes in part ADR-0125 Decision 1). The data plane listens on
   `gatewayListen(lan, gport)` through `funcd.WithDataPlaneAddr`: `127.0.0.1:<gport>` without the opt-in, as today,
   `:<gport>` on every address, and `net.JoinHostPort(<address>, <gport>)` on one, which brackets an IPv6 address.
   The control plane, the S3 frontend, the live reload stream and each dev container worker's local API keep their
   127.0.0.1 binds, and dev adds no authenticator. On the LAN the gateway serves what it serves on loopback, with no
   token: every Function's address, every Route and Site, an add-on object's Route, and a Route with a host too, since
   a client sets its own `Host` header. A Route with an `oidc` stance still asks for a sign-in (ADR-0335).
3. **The banner** (amended 2026-11-27). The gateway's URL is on 127.0.0.1 without the opt-in, and on the one address
   given. On every address, each URL on the gateway that the banner prints names, in place of 127.0.0.1 and once, the
   first LAN address found at start: the gateway's line, each Function's `POST` line in folder mode, and in App mode
   each Route's and Site's line that names no host. The LAN addresses are listed with the default route's first, the
   local address of a UDP socket connected to `192.0.2.1` (RFC 5737), which sends nothing, when it is one of them,
   then in the order `net.Interfaces` lists them. A note above the LAN warning reads `the same URLs also answer on
   127.0.0.1:<port>`, followed by each other LAN address with the port. With no LAN address found, the lines keep
   127.0.0.1 and no note is printed. A line with a host keeps its host, since that rule answers only there (ADR-0110
   Decision 1). While the gateway listens off
   loopback, as `platformconfig.IsLoopbackAddr` judges the bound address (the check `checkDevTokenBind` makes), the
   title reads `serving on localhost and the LAN` on every address and `serving on localhost and <address>` on one, in
   place of `serving on localhost`, and the banner warns `⚠ the gateway is reachable from the LAN without
   authentication: anyone on the network can call every Function, Route and Site`, above the egress warning. The
   control, s3 and reload lines keep 127.0.0.1.

## Design model

None: no lifecycle, timer or race. Dev chooses an address once at start and prints its lines once.

## Behavior rows

- **security.md**: a new row "The dev gateway on the LAN", `accepted` (Decisions 1 to 3, Risks accepted).
- **funcdctl-dev.md**: its opening's "on loopback (ADR-0254)" reads "on loopback unless `--lan` (ADR-0361)". The
  chapter is at its 16 KiB cap, so the same paragraph gives back the bytes.

## Temporary workarounds

None.

## Contracts

```go
// cmd/funcdctl/devlan.go (//go:build dev)

// devLANAll is --lan's value when given alone, and dev.lan: true: the gateway listens on every address.
const devLANAll = "true"

// checkLAN refuses a value of --lan or dev.lan (name says which) other than "", "false", devLANAll or an IP address.
func checkLAN(op, name, value string) error

// gatewayListen is the data plane's listen address for a checked lan value and gport (0 takes a free port), one
// address joined with net.JoinHostPort.
func gatewayListen(lan string, gport int) string

// gatewayURLs are the banner's base URLs for the address the data plane bound: local on 127.0.0.1 when it answers
// there, else on its own address; network, one per address that lan returns, only when bound is unspecified; and
// offLoopback, !platformconfig.IsLoopbackAddr(bound.String()).
func gatewayURLs(bound netip.AddrPort, lan func() ([]netip.Addr, error)) (local string, network []string, offLoopback bool, err error)

// lanAddrs are the IPv4 addresses of the machine's up interfaces, loopback ones left out: the default route's first, as
// routeAddr finds it, then net.Interfaces' order (amended 2026-11-27).
func lanAddrs() ([]netip.Addr, error)

// routeFirst is addrs with the address that route returns moved first when it is one of them, else addrs as they are
// (amended 2026-11-27).
func routeFirst(addrs []netip.Addr, route func() (netip.Addr, error)) []netip.Addr

// routeAddr is the address the machine sends from on its default route: the local address of a UDP socket connected to
// 192.0.2.1:9 (amended 2026-11-27).
func routeAddr() (netip.Addr, error)

// shownGatewayURL is the gateway's base URL on the banner's lines: the first LAN address's while the gateway listens on
// every address and one was found, else gatewayURL (amended 2026-11-27).
func (d *devInstance) shownGatewayURL() string

// lanNote is the banner's closing note while its lines name a LAN address: 127.0.0.1 and the other LAN addresses, on
// which the same URLs answer; "" otherwise (amended 2026-11-27).
func (d *devInstance) lanNote() string

// devLANKey is dev.lan: true, false or an IP address, decoded as --lan's text: devLANAll, "" or the address.
type devLANKey string

func (k *devLANKey) UnmarshalYAML(n *yamlv3.Node) error

// lanArgs is dev's cobra Args: with --lan's value devLANAll, a first argument that reads as an IP address and is no
// directory is refused, naming --lan=<it>; then cobra.MaximumNArgs(1).
func lanArgs(cmd *cobra.Command, args []string) error
```

`devConfig` gains `lan string`, checked (`""`, `devLANAll` or an IP address; `false` reads as `""`), and the seam
`findLAN func() ([]netip.Addr, error)` (nil is `lanAddrs`). `devFileKeys` gains the field `LAN devLANKey`, YAML key
`lan`, and `devInstance` gains `networkURLs []string` and `offLoopback bool`. The flag is a string flag whose
`NoOptDefVal` is `devLANAll`.

The key, in `funcdctl.config.yaml`:

```yaml
dev:
  gport: 8795
  lan: true
```

The banner's lines it changes, with two LAN addresses, the default route's first (amended 2026-11-27):

```text
● funcd dev   serving on localhost and the LAN · Ctrl-C to stop
gateway   http://192.168.1.20:8795    invoke functions
route     http://192.168.1.20:8795/api
route     http://login.localhost:8795/
the same URLs also answer on 127.0.0.1:8795 and 10.8.0.2:8795
⚠ the gateway is reachable from the LAN without authentication: anyone on the network can call every Function, Route and Site
```

| Consumes | Writes | Exposes |
|---|---|---|
| `--lan`, `dev.lan`, `--gport`; the interfaces' addresses at start | nothing | the data plane on the chosen addresses; the network lines and the warning |

## Implementation plan

- `cmd/funcdctl/devlan.go` (new): the Contracts' functions and type.
- `cmd/funcdctl/dev.go`: the flag and its usage; `Args: lanArgs`; `devConfig.lan`; `devPlatformOptions` passes
  `gatewayListen` to `funcd.WithDataPlaneAddr` when `lan` or `gport` is set; `startPlatform` fills `gatewayURL`,
  `networkURLs` and `offLoopback` from `gatewayURLs(p.DataPlaneAddr())`; `printBanner` prints the title, the lines on
  `shownGatewayURL`, the note and the warning (amended 2026-11-27); the `Long` text's key list gains `lan`.
- `cmd/funcdctl/devconfig.go`: `lan` in `devFileKeyNames`, `changedDevKeys` and the merge, the flag winning;
  `checkLAN` for the key, and for the flag in `resolveDevRun` before it reads the file, with `false` read as `""`.
- `cmd/funcdctl/dev_app.go`: `appEndpoints` names each host-less line on `shownGatewayURL` (amended 2026-11-27).
- Tests, in `cmd/funcdctl/dev_lan_test.go`, one per Scenario and named after it (`TestScenarioDevLANOff`, …):
  - `dev-lan-every-address` runs the http-api example of funcd-typescript, at its pinned version, read with
    `internal/testkit/langmod` and copied with `os.CopyFS`, and relays over `golang.org/x/net/websocket`, already a
    dependency;
  - the reachability scenarios dial a real LAN address of the host and skip, naming why, when the host has none;
  - `dev-lan-banner` injects two addresses through `findLAN`;
  - `dev-lan-refused` runs its flag cases with no `funcdctl.config.yaml`, `--lan 192.168.1.20 ./app` among them;
  - unit tests of `checkLAN`, `gatewayListen`, `gatewayURLs`, `lanArgs` and the key's decoding, with every value of
    Decision 1, an IPv6 address among them, and of `routeFirst` and `lanNote` (amended 2026-11-27).
- Docs: the funcdctl page of the guide (`--lan` beside `--containers`), the App page's "Run it locally" and the
  networking page's dev bullets; `scripts/agent/d just generate` for the Spec page; the behavior rows; the F278 row.
- **Done when** the scenario and unit tests pass, `just ci` is green and the gate passes.

## Review checklist

- [ ] Without the opt-in the data plane binds `127.0.0.1:<gport>` as before; `--lan` and `dev.lan: true` bind
      `:<gport>`; `--lan=<IP>` binds that address, an IPv6 one in brackets.
- [ ] The control plane, the S3 frontend, the live reload stream and the container workers' local API keep their
      127.0.0.1 binds; dev adds no authenticator.
- [ ] On every address, the gateway's line, each `POST` line and each host-less Route and Site line name the first
      LAN address once, the default route's first; a line with a host names its host; one note names 127.0.0.1 and
      the other LAN addresses; the title and the warning change only while the gateway listens off loopback, by
      `platformconfig.IsLoopbackAddr` (amended 2026-11-27).
- [ ] `checkLAN` refuses every other value, for the flag with or without a file and for the key; `lanArgs` refuses
      a bare `--lan` before a first argument that reads as an IP address and is no directory, naming
      `--lan=<address>`, a second argument or not; a changed `dev.lan` prints the restart line.
- [ ] One test per Scenario, named after it; the WebSocket relays through a LAN address.
- [ ] The Spec page, the guide and the behavior rows name the opt-in and what it exposes.

## Consequences

- **Positive**: a phone on the developer's network opens a dev App, WebSocket included, with no forwarder; nothing
  changes without the opt-in; the listener on every address follows the machine's addresses as they change.
- **Negative**: the LAN URLs and the note name the addresses found at start, so they can go stale after a network
  change; a Route or Site with a host keeps its host on its line (amended 2026-11-27); a page opened from the LAN does
  not reload by itself; a host firewall may ask
  whether funcdctl may take incoming connections; a phone does not sign in through rauthy, which takes http callbacks
  on loopback hosts only (ADR-0336 Consequences).
- **Risks accepted**: with the opt-in, anyone on the network reaches every Function, Route and Site of the dev session
  with no authentication, a Route with a host too; on a shared or public network that is everyone on it. The banner's
  warning is the only guard.

## Open questions

None.

## References

- #1597, the feature issue (tracker #1028); #1601, the amendment of Decision 3
- Vite's `server.host` (`true` listens on all addresses, `--host` alone sets it) and `resolveServerUrls`, read
  2026-11-27: https://vite.dev/config/server-options#server-host,
  https://github.com/vitejs/vite/blob/main/packages/vite/src/node/utils.ts
- spf13/pflag v1.1.0, `NoOptDefVal` and `parseLongArg`: https://github.com/spf13/pflag
