# ADR-0216: App tests — opt-in checks run on demand

- **Status**: Accepted (2026-10-10)
- **Date**: 2026-10-08 (self-accepted 2026-10-10 under adr-batch after drafting, three-lens judging with a skeptic per
  finding, cross-ADR audits and alignment with the disaster-recovery ADRs)
- **Deciders**: green-0-rabbit
- **Tags**: app, tests, api, subresource, cli, config
- **Realizes**: [FEAT-0010/F119](../feat/0010-feat-apps.md) (App tests: opt-in checks run on demand)
- **Source**: Decision 16, the decisions-table row "Proof that the App behaves", the scenario `app-test-on-demand`, the
  contracts `AppTest`, `AppTestHTTP` and `AppTestResult` and the review item "Nothing runs `spec.tests` except
  `funcdctl app test`" of the [App design note](../reports/app-design.md) (decider, 2026-10-06/07).
- **Relates to**: [ADR-0118](0118-eventing-dead-letter-queue.md) (replay, the imperative subresource) ·
  [ADR-0109](0109-sensor-event-action-binder.md) (the Sensor's invoker) ·
  [ADR-0033](0033-data-plane-serving-and-trigger-wake.md) (wake on call) · [ADR-0113](0113-edge-authn-pep.md) (edge
  authn) · [ADR-0098](0098-typed-workflow-edges.md) (run-start input check) ·
  [ADR-0194](0194-api-duration-strings.md), [ADR-0196](0196-utc-millisecond-timestamps.md) ·
  [ADR-0206](0206-restore-and-held-boot.md) (Accepted, the platform hold, `hold.Gate`) · [ADR-0214](0214-app-hooks.md)
  (Proposed, F117: the shared `Invoker` port, the held-pass rule, the GC order) ·
  [ADR-0212](0212-app-self-heal-and-pause.md) (Proposed, F115 pause) · [ADR-0219](0219-app-requirements.md) (Proposed,
  F122 wait) · [ADR-0220](0220-dry-run-engine.md) (Proposed, F123)
- **Extends (additive)**: [ADR-0199](0199-app-resource.md) (Implemented): `AppSpec.tests` and its `App.Validate`
  checks; no part is made from it. [ADR-0200](0200-app-revisions.md) (Implemented): `AppRevisionStatus.tests` and the
  condition `Tested`, written by a second entry of the App reconciler, so its Decision 1 (only the App reconciler
  writes an AppRevision) and Decision 2 (GET-only API) hold. [ADR-0170](0170-owner-garbage-collector.md)
  (Implemented): the pair `(AppRevision, WorkflowRun)`.

## Context & Need

The platform proves that a part is accepted and loaded, never that it behaves: no reconciler calls a handler with
test input (note, "What the platform proves today"). Health (F118) is separate; the decider wants tests explicit,
opt-in and run only when asked, as `helm test`.

**Purpose.** `spec.tests` lists the App's own checks. `funcdctl app test <app>` asks the platform to run them once
and records one result per check on the current AppRevision; nothing else runs them, and a failure changes nothing
else.

## Scenarios

Fixture: ADR-0200's App `todo` with the note's `tests` (Contracts), installed: `todo-1` is current. Config defaults.

- `scenario: app-test-on-demand` — `funcdctl app test todo` while `/api/todos` answers 500 ⇒ `todo-1` shows
  `Tested=False` `TestFailed` naming `api-lists-todos`; `status.tests` holds `api-lists-todos` failed ("got 500, want
  200") and `api-answers` passed; the command exits 1; the App phase and conditions, the other `todo-1` conditions
  and every part's `resourceVersion` are unchanged. Before the command, after the install and a re-apply, `todo-1`
  has no `Tested` condition and `todo-api` received no call.
- `scenario: app-test-passes` — `/api/todos` answers 200 ⇒ `Tested=True`, both results passed; a second run replaces
  `status.tests` with two results whose `at` is later.
- `scenario: app-test-workflow` — a test `plan-runs` names Workflow `todo-plan` with an input ⇒ a WorkflowRun
  `todo-1-plan-runs-…` controlled by `todo-1` reaches `Succeeded` and the result passes naming it; after `todo` is
  deleted the GC collects the run.
- `scenario: app-test-anonymous-edge` — Route `todo-api` with `auth.mode: authenticated`, a test expecting 401 ⇒
  it passes; a test expecting 200 fails with "got 401, want 200", whoever runs the command.
- `scenario: app-test-refused` — while `todo` is paused (once F115 is built, ADR-0212), while `todo-2` is `Deploying`,
  after `todo-2` turned `Failed`, for an App with no tests, for a caller without `update` on App, or while another test
  of `todo` runs ⇒ 409, 409, 409, 409, 403 and 409, each naming the cause; no check runs and nothing is written.
- `scenario: app-test-admission` — two tests named `a`, a test with both `http` and `function`, `input` on an `http`
  test, `status: 600`, or `function: other-fn` that no `functions` entry names ⇒ apply fails (422) naming the field.

## Scope

**In**: `spec.tests` and its checks at apply; the `test` subresource of App; the App reconciler's `Test` entry;
`status.tests` and `Tested`; `funcdctl app test`; `app.testTimeout`; the GC pair. **Out**: health (F118); hooks and
their Invocation records (F117); pause (F115); a test identity and per-kind rights (the IAM work, note open question
1); body, header or output assertions; any automatic or scheduled run (decider); tests of a non-current revision;
dry run (F123); the hold gate itself (ADR-0206).

## Constraints & Decision drivers

Decision 16 as decided; ADR-0200 Decisions 1 and 2 stay true; a spec change stamps an AppRevision
(`internal/app/revision.go:51-70`), and ADR-0200 rejected a server-side writer of the user's spec; `ObjectMeta` has no
annotations (`api/types/v1alpha1/metadata.go:134-152`); no new kind and no run kind; reuse the edge chain, ADR-0214's
`Invoker` and the Sensor's run create; every time read through `Deps.Clock`; no new dependency.

## Alternatives considered

| Option | Lost because |
|---|---|
| A request field in `spec` (a counter the reconciler observes) | a spec change stamps an AppRevision and a rollout (`revision.go:51-70`); ADR-0200 rejected a second writer of the user's spec (its rollback alternative) |
| A request annotation | `ObjectMeta` has none (`metadata.go:134-152`); adding them is a platform-wide change |
| A new run kind (`AppTestRun`) | the request rules it out; a second record beside the AppRevision status |
| `202 Accepted` and a background run the CLI polls | a funcd restart loses the run with no record, and a written `Unknown` would stay; the API server sets no write timeout (`pkg/funcd/funcd.go:1111`), so a request may wait, as `helm test` does |
| The API handler writes the results | a second AppRevision writer, against ADR-0200 Decision 1 |
| Forward the caller's bearer on the HTTP check | it hands the caller's API credential to a request that app code serves |
| A platform identity for the HTTP check | none exists (`internal/auth/authorizer.go:89-97`); the IAM work decides (note open question 1) |
| Function check through the data plane `WithInternal`, or the Sensor's invoker instance | the hooks call Functions through ADR-0214's `Invoker` (note Decision 13), one path, which wakes; the Sensor's instance cuts every call at 30 s (`pkg/funcd/funcd.go:848`) |
| An Invocation per Function check | the result is the record; an Invocation holds neither Function nor input (`api/types/v1alpha1/invocation.go`) and no pair would collect it |

## Decision

1. **The field.** `AppSpec.tests` lists checks. It is part of the spec: each AppRevision's `spec.spec` carries its
   tests, and a change to them stamps a revision (ADR-0200 Decision 3) that writes no part and that Decision 4 refuses
   to test until it is current. It rolls out as any stamp: it waits while a requirement is unmet (ADR-0219 Decision 3:
   App `Deploying`, `Ready=False` `RequirementNotMet`, no self-heal); with hooks it is an `upgrade` or a `rollback`
   (ADR-0214 Decisions 2 and 4): pre-hooks before it becomes current, post-hooks after. No pass runs a check.
2. **Checks at apply.** `App.Validate` (`api/types/v1alpha1/app.go:270`) refuses, 422 naming the field: more than 50
   tests; a repeated `name`; not exactly one of `http`, `function`, `workflow`; `input` on `http`; `http.method` other
   than `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE` or `OPTIONS` (empty means `GET`); an `http.path` not starting
   with `/`; an `http.host` that is not a host name; `http.status` outside 100 to 599; a `function` (`workflow`) that
   no entry of this App's `functions` (`workflows`) section names by `name` or `ref` (`app.go:64-75`).
3. **The request.** `POST /apis/funcd.io/v1alpha1/namespaces/{namespace}/apps/{name}/test` (operation `testApp`, no
   body; it sets `RejectUnknownQueryParameters`, as ADR-0220 Decision 1 asks of every write), an imperative
   subresource as DLQ replay (`internal/controlplane/deadletters.go:92-104`). It authorizes `update` on App in the
   namespace through `middleware.IdentityFrom` and `auth.Authorizer`, as `authorizeDeadLetters` does (`:119-136`): who
   may change the tests may run them. It calls the `AppTester` seam, which `*app.Reconciler` implements, and answers
   200 with the AppRevision as written. If the caller goes away, the run stops and no result is written; a started
   WorkflowRun is left, and the GC pair collects it with its revision.
4. **Preconditions.** `Test` refuses, running and writing nothing: 404 without the App; 409 while `spec.paused` is set
   ("app <app> is paused", as ADR-0214's `Retry`, once F115 adds it), since a pause stops every write of the App (note
   Decision 17) and a part hot-fixed while paused is not the current revision; 409 when `status.currentRevision` is
   empty or differs from `status.latestRevision` (the message names the latest and its phase), so a result describes
   the spec that runs; 409 when the current revision's `spec.spec.tests` is empty; 409 while another `Test` of this
   App runs (a per-App lock in the reconciler); 503 `Unavailable` while `Deps.Hold.Held()` (ADR-0206's inline interface,
   nil ⇒ never held; as ADR-0214's held `retry`, its Decision 8), since a test runs app code that writes data.
5. **Running.** `Test` runs the current revision's `spec.spec.tests` one at a time in list order, under one deadline,
   `app.testTimeout` after its start on `Deps.Clock`; a check not started by then fails with "not run:
   app.testTimeout passed". Each http and function check runs under a context whose deadline is the run's, computed
   on `Deps.Clock` and applied with `context.WithDeadline` (as `internal/workflow/engine.go:955`); a check cut by it
   fails with "timed out: app.testTimeout passed". One check's failure does not stop the next. Every check runs with
   platform rights, as the App writes its parts (ADR-0199 Decision 9), and may wake a scaled-to-zero Function
   (ADR-0033).
   - **http**: one request with `method`, `host` and `path`, no body and no credential, served in-process by the
     data-plane listener chain (`pkg/funcd/funcd.go:1148-1152`), so the Route router, the edge authn PEP, the limits
     and the activator hop apply as to an outside client; no socket is opened, so `server.dataPlaneAddr` and TLS do
     not matter. An empty `host` matches only a Route without one (`api/types/v1alpha1/route.go:25-26`). It passes
     when the status equals `status`; else the message is `got <code>, want <code>`. The body is discarded. An
     `authenticated` Route answers 401 (`internal/edge/authn/authn.go:1-30`).
   - **function**: one call through ADR-0214's `Deps.Invoker` (its Decision 5: a second `sensor.HTTPInvoker` on
     `workerClient(calls, 0)`, not the Sensor's 30 s instance), as the hooks: it wakes the Function, which counts as
     activity, and POSTs a CloudEvent of type `io.funcd.invoke`, source `funcd://<ns>/function/<name>` (as
     `internal/dataplane/normalize.go:88-93`) and data `input`. The call's deadline is the earlier of the run's and
     the Function's `spec.timeout` (`Deps.InvokeTimeout`, `invoke.defaultTimeout`, when unset; ADR-0151). It passes
     when the call returns no error (an answer below 400, `invoker.go:75`); else the message is the error, which
     carries at most 1 KiB of the answer (`invoker.go:21`). No Invocation is recorded.
   - **workflow**: a WorkflowRun created on the internal store, as a Sensor's (`internal/sensor/sensor.go:464-479`):
     `generateName` `<revision>-<test>-` (truncated by `GenerateObjectName`, `metadata.go:204-213`), `spec.workflow`,
     `spec.input`, the App's namespace and resource group, and the AppRevision's controller reference; the run-start
     gate checks the input (ADR-0098). `Test` reads it every 500 ms. It passes at `Succeeded`; `Failed` or `Cancelled`
     fails naming the run and phase; at the deadline the check fails naming the run and its phase, and the run is left.
6. **Results.** `Test` writes the revision it read, with `store.Update` at its `resourceVersion` (a POST action takes
   no `If-Match`: ADR-0210, Scope); on Conflict (a pass wrote it) it re-reads and sets again, at most 3 times, then
   answers 409; NotFound (history trimmed it) answers 409 naming it. It replaces `status.tests` with one
   `AppTestResult` per check in list order (`at` is the check's start on `Deps.Clock`, ADR-0196; `message` at most
   1 KiB) and sets `Tested` through `setCondition` (`internal/app/status.go:221-228`): True with no reason, as
   ADR-0200's True conditions, or False `TestFailed` with message `<k> of <n> failed: <names in list order>`. Nothing
   else changes: not the App, the revision's phase or its other conditions, nor a part. A pass keeps `Tested` and
   `status.tests` as read (`statuses`, `revision.go:207-216`) and never sets them; the App reconciler does not watch
   AppRevision, so the write requeues nothing (`pkg/funcd/funcd.go:1033-1038`). A revision never tested has neither;
   results leave with their revision (ADR-0200 Decision 8).
7. **Records.** `gc.Pairs()` gains `(AppRevision, WorkflowRun)` after `(App, AppRevision)` and after
   ADR-0214's `(AppRevision, Invocation)` when present, so one sweep collects a revision, then its test runs
   (`internal/gc/gc.go:30-43`).
8. **CLI.** `funcdctl app test <app> [-n] [-o json]` (`-n` through `nsOrDefault`, as `history`) calls `sdk.TestApp`
   and prints `REVISION`, then `NAME`, `RESULT` (`pass` or `fail`) and `MESSAGE` per check; `-o json` prints the
   returned AppRevision. It exits 1 with `<k> of <n> tests failed` when `Tested` is False.
9. **Config.** `app.testTimeout`, a duration string (ADR-0194), default `5m`, from 1s to 1h (`v1.MaxInvokeTimeout`).

## Temporary workarounds

- An `authenticated` Route can be checked only for its 401; its backend is checked with a `function` test. Exit: the
  IAM work's test identity (note open question 1).
- `Deps.Hold` stays nil (never held, ADR-0214 Decision 8), so `Test` is never refused for a hold. Exit: ADR-0206's
  implementation passes its `hold.Gate` through `funcd.WithHold` to `app.Deps.Hold`.

## Contracts

```go
// api/types/v1alpha1/app.go (additive)
type AppSpec struct {
	// ADR-0199's fields unchanged
	Tests []AppTest `json:"tests,omitempty" maxItems:"50"` // run only by funcdctl app test
}
type AppTest struct {
	Name     ObjectName      `json:"name"`
	HTTP     *AppTestHTTP    `json:"http,omitempty"` // exactly one of http, function and workflow
	Function ObjectName      `json:"function,omitempty"`
	Workflow ObjectName      `json:"workflow,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"` // function and workflow only
}
type AppTestHTTP struct {
	Method string `json:"method,omitempty"` // default GET
	Host   string `json:"host,omitempty"`
	Path   string `json:"path"`
	Status int    `json:"status" minimum:"100" maximum:"599"`
}

// api/types/v1alpha1/apprevision.go (additive; conditions Applied, ChildrenReady, Current, Tested)
// AppRevisionStatus gains, after ADR-0200's and ADR-0214's fields:
//	Tests []AppTestResult `json:"tests,omitempty"` // the last funcdctl app test
type AppTestResult struct {
	Name    ObjectName `json:"name"`
	Passed  bool       `json:"passed"`
	Message string     `json:"message,omitempty"`
	At      Timestamp  `json:"at"`
}

// internal/controlplane
type AppTester interface {
	Test(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*v1.AppRevision, error)
}
func RegisterAppTest(api huma.API, tester AppTester, authz auth.Authorizer) // plus a stub tester for spec generation

// internal/app: ADR-0214's Invoker and Deps fields and ADR-0206's inline Hold interface{ Held() bool; ReleasedAt() time.Time } (nil ⇒ never held); the first of ADR-0206/0212/0214/0216 to land adds it (ADR-0212 Decision 8).
func (r *Reconciler) Test(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*v1.AppRevision, error)
// Deps gains Edge http.Handler (the listener chain, late-bound as dpHolder) and TestTimeout time.Duration (0 ⇒ 5m).

// pkg/sdk
func (c *Client) TestApp(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*v1.AppRevision, error)

// pkg/funcd
func WithAppTestTimeout(d time.Duration) Option // fault.Invalid unless 1s <= d <= 1h

// internal/platform/config: Config.App gains
//	TestTimeout string `json:"testTimeout,omitempty" env:"FUNCD_APP_TEST_TIMEOUT"`
```

```yaml
spec:
  tests:
    - name: api-lists-todos
      http:
        host: todo.example.com
        path: /api/todos
        status: 200
    - name: api-answers
      function: todo-api
      input:
        op: list
```

| Config key | Env | Default | Bounds |
|---|---|---|---|
| `app.testTimeout` | `FUNCD_APP_TEST_TIMEOUT` | `5m` | 1s to 1h: one deadline for a whole `app test` run |

New reason: `TestFailed` (AppRevision `Tested`). Refusals are problem+json: 403, 404, 409 with "app todo is paused",
"App todo has no current revision", "todo-2 is Deploying" (or `Failed`), "App todo declares no tests", "a test of App
todo is running", and 503 with "the platform is held".

| Consumes | Exposes |
|---|---|
| store: App, AppRevision, Function, WorkflowRun · the data-plane listener chain · ADR-0214's `Deps.Invoker`, `Deps.InvokeTimeout` · ADR-0206's `Deps.Hold` (its inline interface; `serve` passes the `hold.Gate`) · `auth.Authorizer` · `Deps.Clock` · config `app.testTimeout` | `spec.tests` · `status.tests` and `Tested` · `POST …/apps/{name}/test` · `sdk.TestApp` · `funcdctl app test` · GC pair · `WithAppTestTimeout` |

## Implementation plan

1. **Types**: `api/types/v1alpha1/app.go` (`Tests`, `AppTest`, `AppTestHTTP`, the Decision 2 checks) and
   `apprevision.go` (`Tests`, `AppTestResult`), with table tests of every refusal; `just generate`.
2. **Server**: `internal/app/test.go` (`Test`, the lock, the three checks, the write with its Conflict retry) and the
   new `Deps` fields; `internal/controlplane/apptest.go` (route, authorization, stub); `internal/gc/gc.go` (pair);
   the config key, `examples/funcdconfig.yaml`, `cmd/funcd/main.go` and `pkg/funcd` (`WithAppTestTimeout`;
   `Deps.Invoker` as ADR-0214 Decision 5 unless F117 wired it; `Edge` set from the listener chain through a holder,
   as `dpHolder`, `pkg/funcd/funcd.go:1157`; the route).
3. **Client**: `pkg/sdk` `TestApp`; `cmd/funcdctl/app.go` adds `test` and adds it to the group's `Short` list.
4. **Tests**: `internal/app` on `clock.NewManual`, a fake `Invoker`, a fake `Hold` and an `http.Handler` stub as `Edge`:
   pass and fail per check kind; order kept and a failure not stopping the next; "not run" after the deadline; an http
   and a function check cut at the deadline (an `Edge` stub and a fake `Invoker` that block until their context is
   done); a function call's deadline from `spec.timeout` and the default; a workflow run left at the deadline; each
   refusal of Decision 4, the held one included, writes nothing (the paused refusal and its scenario case land with the
   later of F115 and F119); a Conflict re-read; a pass after a test keeps `Tested` and `status.tests` and writes
   nothing; the App and its parts unchanged after a failure. `internal/controlplane`: 403 without `update`, 404, 200
   body; `TestSDKKindPaths_MatchServerRoutes`; the `gc.Pairs()` order; config bounds (`cmd/funcd`, `pkg/funcd`);
   `cmd/funcdctl` columns, `-o json`, exit code; one `TestScenarioApp…` (e2e tag) per Scenario in `pkg/funcd`.
5. **Done**: `just ci` and `just ci-full` green; a passing test per scenario name, the paused case with whichever of
   F115 and F119 comes second; no `go.mod` change.

## Review checklist

- [ ] Only `(*app.Reconciler).Test` reads `spec.tests` to run a check; no pass, timer, hook or watch calls `Test`.
- [ ] `Test` writes only the current AppRevision's `status.tests` and `Tested`, plus a workflow check's WorkflowRun.
- [ ] No pass sets `Tested` or `status.tests`; the App phase rule and the switch read neither.
- [ ] The route authorizes `update` on App before any read; AppRevision keeps GET-only routes.
- [ ] Every refusal of Decision 4, a paused App's included, returns before any check and any write.
- [ ] The http check uses the listener chain and sets no `Authorization` header; the function check uses `Deps.Invoker`.
- [ ] A test WorkflowRun carries the AppRevision's controller reference; `gc.Pairs()` holds `(AppRevision,
      WorkflowRun)` after `(App, AppRevision)` and `(AppRevision, Invocation)`.
- [ ] Every time read in `Test` goes through `Deps.Clock`, and the run deadline bounds each check's context;
      `message` is at most 1 KiB; `app.testTimeout` is 1s to 1h.

## Consequences

**Positive**: an App carries its own proof, run when asked and recorded with the revision it ran against; tests reuse
the edge, the hooks' invoker and the run gate, so a check sees what a client sees.
**Negative**: a test runs real code: a Function or WorkflowRun writes app data (KV, a Bucket in ADR-0208's blob store)
as it would for a client, and the test author keeps it safe to repeat; a test wakes a scaled-to-zero Function; a
tests-only change stamps an AppRevision, which counts against `app.revisionHistory` and can trim the rollback target
(ADR-0200 Decision 8), with hooks (ADR-0214) calls the App's pre- and post-hooks before `app test` can run, and while
a requirement is unmet (ADR-0219) waits, turning a serving App `Deploying` without self-heal; the API request lasts up
to `app.testTimeout`.
**Risks accepted**: a Function that answers an error with a secret puts it in `message`, as an Invocation's error does
today (`internal/sensor/sensor.go:481-500`); results are lost when funcd restarts during a run; the edge limits count
a test request.

## Open questions

1. A test identity for `authenticated` Routes and per-kind rights for the calls → the IAM work (note open question 1).
2. Body, header or output assertions → a later ADR, if a case needs them.

## References

- [App design note](../reports/app-design.md) (Decisions 13, 16, 17; contracts; review checklist) ·
  [FEAT-0010](../feat/0010-feat-apps.md) · `internal/controlplane/deadletters.go:17-136` ·
  `internal/sensor/invoker.go:21-80` · `internal/app/revision.go:51-70`, `:207-242` · `pkg/funcd/funcd.go:1033-1038`,
  `:1148-1157`.
