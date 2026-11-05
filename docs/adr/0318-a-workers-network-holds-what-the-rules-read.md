# ADR-0318: A worker's network holds what the rules read (amends ADR-0115)

- **Status**: Accepted (2026-11-05): the decider delegated acceptance to the session driving this work, on the conditions that no Blocker or Major stays open, the decision stands and Open questions reads None; judged once, its one Major folded
- **Date**: 2026-11-05
- **Deciders**: green-0-rabbit
- **Tags**: egress, network, security, containerd
- **Realizes**: [FEAT-0007/F232](../feat/0007-feat-egress-control.md) (a worker's network holds what the rules read)
- **Supersedes (in part)**: [ADR-0115](0115-worker-network-isolation.md) — Decision A's rule, the sentence that says which frames `funcd_lateral` drops

## Context & Need

ADR-0115 promises default-deny from a worker to the host and no direct traffic between workers, FEAT-0007's exit criterion asks the same, and the rules read IPv4 unicast addresses. **Purpose.** The promise holds for every IP address form: a worker holds what the rules read, the rules stop what reaches a sibling through a group address, and a rule that reads an IPv4 address reads only IPv4. Callers: the containerd driver's `Create`, `sandboxOpts` and `Manager.Apply`.

## Scenarios

- `scenario: worker-eth0-has-no-ipv6`: Given egress on, When a worker runs, Then its `eth0` shows no `inet6` address, `/proc/sys/net/ipv6/conf/eth0/disable_ipv6` reads 1, its own write to that file fails, and a plain connect to the node's link-local address fails. Its loopback keeps `::1`, so an application binds `::` or `::1`.
- `scenario: ipv6-setting-failure-stops-the-worker`: Given egress on, When the setting cannot be written, Then `Create` fails as a CNI failure does and leaves nothing behind. With egress off nothing is written. On a kernel with no IPv6 there is nothing to write, and the worker starts.
- `scenario: no-worker-holds-net-raw`: Given egress on or off, When a Function's worker or an engine runs, Then `CapEff` and `CapBnd` in its `/proc/<pid>/status` lack bit 13, and the rest of containerd's default list stands. A Node worker, a Python worker and an engine start and serve.
- `scenario: worker-traffic-reaches-no-sibling`: Given egress on, When a worker sends plain UDP to a sibling's address, `255.255.255.255` or `239.1.1.1`, Then no sibling's listener receives it. A worker's ARP passes, and the node still reaches a worker.
- `scenario: other-traffic-stays-untouched`: Given egress on, When another host connects to a service on the node over IPv6, from an address with the worker subnet's bytes where IPv4 holds a source, such as `0a3f` in its third group under `10.63.0.0/16`, Then it reaches that service. With `br_netfilter` not loaded, on another bridge of the node, IPv6 between two hosts with `0a3f` in their third and fifth groups, where IPv4 holds a source and a destination, passes, and so does IPv4 broadcast between two hosts outside the subnet.

## Scope

Unchanged (ADR-0115), and **In**: beside the two host nftables objects, Decision D's step in `Create`, and Decision E's `sandboxOpts`, which holds with egress on or off. **Out**: the user a worker runs as; any other capability; IPv6 worker egress, which a later ADR would decide.

## Constraints & Decision drivers

Unchanged (ADR-0115), and:

- The host rules stay the substrate. ADR-0115's Alternatives reject per-worker netns nftables for its window between CNI-up and rule-apply and its churn. Decision D writes no rule, and it runs before the task starts, in ADR-0269 Decision 3's order, so it has no such window.
- Removing a default capability conforms to ADR-0011 ("no added caps"), and ADR-0267's Scope leaves capabilities out, so neither changes. What a worker sends over IP is what plain sockets send, which is what ADR-0117's source identity and ADR-0269 Decision 1 read.
- In the bridge family `meta nfproto` reads `NFPROTO_BRIDGE`, so an IPv4 match there reads the ethertype.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| `disable_ipv6` on `eth0` and `default` in the worker's netns | one write before the task starts; the rules stay IPv4; `::` and `::1` still bind | a step per worker beside the host rules | chosen by the decider |
| `disable_ipv6` on `all` | one write, every interface | takes `::1` from loopback, where an application may bind; Node's `listen(port)` binds `::` when it can | rejected by the decider |
| IPv6 rules in `funcd_lateral` and the input chain | no step per worker; the shape of ADR-0115's rules | a link-local address names no subnet, so each rule names a bridge; the gateway governs IPv4 only (ADR-0117), so an IPv6 path has no policy behind it | rejected |
| `linux.sysctl` for `default` in the OCI spec | the runtime applies it at task create | `eth0` gets it only by inheritance when CNI makes it, and nothing reads the result; a kernel with no IPv6 fails every task | rejected |
| `tuning` CNI plugin | CNI's own mechanism | edits the conflist (ADR-0056 never-clobber) and adds a plugin to provision | rejected |
| Drop `CAP_NET_RAW` with egress on only | a worker with egress off keeps `ping` | two sandboxes for one runtime; no shim, example or lane opens a raw socket | rejected by the decider |
| Drop every capability, add back what a worker uses | the smallest list | a study per runtime and engine of what each uses; a decision of its own | out of scope |
| Match group traffic by destination MAC | one rule for broadcast and multicast | ARP shares the broadcast MAC, so an ethertype match is needed anyway; the IPv4 field names both | rejected |

## Decision

**A. Lateral deny — table `funcd_lateral` (family `bridge`), its rule replaced.** A chain hooked at bridge `forward` **drops** an IPv4 frame (`meta protocol ip`) whose source is in the worker subnet and whose destination is in the worker subnet, is the limited broadcast `255.255.255.255`, or is multicast `224.0.0.0/4`. A frame of another ethertype, ARP among them, and an IPv4 frame from another source pass, on any bridge of the node. The rest of Decision A stands.

**C. An IPv4 match first.** Every rule of `funcd_egress` that reads an IPv4 address matches `meta nfproto ipv4` first, so an IPv6 packet stays out of the prerouting redirects and skips and the forward and input accepts that read one. ADR-0115 Decision B's rules, order and policies stand.

**D. A worker's `eth0` has no IPv6.** With `server.network.egress` on, the containerd driver writes 1 to `net/ipv6/conf/eth0/disable_ipv6` and `net/ipv6/conf/default/disable_ipv6` under `/proc/sys`, in the worker's netns, after the CNI setup and before the task starts, for every worker. It leaves `all` and `lo` alone, so loopback keeps IPv6. A failed write fails `Create`, as a CNI failure does. A worker cannot change the setting, as containerd's default spec mounts its `/proc/sys` read-only, which `TestProcSysStaysReadOnly` pins. A kernel that has no `/proc/sys/net/ipv6`, also after a plain IPv6 socket, which loads IPv6 where it is a module, has no IPv6 to switch off, so nothing is written and the worker starts. With egress off the driver writes nothing.

**E. No worker holds `CAP_NET_RAW`.** `sandboxOpts` removes it for every worker, egress on or off. The rest of containerd's default list stands.

## Design model

None: one write before the task starts, in an order ADR-0269 fixes, and a table of rules read on one packet, with no actors, timers or retries. `docs/design/platform/start-stop.fizz` models only that the host rules go in before any worker runs, which stands.

## Behavior rows

Chapter Security. **Changed**: the note's sentence on what workers reach with egress on. **New**: `A worker's eth0, egress on`; `A worker's capabilities`; `A worker's limited broadcast or multicast, egress on`; `IPv6 and other bridges' traffic on the node, egress on`; `What the node routes, egress on`, `accepted`, from ADR-0115 Decision B.

## Temporary workarounds

None.

## Contracts

```go
// internal/runtime/containerd: Config gains
Egress bool // server.network.egress: each worker's eth0 gets no IPv6 before its task starts (ADR-0318)

// internal/network
// DisableWorkerIPv6 applies Decision D in the netns at netnsPath.
func DisableWorkerIPv6(netnsPath string) error
```

`internal/network`'s rule plan gains `kindLateralBroadcastDrop` and `kindLateralMulticastDrop`. No new config key.

| Consumes | Exposes |
|---|---|
| `server.network.egress`, the worker's netns path after CNI setup | `disable_ipv6` of `eth0` and `default` in that netns |

## Implementation plan

1. `internal/network/nftables_linux.go`: the two kinds, an IPv4 match in each address rule. `internal/network/fence_linux.go`, `fence_other.go`: `DisableWorkerIPv6`.
2. `internal/runtime/containerd/containerd_linux.go`: `Config.Egress`, also in `containerd_other.go`, the call before `FenceNetns`, and `oci.WithDroppedCapabilities` in `sandboxOpts`. `cmd/funcd/main.go`: passes the key.
3. `e2e/egress.venom.yml`: the three cases, with a second Function applied in the VM as the sibling; each process that stands for the worker's own write runs in the task (`ctr task exec` with `node`, as the image has no shell), as `nsenter -n` keeps the node's `/proc`. `e2e/sql.venom.yml`: one case.
4. Docs: `docs/behavior/security.md`; in `blueprint.md` the capability list under *Security and Isolation*, tier 2's sentence on what user code can open under *Network manager*, and the lateral sentence under *Egress control*; `docs/PROJECT-SUMMARY.md`; `examples/funcdconfig.yaml`.

**Test plan**:

| Scenario | Test |
|---|---|
| `worker-eth0-has-no-ipv6` | `TestScenarioWorkerEth0HasNoIPv6` (the writes, on a temporary tree); `TestProcSysStaysReadOnly`; egress lane `worker-eth0-has-no-ipv6`; the netns run |
| `ipv6-setting-failure-stops-the-worker` | `TestScenarioIPv6SettingFailureStopsTheWorker`; the first test's cases for a missing file and a kernel with no IPv6 |
| `no-worker-holds-net-raw` | `TestScenarioNoWorkerHoldsNetRaw`; egress lane `worker-lacks-cap-net-raw`; a case of the sql lane, egress off, for a Python worker and an engine, whose user is not root, so `CapBnd` is what shows; every lane, for start and serve; one sql lane run with egress on |
| `worker-traffic-reaches-no-sibling` | `TestScenarioWorkerTrafficReachesNoSibling`; egress lane `worker-traffic-reaches-no-sibling`; the netns run |
| `other-traffic-stays-untouched` | `TestScenarioOtherTrafficStaysUntouched`; `TestEveryAddressRuleMatchesIPv4First`; the netns run |

The netns run is the real rule set (`network.New(true, …).Apply`, `DisableWorkerIPv6`, `FenceNetns`) in `ip netns` namespaces in `funcd-linux-tests`, over plain TCP and UDP sockets, as the egress lane's steps use, and it records whether `br_netfilter` is loaded. Linux tests run unprivileged, so a kernel's effect is the run's and the lane's.

**Done**: the tests pass; each new egress case fails with its part reverted; the netns run and the sql lane run with egress on pass, each recorded in the review report with what ran; `scripts/agent/gate.sh` and `scripts/agent/lanes.sh <branch>:all`, which ends with the metastore lane, pass; F232 → `reviewing`.

## Review checklist

- [ ] `planRules` holds Decision A's three lateral drops, each with `meta protocol ip` first; ARP, IPv6 and an IPv4 frame from another source pass, and a mutant of each match fails a test.
- [ ] Every planned rule that reads a network-header field matches IPv4 first.
- [ ] `DisableWorkerIPv6` writes `eth0` and `default` only, on a thread no other goroutine ever runs on; a missing file under a present tree is an error; `Create` runs it after the CNI setup and before `FenceNetns`, and a failure takes the existing cleanup. `/proc/sys` stays in a worker's `ReadonlyPaths`.
- [ ] `sandboxOpts` removes `CAP_NET_RAW` for every kind, ahead of `withSeccompProfile`, and the rest of the default list stands.
- [ ] Each new egress case fails with its part reverted, and a Node worker, a Python worker and an engine start and serve with egress on.
- [ ] Every test, lane and run uses plain TCP and UDP sockets.

## Consequences

- **(−)** A tool in a worker that opens a raw socket, such as `ping` or `traceroute`, gets a permission error, egress on or off. No shim, example or lane uses one (funcd-typescript v0.24.0, funcd-python v0.19.1, funcd-functions v0.25.1, read 2026-11-05).
- **(−)** With egress on, a worker's own IPv6 dial off its loopback fails at once. The gateway governs IPv4 only (ADR-0117).
- **(−)** What the node routes takes `funcd_egress`'s forward policy, whoever sent it, IPv4 or IPv6 (ADR-0115 Decision B), and where `br_netfilter` is loaded so does a frame any bridge of the node switches. A host on another bridge whose IPv4 source falls in the worker subnet stays under Decision A.
- **(−)** A unit that makes `/proc/sys` read-only for funcd, such as one with `ProtectKernelTunables=true`, stops every worker with egress on. The containerd unit `funcd install` writes has no such line; `configs/systemd/funcd.service` is the process-mode unit.
- **(neutral)** A thread per `Create` with egress on, which ends with the write. ADR-0317's IPv6 rows stay unread while Decision D holds.

## Open questions

None.

## References

[FEAT-0007](../feat/0007-feat-egress-control.md) · ADR-0011 · ADR-0056 · ADR-0115 · ADR-0117 · ADR-0267 · ADR-0269 Decision 3 · ADR-0272 Decision 7 · ADR-0313 · ADR-0317 · [the kernel's `disable` and `disable_ipv6` settings](https://docs.kernel.org/networking/ipv6.html) and [`inet6_init`](https://github.com/torvalds/linux/blob/master/net/ipv6/af_inet6.c), which with `disable` set returns before it registers the IPv6 sysctls, read 2026-11-05.
