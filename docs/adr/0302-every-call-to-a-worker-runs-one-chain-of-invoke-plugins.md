# ADR-0302: Every call to a worker runs one chain of invokePlugins

- **Status**: Reviewing 2026-10-30; Accepted 2026-10-30 by an `adr-batch` run the decider started ("then adr-batch till completion");
  judged twice: round 1's Blocker (the by-name order) and two Majors folded; round 2 found no Blocker or Major,
  and its three Minors on the refusal's own answer, the client mapping and the error handler's order are folded
- **Date**: 2026-10-30
- **Deciders**: green-0-rabbit
- **Tags**: gateway, data-plane, workflow, eventing
- **Realizes**: [FEAT-0006/F218](../feat/0006-feat-ingress-hardening.md) (one call lifecycle for every call to a worker)
- **Refines**: [ADR-0013](0013-gateway-ingress-httputil-primary.md): its `Middleware` chain stays the request-level seam.
- **Relates to**: the steps of Decision 4 · the callers [ADR-0064](0064-fn-to-fn-rpc-links.md),
  [ADR-0094](0094-workflow-engine-core.md), [ADR-0108](0108-eventsource-v2-named-events.md), [ADR-0214](0214-app-hooks.md)
  · [ADR-0301](0301-funcd-defines-each-contract-rule-once.md) · [#1345](https://github.com/pyvvo/funcd/issues/1345), [#1339](https://github.com/pyvvo/funcd/issues/1339)

## Context & Need

funcd's gateway has two layers. ADR-0013's edge chain wraps every request before routing, so it never knows which
function is called. The per-call steps are written in sequence in `serveFunction`
(`internal/dataplane/dataplane.go:229-308`), and no per-call step sees the worker's answer: the activator's
ReverseProxy writes it to the caller (`internal/activator/activator.go:487`). Three more paths reach a worker without
`serveFunction`: the workflow dispatcher (`internal/workflow/dispatch.go:131-154`) and the sensor invoker, which serves
Sensor actions and App hooks (`internal/sensor/invoker.go:45-78`; `pkg/funcd/funcd.go:1125` and `:1396`), wake the
function and POST to it themselves. So a check that must hold on every call, such as the contract check that #1339
moves into funcd, has no place to run.

The purpose: one ordered chain of built-in steps, invokePlugins, that every call to a worker runs, at three points:
before any answer depends on the Function, before the worker is called, and after it answers. The steps of
`serveFunction` become its first plugins, and no answer changes.

## Scenarios

- `scenario: a-refused-call-wakes-nothing` — Given a Function scaled to zero and a plugin whose Before refuses, When it
  is called from the listener, as a workflow step and as a sensor action, Then each call gets the refusal and the
  Function is never scaled up.
- `scenario: every-caller-runs-the-chain` — Given a plugin that records each call's caller, When the Function is
  called from the listener, by another Function, as a workflow step, as a sensor action and as an App hook, Then the
  plugin recorded each call's caller before the worker received it.
- `scenario: an-after-plugin-replaces-the-answer` — Given a plugin whose After refuses an answer whose body is
  `{"bad":true}`, When the worker answers that body on each path, Then the caller gets the refusal instead; When the
  worker answers another body, Then it reaches the caller unchanged.
- `scenario: an-unread-answer-streams` — Given an http Function that streams server-sent events and an After plugin
  that does not read the body, When an external call arrives, Then the first event reaches the caller before the
  stream ends.
- `scenario: the-by-name-form-keeps-its-stance` — Given an `open` namespace and a Function with
  `ingress.auth: authenticated` served by name, When an anonymous call arrives at `/function/<name>`, Then it gets 401
  and the Function is never woken; Given an `authenticated` namespace, When an anonymous call names a Function that
  does not exist, Then it gets 401, not 404.

## Scope

In: the `internal/invoke` chain, the seven steps of `serveFunction` as its plugins, and its three entries.

Out:
- a new check: the contract plugin is the next ADR, which closes #1339;
- plugins a user configures or writes: FEAT-0006 chose "typed fixed policy fields, not a plugin registry", and an ADR
  that wants a registry supersedes that narrowing first;
- the edge chain, unchanged; the static and Lake upstream backends (`serveStatic`, `serveUpstream`), which call no
  worker;
- the dispatcher's spec-as-grant and both clients' `RefuseHTTP` check, which decide whether a call is made and stay
  before the chain.

## Constraints & Decision drivers

- Scale-to-zero ([ADR-0016](0016-activator-scale-to-zero.md), [ADR-0033](0033-data-plane-serving-and-trigger-wake.md)):
  a call refused before the worker never wakes it.
- Streaming stays first-class (ADR-0013 Decision 3): an answer is buffered only when a plugin reads its body.
- FEAT-0006: the gateway proxies, the activator wakes and the PDP decides, so plugins call the PDP and leave waking
  to the activator; one binary, in process.
- The decider's answers (2026-10-30): built-in plugins at points before and after the call; every call that reaches a
  worker runs the chain; the existing steps move in now; the name invokePlugin.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **Typed plugins at three points, one chain run by three entries** | The order rules are explicit; no wake on refusal by construction; fits the client paths | A second seam beside ADR-0013's | **chosen** |
| ADR-0013's `Middleware`, built per function | No new type | An after-step must wrap the ResponseWriter, sees the activator's own 503 as the worker's, and covers only calls through the handler | rejected |
| The dispatcher and sensor call the data plane's handler in process, as fn-to-fn does | One HTTP entry | Changes their transport and their error kinds: a gone pin's NotFound becomes a 404 status (`activator.go:262`) | rejected |
| Two points, the Function read lazily by the first plugin that needs it | Simpler | A missing Function in a held namespace would answer 503 instead of today's 404 | rejected |
| Keep `serveFunction`'s steps inline; the chain only for new checks | Smaller change | Two places hold per-call logic | rejected (decided: move them now) |

## Decision

1. **`internal/invoke` holds the chain.** An invokePlugin is a step compiled into funcd. It names itself, says which
   calls it applies to (by caller and by the call's facts, asked anew at each point), and runs at one or more points:
   - **Admit**: the target and its auth stance are known; no answer has depended on the Function yet.
   - **Before**: the entry has resolved the Function; the worker has not been called or woken.
   - **After**: the worker gave its final answer; the caller has not seen it.

   At each point the plugins run in the chain's order, and the first refusal stops that point. A refusal at Admit or
   Before ends the call: no worker runs, so nothing wakes. A refusal at After replaces the worker's answer.
2. **One chain, built once** by `pkg/funcd`, in this order: authn, hold, rate, credentials, prefix, envelope, deadline.
   A later ADR appends its plugins.
3. **Three entries run it.**
   - The data plane, Route form and fn-to-fn calls: it clones the inbound request into the `Call`, runs Admit (for an
     external call with the Route's stance, before any store read), reads the Function (404 when it is missing) and
     fills the observ label, runs Before, sets the remainder path and the `FunctionRef`, and calls the activator with
     the answer hook, which runs After.
   - The data plane, by-name form ([ADR-0241](0241-one-exposure-shape.md) Decision 4): the Function read comes
     first, because the stance comes from its `ingress.auth`. A Function that is missing or not served by name gets
     Admit with the namespace's stance, then 404. A served one gets Admit with its own stance, the observ label, then
     Before, with no second read.
   - The workflow dispatcher and the sensor invoker, after spec-as-grant and `RefuseHTTP`: they build the request
     before the wake, run Admit and Before, wake, set the request's URL to the upstream, POST, and run After on the
     response. A refusal with its own status and body goes through the client's existing mapping of a worker's answer,
     as if the worker had answered it. A problem refusal maps by its status: the dispatcher returns
     `Permanent(refusal)` for a 4xx and the refusal itself, retryable, for a 5xx; the sensor invoker returns
     Unavailable wrapping it. `HTTPInvoker.Caller` tells an App hook from a Sensor action, which an unset field means.
4. **The moved steps keep their rules and their callers:**

   | Plugin | Point | Callers | Rule kept |
   |---|---|---|---|
   | authn | Admit | External | [ADR-0113](0113-edge-authn-pep.md): before any answer that depends on the Function; fail closed with no PEP |
   | hold | Before | External, FnToFn | [ADR-0290](0290-a-namespace-held-for-its-data-refuses-calls.md): 503 with Retry-After 30, after authentication |
   | rate | Before | External | [ADR-0164](0164-rate-limit-per-target.md): one bucket per resolved Function; 429 with Retry-After |
   | credentials | Before | External, stance `authenticated` | [ADR-0171](0171-static-credential-list.md) Decision 5: no Authorization or X-Api-Key |
   | prefix | Before | External, to an http Function | [ADR-0243](0243-http-functions.md) Decision 3: X-Forwarded-Prefix |
   | envelope | Before | External, not a bodiless upgrade | [ADR-0134](0134-gateway-normalized-invoke-body.md), [ADR-0181](0181-edge-forwards-bodiless-upgrades.md): 413 past 1 MiB |
   | deadline | Before | External | [ADR-0151](0151-external-invoke-deadline.md): `spec.timeout`, else `invoke.defaultTimeout` |
5. **An answer is read only on demand.** After sees the status and the headers of a final answer, never a 101, whose
   tunnel follows it. A plugin that needs the body asks for it with a byte limit, and only then is the answer
   buffered; an answer whose body no plugin reads streams as today. A body over the limit is the plugin's to refuse.
6. **A refusal answers** as an RFC 9457 problem from its error, with its headers (Retry-After) first. The one exception
   is a refusal that stands in for the worker's own documented answer: ADR-0301 keeps the contract's 422 and 500
   bodies as ADR-0058's `{error, details}` (ADR-0301, Contracts), so such a refusal carries its own status, headers
   (its Content-Type included) and body.

## Design model

None: the chain runs in sequence inside one call and adds no state, timer or actor (ADR-0272 Decision 1).

## Temporary workarounds

None.

## Contracts

```go
// internal/invoke (new): the chain every call to a worker runs (ADR-0302).
package invoke

type Caller int

const (
	External     Caller = iota + 1 // the data-plane listener, by name or through a Route
	FnToFn                         // a Function's own call (ADR-0064)
	WorkflowStep                   // a workflow step (ADR-0094)
	SensorAction                   // a Sensor's function action (ADR-0108)
	AppHook                        // an App's lifecycle hook (ADR-0214)
)

type Call struct {
	Caller    Caller
	Target    activator.FunctionRef // namespace, name and a step's revision pin (ADR-0190)
	Stance    v1.AuthMode           // External: the Route's, the Function's ingress.auth, or the namespace's
	Remainder string                // External and FnToFn: the function-relative path
	Received  time.Time             // External: when the listener received the call (ADR-0151)
	Request   *http.Request         // the data plane's clone, or a client path's own request; Before may replace it
	Function  *v1.Function          // set once the data plane read it (Decision 3); nil on the client paths
}

// Refusal ends a call before its worker, or replaces the worker's answer: a problem from Err, or, when Status is
// set, that status with Header and Body as they are (Decision 6).
type Refusal struct {
	Err    error
	Header http.Header
	Status int
	Body   []byte
}

func (r *Refusal) Error() string                     // Err's text, else the status and the body
func (r *Refusal) Unwrap() error                     // Err, so fault.KindOf reads a problem's kind
func (r *Refusal) StatusCode() int                   // Status, else fault.StatusOf(Err)
func (r *Refusal) WriteAnswer(w http.ResponseWriter) // the headers, then Body or the problem; no Content-Type of its own

type Answer struct {
	Status int
	Header http.Header
	// unexported: the response whose body Body reads
}

func NewAnswer(res *http.Response) *Answer         // Body puts what it read back on res.Body
func (a *Answer) Body(limit int64) ([]byte, error) // up to limit bytes; a longer body is a fault.PayloadTooLarge

// Plugin is an invokePlugin; it also implements one or more point interfaces. Applies reads the call's facts.
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
// internal/activator (new): ServeHTTP passes a final answer from the worker to check before the caller sees it, and
// writes the error check returns with its WriteAnswer method. The activator does not import internal/invoke.
func WithAnswerCheck(r *http.Request, check func(*http.Response) error) *http.Request

// internal/dataplane (new): the built-in plugins in Decision 2's order, and the chain Handler runs; without
// WithChain, Handler builds the built-in chain from its own arguments.
func Plugins(enf *authn.Enforcer, lim *limit.TargetLimiter, held hold.CallGate, defaultTimeout time.Duration) []invoke.Plugin
func WithChain(ch *invoke.Chain) Option

// internal/edge/limit (new): the per-target rate check without writing, safe on a nil limiter; Throttle stays, on Take.
func (l *TargetLimiter) Take(ns v1.NamespaceName, name v1.ObjectName) (retryAfter string, err error)

// internal/workflow and internal/sensor (new fields; a nil Chain runs nothing):
//   DispatchDeps.Chain *invoke.Chain
//   HTTPInvoker.Chain  *invoke.Chain
//   HTTPInvoker.Caller invoke.Caller // AppHook for the App reconciler's invoker; unset means SensorAction

// api/fault (new): the HTTP status WriteProblem writes for err.
func StatusOf(err error) int
```

The chain consumes the steps' existing inputs (the PEP, the target limiter, the call gate, `invoke.defaultTimeout`)
and adds no config key, field, flag or dependency.

## Implementation plan

1. `internal/invoke/invoke.go`: the Contracts; `api/fault/problem.go`: `StatusOf`, from `WriteProblem`'s table.
2. `internal/dataplane/plugins.go`: the seven plugins, each moving its code out of `serveFunction` with its problem
   text unchanged, and `Plugins`. `dataplane.go`: Decision 3's two orders; the answer hook returns nil when After
   returns a nil `*Refusal`; `WithChain`.
3. `internal/edge/limit/limit.go`: `Take`, returning `writeTooMany`'s Retry-After value; `Throttle` written on it.
4. `internal/activator/activator.go`: `WithAnswerCheck`; `forward`'s `ModifyResponse` restores the edge headers, then
   runs the check on a final answer; its error handler writes a `WriteAnswer` error first, before its deadline and
   upstream-failure branches.
5. `internal/workflow/dispatch.go`, `internal/sensor/invoker.go`: Decision 3's client order and mapping.
6. `pkg/funcd/funcd.go`: one `limit.TargetLimiter` and one chain from `dataplane.Plugins`, passed to the data plane,
   the dispatcher and both sensor invokers (`Caller` AppHook at :1396).
7. Tests: `internal/invoke` unit tests (the order; a refusal stops its point; `Applies` asked at each point; After
   replaces; `Body` limits and keeps the body; both forms of `WriteAnswer`, `StatusCode` and `Error`); one test per
   Scenario named `TestScenario` plus its name in CamelCase, the first three once per entry (`internal/dataplane`,
   `internal/workflow`, `internal/sensor`).
8. `just ci` green. Definition of done: every Scenario test passes, and the existing `internal/dataplane`,
   `internal/workflow`, `internal/sensor` and `internal/edge/limit` tests pass unchanged.

## Review checklist

- [ ] `internal/invoke` matches the Contracts.
- [ ] The chain's order is authn, hold, rate, credentials, prefix, envelope, deadline, at Decision 4's points and on
      its callers; each entry runs Decision 3's order, by-name form included; `serveFunction` keeps none of their code.
- [ ] The dispatcher and both sensor invokers run the same chain around their wake, with Decision 3's mapping.
- [ ] The activator runs the check only on a final answer and writes a `WriteAnswer` error before any other branch.
- [ ] Each Scenario has its named, passing test(s); no new config key, API field, CLI flag or dependency.

## Consequences

- Positive: a check written once covers all five callers, in one ordered list; a refused call wakes nothing.
- Negative: a second seam beside ADR-0013's chain. On the client paths `Call.Function` is nil, so a plugin that needs
  the spec there reads it itself, a pinned step's from its Revision (ADR-0190).
- Risks accepted: a plugin that reads an answer's body holds up to its limit in funcd's memory, and waits for that many
  bytes or the end of the body; the ADR-0151 deadline bounds only the answer's headers.

## Open questions

None.

## References

- Issues [#1345](https://github.com/pyvvo/funcd/issues/1345) (F218) and [#1339](https://github.com/pyvvo/funcd/issues/1339),
  with the refine brief of 2026-10-30 recorded on #1339.
- [FEAT-0006](../feat/0006-feat-ingress-hardening.md): the survey of APISIX and Pingora, and the narrowing "typed
  fixed policy fields, not a plugin registry"; Apache APISIX 3.19,
  [Plugin](https://apisix.apache.org/docs/apisix/terminology/plugin/), the model the decider pointed at.
- Go `net/http/httputil.ReverseProxy`: an error `ModifyResponse` returns goes to `ErrorHandler`.
