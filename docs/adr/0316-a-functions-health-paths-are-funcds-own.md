# ADR-0316: A Function's /health/* paths are funcd's own

- **Status**: Accepted (2026-11-05); the decider delegated acceptance to the session driving this work, on the conditions
  that it decides only the behavior they chose, that no Blocker or Major stays open and that Open questions reads `None.`.
  Judged once: no Blocker, and its three Majors are folded
- **Date**: 2026-11-05
- **Deciders**: green-0-rabbit
- **Tags**: edge, data-plane, health, shim
- **Realizes**: [FEAT-0006/F230](../feat/0006-feat-ingress-hardening.md) (a Function's health paths are funcd's own)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0316` back-link: [ADR-0110](0110-route-v2-declarative-edge-exposure.md)
  Decision 6.2 and [ADR-0243](0243-http-functions.md) Decision 3 — the edge forwards a remainder unless it lies under
  `/health/`.
- **Relates to**: [ADR-0215](0215-built-in-health.md) Decisions 1, 4 and 5 ·
  [ADR-0241](0241-one-exposure-shape.md) Decision 4 ·
  [ADR-0302](0302-every-call-to-a-worker-runs-one-chain-of-invoke-plugins.md) Decisions 1 and 2 ·
  [ADR-0030](0030-function-execution-runtime-shim-node.md) §1 · [ADR-0046](0046-pooling-placement-policy.md) Decision 5 ·
  [ADR-0113](0113-edge-authn-pep.md) · [ADR-0164](0164-rate-limit-per-target.md) ·
  [ADR-0290](0290-a-namespace-held-for-its-data-refuses-calls.md)

## Context & Need

A worker's `/health/*` paths answer funcd's probes: liveness, readiness and a pool host's members (ADR-0030 §1, ADR-0215
Decisions 1, 4 and 5). Those probes call the worker at its own address, not through the data plane
(`internal/function/liveness.go`, `internal/function/readiness.go`, `probeMembers` in `internal/function/function.go`). A
provider's engine probe does the same (`internal/provider/probe.go`), on the engine's own path. The data plane forwards a
call's path remainder to the worker (ADR-0110 Decision 6.2, ADR-0243 Decision 3), and neither excepts a remainder under
`/health/`.

**Purpose.** The data plane answers such a call itself: from the listener nothing reads these paths, and funcd's probes read
them on the worker.

## Scenarios

The Function `api` is `open` and `Ready`, and `idle` is scaled to zero, unless a scenario says otherwise.

- `scenario: a-health-path-answers-404` — *When* a call with any method names `/function/api/health/readiness`,
  `/function/idle/health/liveness`, `/function/api/health/members`, `/function/api/health/other`,
  `/function/ghost/health/readiness` (no such Function) or `/function/closed/health/readiness` (`ingress.enabled: false`), or
  `/api/health/readiness` through a Prefix Route `/api`, or `/health/readiness` through a Prefix Route `/`, *Then* it answers
  404 `no route for "<path>"`, no worker receives it and `idle` stays at zero.
- `scenario: a-health-path-is-read-resolved` — *When* the remainder is `/./health/readiness`, `/x/../health/readiness`,
  `//health/readiness`, `/health//readiness`, `/%68ealth/readiness`, `/%2e%2e/health/readiness`, `/health/` or
  `/health/readiness/`, *Then* it answers the same 404.
- `scenario: the-stance-answers-first` — *Given* a Function whose stance is `authenticated`, by name or through a Route,
  *When* a call under `/health/` has no credential, *Then* it answers 401; a credential for another namespace, 403; a
  valid one, 404. Nothing wakes.
- `scenario: other-paths-reach-the-worker` — *When* the remainder is `/health`, `/health/.` (which reads as `/health`),
  `/healthz`, `/health-check`, `/devices/health/readiness` or `/`, or `/health/readiness` through a Prefix Route `/health`,
  which leaves `/readiness`, or through an Exact Route at that path, which leaves `/`, *Then* the worker receives it as today,
  so an http app's own `/health` route keeps working.
- `scenario: another-function-keeps-the-path` — *When* another Function calls `/health/readiness` on an http Function over
  its link (`ctx.fetch`, ADR-0243 Decision 6), *Then* the call reaches the worker as today.
- `scenario: funcds-probes-read-the-worker` — *Given* real Node and Python workers, *When* funcd probes them, *Then* the
  Function turns `Ready` and a binding that fails its check still reads `DependencyNotReady` (ADR-0215), while
  `/function/<name>/health/readiness` from the listener answers 404.

## Scope

**In**: a call from the listener on the data plane's Function paths, by name and through a Route.

**Out**: `serveStatic` and `serveUpstream`, which call no worker; a call from another Function, a workflow step, a
Sensor action and an App hook, which do not come from the listener; the shims and their paths, unchanged; a setting
that opens these paths.

## Constraints & Decision drivers

- The decider's choice: every path under `/health/` answers 404, on the path form and on a Route, and `/health` alone
  and any other path stay as they are.
- A stance answers before anything that depends on the Function (ADR-0113, ADR-0241 Decision 4).
- A check that holds on a call is an invokePlugin, and a refusal before the worker wakes nothing (ADR-0302 Decision 1).
- funcd's probes must not change: a readiness probe has 100 ms (ADR-0215).

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. An invokePlugin at Admit, after authn** | ADR-0302's seam; reads no store; takes no rate token and asks no hold | one more plugin; one path clean per external call | **Chosen** |
| B. The same plugin at Before | sits beside the other per-call checks | the answer then depends on the Function, the hold and the rate token | Rejected |
| C. The shim tells funcd's probe from a forwarded call | no edge rule | the shims and every built image change; a contract change for a path nobody else uses | Rejected |
| D. An After plugin drops the answer's body | leaves the path open | wakes a worker to answer nothing | Rejected |
| E. Name the three shim paths at the edge | an http app keeps other routes under `/health/` | the edge must follow each shim path; the decider chose the whole prefix | Rejected |

## Decision

1. **The rule.** A call from the listener, by name or through a Route, with any method, whose function-relative remainder
   lies under `/health/` answers 404 `no route for "<path>"`, whatever the Function's state. It wakes nothing and reaches
   no worker. `/health` itself does not lie under it.
2. **The seam.** A new invokePlugin, `health-paths`, runs at Admit on `External` calls only. It comes last in
   `dataplane.Plugins` (ADR-0302 Decision 2) and so runs after authn: 401 and 403 answer first. It reads no store, answers
   before the Function read of the Route form, and takes no per-target token (ADR-0164) and asks no hold (ADR-0290), which
   run at Before. The by-name form's Admit for a missing or closed Function runs it too, so all of them give this one 404.
3. **It reads the resolved remainder.** The check is made on the remainder with its dot segments and repeated slashes
   resolved as a path rooted at `/`, keeping a trailing slash: the activator's `rootedClean`, which `rebase` already uses.
   So what a `/` rule leaves, `health/readiness`, counts, and `/health/.` reads as `/health`. It reads the remainder, not the
   upstream's path, so a pooled Function, whose upstream keeps `/function/<name>` (ADR-0046 Decision 5), answers the same.
4. **funcd's probes are unchanged.** They go straight to the worker, and `GET /health/dependencies` is served on the invoke
   socket, not on the data plane (ADR-0215 Decision 3). ADR-0243 Decision 4 stands: the shim's `/health/*` shadows an http
   app's on the worker.

## Design model

None: a check on one call, with no state, timer, race or delivery (ADR-0272 Decision 1).

## Behavior rows

[Calls](../behavior/calls.md): adds the rows `A path under /health/` (404 after the stance, no wake, `decided`),
`An http app's route under /health/` (no answer through the edge, `accepted`) and `A health path over a link` (reaches the
worker, `accepted`); the machine's edge `Admit --> Refused` gains `a health path`; the row
`A GET or another path on a cloudevent Function` now leaves out `/health/`. [Health](../behavior/health.md): adds the row
`Who reads a worker's health paths` (funcd's probes on the worker's address, and a Function over a link, `decided`).

## Temporary workarounds

None.

## Contracts

```go
// internal/dataplane/plugins.go (new): appended to Plugins.
type healthPathsPlugin struct{}

func (healthPathsPlugin) Name() string                { return "health-paths" }
func (healthPathsPlugin) Applies(c *invoke.Call) bool // c.Caller == invoke.External and the remainder is under /health/
func (healthPathsPlugin) Admit(ctx context.Context, c *invoke.Call) *invoke.Refusal // fault.NotFoundf(callOp, "no route for %q", c.Request.URL.Path)

// internal/activator: rootedClean is exported, unchanged.
func RootedClean(p string) string
```

| Consumes | Writes | Exposes |
|---|---|---|
| `invoke.Call`'s caller and remainder | nothing | a 404 problem; no config key, field, flag or dependency |

## Implementation plan

1. `internal/activator/activator.go`: export `RootedClean`. `internal/dataplane/plugins.go`: the plugin, appended to
   `Plugins` and to its doc comment; `TestPluginsKeepTheirCallers` gains it.
2. `internal/dataplane/healthpaths_test.go`: one `TestScenario…` per scenario above but the last, named after it, each with
   `t.Parallel()`, on a recording upstream and the spy scaler; a pooled upstream, a rate token and a held namespace each get
   a test too. `pkg/funcd/health_e2e_test.go`: `TestScenarioFuncdsProbesReadTheWorker`, on the real shims.
3. The rows of *Behavior rows*. funcd-typescript's README, one docs-only line on what a path under `/health/` answers from
   the edge, with no release and no `go get`.
4. Done when `just ci`, `scripts/agent/gate.sh` and each Scenario's test pass, a main-versus-branch bench of the call path
   shows the cost, and no shim, probe or existing test other than `TestPluginsKeepTheirCallers` changes.

## Review checklist

- [ ] `health-paths` implements only Admit, comes after authn and applies only to `External` calls.
- [ ] The check reads the remainder through `RootedClean`; `/health` and `/healthz` pass, `/health/` does not.
- [ ] `health-paths` reads no store and, on the Route form, answers before the Function read; a refused call takes no
  per-target token (ADR-0164), asks no hold, wakes nothing and reaches no worker.
- [ ] The probes, the shims and `/health/dependencies` are untouched.
- [ ] One test per Scenario, named after it; the chapters' rows match *Behavior rows*.

## Consequences

- **Positive**: from the listener nothing reads these paths, and one rule covers both address forms, solo or pooled.
- **Negative**: an http app's own routes under `/health/`, such as `/health/db`, stop answering through the edge; the app
  moves them, for example to `/healthz/db` or `/status/db`. Each external call pays for one path clean: 16 to 66 ns and 1 to
  3 allocations on the judge's prototype.
- **Risks accepted**: a call from another Function still reaches the paths, over a link it holds; a call to `/health`
  alone wakes a cold worker, as any other path does.

## Open questions

None.

## References

- [ADR-0215](0215-built-in-health.md) · [ADR-0243](0243-http-functions.md) · `internal/dataplane/dataplane.go`
  (`serveFunction`, `parseFunctionPath`) · `internal/activator/activator.go` (`rebase`).
