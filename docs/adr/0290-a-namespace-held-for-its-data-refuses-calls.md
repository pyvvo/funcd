# ADR-0290: A namespace held for its data refuses calls to its Functions

- **Status**: Implemented (2026-10-27), review passed ([report](../reviews/adr-0290-implementation-claude-opus-5-5.md));
  Reviewing 2026-10-27; accepted 2026-10-27, by an `adr-batch` run on the decider's delegation,
  after one `adr-judge` round with no Blocker; folded: the upstream's detail names no hold, the S3 gateway out of
  scope (#1302), a daemon test instead of a lane step, `hold.NeverCall`)
- **Amended in place**: 2026-10-27, by the decider on [#1302](https://github.com/pyvvo/funcd/issues/1302): the S3
  gateway refuses writes into a held namespace's Buckets (Decision 6)
- **Date**: 2026-10-27
- **Deciders**: green-0-rabbit
- **Tags**: disaster-recovery, data-plane, hold
- **Realizes**: [FEAT-0009/F205](../feat/0009-feat-disaster-recovery.md) (a namespace waiting for its data refuses calls)
- **Answers**: [#1297](https://github.com/pyvvo/funcd/issues/1297), with the decider's choices of 2026-10-27 recorded
  on it: refuse calls with 503 until the owner's release, for both namespace hold reasons; Sites keep serving; no new
  field or API; Cedar unchanged
- **Supersedes (in part)**: [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) Decision 8's list of what
  the per-namespace hold gates: the data plane's calls into the namespace join it
- **Relates to**: [ADR-0206](0206-restore-and-held-boot.md) (the platform hold, unchanged) ·
  [ADR-0228](0228-dr-implementation-addenda-two.md) (the in-place Restore's `restore` marker) ·
  [ADR-0287](0287-a-platform-restore-finds-the-backups-made-after-its-point.md) (the runbook's step 8) ·
  [ADR-0120](0120-static-asset-serving-route.md) (the static backend) ·
  [ADR-0138](0138-external-catalog-ingress-and-route-aggregation.md) (the upstream backend) ·
  [ADR-0135](0135-managed-identity.md) (the S3 gateway)

## Context & Need

After a platform restore, `funcd restore run` holds each namespace that has a BackupSchedule (marker reason `data`,
`internal/restore/run.go:264`): its stores came back without their data, and its owner restores them, then runs
`funcdctl hold release --namespace`. An in-place Restore holds its namespace the same way while it loads (reason
`restore`). The hold pauses the namespace's timers, blob sources, Sensors, runs, App and slots, but nothing on the
call path reads it, so the namespace's Functions keep answering: a write lands in an empty store, and the owner's
restore then replaces that store and erases a write the platform acknowledged. Writes through the S3 gateway, its own
listener (ADR-0135), land the same way. Experiment 0014 (pyvvo/experiment#26, private) saw it in both of its SQL host
losses. This ADR makes a held namespace refuse calls, and writes through the S3 gateway, until its data is back.

## Scenarios

- **scenario: data-hold-refuses-calls**: Given `restore run` held `orders` for `data`, When a client calls an `orders`
  Function by its Route or by `/function/<name>`, Then it gets 503 with `Retry-After: 30` and a problem that names the
  namespace and its hold, and no worker is started.
- **scenario: release-serves-calls**: Given that hold, When the owner runs `funcdctl hold release --namespace orders`,
  Then the next call is served.
- **scenario: restore-load-refuses-calls**: Given an in-place Restore loading `orders` (reason `restore`), When a call
  arrives, Then it gets the 503, whose problem names the Restore, until the load releases the marker.
- **scenario: internal-call-refused**: Given `orders` held, When a `web` Function calls an `orders` Function by name,
  Then that call gets the 503.
- **scenario: lake-upstream-refused**: Given `orders` held, When a client reaches an `orders` Lake's query upstream
  through its Route, Then it gets 503 with `Retry-After: 30` and the upstream's usual detail, which names no hold.
- **scenario: site-serves-while-held**: Given `orders` held, When a browser fetches a static file of an `orders` Site,
  Then it gets the file.
- **scenario: auth-before-hold**: Given `orders` held, When a caller without credentials calls a Function that needs
  them, Then it gets 401 as today, and the answer says nothing about the hold.
- **scenario: platform-hold-unchanged**: Given only the platform is held (no namespace marker), When a call arrives,
  Then it is handled as today.
- **scenario: unheld-namespace-unaffected**: Given `orders` held, When a client calls a `web` Function, Then it is
  served.
- **scenario: gateway-write-refused**: Given `orders` held, When its owner puts, deletes or uploads an object through
  the S3 gateway, Then it gets the S3 error `ServiceUnavailable` (503), whose message names the hold, and the Bucket
  is unchanged; after the release the write lands.
- **scenario: gateway-read-served**: Given `orders` held, When a client reads or lists its Bucket through the S3
  gateway, Then it is served.
- **scenario: gateway-restore-load-refused**: Given an in-place Restore loading `orders`, When a part of an upload
  begun before arrives, Then it gets the 503, which names the Restore, and the upload can still be aborted.
- **scenario: gateway-auth-before-hold**: Given `orders` held, When a caller the PEP denies writes, Then it gets 403
  as today, which says nothing about the hold.
- **scenario: gateway-unheld-namespace-served**: Given `orders` held, When a `web` caller writes to a `web` Bucket,
  Then the write lands.

## Scope

**In**: a check, against the hold's in-memory namespace markers, on the data plane's Function and upstream paths and
on the S3 gateway's writes; a read-locked marker lookup; the restore runbook's step 8 and the workload backup guide.
**Out**: the platform hold, during which ADR-0206 keeps the data plane serving ("API, controllers and data plane
serve"), since its held boot loads every store before the start; an owner's
opt-out that keeps serving during the hold (no one has asked; it would be its own ADR); Cedar; calls already in flight
when an in-place Restore's hold begins (ADR-0222 Decision 7's load rules, unchanged); a metric of refused calls.

## Constraints & Decision drivers

- An acknowledged call must not be erased by the owner's restore: that is the track's goal (FEAT-0009).
- A refused call must fail before any work, with an answer a client retries (503, `Retry-After`), not a permission
  error after side effects.
- No new API, field or config key: the hold already exists, is written by `restore run` and the in-place Restore,
  and is lifted by `hold release --namespace`.
- The check runs on every call, so it reads memory only: no store or disk access.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| **A. The data plane asks the existing hold** | One check where every call passes; reuses the hold's writers and release; a clean, retryable 503 before any work | The whole namespace waits, reads included | **Chosen** (the decider, 2026-10-27) |
| B. A BackupSchedule field `whileRestoring: refuse \| serve` | Per-owner choice | New API, a marker bit and a controller sync for a knob no one asked for | Rejected: no need yet; the hold is the switch |
| C. A Cedar context attribute and forbid policies on writes | Reuses the PEPs; per-store precision | The driver evaluates with an empty context (`internal/auth/cedar/cedar.go:104`); a 403 mid-request after side effects, not retried; reads of empty stores still answer; volume writes and `auth.mode open` Routes never reach the PDP; availability mixed into authorization | Rejected |
| D. The App reconciler stops serving | Close to the App | Calls never pass through it; the hold is per namespace; standalone Functions have the same problem | Rejected |
| E. A runbook step: stop the namespace's traffic by hand | No code | Manual and unenforced; experiment 0014's loss happened under the current runbook | Rejected |

## Decision

1. **The gate.** `serveFunction` (`internal/dataplane/dataplane.go:212`) asks `held.NamespaceMarker(ns)` after the
   caller's authentication (`s.enforce`; internal calls skip only that step), the Function's lookup and the
   observability fill (`:231-233`), before the limiter and the activator hop (`:287`). A marker ⇒ the call is
   refused. The activator's other callers, workflow dispatch (`internal/workflow/dispatch.go:193`), the sensor
   invoker (`internal/sensor/invoker.go:54`) and App hooks, already wait while their namespace is `HeldIn`
   (`internal/workflow/reconcile_run.go:106`, `internal/sensor/sensor.go:715`, `internal/app/hooks.go:473`).
   `serveUpstream` (`:338`) asks the same for the matched entry's namespace after its observability fill, before it
   proxies.
2. **The answer.** A refused call gets `fault.Unavailablef` (503, problem type `urn:funcd:problem:unavailable`) and
   the header `Retry-After: 30` (the constant `heldRetryAfter`, written as `internal/edge/limit/limit.go:129` writes
   its own). On the Function path, which runs after authentication, the detail is `namespace <ns> is held for its
   data since <since>: calls resume after funcdctl hold release --namespace <ns>`, or for the reason `restore`
   `namespace <ns> is held while Restore <name> loads it since <since>`. Behind an `open` stance anyone may call, so
   anyone reads that detail: it names the namespace, which the Route's host already shows, and no data. The upstream
   path runs no edge PEP (`dataplane.go:335-336`), so its detail is its existing `edge upstream unavailable`: an
   unauthenticated caller cannot tell a hold from an engine that is down.
3. **The lookup.** `NamespaceMarker` (NEW, `internal/platform/hold`) reads the in-memory markers under the hold's read
   lock: the `restore` marker if present (its detail names the Restore), else the `data` marker; none ⇒ false. The
   platform's own hold is not a namespace marker and does not count.
4. **Unchanged.** `serveStatic` (a Site's files), the platform hold, Cedar and every PEP, the hold's writers, its
   release, its per-namespace runners and `funcdctl hold status`.
5. **Wiring.** `dataplane.Handler` keeps its parameters and gains a variadic `opts ...Option` (functional options,
   ADR-0002), so its 25 existing callers stay as they are. `WithCallGate` sets the gate; without it the Server asks
   `hold.NeverCall`. The daemon passes `WithCallGate(c.callGate())`, which is `hold.NeverCall` without `WithHold`, as
   `c.gate()` and `c.nsGate()` do (`pkg/funcd/funcd.go:426-440`).
6. **The S3 gateway** (amended 2026-10-27). Each write, `PutObject`, `DeleteObject`, `DeleteObjects` and a multipart
   upload's create, part and complete, asks `held.NamespaceMarker` for the caller's namespace after the PEP's allow
   and the Bucket's lookup (`authorizeWrite`, `internal/blob/s3gateway/backend.go`); `DeleteObjects` refuses the
   whole request at its first allowed key, before any delete. A marker ⇒ the S3 error `ServiceUnavailable` (503),
   which S3 clients retry with backoff, with Decision 2's detail ending `writes resume after …`; versitygw writes no
   `Retry-After`. Reads and `AbortMultipartUpload`, which writes nothing and frees the multipart budget, are served.
   `s3gateway.Deps.Held` carries the gate (nil ⇒ `hold.NeverCall`); the daemon passes `c.callGate()`.

## Design model

None: each call reads the hold's current markers once, with no protocol of its own; the hold's transitions are
ADR-0222's and ADR-0228's.

## Temporary workarounds

None.

## Contracts

```go
// Package hold (internal/platform/hold), NEW beside NamespaceGate.

// CallGate is what the data plane asks before it serves a call into a namespace (ADR-0290).
type CallGate interface {
	// NamespaceMarker is the marker holding ns: its restore marker if any, else its data marker. False when ns holds
	// neither; the platform's own hold never counts.
	NamespaceMarker(ns v1.NamespaceName) (Marker, bool)
}

// NeverCall is the call gate of a platform without a hold.
var NeverCall CallGate = never{}

func (never) NamespaceMarker(v1.NamespaceName) (Marker, bool)
func (h *Hold) NamespaceMarker(ns v1.NamespaceName) (Marker, bool)

// Package dataplane (internal/dataplane), NEW: options for Handler; no WithCallGate ⇒ hold.NeverCall.
type Option func(*Server)

func WithCallGate(g hold.CallGate) Option

func Handler(st store.Store, act *activator.Activator, rtr router.Router, baseDomain string, enf *authn.Enforcer,
	lim *limit.TargetLimiter, stat *static.Handler, defaultTimeout time.Duration, logger *slog.Logger,
	opts ...Option) http.Handler

// Package s3gateway (internal/blob/s3gateway), amended: Deps gains the gate; its other fields are unchanged.
type Deps struct {
	// Held is the namespace hold under which a write into the caller's namespace is refused (ADR-0290 Decision 6);
	// nil ⇒ hold.NeverCall.
	Held hold.CallGate
}
```

| Consumes | Exposes |
|---|---|
| The hold's in-memory namespace markers (`.hold-ns/<ns>.<reason>`, loaded at start, written by `restore run`, the in-place Restore and `HoldNamespace`, removed by `hold release --namespace`) | On a held namespace's Function or upstream path: 503, `Retry-After: 30`, the problem detail of Decision 2; on its S3 gateway writes: the S3 error `ServiceUnavailable` (503) with that detail |

## Implementation plan

- `internal/platform/hold/namespace.go`: `CallGate`, `NeverCall`, `(never).NamespaceMarker`,
  `(*Hold).NamespaceMarker`; tests in `namespace_test.go` (the `restore` marker preferred, the `data` marker, none,
  the platform hold alone ⇒ false, `NeverCall` ⇒ false).
- `internal/dataplane/dataplane.go`: `Option`, `WithCallGate`, the `held` field (default `hold.NeverCall`), the
  `heldRetryAfter` constant and one helper that sets `Retry-After` and writes the problem, used by `serveFunction`
  and `serveUpstream`. Tests in the package's existing harness, one per scenario: `TestScenarioDataHoldRefusesCalls`,
  `…ReleaseServesCalls`, `…RestoreLoadRefusesCalls`, `…InternalCallRefused`, `…LakeUpstreamRefused`,
  `…SiteServesWhileHeld`, `…AuthBeforeHold`, `…PlatformHoldUnchanged`, `…UnheldNamespaceUnaffected`; each refused
  case asserts the activator was not reached.
- `pkg/funcd/funcd.go`: `c.callGate()` beside `c.nsGate()`, and `dataplane.WithCallGate(c.callGate())` on the
  `Handler` call (`:1502`). A test in `pkg/funcd/hold_internal_test.go` writes a `data` marker with
  `hold.MarkNamespaces`, as `restore run` does, assembles the daemon `WithHold`, and through its data-plane handler
  sees a call into the held namespace refused, a call into another namespace not refused, and the call no longer
  refused after `ReleaseNamespace`. The app-todo lane stays unchanged: its only platform restore (D2) restores a
  generation without a BackupSchedule, so it holds no namespace for data.
- `internal/blob/s3gateway` (amendment): `Deps.Held`, the backend's `held` field, `authorizeWrite` and `refuseHeld`
  for the writes of Decision 6, and `DeleteObjects`' whole-request refusal. Tests in `hold_test.go` over the
  package's harness, one per gateway scenario: `TestScenarioGatewayWriteRefused`, `…ReadServed`,
  `…RestoreLoadRefused`, `…AuthBeforeHold`, `…UnheldNamespaceServed`. `pkg/funcd/funcd.go` passes
  `Held: c.callGate()`; `TestS3GatewayAsksTheNamespaceHold` (`pkg/funcd/hold_internal_test.go`) sees the owner's
  write refused until the release.
- `examples/restore-runbook.md` step 8 and `examples/workload-backup.md` ("After a platform restore"): until the
  owner's release, calls to the namespace's Functions and writes through the S3 gateway get 503, and its Sites keep
  serving.
- **Done when**: the tests above pass with `-race`; build, vet (plain, dev, e2e, Linux) and lint are clean;
  `scripts/agent/gate.sh` passes, its e2e suite included; `just check-hygiene check-specs` passes.

## Review checklist

- [ ] `serveFunction` checks after `enforce`, the Function lookup and the observability fill, and before the limiter
      and the activator; internal calls are checked.
- [ ] `serveUpstream` checks and answers `edge upstream unavailable`; `serveStatic` does not check.
- [ ] A refused Function call answers 503, `Retry-After: 30` and Decision 2's detail; the activator is not reached.
- [ ] `NamespaceMarker` prefers `restore`, ignores the platform hold, reads memory only under the read lock.
- [ ] Without `WithCallGate` the Server asks `hold.NeverCall`; the daemon passes `c.callGate()`; the existing
      `Handler` callers are unchanged.
- [ ] No new field, config key or API; Cedar and the hold's writers and release are untouched.
- [ ] The S3 gateway's writes ask the hold after the PEP's allow and the Bucket's lookup; reads and
      `AbortMultipartUpload` do not; `DeleteObjects` refuses whole before any delete; the daemon passes `Held`.
- [ ] Every scenario has its named test; both daemon tests and both docs are updated.

## Consequences

- **Positive**: a call, or a write through the S3 gateway, into a namespace that waits for its data can no longer
  write what the owner's restore then erases; one check reuses the hold the platform already writes and lifts; a client sees a retryable 503 before any
  work.
- **Negative (accepted)**: a held namespace's Functions are down, reads included, until its owner's release; an owner
  who forgets the release keeps them down (`funcdctl hold status` lists the hold); a Site's own calls to those
  Functions fail meanwhile. An S3 client's writes fail with 503 until the release, a Lake engine's catalog saves
  included (ADR-0289 retries them). Each call takes the hold's read lock, so a call waits while a hold or a release writes and
  syncs its marker under the write lock (`namespace.go:59-74`, `:99-135`); moving that I/O out of the lock is a
  separate change.

## Open questions

- An owner's opt-out that keeps serving during the hold: a future ADR, if someone needs it.

## References

- [#1297](https://github.com/pyvvo/funcd/issues/1297) and its decision comment; tracker #1276;
  [#1302](https://github.com/pyvvo/funcd/issues/1302), the S3 gateway's writes, answered by the amendment.
- Experiment 0014, a backup target that lags or fails (pyvvo/experiment#26, private).
- `internal/platform/hold/namespace.go:41` (`HeldIn`, true while the platform is held), `:77` (`NamespaceMarkers`).
