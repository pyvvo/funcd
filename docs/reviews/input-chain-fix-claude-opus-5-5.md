## Verdict: pass — 0 blockers, 0 majors, 1 minor  (input-chain fix, model: claude-opus-5-5)

This reviews commit `a385d94a`, `fix(network): a worker's traffic to the node stops at the input chain`. The decider asked for it
without filing an issue, so the `TestIssue<N>` name and the `Fixes #N` line do not apply.

The behavior it claims: with `server.network.egress` on, a worker's traffic to the node stops at the `input` chain of `funcd_egress`,
except its replies, the gateway, the forwarder and `InternalAllow`. The chain's policy stays accept, so the node's other traffic is
untouched. The rules asked for sit after the two existing accepts: source in the worker subnet with conntrack established or related,
accept; source in the subnet with the destination and TCP port of each `InternalAllow` entry, accept; source in the subnet, drop.

### Minor
- **The fence's loopback comparison has no unit pin** · attribution: `model` · a mutant that passes `expr.CmpOpNeq` at
  `matchLoopback(expr.MetaKeyOIF, expr.CmpOpEq)` in `fenceRules` passes every test in the package, because
  `TestScenarioFenceRefusesOtherDestinations` reads only rule 0's key and verdict. The commit made that operator a parameter. Nothing
  is wrong today: a scratch test found `fenceRules` equal to `origin/main`'s with `reflect.DeepEqual` for three allow lists, and the
  duckdb lane holds the fence's effect. Fix: one assertion on rule 0's `Cmp`.

### Routed (not this work's)
- **`-race` on the Linux-tagged tests** · attribution: `env` · on Linux it needs cgo, which the cross-compile from this host lacks.
  `TestInputChainStopsAWorkersTraffic` runs in one goroutine and shares nothing. `-race` ran on the package's host tests, and they pass.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix.** With the three new rules removed from `planRules` by an overlay,
  `TestScenarioRulesetProgrammed` fails on the plan and `TestInputChainStopsAWorkersTraffic` fails its seven cases that want a drop. An
  overlay of `origin/main`'s file does not compile against the test file, because the plan assertion names the new kinds.
- **It passes with the fix**, un-skipped, in the `funcd-linux-tests` VM, with the rest of `./internal/network/...`.
- **Mutants.** 13 overlay mutants on the new rules and helpers each fail a test: no drop, the drop first, no loopback check, the wrong
  loopback index, no IPv4 check, no source match, no established rule, established only, related only, no internal rule, and an internal
  rule with any protocol, any port or any destination. The lane's two new cases fail in local lane runs with the rules reverted, and
  with the drop written as the plain source match.
- **The kernel agrees with the test's table.** The real rule set (`network.New(...).Apply`, `FenceNetns`) ran in network namespaces in
  `funcd-linux-tests`, with listeners on a node, a worker, a sibling, a fenced engine and another host. From the worker's IPv4 address:
  UDP and ICMP to the node's addresses get no answer, TCP to a node port reaches the gateway, TCP to the `InternalAllow` endpoint reaches
  it, and `:53` reaches the forwarder. The node's calls to its worker are answered over TCP and UDP. A closed UDP port on the worker
  gives `connection refused`, so the related ICMP error comes through. The node's calls to its own addresses are answered, and so are
  another host's IPv4 calls and its IPv6 UDP from an address that holds the subnet's bytes at the IPv4 source offset. A lane VM on this
  code reads the rule back as `iif != "lo" ip saddr 10.63.0.0/16 drop`.
- **The two conditions beyond the plain source match are needed.** In the same run, with the drop as the plain source match, the node's
  call to its bridge address gets no answer, as it comes in on loopback with that address as its source. The egress gateway's dial for
  a CIDR rule is such a call, and `cidr-rule-reaches-the-bridge-address` pins it. With no IPv4 check, the IPv6 host's call above stops.
  Both serve "the node's other traffic is untouched".
- **The lane.** The branch's egress lane ends `final status: PASS`. `worker-udp-to-the-node-gets-no-answer` prints `node answered` and
  `worker no answer`, and `cidr-rule-reaches-the-bridge-address` passes. The PR reports the lane's run.
- **Engines and the change feed.** The fenced engine reaches `10.63.0.1:7075`, the change feed's path (`internalAllow` in
  `cmd/funcd/main.go`), and the node reaches the engine. A local run of the sql lane with egress on ends `final status: PASS`.
- **It is a bug.** The two checks: the same namespace run from `origin/main` shows what the request describes, and nothing decides it.
  It breaks ADR-0115 Scope ("else default-deny — which also covers worker→host/LAN") and Decision B, which lists worker→host among what
  falls to default-deny. ADR-0115's input-chain bullet says what the chain accepts, not that the rest passes. ADR-0315's Scope leaves
  out the chain's policy, which stays accept. ADR-0117's "TCP-only (no UDP/TPROXY)" is about capture. ADR-0269's fence reads no host
  rule, and its engines' traffic takes the same `InternalAllow` accept. No row of `docs/behavior/` accepts it, and the chapter already
  said UDP other than DNS is dropped. Issue searches (input chain, nftables, worker and host, egress and UDP) and the board's cards hold
  nothing on it. `TestScenarioRulesetProgrammed` pins the policy only.
- **Cause, not symptom.** The chain had no rule that ends a worker's traffic. The fix adds that rule, past the accepts, as ADR-0115
  Decision B orders them: every accept precedes the default-deny. No timeout, retry or swallowed error.
- **No ADR contradicted or edited.** The commit touches no file in `docs/adr`.
- **Scope.** Every hunk serves the request. The `fence_linux.go` hunk moves its inline expressions to the helpers the new rules share.
  `TestScenarioRulesetProgrammed` changes one message and gains the chain's order. No test was weakened or deleted.
- **Reuse.** `matchIPv4`, `matchLoopback` and `matchEstablished` come out of `fenceRules`, and `kindInputInternal` shares
  `kindForwardAccept`'s case. The test reuses `goodPolicy` and `forwarderPolicy`. Nothing in the module's tests or in `google/nftables`
  reads rules over packets, so `runRule` duplicates nothing.
- **Conventions.** Block-style YAML in the lane's embedded policy, imports at the top, comments that say why, and no new number that
  changes behavior: loopback's ifindex is the literal the fence already had.
- **Docs.** `blueprint.md`, `examples/funcdconfig.yaml` and `docs/behavior/security.md` carry the change in the request's words, and
  `just check-behavior` passes.
- **Checks.** `go test -race ./internal/network/...` (host), `go vet` and `golangci-lint` on host and Linux (0 issues), the Linux build,
  `go test -run Lane ./scripts/ ./cmd/funcd/` and `TestIssue341_ExampleDocumentsEveryKeyWithDefault` pass.

### Not done
- The gate and the e2e suite: they run once per PR, and the PR reports them.

### Definition of Done
12 / 12 items hold (the `/fix-review` fix checklist, with no issue number in the test's name or the commit). Item 13 does not apply: no
design spec models these rules, and `docs/design/platform/start-stop.fizz` models only that they go in before any worker runs. Item 3
holds without `-race` on the Linux-tagged test (`env`, above). Item 8 holds for what this review ran; the gate is the PR's.

### Model scorecard
Recorded: claude-opus-5-5 on input-chain (fix) → pass, 0/0/1, 1 model-attributed, DoD 12/12. See docs/reviews/model-scorecard.md.

### Recommendation
Nothing blocks the PR. The one assertion on the fence's loopback comparison can go in with it.
