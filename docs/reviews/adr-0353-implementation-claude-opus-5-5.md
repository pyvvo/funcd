## Verdict: changes requested — 0 blockers, 0 majors, 1 minor (ADR-0353 implementation, model: claude-opus-5-5)

This report reviews ADR-0353's part of the branch `feat/tunnel-edge` at 03aa1e38: the commit 48b7a41a, after ADR-0352's
78eb3917 ([adr-0352-implementation-claude-opus-5-5.md](adr-0352-implementation-claude-opus-5-5.md)) and before
ADR-0354's 03aa1e38 ([adr-0354-implementation-claude-opus-5-5.md](adr-0354-implementation-claude-opus-5-5.md)). The
reviewer did not write the work, and every check below was run, not read.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

- **The gateway scenario's named test is not in `pkg/funcd`** · model · `internal/network/egress/egress_test.go:668`.
  Plan step 3 asks one `TestScenario…` per scenario in `pkg/funcd`. `TestScenarioGatewayNeverDialsTheEdge` is a unit
  test of `decideConnect` with a hand-made `Deps.Edge`. The platform's half, the bound listener filling the gateway's
  edge set, is the separate `TestTheEdgeListensOnTheBridgeWhereTheGatewayRuns`
  (`pkg/funcd/bridge_pin_internal_test.go`). Together they cover the scenario, but no one test takes it from the
  platform's bind to the gateway's refusal. Fix: add `TestScenarioGatewayNeverDialsTheEdge` to `pkg/funcd`: on a
  platform bound on every address, the gateway's decision for a worker connecting to a node address at the bound port is
  a refusal. Keep the unit test under another name.

### Routed (not this work's)

- **An intermittent failure in the first full run, on no path this work changes** · elsewhere: the flake tracker
  (#1142).
  - The failure: in the first `just ci-full`, on a host running two other test suites,
    `TestFunctionAnswersMatrix/invokeFunction/a_body_over_1_MiB` answered 400 "could not read request body" instead of
    413.
  - Why it is not this work's: that 400 is the envelope plugin's (`internal/dataplane/plugins.go:147`). Its read runs
    inside `s.chain.Before`, before any line the branch adds on that request. The pin reads no body, and the client
    middleware passes through when no proxy is trusted.
  - The test passed six runs alone and passed in the second full run.
  - It is surprising and not checked on the base yet: a question for the flake tracker, not a finding of this work.
- **An e2e failure under host load** · elsewhere: the flake tracker (#1142).
  - The failure: `TestScenarioKeyRenewalUnderLoad/rolling` failed in two runs of `just test-e2e` while other sessions'
    gates shared the host. It read "rolling renewals left s Degraded/DependencyNotReady", then "Degraded/Renewing".
  - It passed in the base de9a0081's full suite and in the head's full suite on a quiet host (413 s, exit 0), as well
    as three runs alone. Its `single` subtest had a load flake before (#1246, closed by #1250).
  - Why it is not this work's: the test sets no egress gateway, bridge address or trusted proxy
    (`pkg/funcd/key_renewal_e2e_test.go`), so the pin and the client middleware are inert there.
  - Its own comment calls it a smoke test at tight ratios. Load decides it, not this branch.

### ✅ Verified correct (keep it)

- **The checks, run on 03aa1e38 in a worktree of its own.**
  - `just ci` passed in full on its second run:
    - tidy, generate and fmt left nothing to commit;
    - `check-hygiene` was clean, `check-behavior` and `scripts/check-generated.sh` passed;
    - `golangci-lint run ./...` and the dev-tagged lint each reported 0 issues;
    - every package of `go test ./...` passed, and so did the dev-tagged tests;
    - `go build ./...` and `go mod verify` passed.
  - `just test-e2e` passed on a quiet host (413 s, exit 0). Its two earlier runs failed on
    `TestScenarioKeyRenewalUnderLoad/rolling` while other gates shared the host (routed above); the base de9a0081's
    suite passed as well. With `just ci`, that is `just ci-full` on 03aa1e38.
  - On Linux: `GOOS=linux go build ./...` and `go vet ./internal/... ./pkg/... ./cmd/...` passed, and `golangci-lint`
    reported 0 issues.
  - `just test-linux` passed in the funcd-linux-tests VM (exit 0, 49 s), including `internal/network` and
    `internal/runtime/containerd`.
  - `scripts/agent/stress.py --base de9a0081` ran the 47 Test functions the branch adds or changes, each alone under
    `-race`: 0 failed, 28 of them 20 times within the budget (342 s). The longest single run took 1.17 s, and every new
    scenario test calls `t.Parallel()`.
- **Mutants of the key lines, killed.** Each was one edit, applied with `go test -overlay` outside the tree:
  - another namespace's edge entry served: `TestPinServesTheCallersNamespaceOnly`,
    `TestScenarioOtherNamespaceHostRefused`, `TestScenarioAWorkerIsPinnedToo`, `TestScenarioDefaultCaller`;
  - the by-name namespace unchecked: `TestPinServesTheCallersNamespaceOnly`, `TestScenarioByNamePinned`,
    `TestScenarioDefaultCaller`;
  - a foreign `X-Funcd-Namespace` let through: `TestPinServesTheCallersNamespaceOnly`, `TestScenarioAWorkerIsPinnedToo`;
  - an unknown caller served: `TestPinServesTheCallersNamespaceOnly`, `TestScenarioUnknownCallerRefused`;
  - the callback check skipped: `TestCallbackOfAPinnedCallerStaysInItsNamespace`, `TestScenarioSignInPinned`;
  - the gateway's edge refusal skipped: `TestScenarioGatewayNeverDialsTheEdge`.
- **Decisions 3 and 4, the pin** (`internal/dataplane/dataplane.go`, `pin`). A request whose local address is the
  bridge address is named through `egress.Sources`, and an unknown or conflicting source gets 403 on every path, before
  anything is served. `X-Funcd-Namespace` is refused unless it names the caller's own namespace. Every later decision
  then checks the caller's namespace before any backend runs:
  - the edge match's namespace, before its stance, static file, upstream or PEP;
  - the by-name namespace of ADR-0240 Decision 6;
  - the sign-in callback, refused before `g.exchange` when its state names another namespace's provider
    (`internal/edge/oidc/oidc.go`).

  The logout is served, and an internal call is left alone (`TestPinLeavesAnInternalCallAlone`). The refusal names no
  namespace ("outside the caller's namespace").
- **The scenario tests** (`pkg/funcd/bridge_pin_internal_test.go`) run a real in-memory platform with the gateway and a
  bridge address, Routes and Sites in three namespaces, and calls that carry a local and a remote address:
  - `by-name-pinned` checks the header, no header, and the caller's own header;
  - `default-caller` checks `default`'s host-less entries, its by-name calls and team's reserved host;
  - `sign-in-pinned` counts the token endpoint's calls: none for the pinned callback, one for the visitor's.
- **Decision 1, the listener.** It exists only when the gateway's indexes do (`pkg/funcd/bridge_edge.go`,
  `bridgeEdge`). It listens at the bound port, through the wildcard listener or a listener of its own, which is retried
  on `EADDRNOTAVAIL` each `bridgeBindRetry`, and it is served by the same TLS-aware `serve` as the bound listener
  (`pkg/funcd/funcd.go`, `Run`). `TestTheEdgeListensOnTheBridgeWhereTheGatewayRuns` covers the loopback, wildcard and
  no-gateway cases.
- **Decision 2, the way in.** `InternalAllow` gains the bridge address at the bound port before `executor.Start`
  applies the policy. The gateway's `Deps.Edge` refuses the bridge address and the bound address at that port, and
  every node address under a wildcard bind, before the PDP (`internal/network/egress/gateway.go`, `onEdge`). A failed
  read of the node's addresses permits nothing.
- **The lane.** The egress lane's `tunnel-reaches-its-hosts` passed on containerd with the other 15 cases. The data
  plane listened on `127.0.0.1:8081` and `10.63.0.1:8081`. The engine on the bridge address got 200 for its own host,
  and 403 `outside the caller's namespace` for another namespace's host and for a foreign `X-Funcd-Namespace`. The
  public listener was unchanged.
- **The docs.** The calls.md rows, egress.md's "A worker's traffic to the node, egress on" and its gateway refusal, and
  the guide's network pages.
- **Tracking.** The ADR at `Reviewing`, its substance unchanged since acceptance; F273 at `reviewing`; the back-links in
  ADR-0238, ADR-0240, ADR-0332 and ADR-0315.

### Definition of Done

13 / 13 items hold: the 4 of the ADR's review checklist, and the 9 generic items that apply. The gateway scenario has
its named, passing test, at unit level; the Minor is the plan's place for it.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0353 (implementation) → changes-requested, 0/0/1, 1 model-attributed. See
docs/reviews/model-scorecard.md.

### Recommendation

The one finding is a Minor, which the skill alone would pass. The decider wants nothing left open in this review, so it
goes back to the builder, and the ADR and its feat row stay at `Reviewing` and `reviewing` until the next round. Fix the
gateway scenario's test in `pkg/funcd`; the next round reruns only what that touches and stamps the ADR. The PR closes
#1553 (`Fixes #1553`).
