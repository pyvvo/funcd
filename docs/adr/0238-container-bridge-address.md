# ADR-0238: The container bridge address

- **Status**: Implemented (2026-10-12, review passed). Reviewing (2026-10-12, implementation done). Accepted (2026-10-12, self-accepted under the decider's batch
  delegation; judged twice: the first round kept the proxies node-private, superseded ADR-0085's S3 endpoint default in
  part, moved the lane proof to the in-repo duckdb config and pinned the alias rules; the second found no Blocker or
  Major)
- **Amended before merge** (2026-10-12, at the decider's request): `catalog.proxyHost` is removed instead of kept as an
  alias; the configs pinned from funcd-functions and funcd-python drop it in releases this change pins.
- **Date**: 2026-10-12
- **Deciders**: green-0-rabbit
- **Tags**: network, containerd, config, sql, catalog, s3
- **Realizes**: [FEAT-0011/F136](../feat/0011-feat-sql.md) (the bridge address engines and workers call back on)
- **Supersedes (in part)**, with a `Superseded in part by: ADR-0238` back-link at acceptance:
  [ADR-0085](0085-s3-in-platform-identity-funcd-keypair.md), the default of `AWS_ENDPOINT_URL_S3` only (its contract
  table's `http://<s3gateway.listenAddr>`); every other clause stands.
- **Relates to**: [ADR-0235](0235-sql-change-feed.md) Decision 2 (the change feed binds "on the bridge gateway address
  under containerd"; this ADR decides how that address is found) · [ADR-0137](0137-per-caller-catalog-query-rbac.md)
  (the node-private catalog query proxies) · [ADR-0056](0056-temporary-runtime-self-provisioning.md) (the conflist and
  its bridge `funcd0`) · [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (configs in the pinned repos)

## Context & Need

Under containerd each worker and engine runs in its own network namespace, where `127.0.0.1` is the container itself.
Three daemon listeners are called back from containers:

| Listener | Address source today | Empty means |
|---|---|---|
| SQL change feed (ADR-0235) | `catalog.proxyHost` | `127.0.0.1`: engines never reach it; one warning at start |
| Catalog query proxies (ADR-0137) | `catalog.proxyHost` | `127.0.0.1`: workers cannot query a catalog |
| S3 gateway endpoint (ADR-0085) | `s3gateway.endpoint` | `http://<s3gateway.listenAddr>`, loopback by default |

The value is one fact, funcd's address on its own bridge: `internal/runtime/containerd/cni_conf.go` writes bridge
`funcd0` with `isGateway` and host-local IPAM over `runtime.containerd.subnetCIDR` (default `10.63.0.0/16`), so the
bridge holds the subnet's network address + 1 (host-local's default gateway). Today the operator must copy it into a
field named after the catalog, and the SQL feed's dependency on a `catalog.` field looks like a coupling that does not
exist.

## Scenarios

- `scenario: bridge-address-from-subnet` — Given containerd with the default subnet, egress on and no address
  configured, When funcd starts, Then the change feed and the catalog proxies bind and publish `10.63.0.1`, and the
  egress allow list holds `10.63.0.1:<sql.changeFeedPort>`.
- `scenario: bridge-address-follows-subnet` — Given `subnetCIDR: 10.70.0.0/16`, Then the address is `10.70.0.1`.
- `scenario: bridge-address-explicit` — Given `runtime.containerd.bridgeAddress: 10.70.0.9` in that subnet, Then that
  address is used.
- `scenario: bridge-address-outside-subnet-refused` — Given a `bridgeAddress` outside `subnetCIDR`, Then funcd refuses
  to start, naming the field.
- `scenario: proxy-host-refused` — Given a config that still sets `catalog.proxyHost`, Then funcd refuses to start,
  naming it (the config is parsed strictly).
- `scenario: process-runtime-loopback` — Given the process runtime (`funcdctl dev`), Then the address is `127.0.0.1`.
- `scenario: s3-endpoint-from-bridge` — Given containerd, `s3gateway.listenAddr: 0.0.0.0:9000` and no endpoint, Then a
  worker's `AWS_ENDPOINT_URL_S3` is `http://10.63.0.1:9000`.

## Scope

**In**: the field, its default, the removal of `catalog.proxyHost`, and the three readers. **Out**: changing
`s3gateway.listenAddr`'s default
(a loopback S3 gateway stays unreachable from containers, as today); custom CNI setups beyond setting the field.

## Constraints & Decision drivers

- The default must be right for every standard install, with nothing to configure.
- Existing configs keep working.
- One fact, one field.

## Alternatives considered

1. **Keep `catalog.proxyHost` and only default it from the subnet**: least change, but the misleading name stays and the
   S3 endpoint stays separate. Rejected.
2. **A new top-level section**: visible, but apart from the subnet it is derived from and meaningless without
   containerd. Rejected.
3. **Read the address from the bridge interface at start**: exact even for a hand-edited CNI config, but needs the
   bridge to exist before the listeners bind and adds netlink code. Rejected; the explicit field covers custom setups.

## Decision

1. **The field.** `runtime.containerd.bridgeAddress` (env `FUNCD_BRIDGE_ADDRESS`) is funcd's IP on its container
   bridge, and must lie in `runtime.containerd.subnetCIDR`. Empty ⇒ the subnet's network address + 1. The process
   runtime does not read it and uses `127.0.0.1`.
2. **No alias.** `catalog.proxyHost` and `WithCatalogProxyHost` are removed; a config that sets the key is refused at
   start, as any unknown key is.
3. **The readers.** The catalog proxies and the change feed bind the bridge address itself, retried until the bridge
   exists, and publish it: the proxies stay node-private (they bound `0.0.0.0` when `catalog.proxyHost` was set). The
   egress allow list holds `<bridge address>:<sql.changeFeedPort>`, as today; the S3 and catalog ports stay
   operator-listed. `cmd/funcd` computes the S3 endpoint once and passes it to `WithS3Gateway`: an empty
   `s3gateway.endpoint` becomes `http://<bridge address>:<port>` when `s3gateway.listenAddr`'s host is empty,
   unspecified (`0.0.0.0`, `::`) or the bridge address, else `http://<listenAddr>` as before.
4. **The library.** `pkg/funcd` gains `WithBridgeAddress`. Through `cmd/funcd` the address is never empty under
   containerd; `pkg/funcd` keeps the loopback warning for an embedder that runs containerd without it.

## Temporary workarounds

None.

## Contracts

```yaml
apiVersion: funcd.io/v1alpha1
kind: FuncdConfig
runtime:
  mode: containerd
  containerd:
    subnetCIDR: 10.63.0.0/16
    bridgeAddress: 10.63.0.1
```

```go
// internal/platform/config: the effective bridge address: under containerd runtime.containerd.bridgeAddress or the
// subnet's network address + 1, else 127.0.0.1. The error names the field.
func (c *Config) BridgeAddress() (netip.Addr, error)

// pkg/funcd
func WithBridgeAddress(addr netip.Addr) Option
```

| Consumes | Exposes |
|---|---|
| `runtime.containerd.subnetCIDR`, `runtime.containerd.bridgeAddress` | the address the catalog proxies, the change feed, the allow list and the S3 endpoint use |

## Implementation plan

1. `internal/platform/config`: the field, `BridgeAddress()` and the subnet rule; `catalog.proxyHost` removed.
2. `cmd/funcd/main.go`: `BridgeAddress()` wherever `cfg.Catalog.ProxyHost` is read today, and the S3 endpoint computed
   once and passes it to `WithS3Gateway`; the library's fallbacks in `internal/function/function.go` and
   `pkg/funcd/funcd.go` stay for embedders; `pkg/funcd`: `WithBridgeAddress`, the proxies bound on the address, and one
   start-up log line naming the effective bridge address.
3. Configs in this repo: `e2e/fixtures/duckdb-funcdconfig.yaml` drops `catalog.proxyHost` and `s3gateway.endpoint`, so
   the duckdb lane proves the defaults; `examples/funcdconfig.yaml` documents the field; the `sql` lane's step that
   greps `proxyHost` reads the bridge address log line instead. funcd-functions and funcd-python drop the key from the
   configs this repo pins, in releases this change pins (ADR-0141).
4. Tests: one `TestScenario<Name>` per scenario, in `internal/platform/config` and `cmd/funcd`; they check the
   addresses passed to `pkg/funcd`, and the duckdb lane proves the real bind on the bridge.

**Definition of done**: the tests pass; the duckdb, `sql` and `sql-examples` lanes pass; the gate passes; no username or
absolute path in a changed file.

## Review checklist

- [ ] No reader takes the address from anywhere but `BridgeAddress()`.
- [ ] No code or pinned config reads or sets `catalog.proxyHost`.
- [ ] The process runtime still uses loopback.
- [ ] One test per scenario, named for it.

## Consequences

- A standard containerd install needs no address setting; the change feed and the catalog work out of the box.
- funcd never rewrites an existing conflist, so after a `subnetCIDR` change or with an operator's own conflist the
  derived address can be stale: such installs set `bridgeAddress`.
- A config that still sets `catalog.proxyHost` no longer starts; the fix is to delete the line.

## Open questions

None.

## References

- `internal/runtime/containerd/cni_conf.go` (the bridge and its IPAM), `pkg/funcd/funcd.go` (the feed's host),
  `cmd/funcd/main.go` (the allow list and the readers).
