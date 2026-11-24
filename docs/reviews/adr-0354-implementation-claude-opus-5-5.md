## Verdict: changes requested — 0 blockers, 0 majors, 1 minor (ADR-0354 implementation, model: claude-opus-5-5)

This report reviews ADR-0354's part of the branch `feat/tunnel-edge` at 03aa1e38: the commit 03aa1e38 itself, after
ADR-0352's 78eb3917 ([adr-0352-implementation-claude-opus-5-5.md](adr-0352-implementation-claude-opus-5-5.md)) and
ADR-0353's 48b7a41a ([adr-0353-implementation-claude-opus-5-5.md](adr-0353-implementation-claude-opus-5-5.md)). The
reviewer did not write the work, and every check below was run, not read.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

- **A doc comment moved onto the wrong function** · model · `cmd/funcd/main.go:1107`. The new `trustedProxies` was
  inserted between `limitsConfig`'s doc comment and its declaration. The line "limitsConfig maps server.limits to the
  data-plane limiter's Config (ADR-0112, ADR-0164)" now opens `trustedProxies`'s comment (`:1107-1110`), and `func
  limitsConfig` (`:1127`) has none. The linters do not flag it (both runs: 0 issues). Fix: move that line back above
  `func limitsConfig`.

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
  - a `cidrs` entry matching the worker subnet: `TestProxiesClient`, `TestScenarioAFunctionCannotForge`;
  - the caller's `X-Forwarded-For` and `X-Real-IP` kept for a worker: `TestBackendsGetTheClientAddress`,
    `TestScenarioTunnelClientFromHeader`, `TestScenarioForwardedForWalk`, `TestScenarioAFunctionCannotForge`.
- **Decision 1, the key** (`internal/platform/config/config.go`). `server.trustedProxies` is file-only (`env:"-"`).
  `validateTrustedProxies` refuses an entry with both lists or neither, a range that does not parse, and a header that
  is not a field name, and each error names its key. `TestTrustedProxies` covers each case, and the example config and
  the guide's running-funcd page document the key in block YAML.
- **Decisions 2 and 3, the client address** (`internal/edge/proxies.go`). The first entry that matches the peer decides.
  A header counts when it holds exactly one IP address, else the peer is the client. The walk reads `X-Forwarded-For`
  from the right past trusted ranges: the leftmost address when every one is trusted, the peer at a bad element. A
  `cidrs` entry never matches the worker subnet. An `addonKinds` entry matches only an engine whose stored object's kind
  is the entry's, spelled the same, read through `egress.Sources` and the store (`pkg/funcd/bridge_edge.go`,
  `clientAddress`). `TestProxiesClient` holds the table; `TestScenarioAFunctionCannotForge` puts a `10.0.0.0/8` entry
  over the worker subnet.
- **Decision 4, its uses.**
  - The client middleware runs ahead of the access log and the limiter. Both read `edge.ClientOf`
    (`internal/edge/limit/limit.go`, `internal/edge/observ/observ.go`; `TestRateKeyIsTheClientAddress`).
  - For a worker, the data plane drops the caller's `X-Forwarded-For`, `Forwarded` and `X-Real-IP`. It also rewrites the
    clone's `RemoteAddr` to the client, so the activator's proxy writes the client address alone. A fn-to-fn call is
    left alone. An upstream entry gets the same `X-Forwarded-For` (`internal/dataplane/dataplane.go`, `asClient`;
    `TestBackendsGetTheClientAddress`).
  - With no entry, the middleware is a pass-through, and the client is the peer.
  - The start-up warning for an `addonKinds` entry with no engine index is wired (`warnUnmatchedKinds`;
    `TestAnAddonKindsEntryWithNoEngineIndexWarns`).
- **Decision 5.** No header sets the scheme or the host (`TestScenarioSchemeNeverFromHeader`, through the sign-in's
  callback URL).
- **The docs.** The calls.md rows, security.md's row with ADR-0171 Decision 5's last sentence, the guide's
  running-funcd page and `examples/funcdconfig.yaml`.
- **Tracking.** The ADR at `Reviewing`, its substance unchanged since acceptance; F272 at `reviewing`; the back-links in
  ADR-0164 and ADR-0171. The plan names no lane, and the work needs none: no step depends on the runtime.

### Definition of Done

13 / 13 items hold: the 4 of the ADR's review checklist, and the 9 generic items that apply. The plan names no lane, and
none is needed.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0354 (implementation) → changes-requested, 0/0/1, 1 model-attributed. See
docs/reviews/model-scorecard.md.

### Recommendation

The one finding is a Minor, which the skill alone would pass. The decider wants nothing left open in this review, so it
goes back to the builder, and the ADR and its feat row stay at `Reviewing` and `reviewing` until the next round. Fix the
doc comment in `cmd/funcd/main.go`; the next round reruns only what that touches and stamps the ADR. The PR closes #1554
(`Fixes #1554`).
