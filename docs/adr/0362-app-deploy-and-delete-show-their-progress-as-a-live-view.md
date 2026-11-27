# ADR-0362: funcdctl app deploy and app delete show their progress as a live view

- **Status**: Proposed
- **Date**: 2026-11-27
- **Deciders**: green-0-rabbit
- **Tags**: cli, apps
- **Realizes**: [FEAT-0010/F279](../feat/0010-feat-apps.md) (a live view of an App's deploy and delete)
- **Supersedes (in part)**: [ADR-0217](0217-app-templates-values-and-rendering.md) Decisions 8 and 9, what deploy and
  delete print while they wait; their waits, refusals and exit codes stay as ADR-0217 decides them (Decision 7)
- **Relates to**: [ADR-0219](0219-app-requirements.md) (requirements) ·
  [ADR-0214](0214-app-hooks.md) (hooks) · [ADR-0220](0220-dry-run-engine.md) (the plan) ·
  [ADR-0199](0199-app-resource.md) and [ADR-0357](0357-an-app-never-waits-for-its-stacks-and-a-part-waits-for-their-outputs.md)
  (part states, deletion protections) · [ADR-0125](0125-funcdctl-dev-local-run.md) (the dev banner's style)

## Context & Need

`funcdctl app deploy` prints one plain line each time a part's state or reason changes (`report`,
`cmd/funcdctl/app.go:824`), and `app delete` prints `deleted`, `waiting` and `kept` lines (`waitTreeGone`,
`cmd/funcdctl/app.go:963`). Neither says what a Pending part waits on or how long anything took, and the lines carry no
color. On 2026-11-27 the decider asked for what `terraform apply` and `destroy` show: every part in order, its state,
what it waits on and the time it spends, drawn like the `funcdctl dev` banner (`cmd/funcdctl/dev.go:864`).

Purpose: the developer who runs a deploy or a delete sees at a glance what is done, what waits and on what, and for how
long; a CI log keeps lines it can read.

## Scenarios

- `app-view-deploy` — *Given* a terminal and an App with a KVStore, a Function that binds it and a Route to the
  Function, *when* `funcdctl app deploy` runs, *then* a box redrawn each second shows the revision, the plan's counts,
  `<ready>/<parts> ready`, the elapsed time and the deadline, and one row per part with its action, state, reason and
  time, and the last draw stays when the command exits.
- `app-view-lines` — *Given* the same deploy with its output in a pipe, *then* it prints ADR-0217's lines, each part's
  line ending `after <d>` when it settles, and no ANSI escape.
- `app-view-waits-on` — *Given* a Function that binds a Lake of the App that is Pending, *then* the Function's row
  names `Lake/<name>` as what it waits on.
- `app-view-requires` — *Given* an App that requires `payments` at `^2.0`, Deploying, and a missing `billing`,
  *then* a row per required App shows its version, its range and `is not Ready (Deploying)` or `does not exist`, the
  header reads `waiting for 2 of 2 required Apps · no deadline yet`, and no part row is drawn.
- `app-view-hooks` — *Given* a pre-hook and a post-hook, *then* each has a row that shows it running, done or failed,
  with the time from its Invocation, and deploy exits once the post-hook is done, 1 when it fails.
- `app-view-delete` — *Given* `funcdctl app delete` on an App whose KVStore an engineless part's Function binds,
  *then* rows show each object deleted with its time, the store waiting with `InUse: <Kind>/<name>`, and a `retain`
  store kept.
- `app-view-deadline` — *Given* a rollout whose clock starts, then restarts on a resume, *then* the revision's
  `status.deadline` moves with it, and the view counts down to it.

## Scope

- **In**: the output of `app deploy` and `app delete` while they wait, on a terminal and in a pipe; the per-part times
  and waits-on; the required-App and hook rows; `AppRevisionStatus.deadline`; deploy's wait for its post-hooks; the
  docs and the behavior rows.
- **Out**: a dependency graph command (`app graph`, a later card); per-part times kept by the server; `funcdctl dev`'s
  App mode, whose terminal carries the banner and the function logs; `app rollback` and `app retry`, which wait for
  nothing.

## Constraints & Decision drivers

- The decider, 2026-11-27: option A (the CLI's clock, no per-part server field), by default on a terminal, a new ADR,
  a row per required App; drawn like the dev banner.
- lipgloss v1.1.0 (MIT) and go-isatty are already direct dependencies (`go.mod`; the banner, `cmd/funcdctl/job.go:333`).
- `deployPoll` is 1 s (`cmd/funcdctl/app.go:278`): a time is as precise as one poll.
- The deadline is `deadline` in `internal/app/revision.go:309`: the latest of `startedAt`, a resume, a hold's release,
  a pre-hook's end and a lag's end, plus `app.upgradeTimeout`, a server key the CLI cannot read.
- `gc.InUse` already reads what a part names, through `uses` (`internal/gc/gc.go:634`), for the kinds a prune protects.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. A live view on the CLI's clock, one server field for the deadline** | the whole visible change; no new dependency | times to the second; a restarted CLI starts its clocks again | **Chosen** |
| B. A, plus a per-part `since` in `status.children` | exact times that survive a restart | an API field written at each state change, for a precision one poll already gives | Rejected by the decider |
| C. A, plus `funcdctl app graph` | the whole graph at rest | not needed to follow a deploy | Deferred: a card, reusing Decision 4 |
| D. bubbletea for the redraw | a full TUI | a new dependency to redraw one box | Rejected: ANSI cursor moves suffice |
| E. Only with a `--watch` flag | today's output by default | the view the command exists to show stays hidden | Rejected by the decider |
| F. The deadline computed in the CLI | no API field | it needs `app.upgradeTimeout` and the hold's release, both server-side | Rejected |

## Decision

1. **Where it shows.** `app deploy` and `app delete` draw the live view when their stdout is a terminal
   (`isatty.IsTerminal`) and `TERM` is not `dumb`, else they print lines (Decision 6). `--no-wait` and `--dry-run`
   print what they print today.
2. **The deploy view.** A box with lipgloss's rounded border in the dev banner's palette (`cmd/funcdctl/dev.go:868-872`),
   redrawn each `deployPoll` by moving the cursor up over the last draw:
   - the title `● funcd app deploy   <app> <version> · revision <rev> · Ctrl-C stops the wait`;
   - the plan line, the counts of create, update and prune parts and of hooks, from a dry-run of the same apply made
     just before it (ADR-0220 Decision 5);
   - the progress line, `▰` per settled part and `▱` per other, `<settled>/<parts> ready · <now − startedAt> ·
     deadline in <deadline − now>` (Decision 5);
   - a REQUIRES row per `spec.requires` entry: `App/<name>`, its `status.requires[].version`, the entry's range, and
     Ready when met, else `app.Unmet`'s text. While one is unmet, the progress line reads `waiting for <n> of <m>
     required Apps · no deadline yet` and the parts section reads `⋯ <n> parts wait for the required Apps; none is
     written and no hook runs` (ADR-0219 Decision 3);
   - a row per hook of the revision: running while the App's Ready reason names it, then done or failed from
     `status.hooks`, with the time from its Invocation's `startTime` and `endTime`;
   - a row per `status.children` entry, in its order: ✓ Ready or NotStarted, ◐ Pending or Lagging, ✗ Pending with the
     reason ChildInvalid or ChildNotOwned, ⋯ Pruning; `<Kind>/<name>`, the plan's action or none, the state and
     reason, what it waits on (Decision 4) and its time;
   - the App's Ready line, as today.
   The last draw stays on screen when the command exits.
3. **The delete view.** The title `● funcd app delete   <app> · <gone> of <noted> gone · <elapsed>`, then a row per
   object ADR-0217 Decision 9's walk notes: ✓ deleted with its time; ◐ waiting, with its own Ready reason when it has
   one, else `InUse: <Kind>/<name>` for a noted object still present that names it (Decision 4); ■ kept, for a store
   that stays.
4. **Waits on.** `gc.Names(u)` lists every object `u`'s spec names, each once, in field order: a Function's or a Job's
   bindings (`kv`, `sql`, `blob`, `lake`, `addon`, `config`) and links, a Lake's `blob` and `config`, a Route rule's
   `function` and static `bucket`, a Site's blob `bucket` and runtime `configMap`, an EventSource's `bucket`, and a
   Sensor's `workflow` and `function`. A row waits on the first part of the App that `u` names and that is not
   settled. `uses` becomes a check over `Names` limited to the kinds and fields it reads today, so `InUse` answers as
   before.
5. **Times and the deadline.** A part's time runs on the CLI's clock from the first poll that sees it unsettled to the
   first that sees it settled (Ready or NotStarted; on delete, gone), with `…` while it runs and `–` for a part the
   plan leaves alone. `AppRevisionStatus.deadline` (new) is `deadline`'s result: the reconciler writes it with
   `startedAt` and again each time an input of `deadline` moves it, and clears nothing once the revision is final.
6. **Lines.** Off a terminal, the lines of ADR-0217 Decisions 8 and 9, plus ` after <d>` on a part's line when it
   settles, and one `App/<app> requires App/<name>: <text>` line per change of a requirement's state; lipgloss renders
   no escape there.
7. **Exits.** As ADR-0217 Decisions 8 and 9, with deploy exiting once the revision's post-hooks are done, as Decision 8
   says and `waitDeployEvery` does not do today (the `funcdctl app deploy at the switch` row of
   `docs/behavior/apps.md`, `differs`); a failed post-hook exits 1 and prints `funcdctl app retry <app>`.

## Design model

None: the view reads states that the reconcilers write, once a second, and `deadline` publishes a value the reconciler
already computes. No lifecycle, timer or race is added.

## Behavior rows

- **apps.md**: `funcdctl app deploy at the switch` becomes `decided` (Decision 7); a new row "The live view of a
  deploy or a delete" (Decisions 1 to 6); the `waiting` row names the view's reasons.

## Temporary workarounds

None.

## Contracts

```go
// cmd/funcdctl/appview.go (new)

// appView draws a deploy's or a delete's progress: a live box on a terminal, lines elsewhere (Decisions 1 and 6).
type appView struct{}

func newAppView(out io.Writer, live bool, now func() time.Time) *appView

// deploy draws one poll of a deploy: the App, the followed revision (nil before its stamp), the plan's action per
// part, the required Apps by name and the App's parts by reference.
func (v *appView) deploy(app *v1.App, rev *v1.AppRevision, plan map[v1.ObjectRef]v1.PlanAction,
	required map[v1.ObjectName]*v1.App, parts map[v1.ObjectRef]v1.Object, hooks map[v1.ObjectName]*v1.Invocation) error

// remove draws one poll of a delete: the noted objects, those still present and the stores kept.
func (v *appView) remove(app v1.ObjectName, noted []v1.ObjectRef, present map[v1.ObjectRef]v1.Object, kept []v1.ObjectRef) error

// done leaves the last draw on screen.
func (v *appView) done() error
```

```go
// internal/gc

// Names lists the objects u's spec names, each once, in field order (Decision 4).
func Names(u v1.Object) []v1.ObjectRef

// internal/app

// Unmet is why dep does not meet e, "" when it does, in ADR-0219 Decision 2's order.
func Unmet(e v1.AppRequirement, dep *v1.App) string
```

```go
// api/types/v1alpha1/apprevision.go, AppRevisionStatus
Deadline *Timestamp `json:"deadline,omitempty" doc:"When the rollout fails if a part is still not Ready: the latest of startedAt, a resume, a hold's release, a pre-hook's end and a lag's end, plus app.upgradeTimeout. Unset before the clock starts."`
```

| Consumes | Writes | Exposes |
|---|---|---|
| the App, its AppRevisions, its parts, the required Apps and the hooks' Invocations, each poll; a dry-run plan | `AppRevisionStatus.deadline` (the reconciler) | the live view, or lines |

## Implementation plan

- `cmd/funcdctl/appview.go` (new): the view, with lipgloss and the banner's palette; `cmd/funcdctl/app.go`: deploy asks
  for the dry-run plan, then draws each poll through the view, and waits for the post-hooks; delete draws through it.
- `internal/gc/gc.go`: `Names`, and `uses` over it; `internal/app/requires.go`: `unmet` exported as `Unmet`;
  `internal/app/revision.go`: the reconciler writes `deadline`.
- `api/types/v1alpha1/apprevision.go`: the field; `scripts/agent/d just generate` for the OpenAPI document and the
  Spec tabs.
- Tests, one per Scenario, named after it (`TestScenarioAppViewDeploy`, …): the view renders to a buffer at a fixed
  width with lipgloss's renderer forced to its ASCII profile; `app-view-lines` asserts no escape byte;
  `app-view-deadline` runs the reconciler. Unit tests: `Names` for each kind and field; `InUse` answers unchanged over
  a table of every kind it protects; `Unmet` for each of its answers.
- Docs: the guide's App page (deploy and delete), the behavior rows, the F279 row.
- **Done when** the tests pass, `just ci` is green, the gate passes, and the `app-todo` lane, whose deploy prints to a
  pipe, still passes.

## Review checklist

- [ ] On a terminal the box is drawn and redrawn in place, in the banner's palette; off one, lines with no escape.
- [ ] Rows for required Apps, hooks and parts, in that order, with the states, reasons, waits-on and times above.
- [ ] `Names` covers every field of Decision 4; `InUse` returns what it returned before for every protected kind.
- [ ] `status.deadline` moves with every input of `deadline`; the view counts down to it.
- [ ] Deploy exits after the post-hooks, 1 on a failed one; every other exit as ADR-0217 Decisions 8 and 9.
- [ ] One test per Scenario, named after it; the docs and behavior rows name the view.

## Consequences

- **Positive**: a deploy and a delete read at a glance, with what waits on what and for how long; CI logs gain the
  times; the server's deadline is visible.
- **Negative**: times are to the second and restart with the CLI; each deploy adds one dry-run admission pass; a row
  names only the first part it waits on; deploy now returns after the post-hooks, later than today.
- **Risks accepted**: a terminal that misreports itself gets escape codes; `TERM=dumb` and a pipe print lines.

## Open questions

None.

## References

- [#1606](https://github.com/pyvvo/funcd/issues/1606), the feature issue (tracker #871), with the decider's choices.
- The decider's brief, 2026-11-27, with the mockups of the deploy, delete and required-App views (in the session).
- ADR-0217 Decisions 8 and 9; ADR-0219 Decisions 2 and 3; ADR-0214 Decision 4; ADR-0220 Decision 5.
- `cmd/funcdctl/app.go:759-994`, `internal/gc/gc.go:592-700`, `internal/app/revision.go:309-334`.
