# ADR-0172: Revision integrity — read-only through the API, fail closed when missing, a name for every Function

- **Status**: Proposed
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: function, revision, api, naming, lifecycle, sdk, cli
- **Realizes**: [FEAT-0000/F13](../feat/0000-feat-v1.md) (apply → Revision stamped → deployed; the `Revision` kind)
- **Supersedes (in part)**, each keeping its status with a `Superseded in part by: ADR-0172` back-link at acceptance:
  - [ADR-0005](0005-api-surface-code-first-huma.md) Decision §2 "For each kind, an input struct … and an output
    struct … are registered with `huma.Register`" (lines 138–143), Contracts "… list/replace/delete × the 15 kinds …"
    (line 212) and "`RegisterRoutes` registers every kind's CRUD operation" (line 218), Implementation plan "the 15
    kinds' CRUD" (line 287), Review checklist "CRUD operations are registered for the 15 kinds" (line 319):
    `Revision` registers only list and get (Decision 1).
  - [ADR-0018](0018-api-server-authn-rbac-admission.md) Purpose "a store-backed `Handlers` that CRUDs the 15 kinds"
    (line 54), Scope "generic CRUD helpers over `store.Store` for all 15 kinds; … the 75 interface methods"
    (lines 97–98): `Revision` has only get and list (Decision 1).
  - [ADR-0020](0020-function-contract-lifecycle.md) C2 "The reconciler never mutates a stamped Revision" (line 119):
    narrowed to its `spec`; the ownerRef written on adoption is the one metadata write (Decision 5).
  - [ADR-0035](0035-artifact-digest-resolution-at-revision.md) Decision 3 "early-returns if one exists" and "when no
    Revision exists for the generation and the spec digest is empty, resolve" (lines 67–68): a gone stamped
    Revision fails closed instead (Decision 3).
- **Refines** [ADR-0024](0024-funcdcli-and-sdk.md) Decision §1 (lines 104–113), Contracts (lines 182–185), Exposes row
  (line 202), source-compatible: `Apply`, `Create`, `Delete` refuse a read-only kind before any request (Decision 2).
- **Made true** (unchanged): ADR-0035 lines 69–70 and 158–159; [ADR-0048](0048-dto-validation-reference.md) line 139
  (`RevisionSpec` is server-stamped); [ADR-0143](0143-redeploy-by-revision-switch.md) lines 214–215 (container ID ≤ 67
  characters), now for every valid Function name; blueprint line 49.
- **Relates to**: ADR-0048 line 95 (names 1–63) · ADR-0015 (requeue) · ADR-0047 (no-op writes) · queued ADR-0163
  (retry timing) · Proposed ADR-0170 (collects owned Revisions; `retireStale` kept;
  obligations (a)–(d) met by Decisions 1, 5, 3 and Scope) · Proposed [ADR-0161](0161-truthful-function-ready.md)
  (Decision 6) · Proposed ADR-0158: its plan step 3 (`poolKeyFor` serves
  `sameKeyFunctions`) also rewrites `sameKeyFunctions`; whichever lands second merges Decision 7's filter;
  `RevisionMissing` uses its `Ready`/`Degraded --> Failed` blueprint edge · Proposed
  ADR-0152 Contracts (container IDs: solo contain `.`, pool `_`, engine neither); this ADR keeps that disjoint: a revision name,
  hashed too, carries no `.` or `_`.

## Context & Need

The reconciler finds a Revision only by the name `<fn>-<generation>`, trusts its stored digest, runtime, handler and
image (`internal/function/function.go:1236-1292`, `:875-887`, `pool.go:137-146`), and resolves the tag again when none
is stored. On 1193be6 the API allows every Revision write (`routes.go:304-364`, `handlers.go:413-443`; only validate
admission runs):

- #52: `PUT revisions/greeter-1` (other digest) 200, a restart or wake runs it; `DELETE` 204, after a restart the moved
  tag ships while generation 1 shows Ready.
- #52 pre-create: `POST greeter-2` answers 200, stored without owner (#328); generation 2 adopts it unresolved.
- #54: when `len(name) + 1 + digits(generation) > 63` the store refuses the name (`internal/store/store.go:294`): a
  61-character name stops at generation 10, 62 or 63 never deploys; the status stays Ready and retries never end.
- Purpose: only the reconciler decides what code a Function runs; a missing Revision stops it visibly; every name stamps.

## Scenarios

- `scenario: revision-writes-refused` — Given `greeter` with `greeter-1` in `team-a`, When a developer or an admin
  sends `POST …/revisions`, `PUT …/revisions/greeter-1` or `DELETE …/revisions/greeter-1`, Then each answers 405
  problem+json with `Allow: GET`, nothing changes; get and list answer 200.
- `scenario: refused-edit-keeps-digest` — Given `greeter` from tag `v1` at A, When a `PUT` with digest E is refused,
  the tag moves to B and funcd restarts, Then the worker runs A.
- `scenario: missing-revision-fails-closed` — Given `greeter` Ready at generation 1 (A), When `greeter-1` is deleted
  through the store, the tag moves to B and the worker stops, Then `greeter` is Failed, Ready and RevisionReady False,
  reason `RevisionMissing`; no worker runs, nothing is resolved or created.
- `scenario: reapply-recovers-missing-revision` — Given that end state, When re-applied with a spec change, Then
  `greeter-2` is stamped at B and `greeter` is Ready.
- `scenario: recreated-function-stamps-afresh` — Given `greeter` at generation 3 deleted, When re-applied (generation
  1), old Revisions stored or not, owned or not, Then a `greeter-1` naming the new UID is stamped from the tag's current
  digest, Ready; `RevisionMissing` never appears.
- `scenario: long-name-deploys-with-hashed-revision` — Given a 62-character `L`, solo or pooled, When applied, Then
  Ready with Revision `L[:52]-<h>-1` (`<h>` the first 8 hex digits of SHA-256 of `L`), stable across a restart; a
  61-character `M` at generation 10 gets `M[:51]-<h>-10`.
- `scenario: short-name-revision-unchanged` — Given `greeter`, or a 61-character name at generation 9, When applied,
  Then its Revision is `greeter-1`, or `<name>-9`.
- `scenario: revision-name-taken-writes-status` — Given `L` and Function `S` = `L[:52]-<h>` Ready with `S-1`, When `L`
  is applied, Then `L` is Failed, reason `RevisionStampFailed` naming `S`; `S` and `S-1` unchanged.
- `scenario: revision-create-refused-writes-status` — Given `greeter` not yet stamped, When the store refuses the
  create as invalid, Then `greeter` is Failed, reason `RevisionStampFailed` with the store's message; the pass returns
  an error that `errors.Is` `errRevisionStampFailed`, retried (ADR-0015).
- `scenario: pooled-missing-revision-left-out` — Given pooled `a` and `b` Ready (A), When `a-1` is deleted, the tag
  moves to B and the pool worker restarts, Then `a` is Failed, reason `RevisionMissing`, `b` is Ready, nothing at B.

## Scope

In: the Revision API surface (routes, handlers, stubs, OpenAPI, SDK, `funcdctl`); `ensureRevision` for a missing,
foreign, ref-less or unstampable Revision; naming and its callers; pooled members. Out: deleting owned Revisions
(ADR-0170); retention (ADR-0139 line 296 follow-on); retry timing (ADR-0163); redeploy without a spec change,
rollback; Revisions created or edited through the API before this ADR (Consequences).

## Constraints & Decision drivers

- What was validated is what ships (ADR-0020 C2, blueprint lines 46 and 49); status is server-owned
  (`handlers.go:197-200`). Revisions
  stamped before 2dc9d26 (v0.2.1) have no ownerRef and must still be adopted.
- Names are DNS-1123 labels ≤ 63; stored names must not change (workers carry them, `containerd_linux.go:291`,
  `:375`); the name derives from the Function alone (`pool.go:138`, `:155`).

## Alternatives considered

| Option | Outcome |
|---|---|
| **A2. API serves get and list only** (Knative: users cannot create or update a Revision) | **chosen** — one gate closes all three writes |
| A1. Admission refuses Update (as `internal/controlplane/admission/workflowrun.go:94`) | rejected — delete and pre-create stay open |
| A3. Read-only except deleting an idle Revision (Knative's `kn revision delete`) | rejected — more admission; ADR-0170 deletes with the Function |
| **B3. A missing stamped Revision fails closed** | **chosen** — never ships unvalidated code |
| B1. Resolve the tag again (today) | rejected — silently ships new code |
| B2. Re-create from a recorded digest | rejected — `FunctionStatus` records none |
| **C1. `<fn>-<gen>` while it fits, else `<truncated fn>-<hash>-<gen>`** (Knative `kmeta.ChildName`) | **chosen** — stored names unchanged |
| C2. Always hash | rejected — renames Revisions under running workers |
| C3. Cap the Function name | rejected — rejects names valid today |

## Decision

1. **Read-only API.** `registerRevision` keeps `listRevisions` and `getRevision`; `createRevision`, `replaceRevision`,
   `deleteRevision` go, with their `Handlers`, `storeHandlers` and `StubHandlers` methods. `POST …/revisions` and
   `PUT`/`DELETE …/revisions/{name}` answer 405 problem+json, `Allow: GET`, for every authenticated principal
   (`installRouterErrors`; `Authn` answers 401 first); no route takes `PATCH`; `just generate` drops them from OpenAPI.
   The reconciler writes via `store.Store`: `Create`/`Delete` unchanged, plus the adoption's metadata-only `Update` (Decision 5).
   The one API-initiated delete reaching a Revision is ADR-0170's ResourceGroup force, for a ref-less group member.
2. **SDK and CLI.** `sdk.Client.Apply`, `Create` and `Delete` refuse a read-only kind (`sdk.ReadOnlyKind`:
   `Revision`) with `fault.Invalid` "Revision is read-only: the Function reconciler writes it" before any request.
   `funcdctl apply -f` checks it in its offline pre-flight, so a file with a Revision applies none of its documents;
   `funcdctl delete revision <name>` fails in the SDK; both exit 1. `problemToFault` is unchanged.
3. **Fail closed.** `ensureRevision` resolves the tag only on the create path: nothing of this Function stored under
   `revisionName(fn)` and `fn.Status.CurrentRevision` is not that name. When nothing is stored but `currentRevision`
   is that name, it returns `errRevisionMissing`, resolving and creating nothing. `currentRevision` is set only after
   a create or adoption, so a never-stamped generation takes the create path. A spec change recovers; an identical re-apply
   is a no-op (ADR-0047), so the Function stays `RevisionMissing`.
4. **The name** is `revisionName`: `<fn>-<gen>` while that fits 63, else the first `53 − digits(gen)` characters,
   `-`, 8 hex digits of SHA-256 of the Function name, `-`, the generation: exactly 63, a DNS-1123 label, chosen per
   generation; `ensureRevision`, `pinnedMember`, `servingMember` call it; no stored Revision is renamed.
5. **Ownership and collisions.** `<p>-<h>-<g>` is also the unhashed name of a Function `<p>-<h>`. `revisionOf`
   (replacing `ownedByAnother`) judges a stored Revision: `revSelf` — controller ref names this Function and UID, or
   ref-less with `spec.function` naming it and the status (`currentRevision` or `servingRevision`) naming it;
   `revNamesake` — controller ref with another UID (#55), or ref-less with `spec.function` naming it and unnamed by
   the status; `revOther` — the ref, or ref-less `spec.function`, names another Function. `ensureRevision` drops a
   `revNamesake` (`dropRevision`) and stamps afresh. A collision fails the later Function: a `revOther` is never
   adopted, dropped or overwritten; `errRevisionStampFailed` names the other Function. So two solo workers never share
   a container or CNI ID: a Revision name is a store key unique per namespace and a `revOther` is refused. Adopting a ref-less `revSelf` writes the controller ownerRef with `store.Update`, metadata
   only; an error, Conflict included, is returned before `currentRevision` is set. `revisionTemplate` reads only
   `revSelf` (`revOther` `fault.Conflict`, `revNamesake` `fault.NotFound`).
6. **Status on failure.** `Reconcile` maps `errRevisionMissing` to `gateFailed` with reason `RevisionMissing`, and
   `errRevisionStampFailed` (`revOther`, or a `store.Create` refused with `fault.Invalid`) to `RevisionStampFailed`, both
   phase Failed with the error as message; `gateFailed` requeues at the supervision period. A running worker serves
   until it exits; none is replaced or woken. `RevisionMissing` returns no error; the repeated status write is a no-op
   (ADR-0047). `RevisionStampFailed` returns the error after its status write, retried by controller backoff
   (ADR-0015). Any other `Create` error (`fault.Internal`, engine I/O, ctx) is returned unwrapped and stays retryable,
   never Failed. A create Conflict re-reads: `revSelf` is adopted, `revOther` is `errRevisionStampFailed`, a
   `revNamesake` returns the Conflict (retried; the next pass drops it). Under ADR-0161, `Reconcile` returns
   `RevisionStampFailed` wrapped in `routeError`, which ADR-0161 Decision 1 returns as-is after the pass's own status
   write; the two land in either order. Other `ensureRevision` errors stay `failPass`'s.
7. **Pooled members.** `pinnedMember` returns `errRevisionMissing` for a stamped member whose Revision is gone, Conflict
   for `revOther`; `sameKeyFunctions` keeps it at its serving revision (`servingMember`), else leaves it out (its pass
   reports it). An unstamped member (NotFound, `revNamesake`) is returned as stored. No stamped member enters the pool
   manifest without its digest.

## Temporary workarounds

None.

## Contracts

```go
// internal/function/function.go (imports add crypto/sha256 and encoding/hex)
const maxRevisionName = 63 // a DNS-1123 label, validated by store.Create
const revisionHashLen = 8  // hex digits of SHA-256(Function name) in a shortened name

// revisionName is the Revision a Function's generation stamps (ADR-0020, ADR-0172).
func revisionName(fn *v1.Function) string {
	gen := strconv.FormatInt(fn.Generation, 10)
	name := string(fn.Name)
	if len(name)+1+len(gen) <= maxRevisionName {
		return name + "-" + gen
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "-" + hex.EncodeToString(sum[:])[:revisionHashLen] + "-" + gen
	return name[:maxRevisionName-len(suffix)] + suffix
}

type revisionOwner int

const (
	revSelf revisionOwner = iota
	revNamesake
	revOther
)

// revisionOf follows Decision 5: a controller ref decides first (this UID revSelf, another UID of this name
// revNamesake, another Function revOther); ref-less, spec.function naming another Function is revOther, then
// revSelf if the status names it, else revNamesake.
func revisionOf(rev *v1.Revision, fn *v1.Function) revisionOwner

var (
	errRevisionMissing     = errors.New("the stamped revision of this generation is missing")
	errRevisionStampFailed = errors.New("revision could not be stamped")
)
```

`ensureRevision(ctx, fn) (digest string, err error)` keeps its signature:

| `Get(revisionName(fn))` | `revisionOf` / status | Result |
|---|---|---|
| found | `revSelf` | ref-less: ownerRef via `store.Update` (error returned); then `currentRevision` = name, its digest |
| found | `revNamesake` | `dropRevision`, then the create path |
| found | `revOther` | `errRevisionStampFailed`; no write |
| NotFound | `currentRevision` == name | `errRevisionMissing`; no resolve, no create |
| NotFound | `currentRevision` ≠ name | pin (spec digest, else resolve), `retireStale` (ADR-0170), `store.Create` |
| other error | — | returned (retried), as today |
| `Create` Conflict | re-Get: `revSelf` / `revNamesake` / `revOther` / error | adopt / Conflict returned (retried; next pass drops it) / `errRevisionStampFailed` / returned |
| `Create` `fault.Invalid` | — | `errRevisionStampFailed` wrapping it |
| `Create` other error | — | returned (retried) |

```go
// pkg/sdk/kinds.go
func ReadOnlyKind(k v1.Kind) bool { return k == v1.KindRevision }
```

| Consumes | Exposes |
|---|---|
| `store.Store` `Get`/`Create`/`Update`/`Delete` of Revisions; `status.currentRevision`, `status.servingRevision` | `GET …/revisions`, `GET …/revisions/{name}`; 405 + `Allow: GET` for writes; `sdk.ReadOnlyKind`; reasons `RevisionMissing`, `RevisionStampFailed` (on RevisionReady always, on Ready only when nothing serves); names ≤ 63; adopted Revisions carry a controller ownerRef |

## Implementation plan

1. Control plane: remove `createRevisionInput`, the three operations (`routes.go:293-296`, `:318-330`, `:343-363`),
   their `Handlers`, `storeHandlers` (`handlers.go:413-419`, `:433-443`) and `StubHandlers` (`stubs.go:259-268`,
   `:282-302`) methods; `just generate`.
2. `pkg/sdk` and `cmd/funcdctl`: `ReadOnlyKind`; `TestSDKKindPaths_MatchServerRoutes` requires only GET for it.
3. `internal/function`: `revisionName`, `revisionOf`, `ensureRevision`, `Reconcile`, `revisionTemplate`,
   `pinnedMember`, `sameKeyFunctions`.
4. Tests `TestScenario` + CamelCase name: `TestScenarioRevisionWritesRefused` (`internal/controlplane/revision_test.go`,
   developer and admin); `TestScenarioRefusedEditKeepsDigest` (`pkg/funcd/revision_e2e_test.go`, tag `e2e`, a
   resolving, recording materializer, short data dir); `TestScenarioPooledMissingRevisionLeftOut` (`internal/function/pool_test.go`);
   the other seven in `internal/function/digest_test.go` — `TestScenarioLongNameDeploysWithHashedRevision` computes
   the hashed name (not `fnRev`); `revision-create-refused-writes-status` calls `Reconcile` and asserts
   `errors.Is(err, errRevisionStampFailed)`. Unit tests: `revisionName` (short; exactly 63; 61 at gen 10; 62, 63 at 1;
   `math.MaxInt64`; valid, ≤ 63, stable); `revisionOf` (each Decision 5 case); adoption ownerRef and its `Update`
   error; the SDK refusal sends no request; `TestCLIRevisionIsReadOnly` (`cmd/funcdctl/cli_test.go`, `execCLI`):
   `apply -f` of a ConfigMap plus a Revision applies neither, `delete revision x` fails, each `fault.Invalid`
   with the read-only message.
5. Done: every scenario's test passes; `just ci-full` and the Lima lanes green.
6. Feat row F13 links this ADR (`revision integrity: adr`); at acceptance, back-links in ADR-0005, ADR-0018,
   ADR-0020, ADR-0035. No blueprint change.

## Review checklist

- [ ] No Revision write in OpenAPI or routes; 405 for developer and admin; SDK and `funcdctl` refuse before sending.
- [ ] `revisionName` alone forms a Revision name (non-test); the reconciler's one `Update` is the adoption ownerRef;
      no path adopts, drops or overwrites `revOther`. Each scenario has its passing test; no identity or path leak.

## Consequences

- Positive: #52, #54 fixed; tracker #210 closes. Negative: no API write of a Revision; old Revisions accumulate
  until the ADR-0139 retention follow-on; a never-adopted ref-less Revision outlives its Function (only a ResourceGroup
  force removes it); a lost Revision keeps its Function down until a spec change; long names read worse.
- Risks accepted: a Revision API-edited before this ADR stays `revSelf` with its edited digest; a pre-2dc9d26 stamp
  whose status write was lost is re-stamped from the tag; a developer can take a long Function's next name with a
  Function `<p>-<h>` (and can already delete it); a pre-ADR API-created ref-less Revision under a Function's next name whose
  `spec.function` names another Function is `revOther` (`RevisionStampFailed` until a spec change); `RevisionStampFailed` retries ~1/s until ADR-0163.

## Open questions

None.

## References

- Issues [#52](https://github.com/pyvvo/funcd/issues/52), [#54](https://github.com/pyvvo/funcd/issues/54),
  [#210](https://github.com/pyvvo/funcd/issues/210), #328, #55; 2dc9d26; Knative Revisions, `kmeta.ChildName`.
- Code cited at 1193be6; unchanged at origin/main 5dbb7fb except line shifts in `internal/function/pool.go` (+1) and
  `internal/runtime/containerd/containerd_linux.go`.
