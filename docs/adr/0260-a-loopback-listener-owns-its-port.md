# ADR-0260: A loopback listener owns its port

- **Status**: Implemented (2026-10-22), review passed on its first round with two Minors; the hanging test assertion
  was fixed before the stamp, and the two drivers' small line-buffering repeat stays
  (docs/reviews/adr-0260-implementation-claude-opus-5-5.md); Reviewing (2026-10-22), implemented for #1154. Accepted (2026-10-22). The decider accepted this text ("accept ADR-0260"), confirming the drafting
  agent's three choices: the catalog engine's held port, the SQL engine started again only on `address already in
  use`, and port 0 for containerd without a bridge address. Judged three times: round 1's two Majors and five Minors
  and round 2's Major (settled by a probe on linux and darwin) and three Minors are folded in; round 3's verdict:
  accept.
- **Superseded in part by**: [ADR-0262](0262-the-lake-engine-runs-duckdb-2.md) (2026-10-23) — the Lake half of Decision 3: the dev Lake
  engine serves on port 0 and reports the port its `listen_url` row names, with no port held first.
- **Date**: 2026-10-22
- **Deciders**: green-0-rabbit
- **Tags**: sql, catalog, dev, testing, network
- **Realizes**: [FEAT-0011/F132](../feat/0011-feat-sql.md) (SQL change feed) (#1154)
- **Supersedes (in part)**, with a `Superseded in part by: ADR-0260` back-link at acceptance (Implemented; origin/main
  lines): [ADR-0235](0235-sql-change-feed.md) Decision 2 (80-82), "port `sql.changeFeedPort` (default 7075, fixed),
  bound … on loopback in process mode". On loopback, an unset port becomes the port that a bind of `127.0.0.1:0` gets.
- **Relates to**: [ADR-0237](0237-sql-in-funcdctl-dev.md) (the SQL dev engine, F133) ·
  [ADR-0130](0130-funcdctl-dev-catalog-consumer-and-query-lane.md) (the catalog dev engine) ·
  [ADR-0238](0238-container-bridge-address.md) (the bridge address) · #758, #1155 (test ports) · #627 (dev's S3 port,
  picked again when taken) · #1152 (the flake sweep)

## Context & Need

The daemon serves the SQL engines' change feed on `sql.changeFeedPort`, 7075 by default (ADR-0235 Decision 2). With a
bridge address the port must be fixed: the feed binds that address, and `cmd/funcd` names the port in the network's
internal allow list (`cmd/funcd/main.go:1039`). On loopback the fixed port only collides. Every test platform binds
127.0.0.1:7075, one wins and the others retry each second (`internal/services/sql/changefeed.go:118-134`), and an
engine's changes reach whichever platform holds the port, which refuses a token it does not know (#1154, a merge-queue
failure in run 38827305191). funcdctl dev avoids 7075 by reserving a free port and releasing it before the feed binds
it (`cmd/funcdctl/dev.go:1063-1071`), the gap that #758 and #1155 removed from the tests.

The dev engines take the address to bind as an argument, so a free port is reserved and released for them too
(`internal/sql/devengine/devengine.go:305-311`, `internal/catalog/devengine/devengine.go:262`). Probed on 2026-10-22
with the pinned DuckDB 1.5.6 on darwin/arm64 and on linux/arm64 as a non-root user (the `funcd-linux-tests` VM), and
from rqlite v10.5.3's source (`images/runtime/rqlite/rqlite.pin`):

- rqlited binds with a plain `net.Listen` and, on a taken port, ends with `failed to listen on …` (Raft) or `failed to
  start HTTP server: …` (`cmd/rqlited/main.go:96`, `:208`), Go's `bind: address already in use`; once bound it logs
  `service listening on <addr>` (`http/service.go:356`, `:415`).
- DuckDB's Quack server binds with SO_REUSEPORT. A port a Go `net.Listen` holds fails `quack_serve` (`IO Error:
  Failed to bind DuckDB Quack RPC server …`). Beside a listener of the same user that set SO_REUSEPORT it binds, while
  a Go `net.Listen` of that port fails; a port another Quack server holds is shared without an error. `quack_serve`
  refuses port 0 (`Invalid Port`). Any init error ends the CLI, its stdin held open or not (`Encountered errors while
  executing init file … Exiting.`, exit 1), before a row is printed; on success the row `http://127.0.0.1:<port>`
  reaches a redirected stdout within half a second.

The catalog engine's start counts any process that accepts a connection on the port as its engine (`waitReady`,
:391-411); the SQL engine's counts any server that answers `/readyz` (:372-396). On 2026-10-22 the decider chose that
the feed binds its own port on loopback, and that this ADR covers the dev engines too.

## Scenarios

- **scenario: platforms-own-feeds** — *Given* two platforms in one process, on loopback, with `sql.changeFeedPort`
  unset, *Then* each feed listens on its own port from the start, neither logs "cannot listen yet", and each
  platform's engines are given their own feed's URL.
- **scenario: set-port-kept** — *Given* `sql.changeFeedPort: 7090` on loopback, *Then* the feed listens on
  127.0.0.1:7090 and the engines' URL names 7090.
- **scenario: bridge-keeps-7075** — *Given* a bridge address and `sql.changeFeedPort` unset, *Then* the engines' URL
  names the bridge address and 7075, as today.
- **scenario: catalog-engine-holds-its-port** — *Given* `funcdctl dev` starting a catalog engine, *Then* a Go
  `net.Listen` of the engine's port fails from the moment the port is chosen, and the catalog is Ready only once its
  own engine printed its address.
- **scenario: sql-engine-port-taken** — *Given* `funcdctl dev`, *When* another process takes a port chosen for an SQL
  engine before the engine binds it, *Then* the engine is started again with new ports and the database is Ready at
  an address its own engine serves; *When* the engine ends for another reason, *Then* the start fails at once with the
  engine's message.

## Scope

- **In**: the change feed's listener on loopback; funcdctl dev's feed port; when a dev engine's start counts it ready.
- **Out**: the feed with a bridge address (unchanged); the daemon's other listeners, whose addresses come from config
  or flags; dev's S3 frontend port (#627); test helpers (`internal/testkit/freeport`, #1155).

## Constraints & Decision drivers

- A listener that funcd opens binds its port once and keeps it: no reserve-then-release gap.
- With a bridge address the allow list names the port, so it stays fixed there.
- rqlited and DuckDB take the address to bind as an argument; only the Quack server binds beside a held socket.
- A retry must not turn a real engine failure into a port problem.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| Chosen: the loopback feed binds `127.0.0.1:0`; the catalog engine's port stays held until it serves; an SQL engine that lost its port starts again | No shared feed port; no catalog gap; a lost SQL port heals within the start | The loopback feed port changes at each start | Chosen by the decider |
| Each test sets `WithSQLChangeFeedPort(freeport.Port(t))` | No product change | Every platform in every test needs it, and a forgotten one collides again; dev keeps its gap | Rejected |
| Reserve a free port, release it, bind it (dev today) | Small change | Keeps the gap | Rejected (#758, #1155) |
| The engines bind port 0 and report the port they got | No gap | `quack_serve` refuses port 0; rqlited's ports are on its command line | Rejected (probed) |
| Start any engine again after any early exit | Simple | Reports a real failure (ATTACH, auth, a held data lock) as a port problem after three starts | Rejected |

## Decision

1. **The feed on loopback.** With no bridge address and `sql.changeFeedPort` unset, the platform binds `127.0.0.1:0`
   when it is built (`funcd.New`), keeps that listener, and gives the engines the change feed URL with the port it got;
   `Run` serves it. A set `sql.changeFeedPort` is bound as today. With a bridge address nothing changes: 7075 by
   default, bound once the bridge exists. Containerd without a bridge address, whose engines cannot reach a loopback
   feed either way (the warning at `pkg/funcd/funcd.go:1168` stays), binds port 0 like process mode.
2. **funcdctl dev** passes no feed port: its platform runs on loopback, so Decision 1 gives it one.
3. **The dev engines.**
   - **The catalog engine:** the start binds `127.0.0.1:0` itself, with SO_REUSEPORT, and gives the engine that port,
     so the port is held from the moment it is chosen; the Quack server binds beside the held listener, and a plain
     bind cannot take the port. The start closes the held listener once the engine's standard output names its
     address, the row of its init's `quack_serve` call, and before it reports Ready; nothing dials the port before
     then. A connection accepted on the port no longer counts. An engine that ends before its row fails the start at
     once with its error output. Every failed start (an early end, a timeout) closes the held listener too.
   - **The SQL engine:** rqlited cannot bind beside a held socket, so it keeps a free port for each address, released
     before it starts, as today. The start counts it ready only once its own output said `service listening on <its
     HTTP address>` and `/readyz` answers 200. An engine that ends after reporting `address already in use` is started
     again at once, each start a whole launch with new ports after the failed one is stopped, three starts in all, as
     dev's S3 port (`cmd/funcdctl/dev.go:937`), each retry logged at Warn. Any other end fails the start at once with
     the engine's output.
   - A start that times out fails as today.

## Temporary workarounds

None.

## Contracts

- `sql.changeFeedPort` (`internal/platform/config/config.go:396-398`): unset ⇒ 7075 with a bridge address, else the
  port of a loopback bind of port 0 at start. No new key or option; `WithSQLChangeFeedPort` still pins a port.
- `internal/services/sql` (new): `func (f *Feed) Listen(ctx context.Context, host string, port int) (net.Listener,
  error)` binds once, port 0 allowed; `func (f *Feed) ServeListener(ctx context.Context, ln net.Listener) error` serves
  until ctx ends and treats `net.ErrClosed` as a clean stop. `Serve` keeps its retry for the bridge and a set port,
  built on the two.
- `pkg/funcd`: on loopback with the port unset, `New` holds the listener and the engines' URL takes its port, in place
  of `sqlChangeFeedPortOrDefault` (`funcd.go:407-408`, used at `:1177`, `:1677`); `Run` serves it; `Shutdown` closes it
  unconditionally, as it closes `p.listener` (`:1826-1831`).
- `cmd/funcdctl/dev.go`: the feed's `freeLocalAddr` reservation and its `WithSQLChangeFeedPort` call are removed.
- `internal/catalog/devengine`: the port picker returns the held listener, a `net.ListenConfig` whose `Control` sets
  SO_REUSEPORT (`golang.org/x/sys/unix`), not a port number; the start reads the engine's stdout (discarded today) for
  `http://<addr>`.
- `internal/sql/devengine`: `lineLogger` also reports the engine's `service listening on <addr>` and `address already
  in use` lines to the start.
- Both drivers take their port picker as a test seam, as dev's `pickS3Addr` does (`cmd/funcdctl/dev.go:156`).

## Implementation plan

- **funcd**, one PR that closes #1154 (`Fixes #1154`):
  - `internal/services/sql/changefeed.go`: `Listen` and `ServeListener`; `Serve` built on them.
  - `pkg/funcd/funcd.go`: the loopback bind in `New`, the URL from the bound port, the serve in `Run`, the close in
    `Shutdown`; the comments that say "0 ⇒ 7075" (`funcd.go:306`, `options.go:64`, `config.go:396-397`) and
    `examples/funcdconfig.yaml:247` give the default per case.
  - `cmd/funcdctl/dev.go`: drop the feed reservation.
  - The two dev engine drivers, as the Contracts say.
  - Tests: one per scenario, named after it, and `TestIssue1154_PlatformsOnLoopbackDoNotShareTheFeedPort`. The engine
    tests use fake engines that behave as probed: a fake rqlited that logs `service listening on` and serves
    `/readyz`, or ends with `address already in use`; a fake DuckDB that prints the row, or prints an init error and
    exits 1. `just ci` runs them with `-tags dev` (`justfile:44`), so the hold test runs on darwin and on linux.
  - The real engines hold the two output lines across an upgrade: rqlited's through the real-engine tests of
    `cmd/funcdctl/dev_sql_test.go` (skipped without a built engine, `:29-35`), the catalog engine's row through a new
    `-tags dev` test that runs the bundled engine and skips on the placeholder the same way. The PR description
    records rqlited's run on a taken port.
- At acceptance: the ADR-0235 back-link, the F132 row, and in `docs/PROJECT-SUMMARY.md` the new row plus a
  superseded-in-part note on ADR-0235's row.
- **Done when** `go build`, `go vet`, `golangci-lint` and `go test` pass, with and without `-tags dev`, and
  `scripts/agent/gate.sh` passes.

## Review checklist

- [ ] On loopback with the port unset, `New` binds `127.0.0.1:0` once and the engines' URL names the bound port; a
      set port is bound as set; with a bridge address the default stays 7075.
- [ ] No reserve-then-release remains for the feed, in `pkg/funcd` or `cmd/funcdctl`.
- [ ] The catalog engine's port is bound by the start itself and held until its `quack_serve` row; a TCP accept
      alone is not ready.
- [ ] An SQL engine is ready only after its own `service listening on` line; only `address already in use` starts it
      again, three starts in all; any other end fails at once with the engine's output.
- [ ] One test per scenario, named after it, and `TestIssue1154_PlatformsOnLoopbackDoNotShareTheFeedPort`.

## Consequences

- **Positive**: test platforms, dev runs and process-mode daemons no longer share the feed's port, and its retry
  warnings leave the logs; a catalog engine's port cannot be taken before it serves, and another server cannot pass
  for a dev engine; a catalog engine that ends is reported at once instead of after its 20 s wait.
- **Negative**: on loopback the feed's port changes at each start; only the platform's own engines call it. The dev
  drivers depend on two lines of the engines' output; the real-engine tests check them at each engine upgrade.
- **Risks accepted**: a process that binds a held catalog port on purpose with SO_REUSEPORT shares it.

## Open questions

None.

## References

- #1154; #1152; #758, #1155; #627.
- `internal/services/sql/changefeed.go:110-140`; `pkg/funcd/funcd.go:306`, `:407-408`, `:1160-1177`, `:1669-1680`,
  `:1826-1831`; `pkg/funcd/options.go:64`; `cmd/funcd/main.go:1039`; `cmd/funcdctl/dev.go:156`, `:937`, `:962-970`,
  `:1063-1071`, `:1248-1260`; `internal/sql/devengine/devengine.go:260-284`, `:305-311`, `:372-396`;
  `internal/catalog/devengine/devengine.go:247-319`, `:357`, `:391-421`.
- rqlite v10.5.3: `cmd/rqlited/main.go:96`, `:208`; `http/service.go:356`, `:415`.
