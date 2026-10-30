# ADR-0302: Every call to a worker runs one chain of invokePlugins

- **Status**: Proposed
- **Date**: 2026-10-30
- **Deciders**: green-0-rabbit
- **Tags**: gateway, data-plane, workflow, eventing
- **Realizes**: [FEAT-0006/F218](../feat/0006-feat-ingress-hardening.md) (one call lifecycle for every call to a worker)
- **Refines**: [ADR-0013](0013-gateway-ingress-httputil-primary.md): its `Middleware` chain stays the request-level seam;
  this ADR adds a per-call seam beside it.
- **Relates to**: the ADRs of the steps Decision 4 moves, each rule unchanged · the callers
  [ADR-0064](0064-fn-to-fn-rpc-links.md), [ADR-0094](0094-workflow-engine-core.md) and
  [ADR-0108](0108-eventsource-v2-named-events.md) · [ADR-0301](0301-funcd-defines-each-contract-rule-once.md), whose
  rules a later plugin applies · issues [#1345](https://github.com/pyvvo/funcd/issues/1345) (this feature) and
  [#1339](https://github.com/pyvvo/funcd/issues/1339)

## Context & Need

funcd's gateway has two layers. The edge chain (ADR-0013's `gateway.Middleware`: WriteStall, Recover, RequestID,
observ, limit, shape; `pkg/funcd/funcd.go:1561`) wraps every request before routing, so it never knows which function
is called. The per-call steps are written in sequence in `serveFunction` (`internal/dataplane/dataplane.go:229-308`):
authn, the Function read, the observ label, the hold, the per-target rate, the credential strip, X-Forwarded-Prefix,
the envelope and the deadline, then the activator. No per-call step sees the worker's answer: the activator's
ReverseProxy writes it to the caller (`internal/activator/activator.go:487`), and only `KeepEdgeHeaders` touches it.
Two more paths reach a worker without `serveFunction`: the workflow dispatcher (`internal/workflow/dispatch.go:131-154`)
and the sensor invoker (`internal/sensor/invoker.go:45-78`) wake the function and POST to it themselves. So a check
that must hold on every call, such as the contract check that #1339 moves into funcd, has no place to run.

The purpose: one ordered chain of built-in steps, invokePlugins, that every call to a worker runs, at three points:
before anything about the Function is read, before the worker is called, and after it answers. Its callers are the
data plane (external and fn-to-fn calls), the workflow dispatcher and the sensor invoker. The steps of `serveFunction`
become its first plugins, and no answer changes.

## Scenarios

- `scenario: a-refused-call-wakes-nothing` — Given a Function scaled to zero and a plugin whose Before refuses, When an
  external call arrives, Then the caller gets the refusal as a problem and the Function is never scaled up.
- `scenario: every-caller-runs-the-chain` — Given a plugin that records each call's caller, When the Function is
  called from the listener, by another Function, as a workflow step and as a sensor action, Then the plugin recorded
  `External`, `Function`, `WorkflowStep` and `SensorAction`, each before the worker received the call.
- `scenario: an-after-plugin-replaces-the-answer` — Given a plugin whose After refuses an answer whose body is
  `{"bad":true}`, When the worker answers that body on each of the four paths, Then the caller gets the refusal
  instead; When the worker answers another body, Then it reaches the caller unchanged.
- `scenario: an-unread-answer-streams` — Given an http Function that streams server-sent events and no plugin that
  reads answers, When an external call arrives, Then the first event reaches the caller before the stream ends.
- `scenario: authn-runs-before-the-read` — Given a Route with `auth: authenticated` to a Function that does not exist,
  When an anonymous call arrives, Then the caller gets 401, not 404, and the store is never asked for the Function.
- `scenario: the-moved-steps-keep-their-answers` — Given the data plane's tests of authn, holds, per-target rate, the
  credential strip, X-Forwarded-Prefix, the envelope, bodiless upgrades and deadlines, When they run on the chain,
  Then each passes unchanged.

## Scope

In: the `internal/invoke` chain (calls, answers, refusals, the plugin interfaces); the seven steps of `serveFunction`
as built-in plugins, in today's order and on today's callers; an answer hook in the activator; the dispatcher and the
sensor invoker running the chain around their own wake and POST; `pkg/funcd` building one chain for the three.

Out:
- a new check: the contract plugin is the next ADR, which closes #1339;
- plugins a user configures or writes: FEAT-0006 chose "typed fixed policy fields, not a plugin registry", and an ADR
  that wants a registry supersedes that narrowing first;
- the edge chain, unchanged; the static and Lake upstream backends (`serveStatic`, `serveUpstream`), which call no
  worker;
- the dispatcher's spec-as-grant and both callers' `RefuseHTTP` check, which decide whether a call is made and stay
  before the chain;
- a step's callers: the hold check stays off workflow steps and sensor actions, as today.

## Constraints & Decision drivers

- No answer changes: each moved step keeps its ADR's rule and its callers.
- Scale-to-zero ([ADR-0016](0016-activator-scale-to-zero.md), [ADR-0033](0033-data-plane-serving-and-trigger-wake.md)):
  a call refused before the worker never wakes it.
- Streaming stays first-class (ADR-0013 Decision 3): an answer is buffered only when a plugin reads its body.
- FEAT-0006's split: the gateway proxies, the activator wakes, the PDP decides. Plugins call the PDP and leave waking
  to the activator. Its narrowing: compiled-in, typed steps, configured by the spec fields that exist.
- The decider's answers (2026-10-30): built-in plugins at points before and after the call; every call that reaches a
  worker runs the chain; the existing steps move in now; the name invokePlugin.
- In process, one binary: ADR-0012 and ADR-0013 rejected a supervised APISIX.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **Typed plugins at three points, one chain run by three entries** | The order rules are explicit; no wake on refusal by construction; fits the client paths | A second seam beside ADR-0013's | **chosen** |
| ADR-0013's `Middleware`, built per function | No new type | An after-step must wrap the ResponseWriter, sees the activator's own 503 as the worker's, and covers only calls through the handler | rejected |
| The dispatcher and sensor call the data plane's handler in process, as fn-to-fn does | One HTTP entry | Changes their transport and their error kinds: a gone pin's NotFound becomes a 404 status | rejected |
| Two points, the Function read lazily by the first plugin that needs it | Simpler | A missing Function in a held namespace would answer 503 instead of today's 404 | rejected |
| Keep `serveFunction`'s steps inline; the chain only for new checks | Smaller change | Two places hold per-call logic | rejected (decided: move them now) |
| User-configured or user-written plugins (APISIX's full model) | Extensible | Contradicts FEAT-0006's narrowing; an API surface and a call-out per call | out of scope |

## Decision

1. **`internal/invoke` holds the chain.** An invokePlugin is a step compiled into funcd. It names itself, says which
   calls it applies to (by caller and by the call's facts), and runs at one or more points:
   - **Admit**: the target and the auth stance are known; nothing about the Function has been read.
   - **Before**: the entry has resolved the Function; the worker has not been called or woken.
   - **After**: the worker answered; the caller has not seen the answer.

   At each point the plugins run in the chain's order. A refusal at Admit or Before ends the call: no later plugin and
   no worker runs, so nothing wakes. A refusal at After replaces the worker's answer.
2. **One chain, built once** by `pkg/funcd`, in this order: authn, hold, rate, credentials, prefix, envelope, deadline.
   A later ADR appends its plugins. The data plane, the dispatcher and the sensor invoker get the same chain.
3. **Three entries run it.**
   - The data plane, for an external or fn-to-fn call: Admit; the Function read (404 when it is missing) and the
     observ label; Before; the remainder path and the `FunctionRef`; the activator, whose answer hook runs After. A
     call by name to a Function not served by name ([ADR-0241](0241-one-exposure-shape.md) Decision 4) runs Admit,
     then gets 404.
   - The workflow dispatcher, after spec-as-grant and `RefuseHTTP`: Admit, Before, its wake and POST, After on the
     response, then its own status mapping.
   - The sensor invoker, after `RefuseHTTP`: Admit, Before, its wake and POST, After, then its status mapping.

   On the dispatcher and sensor paths a refusal maps as the same status from the worker would: for a step, a 4xx is
   permanent and a 5xx retryable.
4. **The moved steps keep their rules and their callers:**

   | Plugin | Point | Callers | Rule kept |
   |---|---|---|---|
   | authn | Admit | External | [ADR-0113](0113-edge-authn-pep.md): before any store read; fail closed with no PEP |
   | hold | Before | External, Function | [ADR-0290](0290-a-namespace-held-for-its-data-refuses-calls.md): 503 with Retry-After 30, after authentication |
   | rate | Before | External | [ADR-0164](0164-rate-limit-per-target.md): one bucket per resolved Function; 429 with Retry-After |
   | credentials | Before | External, stance `authenticated` | [ADR-0171](0171-static-credential-list.md) Decision 5: no Authorization or X-Api-Key |
   | prefix | Before | External, to an http Function | [ADR-0243](0243-http-functions.md) Decision 3: X-Forwarded-Prefix |
   | envelope | Before | External, not a bodiless upgrade | [ADR-0134](0134-gateway-normalized-invoke-body.md), [ADR-0181](0181-edge-forwards-bodiless-upgrades.md): 413 past 1 MiB |
   | deadline | Before | External | [ADR-0151](0151-external-invoke-deadline.md): `spec.timeout`, else `invoke.defaultTimeout` |
5. **An answer is read only on demand.** After sees the status and the headers. A plugin that needs the body asks for
   it with a byte limit, and only then is the answer buffered; an answer whose body no plugin reads streams as today.
   A body over the limit is the plugin's to refuse.
6. **A refusal is an RFC 9457 problem**, written through `fault.WriteProblem` after the refusal's headers
   (Retry-After), as today.

## Design model

None: the chain runs its steps in sequence inside one call and adds no state, timer or actor. Each moved step keeps
its rule unchanged, and the dispatcher's and the sensor's delivery and retries are unchanged (ADR-0272 Decision 1).

## Temporary workarounds

None.

## Contracts

```go
// internal/invoke (new): the chain every call to a worker runs (ADR-0302).
package invoke

// Caller is who makes a call.
type Caller int

const (
	External     Caller = iota + 1 // the data-plane listener, by name or through a Route
	Function                       // a Function's own call (fn-to-fn, ADR-0064)
	WorkflowStep                   // a workflow step (ADR-0094)
	SensorAction                   // a Sensor's function action (ADR-0108)
)

// Call is one call to a function's worker.
type Call struct {
	Caller    Caller
	Target    activator.FunctionRef // namespace, name and a step's revision pin (ADR-0190)
	Stance    v1.AuthMode           // External: the stance the route resolved (ADR-0113); "" otherwise
	Remainder string                // External and Function: the function-relative path
	Received  time.Time             // External: when the listener received the call (ADR-0151)
	Request   *http.Request         // what the worker gets; a Before plugin may replace it
	Function  *v1.Function          // set by the data plane before Before; nil at Admit and on the other paths
}

// Refusal ends a call before its worker, or replaces the worker's answer. Unwrap returns Err, so fault.KindOf and
// fault.WriteProblem read its kind.
type Refusal struct {
	Err    error
	Header http.Header // set before the problem, e.g. Retry-After
}

func (r *Refusal) Error() string
func (r *Refusal) Unwrap() error
func (r *Refusal) ProblemHeader() http.Header  // read by the activator for an After refusal
func (r *Refusal) Write(w http.ResponseWriter) // the headers, then fault.WriteProblem(w, r.Err)

// Answer is the worker's answer at the After point.
type Answer struct {
	Status int
	Header http.Header
	// unexported: the response whose body Body reads
}

// NewAnswer wraps res; Body puts what it read back on res.Body, so the caller still gets the whole body.
func NewAnswer(res *http.Response) *Answer

// Body reads the answer's body, up to limit bytes; a longer body returns a fault.PayloadTooLarge.
func (a *Answer) Body(limit int64) ([]byte, error)

// Plugin is an invokePlugin; it also implements one or more of the point interfaces below. Applies reads the
// call's facts, never its body.
type Plugin interface {
	Name() string
	Applies(c *Call) bool
}
type AdmitPlugin interface{ Plugin; Admit(ctx context.Context, c *Call) *Refusal }
type BeforePlugin interface{ Plugin; Before(ctx context.Context, c *Call) *Refusal }
type AfterPlugin interface{ Plugin; After(ctx context.Context, c *Call, a *Answer) *Refusal }

// Chain runs its plugins in order. It is safe for concurrent use; a nil Chain runs nothing.
type Chain struct{ /* unexported */ }

func NewChain(plugins ...Plugin) *Chain
func (ch *Chain) Admit(ctx context.Context, c *Call) *Refusal
func (ch *Chain) Before(ctx context.Context, c *Call) *Refusal
func (ch *Chain) After(ctx context.Context, c *Call, a *Answer) *Refusal
```

```go
// internal/activator (new): ServeHTTP passes the worker's answer to check before the caller sees it; an error check
// returns is written instead, after the headers of an error that has a ProblemHeader method.
func WithAnswerCheck(r *http.Request, check func(*http.Response) error) *http.Request

// internal/dataplane (new): the built-in plugins in Decision 2's order, and the chain Handler runs; without
// WithChain, Handler builds the built-in chain from its own arguments.
func Plugins(enf *authn.Enforcer, lim *limit.TargetLimiter, held hold.CallGate, defaultTimeout time.Duration) []invoke.Plugin
func WithChain(ch *invoke.Chain) Option

// internal/edge/limit (new; Throttle is removed): the per-target rate check, without writing.
func (l *TargetLimiter) Take(ns v1.NamespaceName, name v1.ObjectName) (retryAfter time.Duration, err error)

// internal/workflow and internal/sensor (new fields; nil runs nothing): DispatchDeps.Chain, HTTPInvoker.Chain.

// api/fault (new): the HTTP status WriteProblem writes for err.
func StatusOf(err error) int
```

| Consumes | Exposes |
|---|---|
| The steps' existing inputs: the PEP, the target limiter, the call gate, `invoke.defaultTimeout` | `invoke.Chain` and the plugin interfaces; `dataplane.Plugins` and `WithChain` |
| No new config key, field, flag or dependency | Unchanged answers on every path |

## Implementation plan

1. `internal/invoke/invoke.go`: the Contracts. `Answer.Body` reads `limit+1` bytes and puts them back on the response.
2. `api/fault/problem.go`: `StatusOf`, from the table `WriteProblem` uses.
3. `internal/dataplane/plugins.go`: the seven plugins, each moving its code out of `serveFunction` unchanged, and
   `Plugins`. `dataplane.go`: `serveFunction` and `serveByName` run Admit, the read, Before, and the activator with
   `WithAnswerCheck`; `WithChain`.
4. `internal/edge/limit/limit.go`: `Take`; the rate plugin's refusal carries `writeTooMany`'s Retry-After.
5. `internal/activator/activator.go`: `WithAnswerCheck`; `forward`'s `ModifyResponse` restores the edge headers, then
   runs the check; its error handler sets a `ProblemHeader` error's headers before the problem.
6. `internal/workflow/dispatch.go`, `internal/sensor/invoker.go`: build the `Call` before the wake, run Admit and
   Before, wake, send `Call.Request` to the upstream, run After with `NewAnswer`, and map a refusal by
   `fault.StatusOf` as the worker's status.
7. `pkg/funcd/funcd.go`: one `limit.TargetLimiter` and one chain from `dataplane.Plugins`, passed to the data plane,
   the dispatcher and both sensor invokers.
8. Tests: `internal/invoke` unit tests (the order, a refusal stops its point, After replaces, `Body` limits and keeps
   the body); one test per Scenario named `TestScenario` plus its name in CamelCase. `every-caller-runs-the-chain`
   and `an-after-plugin-replaces-the-answer` have one test of that name per entry: `internal/dataplane` (External
   and Function), `internal/workflow` and `internal/sensor`. `the-moved-steps-keep-their-answers` is the existing
   `internal/dataplane` tests, unchanged.
9. `just ci` green. Definition of done: every Scenario test passes, no existing data-plane test changed, and no
   answer of any path changed.

## Review checklist

- [ ] `internal/invoke` matches the Contracts; a refusal at Admit or Before stops later plugins and the worker.
- [ ] The chain's order is authn, hold, rate, credentials, prefix, envelope, deadline, at Decision 4's points and on
      its callers; `serveFunction` keeps none of their code.
- [ ] authn runs before the Function read; the read and the observ label run between Admit and Before.
- [ ] The dispatcher and both sensor invokers run the same chain around their wake; a refusal maps by its status.
- [ ] An answer is buffered only when a plugin calls `Body`; the SSE scenario streams.
- [ ] The existing `internal/dataplane` tests pass unchanged; each Scenario has its named, passing test(s).
- [ ] No new config key, API field, CLI flag or dependency.

## Consequences

- Positive: every call to a worker runs one chain, so a check written once covers all four callers; the per-call
  order is one list in one place; a refused call wakes nothing on any path.
- Negative: a second seam beside ADR-0013's chain. The dispatcher and the sensor build their request before the wake,
  to run Before on it.
- Risks accepted: a plugin that reads an answer's body holds it in funcd's memory, up to the limit it asks for; the
  first such plugin (the contract check) sets that limit.

## Open questions

- Whether holds also apply to workflow steps and sensor actions: a separate decision; this ADR keeps today's callers.
- A user-configured plugin list: only after an ADR supersedes FEAT-0006's narrowing.

## References

- Issue [#1345](https://github.com/pyvvo/funcd/issues/1345) (F218); issue [#1339](https://github.com/pyvvo/funcd/issues/1339)
  and the refine brief of 2026-10-30 recorded on it.
- [FEAT-0006](../feat/0006-feat-ingress-hardening.md): the survey of APISIX and Pingora, and the narrowing "typed
  fixed policy fields, not a plugin registry".
- Apache APISIX 3.19, [Plugin](https://apisix.apache.org/docs/apisix/terminology/plugin/): phases run by priority, and
  a request ended early still runs `log`.
- Go `net/http/httputil.ReverseProxy`: an error `ModifyResponse` returns goes to `ErrorHandler`.
