# ADR-0315 implementation review — claude-opus-5-5

## Verdict: changes requested — 0 blockers, 1 major, 6 minors (ADR-0315 implementation, model: claude-opus-5-5)

Reviewed the branch's two commits: the ADR commit and the implementation commit. The forwarder, the gateway, the compiler's list, the rule
plan and the lane steps do what the ADR says, and each of the choices settled at acceptance is in the code. One regression test lost
its first assertion through this change's split of `Serve`, a one-word fix. The ADR stays `Reviewing` and F229 stays `reviewing`. By
this run's instruction I ran no `scripts/agent/gate.sh`, `just ci`, e2e suite or lane: the session driving the batch runs them once at
the end, and the items that need them count as pending, not as findings.

### 🔴 Blockers

None.

### 🟡 Majors

**1. The regression test of issue 368 no longer sees a server left running · attribution: `model` · home: here.**

`serveGoroutineAlive` searches the goroutine stacks for `egress.(*forwarder).Serve` (`internal/network/egress/forwarder_test.go:484-487`).
This change moved the servers into the new `serve` (`internal/network/egress/forwarder.go:103-123`), so after `Serve` returns a server
still running shows only `(*forwarder).serve.func…` frames, and the helper reports false. `require.Eventually` in
`TestIssue368_ServeCancelledEarlyReleasesListeners` now holds at its first tick, whatever is running. Its message `a DNS server goroutine
outlived Serve`, which `docs/reviews/issue-368-fix-claude-opus-5-5.md:20-21` records as the test's failure on the old code, can no longer
appear. Only the two port lines after it still detect the case, and they depend on whether the server has bound by then.

Measured with 30 separate runs of that test from a binary built by `go test -c -overlay`, on a mutant of `serve` that returns on
`ctx.Done()` with no `shutdown()`:

| Helper | no `-race` | `-race` |
|---|---|---|
| as committed | 18 of 30 fail, on the port lines | 29 of 30 fail, on the port lines |
| its string reads `egress.(*forwarder).serve` | 30 of 30 fail, on `a DNS server goroutine outlived Serve` | 30 of 30, the same |

The tree with no mutant passes 30 of 30 with either helper, in both modes. Nothing decided this: ADR-0315 names `TestIssue455` as the
test it changes, and issue 368 is closed with this test as its proof. It is not a Scenario of this ADR, and no assertion was edited to go
green, so it is no Blocker.

Fix (builder): change that one string, and rerun the mutant above.

### Minors

All six are placed here and attributed to `model`, and none changes what the platform does. Minors 1 to 4 are clauses of the ADR that the
code holds and no test pins: each mutant below passes the whole package, and a short scratch test, not committed, passes on the real code
and fails on the mutant. Minors 5 and 6 are wording.

1. **The fallback bind is not pinned** (`pkg/funcd/executor.go:124`). Review checklist line 4 says `buildEgress` never passes an unspecified
   address. `TestBuildEgressBindsTheBridgeAddress` sets `bridgeAddress: "127.0.0.1"`, so `cmp.Or(c.bridgeAddress, "0.0.0.0")` passes all of
   `./pkg/funcd/`. The one caller of `WithBridgeAddress` (`cmd/funcd/main.go:516`) passes a value `CheckBridge` holds inside the subnet; an
   embedder that passes an unspecified address gets a bind on all addresses. Smallest change: a second case with no bridge address.
2. **The refused line's count and shared allowance are not pinned** (`internal/network/egress/forwarder.go:240-246`). Scenario
   `refused-lookup-audited` says the line names how many were refused since the last line, and Decision 2 item 7 says unknown sources
   share one allowance. `TestRefusalLogOneLinePerSourcePerInterval` tests `note` alone, and `TestScenarioRefusedLookupAudited` sees `more=0`
   and one unknown source, so `"more", 0*more` and `key := src` both pass. The `refusalLog` comment also says at most one window per worker
   "being refused", while a window that ended with refusals held back stays until that address is next refused, also after the worker is
   gone. Smallest change: one test through `admit` on a fake clock with three unknown addresses, and that comment.
3. **The attested set is not pinned at the forwarder** (`forwarder.go:72`, `:303`). Decision 2 item 5 says each record binds with its own
   TTL and the token is narrowed to the rules that apply to the worker. `ttl := time.Hour` passes, and so does reading the token for a
   principal with no name, so no test at any level takes an `appliesTo` wildcard rule through to its token; the lane's policy covers the
   whole namespace. The TTL gap predates this change. Smallest change: `f.record` on a fake clock, and `policyRules` with a named wildcard
   rule.
4. **"`dnsResolver` is read only with a forwarder" is not pinned** (`cmd/funcd/main.go:463`, Decision B). Dropping
   `&& n.DNSForwarderPort != 0` passes `./cmd/funcd/`, and such a node then refuses to start on an unparsable key the ADR says is not read.
   Smallest change: a `buildOptions` test with egress on, no port and `dnsResolver: not-an-address`.
5. **Two scenario markers name scenarios the ADR does not have** (`forwarder_test.go:203`, `:233`): `lookup-other-name-refused`,
   `lookup-no-policy-refused`, `lookup-unknown-source`, `lookup-policy-read-fails`, `lookup-applies-to-another`, `lookup-engine-refused`. The
   ADR's are `lookup-refusals` and `lookup-follows-who-a-rule-covers`, which the test names match. Smallest change: those two comments.
6. **One new behavior row is folded and uncited** (`docs/behavior/security.md:57`). The ADR's *Behavior rows* list a name outside host-name
   characters as new. It is three words in the row of a worker's lookup, whose source cites neither scenario
   `lookup-name-outside-host-characters` nor `TestScenarioLookupNameOutsideHostCharacters`. `just check-behavior` passes. Smallest change: add
   the citation, or give it its own row with the `a name outside host-name characters` reason.

### Routed (not this work's)

- **A broken link in the ADR's header** (`docs/adr/0315-what-a-domain-rule-allows.md:14`): `0238-the-container-bridge-address.md`; the file is
  `docs/adr/0238-container-bridge-address.md`. Attribution `adr`, home: the ADR's text, so the session holding acceptance; nothing is merged
  yet, so it can be corrected before the merge. It is the only one of the 15 relative links this change adds that does not resolve.
- **`docs/PROJECT-SUMMARY.md`**, in the plan's step 4. Home: the later stage that writes it, by this run's instruction.
- **Two comments in the pinned `funcd-typescript` v0.24.0** (`examples/egress-probe/fixtures/egresspolicy-wildcard.yaml`, `funcdconfig.yaml`) name
  `storeWildcardPatterns` and `WildcardPatterns`. Home: that repository, at its next release. They change no behavior.

### ✅ Verified correct (keep it)

- **Commands, each exit 0.** Through `scripts/agent/d`:
  - `go test -race ./internal/network/... ./internal/auth/... ./api/types/v1alpha1/ ./internal/platform/config/ ./pkg/funcd/ ./cmd/funcd/`;
  - `go vet` and `go tool golangci-lint run` on the same packages (0 issues);
  - `GOOS=linux GOARCH=arm64 scripts/linux-exec.sh --run scripts/agent/d go test -count=1 -v -exec "$PWD/scripts/linux-exec.sh" ./internal/network/...`:
    81 tests pass, none skipped, `TestScenarioNoForwarderNothingOut` among them;
  - `just check-behavior`, and `go test -run 'Lane' ./cmd/funcd/ ./internal/testkit/langmod/ ./scripts/`;
  - also `go build ./...` for darwin and linux/arm64, `go vet -tags e2e ./pkg/funcd/ ./cmd/funcd/`, `just check-hygiene`, `just check-specs`
    (16 specs, 0 failing, none changed), and `go test -race -count=5` of the new forwarder and gateway tests.
- **The choices settled at acceptance.**
  - A refused lookup writes the throttled line (`forwarder.go:239-285`).
  - `100.64.0.0/10`, multicast and `255.255.255.255` count as restricted (`gateway.go:112-128`, with both edges of `100.64.0.0/10` in the test).
  - With `dnsForwarderPort` 0 no forwarder and no gateway are wired (`cmd/funcd/main.go:479`), and `planRules` names neither `:53` nor the resolver.
  - A name outside host-name characters is refused in `admit`, and `correlator.record` has no second gate.
- **`admit` is the only way a name reaches `record` or the codec.** Read through every non-test caller:
  - `correlator.record` ← `forwarder.record` (`forwarder.go:304`) ← `handle` (`:198`), with `admit`'s name, and `serve`'s mux is `handle`'s only caller.
  - `EncodeNetDestPath` ← `NetDestination.Ref` ← `decideConnect` (`gateway.go:168`), whose domains are `DomainsFor`'s; `Deps.DNS` is this forwarder
    (`pkg/funcd/executor.go:144`), and nothing else builds a `KindNetDestination` reference.
  - `DomainsFor` adds one thing that did not pass `admit`, a rule's `*.` token. It adds it only when it matches an admitted name, so its text past
    `*.` is a suffix of that name, and `EgressPolicy.Validate` holds rule text to DNS labels too. No character outside a host name reaches the codec that way.
  - SNI and Host go only to the audit record's field, and only when attested.
- **The lane's new steps, run verbatim.** The two new cases, cut mechanically from `e2e/egress.venom.yml`, ran under the flake's Venom through
  `limactl shell`, `sudo bash -c`, the heredoc and Python in `funcd-linux-tests`, with `ctr` a stub that prints `ctr task ls`'s columns. Behind
  them ran the real forwarder, gateway, Cedar PDP and rule set in the VM's root netns, a bridge made after the start, and `systemd-run --unit=funcd-demo`
  for the journal. Both suites exit 0.
  - `ss` printed `udp UNCONN … 10.63.0.1:15053 0.0.0.0:*` and `tcp LISTEN … 10.63.0.1:15053 0.0.0.0:*`, and the lookup printed `rcode 5`.
  - The second case passed after 5 retries, when the line was due: `www.example.com rcode 0`, `www.example.org rcode 5`, and
    `msg="egress lookup refused" … domain=www.example.org source=10.63.0.5 reason="no domain rule allows it" more=5`.
  - What the lane alone proves I read instead. The task name follows `env-echo-[0-9]+[.]r0` (`e2e/env-echo.venom.yml:221`) and the columns
    `:250`. The `ctr` string equals `e2e/sql.venom.yml:24`. The pinned `examples/egress-probe/funcdconfig.yaml` sets `log.format: text`,
    `dnsForwarderPort: 15053` and `dnsResolver: 1.1.1.1:53`. The default subnet gives `10.63.0.1` (`internal/platform/config/bridge.go:29`). The
    Function has `minReplicas: 1`, and the probe catches a failed fetch into `ok: false`. The rcode is the low four bits of byte 3, and the query
    bytes are one question with RD set. Fifteen tries two seconds apart outlast the 10 s window. No other lane turns egress on, in this repository or the pinned modules.
- **The real kernel, plan step 7, rerun independently.** No tracked file holds it, so I built a small embedder with `go build -overlay`.
  - Started before the bridge: one `cannot listen yet` warning and no socket on the port; it serves once the bridge exists.
  - After a lookup, with the rule's port 443: `198.51.100.80:443` reached and `:444` closed; `192.168.77.10:443`, `169.254.169.254:443` and
    `10.63.0.1:443` closed, each audited `allowed=false` with `domain=""`.
  - With the name and `cidrs: 192.168.77.10/32` on 5432: `:5432` reached and `:5433` closed.
  - A TCP lookup to another address on `:53` reached the forwarder through the redirect (rcode 2, as the probe's upstream serves UDP only).
  - With no forwarder port: a control query from the node reaches an outside resolver; from the worker UDP times out, TCP is refused, a connection
    is refused, and that resolver counts 0 queries.
  - Limits: no CNI plugin ran, and the worker was no containerd task. The rig removed what it made in the VM.
- **Revert checks, each killed by the scenario tests.**
  - The forwarder forwards every query, as ADR-0117 had it: seven tests fail, among them `TestScenarioLookupAllowedName` and `TestScenarioLookupRefusals`.
  - The answer's own name is recorded: `TestScenarioRuleFiresThroughACNAME`, `TestScenarioAnswerNamesNotRecorded`.
  - A bind on all addresses: `TestBuildEgressBindsTheBridgeAddress`.
  - A restricted address keeps its domains: `TestScenarioDomainRulePublicOnly`, `TestScenarioCIDRRuleNamesTheAddress`.
  - The compiler drops its Function test, or ignores `appliesTo`: `TestScenarioForwarderAndPDPAgree`, so that test does read the real compiler.
  - The rule plan keeps the DNS accept, on Linux: `TestScenarioNoForwarderNothingOut`, `TestPreroutingExcludesInternalBeforeRedirect`.
- **Contracts and Decision.** `DomainRules`, `NewForwarder`, `Deps.NodeAddrs` and `EgressDomains` match the ADR's text; `WildcardPatterns` is gone from code;
  `WithEgressGateway` keeps its signature. The check runs before `client.Exchange`, and a failed `For`, a nil index and nil rules all send nothing up, with a
  counting upstream to show it. A failed read of the node's addresses ends the connection in the gateway's error path, and a connection with no attested name
  does not read them.
- **Each Scenario has its named test**, un-skipped and passing, or the lane step the plan gives it. No scenario test was weakened; the lines removed from
  `forwarder_test.go` are a helper's old body, the two old `NewForwarder` calls and `TestIssue455`'s old field list, and those removed from
  `network_linux_test.go` are what ADR-0115's superseded scenario `dns-resolves` held.
- **Tree and tracking.** The ADR's second commit changes only its status line. Every changed file is in the plan or follows from it; `go.mod` is untouched. The
  `Superseded in part by` lines on ADR-0115 and ADR-0117 are their only edits, and `blueprint.md` is synced in both places. F229 and the ADR read `reviewing`. The
  roadmap holds no row of this era, so it owes nothing. The ADR's References cite no feature issue, so the PR closes none.

### Definition of Done

14 of 16 hold, and 2 are pending. They hold: the six lines of the ADR's Review checklist, and eight applicable generic items (no port here has drivers, so the
contract suite item does not apply). Pending, for the batch's gate and lane pass: the ADR's *Done* line (`just ci` and the `egress` lane) and the generic full
suite. The Major is outside this count: it concerns a test of another issue.

### Model scorecard

To record, by the stage that writes the ledger: claude-opus-5-5 on ADR-0315 (implementation) → changes-requested, 0 blockers, 1 major, 6 minors, 7
model-attributed, DoD 14/16. See docs/reviews/model-scorecard.md once it is written.

### Recommendation

Change the helper's string and rerun the mutant, in one small commit with the six Minors; the link in the ADR's header goes with the session holding acceptance.
Then a short second review of that commit stamps `Implemented`, with the gate and the lane pass still to run over the batch.
