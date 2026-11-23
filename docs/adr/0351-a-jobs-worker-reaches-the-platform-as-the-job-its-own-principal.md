# ADR-0351: A Job's worker reaches the platform as the Job, its own principal

- **Status**: Implemented (2026-11-24), review passed with three Minors and no Major ([report](../reviews/adr-0351-implementation-claude-opus-5-5.md)), its sockets scenario under the decider's ruling of 2026-11-23 on [#1018](https://github.com/pyvvo/funcd/issues/1018#issuecomment-7116120716); Reviewing (2026-11-23, implemented); Accepted (2026-11-23) on the decider's go, after two judge rounds with every Blocker and Major folded
- **Date**: 2026-11-23
- **Deciders**: green-0-rabbit
- **Tags**: jobs, security, cedar, local-api, egress, s3
- **Realizes**: [FEAT-0012/F140](../feat/0012-feat-infrastack.md) (a Job's own identity) and
  [FEAT-0007/F151](../feat/0007-feat-egress-control.md) (egress for Job runs)
- **Supersedes (in part)**, with back-links at acceptance: [ADR-0074](0074-cedar-authorization-resource-access.md)
  (the local API's principal comes from a ref that names its kind);
  [ADR-0116](0116-capability-authorization-framework.md) (the principal objects gain `*Job`);
  [ADR-0310](0310-each-provider-serves-its-own-local-api-facet.md) Decision 1 (a facet's caller names its kind and,
  for a Job, its run); [ADR-0073](0073-kv-bindings-and-subdomains.md),
  [ADR-0233](0233-sql-access-from-functions.md) and [ADR-0327](0327-a-function-writes-the-secrets-it-owns.md) (an
  owner names its kind); [ADR-0275](0275-a-bucket-prefix-owner-names-its-kind.md) (`PrefixOwner` becomes
  `WorkloadRef`); [ADR-0175](0175-engine-is-its-own-principal.md) Decisions 1 and 3 (the egress index and the S3 key
  know a Job); [ADR-0137](0137-per-caller-catalog-query-rbac.md) and
  [ADR-0338](0338-a-function-calls-an-add-on-objects-engine.md) Decision 3 (the Lake token and the add-on proxy's
  token name their kind); [ADR-0117](0117-egress-policy-enforcement.md) (`appliesTo` items name their kind);
  [ADR-0136](0136-roles-and-role-assignments.md) (a role may be assigned to a Job)
- **Relates to**: [ADR-0347](0347-a-job-runs-its-handler-once-per-jobrun-in-a-worker-of-its-own.md) (the run's worker)
  · ADR-0349 (the `Job` entity type) · ADR-0085 · ADR-0306 · ADR-0346 (a dev worker's listener)

## Context & Need

An OpenTofu run holds cloud credentials and writes its stack's state, so it must reach that state and nothing else,
and every run of a Job reaches the same state, so the Job is the unit to authorize. F140 decided that a Job reaches
the platform's services as itself, never as a Function (2026-11-23): every binding and Secret write authorizes as the
Job whatever its runtime, and policies can name it. F151 decided that a policy names a Job as `{kind: Job, name}` in
`appliesTo` (2026-11-23). Today each path that authorizes a worker names a Function by its bare name: the local API's
`Ref{Namespace, Function}` and its sockets, the principal objects, the owners of KV tables, SQL databases and
Secrets, the S3 key, the Lake and add-on proxy tokens, the egress index and `appliesTo`.

## Scenarios

- `scenario: job-writes-as-itself`: Given a Job `tofu` whose binding names a prefix owned by `{kind: Job, name: tofu}`;
  when a run's handler writes it; then the write succeeds, and a Function `tofu` binding the prefix gets 403 on a write.
- `scenario: job-writes-its-secret`: Given a Secret owned by `{kind: Job, name: tofu}`; when the run calls
  `ctx.secret.put`; then it writes, a Function `tofu` gets 403, and the run gets 403 on a Secret its namesake owns.
- `scenario: job-policy-forbid`: Given a namespace Policy that forbids `secret::read` to `Job::"team/tofu"`; then the
  run's `ctx.secret.get` answers 403, and a Function `tofu` still reads.
- `scenario: job-tokens-name-their-kind`: Given a Lake and an add-on object that only the Job `tofu` binds; when a
  Function `tofu` calls each with its own token; then 403, while the run's calls with its tokens succeed.
- `scenario: job-s3-keys`: Given the S3 gateway on and a Bucket binding; then the run's worker gets keys whose
  principal is the Job, and a key derived for a Function `tofu` does not open its prefix.
- `scenario: job-egress-named`: Given egress on and an EgressPolicy with `appliesTo: [{kind: Job, name: tofu}]`
  allowing `api.example.com:443`; then the run reaches it, a Function `tofu` does not, nor does the run under a
  policy with an empty `appliesTo`.
- `scenario: job-writer-role`: Given a RolesAssignment that grants a KV table's writer role to `{kind: Job, name:
  tofu}`; then the run writes that table, and a Function `tofu` gets 403.
- `scenario: job-runs-have-their-own-sockets`: Given `concurrency: 2` and two `Running` runs; then each run's
  `GET /job/run` answers its own run.

## Scope

In: the Job as a Cedar principal; the local API's caller; owners, keys, tokens, writer roles and `appliesTo` that name
a Job. Out: a person as a principal (F147); policies on approvals (F148); add-on kinds in `appliesTo` (the
EgressPolicy ADR for add-on engines); a Job starting another Job's run.

## Constraints & Decision drivers

- A Job is never a Function: a Job and a Function may share a name (F140; ADR-0152's lesson for Lakes).
- Owners name their kind, as a prefix's has since ADR-0275, and `appliesTo` takes the same shape (F151; the decider,
  2026-11-22, for add-on engines: "we give kind and another resource name").
- The binding is the grant; a forbid Policy revokes it (ADR-0116). A credential is never client-asserted (ADR-0074).

## Alternatives considered

- **The Job as a hidden Function principal** (`Function::"<ns>/<job>"`): a same-named Function would share its rights,
  and no policy could tell them apart. Rejected by F140.
- **A principal per run**: every run of a Job reaches the same state. Rejected by F140's why; the run is in the ref
  only to answer `GET /job/run`.
- **An `ownerKind` beside each owner name**: two fields for one fact, unlike ADR-0275's. Rejected.
- **An empty `appliesTo` that covers Jobs**: the decider kept it Functions only (2026-11-22). Rejected.

## Decision

1. **The principal.** A Job is the Cedar principal `Job::"<namespace>/<name>"`: the entity type that ADR-0349 adds as
   a resource becomes a principal too, with a Function's attributes. The capability registry materializes its
   bindings onto it as onto a Function's (ADR-0116): `PrincipalObject` gains `*Job`. Every worker of its runs acts as
   the Job, whatever its runtime or driver, and a namespace Policy names it as it names a Function (`principal is
   Job`, `Job::"<ns>/<name>"`).
2. **The local API's caller.** `local.Ref` is `{Namespace, Kind, Name, Run}`: Kind `Function` or `Job`, and Run the
   run's name for a Job's worker, empty for a Function's. `Manager.SocketFor` and ADR-0346's per-worker listener take
   the whole ref, so every run has a socket of its own, and a Job and a Function of one name never share one. Every
   facet asks Cedar as `<Kind>::"<ns>/<name>"`, and `GET /job/run` (ADR-0348) answers the ref's run. A ref of any
   other kind, the zero value included, is refused. ADR-0347's executor builds its run's ref, and creates a run's
   worker only once no instance of that run's name remains, so a reused run name never shares a socket or a worker.
3. **Owners.** `PrefixOwner` becomes `WorkloadRef{kind, name}`, kinds `Function`, `Lake` and `Job`. A KV table's, a
   SQL database's and a Secret's `owner` take it, at the root and where a Function, Workflow or App declares them,
   so `owner: fn` becomes `owner: {kind: Function, name: fn}`. A Lake owns a prefix as today, and a SQL database too,
   as a Workflow's lake owner does through `ownerFor`. The store-owner admission fills an owner that a Job's own store
   leaves out with the Job. Each write check compares kind and name, and the pool access index counts only Function
   owners and Function `appliesTo` items, as it counts prefixes since ADR-0275. No stored object is migrated.
4. **Keys and tokens.** The in-platform S3 key's kind code gains `J` (ADR-0175 Decision 3's format, ADR-0085's
   derivation). The Lake token's claims gain the kind (`{NS, Kind, Name}`), and the add-on proxy's token signs
   `<kind>/<ns>/<name>`; the Lake gateway and the proxy read each back as `<Kind>::"<ns>/<name>"`, so a Job's token is
   never a same-named Function's.
5. **Writer roles.** A RolesAssignment may name `{kind: Job, name}` (`PrincipalKindJob`), as it names a Function.
6. **Egress.** The egress index (ADR-0175 Decision 1) takes a run's worker's Job from its run's `spec.job`, read from
   the store by the worker's name, and indexes it as `Job::"<ns>/<job>"`. An EgressPolicy's `appliesTo` items become
   `WorkloadRef`s, and admission accepts the kinds `Function` and `Job`; an empty `appliesTo` still applies to every
   Function only, so a Job reaches only what a policy names it for.

## Design model

None. The Decision maps existing checks onto a second kind: no lock, timer, stop path or race of its own.

## Behavior rows

- **security.md**: the Job as a principal, its owners, keys, tokens, roles and policies. **egress.md**: `appliesTo`
  items and a Job's egress.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1 (PrefixOwner renamed; every owner field and EgressPolicySpec.AppliesTo take it)
type WorkloadRef struct {
	Kind Kind       `json:"kind" enum:"Function,Lake,Job"` // each field's admission narrows it
	Name ObjectName `json:"name"`
}
func FunctionOwner(name ObjectName) WorkloadRef
func JobOwner(name ObjectName) WorkloadRef
const PrincipalKindJob PrincipalKind = "Job"

// internal/workernode/local
type Ref struct {
	Namespace v1alpha1.NamespaceName
	Kind      v1alpha1.Kind       // KindFunction or KindJob; any other is refused
	Name      v1alpha1.ObjectName // the Function or the Job
	Run       v1alpha1.ObjectName // a Job's worker's run; empty for a Function
}
func (m *Manager) SocketFor(ref Ref) (string, error)

// internal/lake/gateway: DeriveLakeToken(master, ns, kind, name); internal/services/addon: Token(master, ns, kind,
// name). internal/auth/cedar: entityTypeJob, ADR-0349's, also a principal type. internal/blob/s3gateway: "J".
```

| Field | Before | After |
|---|---|---|
| a KV table's, SQL database's or Secret's `owner`, root or declared | `fn` | `{kind: Function, name: fn}` |
| `Bucket.spec.prefixes[].owner` | `{kind: Function, name: fn}` | the same, kinds `Function`, `Lake`, `Job` |
| `EgressPolicy.spec.appliesTo[]` | `fn` | `{kind: Function, name: fn}` or `{kind: Job, name: job}` |

| Dependency & I/O | Direction | Shape |
|---|---|---|
| Cedar entity `Job` | consumes | the principal of a Job's workers and policies |
| `local.Ref` | consumes | every facet's caller, a Job's with its run |
| the S3 key's `J`, the Lake token's and the proxy token's kind | consumes | a Job's credentials |

## Implementation plan

1. `api/types/v1alpha1`: `WorkloadRef` for `PrefixOwner`, every owner field and `appliesTo`, `PrincipalKindJob`,
   their checks; admission's owner fill and `bindingdeclared` checks; `internal/workflow` `ownerFor`. The examples and
   lanes in pyvvo/funcd-functions that set an owner, updated and released.
2. `internal/auth/cedar`: the `Job` principal, `PrincipalObject` for `*Job`, the write checks and `appliesTo`
   compilation on kind and name.
3. `internal/workernode/local`: `Ref` with its kind and run in every facet, `SocketFor`, ADR-0346's listener, the
   refusal of another kind, `PoolSocketFor`'s member refs as Function refs; `internal/function/poolaccess.go`: Function
   owners and `appliesTo` items only.
4. `internal/blob/s3gateway`: `J`. `internal/lake/gateway` and `internal/services/addon`: the tokens' kind, written and
   read back. `pkg/funcd`: the egress index of a run's worker.
5. Docs: the rows, the guide's Secrets, KV, SQL and egress pages.

Tests, each written to pass: the types' checks; Cedar tests of a Job and a same-named Function in both directions; the
facets' caller tests and the refusal of another kind; the keys' and tokens' kinds; one `TestScenario…` per scenario
in `pkg/funcd`'s e2e suite, the egress one on the Linux lane. Done when `just ci-full` and `just test-linux` pass, in
the PR that implements ADR-0347.

## Review checklist

- [ ] Every scenario has its passing `TestScenario…`.
- [ ] No path authorizes a Job's worker as a Function, nor a Function as a Job of its name, keys and tokens included.
- [ ] Every owner and `appliesTo` item names its kind; each write check compares both.
- [ ] Every run of a Job has its own socket; a ref of another kind is refused; an empty `appliesTo` reaches no Job.

## Consequences

- Every manifest that names an owner by name alone, and every `appliesTo`, takes the `{kind, name}` form.
- A Job needs an EgressPolicy that names it before it reaches outside, once egress is on.
- A run authorizes with its Job's current bindings and policies, not its pinned revision's: an edit during a long
  run changes what it may reach.
- A SQL database that a Workflow's lake owns is written by its role writers only, since a Lake writes no SQL: a
  Function named like the lake, which writes it today through the bare name, no longer does.

## Open questions

None.

## References

- Feature issues #1018 (FEAT-0012 F140) and #1027 (FEAT-0007 F151), under tracker #1028; the decider's answers of
  2026-11-22 (`appliesTo`).
- ADR-0347, ADR-0349, ADR-0074, ADR-0116, ADR-0310, ADR-0073, ADR-0233, ADR-0327, ADR-0275, ADR-0175, ADR-0137,
  ADR-0338, ADR-0117, ADR-0136, ADR-0085.
