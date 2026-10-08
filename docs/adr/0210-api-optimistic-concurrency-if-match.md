# ADR-0210: API optimistic concurrency — replace and delete honor the client's resourceVersion

- **Status**: Proposed
- **Date**: 2026-10-08
- **Deciders**: green-0-rabbit
- **Tags**: api, control-plane, resource-version, optimistic-concurrency, sdk, funcdctl, openapi, disaster-recovery
- **Realizes**: [FEAT-0009/F109](../feat/0009-feat-disaster-recovery.md) (exit clause: a client that holds a version
  from before a restore gets a conflict instead of overwriting)
- **Supersedes in part** (these clauses only; each keeps `Implemented`, back-linked at acceptance; lines at b48c6b9d):
  1. [ADR-0018](0018-api-server-authn-rbac-admission.md): the Temporary workarounds row "`Replace` is
     read-RV-then-Update" (line 233), its exit criterion met here, and the Decision 4 sentence "`Replace` reads the
     current `resourceVersion` and applies it so a client `PUT` is a normal optimistic update" (lines 213–214): a
     replace or delete that carries a version is conditional on it (Decisions 1–3).
  2. [ADR-0048](0048-dto-validation-reference.md): row 102, "server-set; ignored on input", for `resourceVersion` on
     a replace only: the body's value is the replace's precondition (Decision 1). On create it stays ignored. This
     item drops if the decider picks the header-only form (Open questions).
  3. [ADR-0024](0024-funcdcli-and-sdk.md): in the `Apply` contract, "(ADR-0018's replace = read-RV-then-Update)"
     (line 105): a body version makes `Apply`'s PUT conditional (Decisions 1 and 4). It drops with item 2.
  Everything else in all three stands, including ADR-0018 C3 (authorization before admission).
- **Relates to**: ADR-0202 (timeline; preconditions stay string equality) · ADR-0006 (the store's `Update`/`Delete`
  precondition, unchanged) · ADR-0005 (the `Handlers` seam) · ADR-0024 (SDK `Delete`) · ADR-0170 (forced
  group delete) · ADR-0063, ADR-0147 (admission, namespace lock) · ADR-0206 (restore; its `--object` output) · #844

## Context & Need

The store already rejects a stale write: `Update` and `Delete` return `fault.Conflict` when the precondition differs
from the stored version (`internal/store/store.go` `(*store).Update`, `(*store).Delete`). The REST API never passes
the client's version: `replaceObjIf` (`internal/controlplane/handlers.go`) overwrites it with the stored one before
`store.Update`, and `deleteObj` calls `deleteObjIf` with `""` (no precondition). A client holding an old object
therefore overwrites or deletes newer data silently. ADR-0202's restore timeline makes every pre-restore version
differ from a current one, but that protects API clients only if the API forwards their version.

Purpose: a PUT or DELETE that names the version it was based on succeeds only on that version; otherwise 409 and the
object is unchanged. Callers: `pkg/sdk` (`Apply`, `Delete`), `cmd/funcdctl`, any HTTP client.

## Scenarios

- **scenario: stale-replace-conflicts** — Given Function `f` updated from V1 to V2, When a PUT of `f` carries V1 in
  `metadata.resourceVersion` or as `If-Match: "V1"`, Then 409 problem+json `urn:funcd:problem:conflict` and `f` is V2.
- **scenario: current-replace-succeeds** — Given `f` at V2, When a PUT carries V2 (body, header or both), Then 200 and
  `f` holds the new spec at a new version.
- **scenario: unversioned-writes-stay-unconditional** — Given `f` at V2, When a PUT or DELETE carries no version (or
  `If-Match: *`), Then it succeeds as today.
- **scenario: bad-precondition-rejected** — Given `f`, When a PUT carries body V2 and `If-Match: "V1"`, or any write
  carries `If-Match` as `W/"V2"`, `"V1", "V2"`, `V2` unquoted or an empty value, or as two lines `"V2"` and `"V1"`,
  Then 400 and `f` is unchanged.
- **scenario: stale-delete-conflicts** — Given `f` at V2, When a DELETE carries `If-Match: "V1"`, Then 409 and `f`
  exists; with `"V2"`, Then 204 and `f` is gone.
- **scenario: stale-kvstore-replace-conflicts** — Given a KVStore a live Workflow made (ADR-0178 Decision 6), When a
  PUT carries a stale version, Then 409 whose detail says to re-read, not that the Workflow manages the store.
- **scenario: forced-group-delete-honors-version** — Given ResourceGroup `g` with members, When `DELETE ?force=true`
  carries a stale `If-Match`, Then 409 and every member still exists.
- **scenario: pre-restore-version-conflicts** — Given platform A, a backup at revision 100, then a write leaving `f` at
  `T1-130`, and B restored from that backup (ADR-0202 `Load`, new timeline), When a client PUTs or DELETEs `f` on B
  with `T1-130`, Then 409 and `f` keeps B's content.
- **scenario: sdk-held-version-conflicts** — Given an object read with `sdk.Get` and then changed by another writer,
  When the caller `Apply`s its copy, or `Delete`s with `sdk.IfVersion` of it, Then `fault.Conflict`, no POST follows.
- **scenario: workflow-pause-retries-on-conflict** — Given a WorkflowRun whose status changes between the command's
  read and write, When `funcdctl workflow pause` runs, Then it re-reads, the run ends paused, and no 409 is shown.

## Scope

**In**: the 25 PUT and 25 DELETE object operations (`internal/controlplane/routes.go`, `routes_rest.go`),
`replaceObjIf`, `deleteObjIf`, the forced ResourceGroup delete, `pkg/sdk` `Delete`, the `funcdctl workflow` commands
that read, change and apply, the generated OpenAPI spec. **Out**: the timeline and string comparison (ADR-0202);
restore and the hold (ADR-0206); admission and authorization (unchanged); create (POST ignores a version, ADR-0048);
writes that never call `replaceObjIf` or `deleteObjIf`: controllers, the garbage collector and every status write use
`store.Update` with the version they read; `HandoverKVStore` (`kvhandover.go`, a POST action) keeps read-then-update.

## Constraints & Decision drivers

- Q6 (report §5, decided 2026-10-06): clients do not parse a version; a version from another timeline never equals a
  current one. Preconditions stay string equality (ADR-0202 Decision 5).
- Old clients keep working on the new server: no flag day, no required header.
- One 409 for every stale version, whichever form carries it and wherever it is caught (handler or store).
- Fail closed on an ambiguous precondition (400, never a silent choice). No new dependency.

## Alternatives considered

| Option | Outcome |
|---|---|
| `If-Match` only, on both verbs; the body's version stays ignored | Not the default: keeps ADR-0048 row 102, but the Go SDK's read-change-`Apply` (body only) stays unprotected until every client sets the header, so F109 holds only for changed clients. The decider's alternative (Open questions) |
| Body only on PUT; DELETE stays unconditional | Rejected: a stale client can still delete, F109 incomplete |
| DELETE body (Kubernetes `DeleteOptions.preconditions`) or `?resourceVersion=` query | Rejected: some proxies and clients drop DELETE bodies; HTTP already has a header for the precondition |
| Require a version on every write (428, RFC 6585) | Rejected: a manifest has none, so the client would GET first: the same last-writer-wins plus a round trip; breaks every client at once; ADR-0206's `--object` output relies on an unversioned apply |
| 412 Precondition Failed for `If-Match` (RFC 9110 §13.1.1) | Rejected: the same stale version would get 409 from the body or from the store's race and 412 from the header; 412 needs a new `fault.Kind`. The S3 gateway's 412 is S3's protocol |
| Compare only in the store (pass the client's version to `store.Update`) | Rejected: the KVStore guard and admission would judge an object the client never saw and answer with their errors |
| Precondition carried in `ctx` instead of a `Delete<Kind>` parameter | Rejected: a hidden argument; ADR-0170 added `force bool` to the seam the same way |

## Decision

**1. Request form** (proposed; decider confirms at acceptance). A PUT's version is the body's
`metadata.resourceVersion` or the `If-Match` header; a DELETE's is `If-Match`. `If-Match` holds exactly one strong
entity-tag `"<resourceVersion>"`, or `*`, which counts as no version (RFC 9110: true when the object exists, which both
verbs already require). A weak tag, a list (in one line or over several `If-Match` lines), an unquoted or an empty
value is 400 `fault.Invalid`, judged on every line (Contracts). A PUT with both forms: equal is that version,
different is 400. The route passes one string on.

**2. Check.** `replaceObjIf` compares the version with the stored object's right after its `store.Get`, before the
guard and admission; a mismatch is `fault.Conflict` (409, `urn:funcd:problem:conflict`, the existing mapping in
`api/fault/problem.go`) whose detail names the kind and object and says to re-read. On a match the write carries that
version, so a write landing between the Get and `store.Update` still loses with the store's `fault.Conflict`.
`deleteObjIf` makes the same comparison on the Get it does when a Delete admission is registered; otherwise
`store.Delete` compares. Comparison is string equality: a version of another timeline, or a plain number from a lost
history, never equals the stored one (ADR-0202), so every pre-restore version conflicts. Order: 401; 400 for the
precondition's syntax (the resolver, like huma's body validation) and agreement (the route); 403; 400 path and body;
409; guard and admission; store.

| Caller | Today | After |
|---|---|---|
| `Replace<Kind>` of 24 kinds through `replaceObj` | stored version | the client's version, else the stored one |
| `ReplaceKVStore` through `replaceObjIf` with `refuseLiveMarked` | stored version | as above; compared before the guard |
| `Delete<Kind>` of 25 kinds through `deleteObj` (a ResourceGroup without force) | `""` | the client's `If-Match`, else `""` |
| `forceDeleteResourceGroup`: the group's Get, then its final `deleteObj` | no check, `""` | the client's version compared at the Get (409 before any member delete) and passed to the final delete |
| `deleteMember` → `deleteObjIf` with each listed version | listed version | unchanged (internal, already conditional) |
| `HandoverKVStore`, controllers, collector, status writes | own read version | unchanged (they never call these helpers) |

**3. No version** (proposed; decider confirms at acceptance). A write without a version stays unconditional, with no
end date: a PUT is read-then-update, a DELETE has no precondition. A manifest describes desired state and has no
version, and ADR-0206 Decision 5 prints a restored object without one so that `funcdctl apply -f` replaces the current
object. This is the API's contract, not a workaround.

**4. Clients** (proposed; decider confirms at acceptance). The server ships first; no client breaks.

| Client | Change |
|---|---|
| `pkg/sdk` `Apply` | none: `toWireBody` already sends `metadata.resourceVersion` (it drops only `status`), so a read-change-`Apply` becomes conditional and a manifest stays unconditional. A 409 is `fault.Conflict` (`problemToFault`); the POST fallback stays 404-only |
| `pkg/sdk` `Delete` | NEW option `IfVersion(rv)` sends `If-Match: "<rv>"` |
| `funcdctl apply -f` | none: a document with `metadata.resourceVersion` (e.g. saved from `get -o json`) is conditional; the 409 names the document |
| `funcdctl workflow pause`, `resume`, `cancel` (`cmd/funcdctl/workflow.go`) | read, change, apply now conflicts when the run controller writes status meanwhile: re-read and retry on `fault.Conflict`, at most 5 attempts (the `devApplyAttempts` bound, `cmd/funcdctl/dev.go`) |
| `funcdctl dev` `applyDesired`, `funcdctl delete` | none: they send no version; `applyDesired` keeps its retry for the Get-to-Update race |
| TypeScript and Python | no control-plane client exists (verified 2026-10-08, see References); one added later sends the version it holds (body on PUT, `If-Match` on DELETE) and treats 409 as re-read |

**5. OpenAPI** (proposed; decider confirms at acceptance). The spec stays generated (`just generate`,
`internal/controlplane/cmd/specgen`); each of the 50 operations gains an optional `If-Match` header parameter from the
embedded input field. No `ETag` response header (Open questions).

## Temporary workarounds

None. ADR-0018's read-RV-then-Update workaround exits here.

## Contracts

```go
package controlplane // internal/controlplane

// IfMatchParams (NEW) is embedded in every PUT and DELETE input; exported, as huma skips unexported embedded fields.
type IfMatchParams struct {
	IfMatch string `header:"If-Match" doc:"one strong entity-tag \"<resourceVersion>\" or *; the write is conditional on it (ADR-0210)"`
	rv      string
}

// Resolve (huma.Resolver) reads every If-Match line via ctx.EachHeader, as ctx.Header gives the first only and ""
// for empty. It sets rv to "" when absent or "*", else the tag unquoted (never parsed); for an empty value, two or
// more lines, a weak tag, a list or an unquoted value it returns fault.Invalid via wrapFaultError: 400, not huma's 422.
func (p *IfMatchParams) Resolve(ctx huma.Context) []error

// replaceVersion is a PUT's precondition: header or body alone, both when equal; both and different ⇒ fault.Invalid.
func replaceVersion(header, body string) (string, error)

type Handlers interface {
	// … every other method unchanged. Each of the 25 Delete<Kind> gains rv ("" ⇒ no precondition), for example:
	DeleteNamespace(ctx context.Context, name v1.ObjectName, rv string) error
	DeleteResourceGroup(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, force bool, rv string) error
	DeleteFunction(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error
	// Replace<Kind> is unchanged: the route sets the body's metadata.resourceVersion from replaceVersion, and
	// replaceObjIf treats a non-empty value as the precondition.
}
```

```go
package sdk // pkg/sdk

// IfVersion makes Delete conditional on rv: the server answers fault.Conflict when the object's current
// resourceVersion differs (NEW).
func IfVersion(rv string) DeleteOption
```

| consumes | exposes |
|---|---|
| `If-Match` (RFC 9110 §13.1.1); body `metadata.resourceVersion`; `store.Store` `Get`, `Update`, `Delete` preconditions (ADR-0006, unchanged); `fault.Conflict` → 409 (`api/fault/problem.go`) | optional `If-Match` on 25 PUT and 25 DELETE operations in `api/openapi/funcd.v1alpha1.yaml`; 409 on a stale version; 400 on a bad precondition; `sdk.IfVersion` |

## Implementation plan

**Files**: NEW `internal/controlplane/precondition.go` (`IfMatchParams`, `Resolve`, `replaceVersion`); `routes.go`,
`routes_rest.go` (embed `IfMatchParams` in every PUT input, `namespacedDelete`, `clusterScopedDelete` and
`deleteResourceGroupInput`; pass the version); `controlplane.go` (the `Delete<Kind>` signatures); `handlers.go`
(`replaceObjIf` compares before the guard; `deleteObjIf` compares on its Get; the `Delete<Kind>` methods pass `rv`;
`forceDeleteResourceGroup` compares at its Get and passes `rv` to the final delete); `stubs.go` (signatures);
`pkg/sdk/sdk.go` (`IfVersion`, the header on `Delete`); `cmd/funcdctl/workflow.go` (one read-change-apply helper with
the bounded retry for pause, resume and cancel); `api/openapi/funcd.v1alpha1.yaml` regenerated with
`scripts/agent/d just generate`. **go.mod**: none. **Blueprint**: none (it states no PUT or version semantics). At
acceptance, ADR-0018, ADR-0024 and ADR-0048 gain `Superseded in part by: ADR-0210`. **Roadmap and feat** (with the
sibling DR ADRs): a NEW F109 item for ADR-0210 in `docs/roadmap/dr-plan.json` after ADR-0202's item `DR-1`, mirrored
in the slate table and recomputed with `plan_waves.py`; the F109 row links ADR-0210 and moves to `adr`.

**Build order**: after ADR-0202, only for `TestScenarioPreRestoreVersionConflicts` (it uses `Snapshot` and `Load`).

**Test plan**: units `TestIfMatchResolve` (absent, `*`, quoted, weak, list, two lines, unquoted, empty) and
`TestReplaceVersion` in `internal/controlplane/precondition_test.go`. In NEW `internal/controlplane/concurrency_test.go`
(`httptest`, the real store and RBAC): `TestScenarioStaleReplaceConflicts`, `TestScenarioCurrentReplaceSucceeds`,
`TestScenarioUnversionedWritesStayUnconditional`, `TestScenarioBadPreconditionRejected`,
`TestScenarioStaleDeleteConflicts`, `TestScenarioStaleKVStoreReplaceConflicts`,
`TestScenarioForcedGroupDeleteHonorsVersion`, `TestScenarioPreRestoreVersionConflicts`. `pkg/sdk/sdk_test.go`:
`TestScenarioSDKHeldVersionConflicts`. `cmd/funcdctl/workflow_test.go`: `TestScenarioWorkflowPauseRetriesOnConflict`.
An existing test that replaces with a stale held version and expects success is changed to re-read first; the PR
lists each one. The spec drift test (`internal/controlplane/api_test.go`) passes on the regenerated spec.

**Definition of done**: every test above passes under `scripts/agent/d go test -race -count=1` for the touched
packages; `scripts/agent/d just ci` is green; no code outside `internal/store` parses a version; no new dependency; no
identity or path leak.

## Review checklist

- [ ] All 50 PUT and DELETE operations embed `IfMatchParams`; the regenerated spec lists 50 `If-Match` parameters.
- [ ] `replaceObjIf` compares before the guard and admission and sends the compared version to `store.Update`;
      `deleteObjIf` compares on its Get or forwards `rv` to `store.Delete`; equality is plain string comparison.
- [ ] A stale version answers 409 `urn:funcd:problem:conflict`; disagreeing or malformed preconditions answer 400; no
      write happens on either.
- [ ] The forced group delete answers 409 before any member delete; `deleteMember` and `HandoverKVStore` are unchanged.
- [ ] `sdk.IfVersion` sets `If-Match`; `Apply` is unchanged; the workflow commands retry at most 5 times.
- [ ] Each scenario has its named passing test.

## Consequences

**Positive**: the F109 exit clause holds for every API client that sends the version it holds; the Go SDK's
read-change-`Apply` gains lost-update protection without a client change; one 409 contract for body, header and store.
**Negative (accepted)**: a client or manifest that carries a stale version now gets 409 where it overwrote silently;
the workflow commands may take extra round trips; 25 `Handlers` `Delete<Kind>` signatures change; `If-Match` answers
409, not RFC 9110's 412. **Risk**: a client that holds a version but sends none stays last-writer-wins (Decision 3, by design).

## Open questions

| Item | Recommended default (proposed; decider confirms at acceptance) | Why |
|---|---|---|
| Header or body, and precedence | PUT: body or `If-Match`, equal or 400; DELETE: `If-Match` | the body is where a client holds its version today; DELETE has no body; ambiguity fails closed |
| A write without a version | unconditional, no end date | manifests and ADR-0206's `--object` output carry none; requiring one adds a GET and the same race |
| SDK and funcdctl rollout | server first; `sdk.IfVersion`; workflow commands retry 5 times; the rest unchanged | old clients keep working; the only new conflicts are read-change-apply races |
| Exempt writes | none to exempt: no API route writes status (`withStatus` copies the stored status) and controllers write through the store | the helpers serve only client requests; `HandoverKVStore` stays server-side |
| OpenAPI | optional `If-Match` on the 50 operations; no `ETag` header | clients read the version from `metadata`; an `ETag` on every response is an extra contract |
| `Apply` with a version on a missing object | keep the POST fallback | a create overwrites nothing |

## References

- Issue #844; `docs/reports/platform-disaster-recovery-design.md` §4.A (Lineage row), §5 Q6; F109's exit criterion.
- RFC 9110 §8.8.3, §13.1.1; RFC 6585 §3; RFC 9457; Kubernetes API conventions, "Concurrency Control and Consistency".
- huma v2.38.0 (module cache, 2026-10-08): `conditional.Params` embeds `header:"If-Match"` with a `Resolve` that
  sets unexported state; `getParamValue` uses `ctx.Header` (`huma.go:1669`), `Context.EachHeader` (`api.go:101`).
- Language clients checked 2026-10-08: no `apis/funcd.io` or `resourceVersion` in `pyvvo/funcd-typescript` v0.8.2 or
  `pyvvo/funcd-python` v0.5.1 (the versions `go.mod` pins), and GitHub code search of both default branches finds none.
