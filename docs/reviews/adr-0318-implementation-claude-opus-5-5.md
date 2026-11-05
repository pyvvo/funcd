# ADR-0318 implementation review — claude-opus-5-5

## Verdict: pass — 0 blockers, 0 majors, 5 minors (ADR-0318 implementation, model: claude-opus-5-5)

This reviews commit `2811739d`, `feat(egress)!: a worker's network holds what the rules read`, on top of the acceptance commit `0a961d44` and the proposal
commit `74b0895b`, from `origin/main` `c6ada8da`. Each Decision holds on the kernel and on the real driver, and each Scenario has its named test, which
passes. Five Minors go back to the builder as small edits, and none holds the stamp. One of them leaves a clause of the Review checklist short, by one mutant
of nine, on a match that holds on the kernel. ADR-0318 is stamped `Implemented` and F232 `implemented`. `scripts/agent/gate.sh` and
`scripts/agent/lanes.sh <branch>:all`, which *Done* names, are `not run here; the PR's`.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

1. **Minor · model — no test fails when the unicast drop stops reading the source.** Review checklist line 1 asks that a mutant of each match fails a test.
   Eight of the nine matches do. The rule `kindLateralDrop` builds (`internal/network/nftables_linux.go:262`) reads the ethertype, the source and the destination.
   A mutant that replaces `frameSrc` there with `matchIPv4Frame()` passes every test of the package. The rows from another source
   (`internal/network/network_linux_test.go:341-344`) send to another address off the subnet, the limited broadcast or multicast, and none sends into the
   subnet. The code is right: in the netns run, `h2 tcp 10.63.200.3:8080`, from `198.51.100.2` to a host on the second bridge that holds that address, connects, and
   with this mutant it times out. It is Minor because the mutant only widens a drop, for frames that no worker sends, and the mutants of the other eight matches fail a test.
   **What closes it**: the row `{"IPv4 unicast from another source into the subnet", lateralFwd, packet{src: addr("192.0.2.2"), dst: addr("10.63.0.6")}, expr.VerdictAccept}`. As a scratch
   test by overlay, it passes on this code and fails the mutant.

2. **Minor · model — no committed test runs the setting and the fence together.** `writeNetns` (`internal/runtime/containerd/containerd_linux.go:643-653`) is new, and it carries the
   fence's call too. Two overlay mutants pass the driver package's tests: `return nil` in place of `FenceNetns` (line 650), and `return network.DisableWorkerIPv6(netnsPath)` at line 645,
   which skips the fence with egress on. The duckdb lane's `engine-fenced` shows the first, with egress off. Nothing committed shows the second: the egress lane is the only one with
   egress on, and it runs no engine. The code is right: the sql lane with egress on, with one scratch case added, shows an engine with no address on `eth0` whose plain TCP connect to the
   bridge address times out, while a worker's connects to the gateway. **What closes it**: a test of `writeNetns` behind a seam, or a lane case that holds an engine with egress on.

3. **Minor · model — the test of a path that is no netns passes when `Setns`'s result is dropped.** `TestDisableWorkerIPv6RefusesAPathThatIsNoNetns`
   (`internal/network/fence_linux_test.go:84`) says the path "cannot be entered", and asserts only an error. A mutant of `internal/network/fence_linux.go:89` that ignores the
   result of `Setns` passes this test and `TestScenarioIPv6SettingFailureStopsTheWorker`, as the tests run unprivileged and the next open of the setting fails too. The code is right.
   **What closes it**: `require.ErrorIs(t, DisableWorkerIPv6(file), unix.EINVAL)`. As a scratch test by overlay, it passes on this code and fails the mutant.

4. **Minor · model — `docs/PROJECT-SUMMARY.md` has no row for ADR-0318.** The plan's step 4 names this file, and the commit changes only its isolation sentence (line 212). The Decision log ends at
   ADR-0317 (line 818) and Build status reads `ADR-0001…0317` and `304 Implemented` (lines 391-392). `python3 .claude/skills/project-summary/state.py` ends `VERDICT: DRIFT`, with `[APPEND]` for
   ADR-0318, and with no other `[APPEND]`. **What closes it**: the row, and Build status recomputed after this stamp. This review's scope leaves the file alone.

5. **Minor · model — two living sentences say more than the ADR's Consequences.** `blueprint.md:525` says "another host's IPv6 and another bridge's traffic stay untouched", and F232's row
   (`docs/feat/0007-feat-egress-control.md:60`) says "a frame from no worker's address is left alone on any bridge of the node". The ADR's third Consequence, and the chapter's row `What the node
   routes, egress on` (`docs/behavior/security.md:62`), say that what the node routes takes the forward policy, and that where `br_netfilter` is loaded so does a frame any bridge switches. The netns run shows both:
   the two routed connects and the two connects with the module loaded time out. Both sentences came with the acceptance commit, and the plan's step 4 names the blueprint's. The chapter says it
   rightly. **What closes it**: the qualification in the blueprint's sentence, and F232's clause in the chapter's terms: `funcd_lateral` passes such a frame.

### Routed (not this work's)

- **Decision D's IPv6 socket shows on no machine here** · attribution: `env` · home: elsewhere · destination: the first node whose kernel has IPv6 as a module. `funcd-linux-tests` runs
  `6.1.0-50-cloud-arm64` with `CONFIG_IPV6=y`, so a mutant that drops the socket at `internal/network/fence_linux.go:104` passes, and the Test plan names no proof of that clause. It is read, and
  it follows the sentence in Decision D. Nothing goes back to the builder.

### Not run here; the PR's

`scripts/agent/gate.sh`, `just ci`, `just ci-full`, the e2e suite, `go test ./...` and `scripts/agent/lanes.sh <branch>:all`, which ends with the metastore lane. *Done* names the gate and the lanes, and
these runs hold no stamp. Among what they cover: every lane starting and serving with no `CAP_NET_RAW`, as the Test plan's third row says.

### ✅ Verified correct (keep it)

**One named test per Scenario, un-skipped and passing, 20 runs each.** Linux tests ran in `funcd-linux-tests` as an unprivileged user. `-race` is not available there, as cross-compiling refuses it
(`go: -race requires cgo`); `DisableWorkerIPv6` shares only a channel and the open file with its goroutine, and the parent reads the channel before it closes the file.

| Scenario | Unit test | Lane case and other runs |
|---|---|---|
| `worker-eth0-has-no-ipv6` | `TestScenarioWorkerEth0HasNoIPv6`, `TestProcSysStaysReadOnly` | egress `worker-eth0-has-no-ipv6`; the netns run |
| `ipv6-setting-failure-stops-the-worker` | `TestScenarioIPv6SettingFailureStopsTheWorker`, and the first test's cases for a missing file and a kernel with no IPv6 | |
| `no-worker-holds-net-raw` | `TestScenarioNoWorkerHoldsNetRaw` | egress `worker-lacks-cap-net-raw`; sql `SC no-worker-holds-net-raw`, egress off and egress on |
| `worker-traffic-reaches-no-sibling` | `TestScenarioWorkerTrafficReachesNoSibling` | egress `worker-traffic-reaches-no-sibling`; the netns run |
| `other-traffic-stays-untouched` | `TestScenarioOtherTrafficStaysUntouched`, `TestEveryAddressRuleMatchesIPv4First` | the netns run |

**Commands, each through `scripts/agent/d`:**

| Command | Result |
|---|---|
| `go build ./...`, and `GOOS=linux GOARCH=arm64 go build ./...` | exit 0, exit 0 |
| `go vet ./internal/network/... ./internal/runtime/containerd/... ./cmd/funcd/...`, on macOS and with `GOOS=linux GOARCH=arm64` | exit 0, exit 0 |
| `golangci-lint run` of those packages, on macOS and with `GOOS=linux GOARCH=arm64` | 0 issues, 0 issues |
| `GOOS=linux GOARCH=arm64 go test -count=1 -exec scripts/linux-exec.sh ./internal/network/ ./internal/runtime/containerd/` | ok, ok |
| the same with `-count=20 -run` of the nine named tests of those two packages | ok, ok |
| `go test -count=1 -race -v -run 'TestContainerdModeGivesTheDriver…' ./cmd/funcd/`, and in the VM with no `-race` | `TestContainerdModeGivesTheDriverTheEgressSwitch` passes on both |
| `go test -count=1 ./cmd/funcd/ ./internal/platform/config/ ./internal/network/... ./scripts/...` | ok on each, `TestIssue341_ExampleDocumentsEveryKeyWithDefault` and the lane guards among them |
| `just check-hygiene`, `just check-behavior`, `just check-specs` | exit 0 each; 16 specs, 0 failing |
| `gofmt -l internal/network internal/runtime/containerd cmd/funcd` | empty |

**The lanes, run by this review through `scripts/agent/lanes.sh`, one at a time, each with the commit it ran:**

| Ref | Lane | Result | What it shows |
|---|---|---|---|
| `2811739d` | egress | PASS, 77 s | `CapEff` and `CapBnd` lack bit 13 and hold bit 0; `eth0` shows no `inet6`; the setting reads 1; its own write fails with `EROFS`; `::1` and `::` bind; the worker's connect to the node's link-local address fails with `Network is unreachable`, and the node's own connect passes; the sibling hears only the node's `control` |
| `2811739d` | sql, egress off | PASS, 226 s | `SC no-worker-holds-net-raw`, for a Python worker and the rqlite engine: bit 13 clear in `CapEff` and `CapBnd`, bit 0 set in `CapBnd` |
| `043299f5`, scratch: this commit, `server.network.egress` on as `exp/wni-sql-egress` sets it, one case added | sql, egress on | PASS, 226 s | A Node worker, a Python worker and the engine start and serve through every case of the suite. The added case: the engine's and the worker's `eth0` show no `inet6`; the engine's `lo` holds `::1`; `CapBnd` reads `00000000a80405fb` on both, which is containerd's default list less bit 13; `CapEff` reads the same on the worker and 0 on the engine, whose user is not root; the engine's plain TCP connect to the bridge address times out, and the worker's connects |
| `aec34d50`, scratch: this commit, `kindLateralDrop` not planned | egress | FAIL, at `worker-traffic-reaches-no-sibling`'s unicast assertion and in no other case | That assertion reads the rule, which no earlier run showed |
| `e2906b9c`, `exp/wni-rev-lateral` | egress | FAIL, at `worker-traffic-reaches-no-sibling`'s broadcast and multicast assertions and in no other case | The builder's revert, rerun: its log says the same |
| `2811739d` | duckdb, egress off | PASS, 282 s | The DuckDB engine starts and serves with `writeNetns` in `Create` and no `CAP_NET_RAW`; `engine-fenced` reads `HITS=0`, so the fence's call holds in its new place |

Both scratch commits sit on no branch, and neither is part of this work.

**The builder's lane logs, read.** `git diff exp/wni-wip 2811739d` holds documents only, so those logs speak for this code. Each revert ref differs from `exp/wni-wip` by one edit, and each lane fails at its own case
and in no other: `exp/wni-rev-cap` drops the `oci.WithDroppedCapabilities` line and fails `worker-lacks-cap-net-raw`; `exp/wni-rev-ipv6` skips the write and fails `worker-eth0-has-no-ipv6`, while its own-write line still reads `EROFS`;
`exp/wni-rev-lateral` drops the two new rules and fails `worker-traffic-reaches-no-sibling`, at its broadcast and multicast assertions. `exp/wni-sql-egress` passes with egress on, and my own run above agrees.

**The netns run, as *Done* asks, rerun on this commit's code.** `internal/rigprobe` wraps the real `internal/network`, built by `go build -overlay` for linux/arm64 with nothing in the tree. It ran through
`scripts/linux-exec.sh --run` in `funcd-linux-tests`, kernel `6.1.0-50-cloud-arm64`, `CONFIG_BRIDGE_NETFILTER=m`, with `network.New(true, nil).Apply`, `DisableWorkerIPv6` and `FenceNetns`, over plain TCP and UDP sockets, in `ip netns`
namespaces: a node `n0`, workers `w1` and `w2`, a fenced engine `e1`, a host `h1` on an uplink, and hosts `h2` and `h3` on a second bridge. Its script and its output are in the private notes, with the review's reads added. `br_netfilter` was
not loaded, then loaded for the last section and removed; `/var/tmp` and the namespaces were left empty.

| Line | Result |
|---|---|
| rules | `funcd_lateral/forward` 3, `funcd_egress` prerouting 4, forward 1, input 6 |
| `w1` and `e1` `eth0` `inet6`, `w1` `lo` `inet6` | none, none, `::1/128` |
| `w1` `disable_ipv6`: `eth0`, `default`, `lo`, `all` | 1, 1, 0, 0 |
| An interface made later in `w1` | no `inet6`, `disable_ipv6` reads 1 |
| `w1` binds `::1` and `::` | bound, bound |
| `w1` TCP and UDP to the node's link-local address | `Network is unreachable`, both |
| `n0` TCP to its own link-local address | connected |
| `w1` plain UDP to `10.63.0.6`, `255.255.255.255`, `239.1.1.1`, `10.63.255.255` | sent; the sibling's and the fenced engine's listeners hear only the node's `control` |
| `w1`'s neighbour table, read after its UDP to the sibling | `10.63.0.6` `REACHABLE`, so the worker's ARP passes |
| The node's joined listener | nothing. With a control that plans no input-chain drop it hears the limited broadcast, the multicast and the subnet broadcast, so the input chain is what stops them, as the chapter's row says |
| `w1` TCP to `10.63.0.1:8080`, `192.0.2.2:8080`, `10.63.0.1:9000`; UDP to `:53` | gateway, gateway, internal, forwarder |
| `w1` UDP to `10.63.0.1:5354`; TCP to `10.63.0.6:8080` | timed out, timed out |
| `e1` TCP to the fence's endpoint; to `10.63.0.1:8080`; to `10.63.0.6:8080` | connected, timed out, timed out |
| `n0` TCP to `10.63.0.5:8080` | connected |
| `h1` TCP and UDP to `[2001:db8:a3f:1::1]`, third group `0a3f` | `tcp-service`, `udp-service`: the node's own services answer |
| `h2` TCP to `[2001:db8:a3f:2:a3f::3]`, third and fifth groups `0a3f`; to `198.51.100.3` | `h3-service`, `h3-service` |
| `h2` UDP to `255.255.255.255` and `239.1.1.1` | `h3` hears both |
| `h2` TCP to `10.63.200.3`, an address of `h3` in the subnet | `h3-service` |
| `h1` routed to `198.51.100.3` and `[2001:db8:a3f:2:a3f::3]` | timed out, timed out: the forward policy, the row marked `accepted` |
| With `br_netfilter` loaded, `h2` to `h3`, IPv6 and IPv4 | timed out, timed out, as the ADR's third Consequence says |

**The failures of `DisableWorkerIPv6`, as root in the same VM, in private namespaces.** `Create`'s path through them is the scenario test's, and this shows the function on a real kernel.

| Case | Result |
|---|---|
| A read-only `/proc/sys`, the ADR's fourth Consequence | exit 1, `read-only file system`, and the namespace's `eth0` still reads 0 |
| A namespace with no `eth0` | exit 1, `no such file or directory` |
| A plain file for a namespace | exit 1, `invalid argument` |
| A path that does not exist | exit 1 |
| A namespace with an `eth0`, writable | exit 0, and `eth0` and `default` read 1 |
| The node's own `default` and `eth0`, after all of these | 0 and 0, as before them |

**Overlay mutants, 44 on this commit, each against its package's Linux tests in the VM: 38 fail a test.** The builder's `mutants.txt` predates a test's rename, so I reran its 21, with 23 more of each match, rule kind and option.

| Mutants | Result |
|---|---|
| No ethertype match; `meta nfproto` in the bridge family; a wrong ethertype | Each fails `TestScenarioWorkerTrafficReachesNoSibling` and `TestEveryAddressRuleMatchesIPv4First` |
| No ethertype match on the unicast, the broadcast or the multicast rule alone | Each fails `TestEveryAddressRuleMatchesIPv4First`, and the first two `TestScenarioWorkerTrafficReachesNoSibling` too |
| No nfproto match at all; nfproto reads IPv6 | Each fails `TestInputChainStopsAWorkersTraffic`, `TestScenarioOtherTrafficStaysUntouched`, `TestEveryAddressRuleMatchesIPv4First` |
| No IPv4 match on each one rule kind: skip, redirect, the forward and input accepts, DNS redirect, established, input drop | Each fails `TestEveryAddressRuleMatchesIPv4First`, and all but established fail `TestScenarioOtherTrafficStaysUntouched` too |
| No unicast, broadcast or multicast rule; unicast reads no destination; multicast `/3`, `/5` or `208.0.0.0`; another broadcast address | Each fails `TestScenarioWorkerTrafficReachesNoSibling` |
| Broadcast or multicast rule reads no source | Each fails `TestScenarioOtherTrafficStaysUntouched` |
| Unicast rule reads no source | **None** (Minor 1) |
| `232.0.0.0` as the multicast net | None, and it is equivalent: `maskedBytes` makes it `224.0.0.0`. The builder's one survivor is this |
| `all` for `default`; `lo` added; no `eth0`; no `default`; writes 0; a missing tree is an error; a missing file passes | Each fails `TestScenarioWorkerEth0HasNoIPv6` |
| Egress reads false; a failed write is ignored | Each fails `TestScenarioIPv6SettingFailureStopsTheWorker` |
| Egress reads true | Fails every test whose fake pin the write cannot enter |
| Keeps `CAP_NET_RAW`; drops `CAP_NET_ADMIN` instead; drops `CAP_CHOWN` too | Each fails `TestScenarioNoWorkerHoldsNetRaw` |
| `ReadonlyPaths` emptied | Fails `TestProcSysStaysReadOnly` |
| `Setns`'s result ignored | **None** (Minor 3) |
| No fence; the write skips the fence | **None** (Minor 2) |
| No IPv6 socket first | None (routed) |

**Review checklist and Contracts.**
- `planRules` holds the three drops (`nftables_linux.go:173-177`), each from `frameSrc`, which matches `meta protocol ip` first (`:258`). In the bridge family `matchIPv4Frame` reads the ethertype, and in the inet table `matchIPv4` reads `meta nfproto`, as Constraints says.
- `TestEveryAddressRuleMatchesIPv4First` walks every planned rule and each network-header read in it, with its match by family.
- `DisableWorkerIPv6` writes `eth0` and `default` only (`fence_linux.go:110`), on a thread it never unlocks (`:88`), and a missing file under a present tree is an error (`:112`, with `:107` for an absent tree). `Create` calls `writeNetns` after `Setup` (`containerd_linux.go:591-592`), and a failure takes the existing cleanup: the DEL in the pin, the unmount, the task's kill and the container's delete. `/proc/sys` stays in `ReadonlyPaths` (`sandbox_linux_test.go:82`), and the lane reads `EROFS`.
- `sandboxOpts` drops the capability with no read of the kind or of egress (`containerd_linux.go:1319`), and `ociOpts` ends with `withSeccompProfile` (`:1302`). `TestScenarioNoWorkerHoldsNetRaw` compares each set with containerd's generated list for a Function, a Lake and a SQLDatabase.
- `sandboxOpts` has one production caller, `ociOpts` (`containerd_linux.go:1300`). No Go e2e scenario was added, so no speed rule applies.
- Contracts: `Config.Egress` on both platforms (`containerd_linux.go`, `containerd_other.go`), `func DisableWorkerIPv6(netnsPath string) error` on both (`fence_linux.go`, `fence_other.go`), the two rule kinds, and no new config key. `cmd/funcd/main.go` passes the key, which `TestContainerdModeGivesTheDriverTheEgressSwitch` pins for both values.
- Line 5 is the lanes above: each new egress case fails with its part reverted, and a Node worker, a Python worker and an engine start and serve with egress on.
- Every test, lane and run uses plain TCP and UDP sockets. A capability's absence is read from `CapEff` and `CapBnd`, so the permission error of Consequences is read, not run.

**Tree and tracking.**
- The commit touches the plan's files and two more that it explains: `e2e/README.md`'s line for the suite, and the driver-config test. No `go.mod` or `go.sum` change, and no config key.
- `git diff 0a961d44 2811739d` of the ADR is its status line alone. ADR-0115 gains only its `Superseded in part by` line, with the commit `0a961d44`, in the form of ADR-0272 Decision 7.
- `docs/behavior/security.md` holds what *Behavior rows* says: the changed sentence of the note and the five new rows, each with its test or Decision, and each test a row cites exists. `What the node routes, egress on` is `accepted`, citing ADR-0115 Decision B.
- `blueprint.md:52` and `:243` state Decision E, and `:242` matches ADR-0117: TCP redirected, `TPROXY` later, UDP other than DNS dropped. `blueprint.md:525` holds the lateral sentence and Minor 5.
- *Design model* reads `None`, and no file under `docs/design/` changed. The new code holds to ADR-0002: `api/fault` wraps, no new globals, and a named constant for each number.
- The new lane cases keep block-style YAML and short names, and the lane guards pass.

### Definition of Done

18 of 20 hold. The ADR's six lines of Review checklist, less line 1's mutant clause (Minor 1). Four items of *Done*: the tests pass, the netns run and the sql lane with egress on are recorded above, and F232 was at `reviewing`. ADR-0313's Behavior rows. The plan's documents step, short of the
summary's row and of the two sentences (Minors 4, 5). Eight generic items: scenario tests, real behavior, Contracts honored, tree, conventions, dependencies, scope and tracking. No contract suite applies, as no port changed. The gate and `lanes.sh <branch>:all` are `not run here; the PR's`, and they are not counted.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0318 (implementation) → pass, 0 blockers, 0 majors, 5 minors, 5 model-attributed, DoD 18/20. See docs/reviews/model-scorecard.md.

### Recommendation

ADR-0318 is `Implemented` and F232 `implemented`, and the branch merges after its one gate and its lanes. The five Minors go back to the builder as a few small edits before the PR, and none holds the stamp; Minors 1, 3 and 4 are a few lines each. The ADR is now frozen, so whatever the
gate finds in this work is corrected in the code. The PR closes no issue: the ADR's References cite none, a search of issues for `F232` and `ADR-0318` finds none, and F229 and F231 stand the same way.

### After this review: a linked call with egress on

The builder ran this after the verdict, at the decider's request, so it is no part of the verdict. It ran the `fn-to-fn` lane on `6c6404c3` with `server.network.egress` on, from a local ref, `820b123c`, that sets the
lane's config and adds two cases to its suite. No committed lane holds that config. The lane ends `final status: PASS` on real containerd, in 61 s.

| Case | Result |
|---|---|
| `front` invokes `greeter` through its declared link | 200, `via` is `front`, and the greeting holds the name |
| The contract's 422 through `front` | 500 whose body carries `context.invoke("greeter") failed: 422` and `event data does not match the input contract` |
| A direct call of `greeter` with a bad type | 422 |
| `front`'s local-API socket | present |
| `front`'s `eth0` | no `inet6`, as egress on sets it |
| A plain TCP connect to `greeter`'s worker address, `10.63.0.2:8080` | From the node, connected. From `front`'s netns, timed out |
| `front-nolink`, the same artifact with no link | 500 whose body carries a 403, `caller default/front-nolink declares no link "greeter" (no link is no grant)` |
