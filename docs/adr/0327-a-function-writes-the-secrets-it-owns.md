# ADR-0327: A Function writes the Secrets it owns, and built-in roles name Secret readers and writers (amends ADR-0321)

- **Status**: Reviewing (2026-11-13, implementation done; the shims' releases and pins follow); Accepted (2026-11-13), by the decider, with the six choices derived from "copy KV and SQL" confirmed as
  written, after two rounds of an independent judge (a subagent): round 1's Major, four Minors and four Nits and
  round 2's Minor and Nits folded
- **Superseded in part by**: [ADR-0328](0328-the-typescript-secret-client-deletes-with-del.md) (2026-11-13) — Decision 5's TypeScript `delete` and its other mentions: TypeScript deletes with `ctx.secret.del(binding, key)`, as its KV and Blob clients; Python keeps `delete`.
- **Date**: 2026-11-13
- **Deciders**: green-0-rabbit
- **Tags**: secrets, authorization, cedar, roles, local-api, shims
- **Realizes**: [FEAT-0008/F236](../feat/0008-feat-iam.md) (a Function writes the Secrets it owns)
- **Supersedes (in part)**:
  - [ADR-0321](0321-cedar-decides-each-secret-a-workload-reads.md): Decision 2's "No built-in role holds
    `secret::read`"; Decision 4's "it reads no Secret", since a decision now reads the Secret for its owner (a missing
    one still passes); its Scope's "Secret writes (F236, #1432); a role that grants a read"; and its review
    checklist's "no built-in role holds `secret::read`" and "reads no Secret". Its reads, its forbid and its scenarios
    stand.
  - [ADR-0322](0322-a-lakes-forbidden-secret-is-told-in-the-lakes-chapter.md): its Decision's security.md row, which
    the new Secrets chapter takes with the other Secret rows (the split its Consequences postponed).
- **Relates to**: [ADR-0233](0233-sql-access-from-functions.md) Decisions 2, 5 and 6 (the model this copies) ·
  [ADR-0136](0136-roles-and-role-assignments.md) (writer roles feed `writers`) ·
  [ADR-0249](0249-ctx-secret-reads-from-funcd.md) Decisions 1 and 4 (the routes and the shims gain a write) ·
  [ADR-0275](0275-a-bucket-prefix-owner-names-its-kind.md) (its Out: an owner only a Function can be stays a name) ·
  [ADR-0158](0158-pool-member-identity.md) Decisions 5 and 6 (owned access splits a pool; re-keying)

## Context & Need

`ctx.secret` only reads (ADR-0249: `GET /secret`, `GET /secret/{key}`), and a Secret holds only `type` and `data`
(`api/types/v1alpha1/secret.go:15`), so a Function that refreshes a credential, such as an OAuth token, cannot keep it
as a Secret (#1432). No built-in role names Secrets (`internal/auth/cedar/roles_compile.go:36`), while blob, KV and
SQL each have a reader and a writer role, and a writer role is how a principal writes what another owns (ADR-0136).
On 2026-11-13 the decider asked for the missing action and the built-in roles now, copying what KV and SQL do; the
answers are recorded on #1432. The write serves a workload's own code; people keep the API.

## Scenarios

- `scenario: secret-owner-writes` — *Given* Secret `oauth` with `owner: refresher`, bound by Functions `refresher` and
  `api`, *When* `refresher` calls `ctx.secret.put("oauth", "TOKEN", "t2")`, *Then* it returns and `api`'s next
  `ctx.secret.get("TOKEN")` is `t2`; *When* it calls `ctx.secret.delete("oauth", "TOKEN")`, *Then* `api`'s next get
  finds nothing.
- `scenario: secret-write-refused` — *Given* `api` binds `oauth` and is no writer of it, *When* it puts a key, *Then*
  403 naming `builtin/secret-single-writer`, and `oauth` is unchanged; *When* `refresher` puts a key of a Secret it
  does not bind, or a Policy of the namespace forbids its `secret::write` on `oauth`, *Then* 403.
- `scenario: secret-writer-role` — *Given* a RolesAssignment grants `api` `Secret Data Writer` on scope `Secret`
  `oauth`, or on its Namespace, *When* `api` puts a key, *Then* it is written; *Given* `Secret Data Reader` instead,
  *Then* the put is 403, and `api` still reads only the Secrets it binds.
- `scenario: secret-write-checks-the-value` — *When* a put names a key that is not an environment-variable name, or a
  value that holds a NUL, *Then* 400 and nothing is written; *When* the bound Secret does not exist, *Then* 404.
- `scenario: secret-concurrent-writes` — *Given* `refresher` and `api`, with a writer grant, put different keys of
  `oauth` at once, *Then* both keys hold their values.
- `scenario: secret-apply-replaces-written-keys` — *Given* `refresher` wrote `TOKEN`, *When* a person applies `oauth`
  with other data, *Then* `oauth` holds the applied data only.
- `scenario: secret-owner-splits-the-pool` — *Given* pooled Functions `refresher` and `api` of one runtime bind the
  same Secrets and share a pool worker, *When* a person sets `owner: refresher` on `oauth`, *Then* within one pass
  they run in different pool workers.

## Scope

- **In**: `secret::write` and the Secret entity's `owner` and `writers`; `Secret.spec.owner`; the roles `Secret Data
  Reader` and `Secret Data Writer` and the scope kind `Secret`; `PUT` and `DELETE /secret/{binding}/{key}`; both
  shims' `put` and `delete`; owned Secrets in the pool access index; the behavior rows and the security chapter's
  split.
- **Out**: creating or deleting a Secret from a workload; a conditional write of a key; a Lake or an AI gateway as a
  writer, whose engines have no `ctx.secret`; a typed owner; a read of an unbound Secret, which no read path asks
  (Decision 3); cross-namespace Secrets; people's API access, Backup and Restore runs, the start check and the
  platform's own Secrets, all unchanged.

## Constraints & Decision drivers

- Copy KV and SQL, invent nothing (the decider, 2026-11-13): the binding is the read grant, the owner and writer-role
  grants are the writers, the roles and the scope are named like theirs, and the principals stay `Function` and
  `Identity`.
- Nothing that works today changes: a Secret with no owner and no writer grant stays read-only to workloads.
- A write cannot store a value a reader's check refuses: it checks the result as the API does (`Secret.Validate`) and
  as a reader does (`secrets.EnvDataProblem`), which the API does not.
- A shim change is a release in its language repo, then a module bump here (ADR-0141).

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. KV's and SQL's model: the owner and writer roles write, through `PUT` and `DELETE /secret/{binding}/{key}`** | one model for all data; nothing new to learn | a decision reads the Secret and the namespace's RolesAssignments | **Chosen** (the decider) |
| B. A typed owner `{kind, name}`, as a Bucket prefix's (#1432's proposal) | one owner shape with Buckets | only a Function writes a Secret; ADR-0275 kept KV and SQL owners a name for that reason | Rejected |
| C. The role as the read grant: a bound Secret is read only with a role | grants in one place | every binding needs an assignment (ADR-0321 Alternative C), a Lake becomes a principal, and a developer, who may bind a Secret, may apply the assignment too (`internal/auth/rbac/rbac.go:30`) | Rejected (the decider) |
| D. `ctx.secret.put(key, value)` into the one bound Secret the caller owns | a shorter call | ambiguous with two owned Secrets; every other write route names its binding | Rejected |
| E. A Conflict answers 409 at once, or retries with no bound | no loop; or no 409 | two writers of different keys fail for nothing; or a write spins | Rejected: 5 attempts, the control plane's own bound (`ownReadAttempts`) |
| F. An apply keeps the keys a Function wrote | a re-apply resets no token | a merge rule no other apply has | Rejected |

## Decision

1. **The action.** `auth.ActionSecretWrite = "secret::write"` joins the `secret` capability. `builtin_secret.cedar`
   gains `secret-write-base` and `secret-single-writer`, KV's and SQL's pair: a write is permitted only to the
   Secret's `writers`. The `Secret` entity carries `owner`, the owner's `Function` entity, and `writers`, the owner
   plus each principal whose writer-role grant covers the Secret (`WriterLister.SecretWriters`, Namespace and `Secret`
   scopes), as `sqlResource` builds them; a missing Secret is a bare entity with its namespace, so no write is
   permitted. Every Secret decision, reads and readiness checks included, now reads the Secret and the namespace's
   RolesAssignments to build them; a missing Secret still passes ADR-0321 Decision 4's readiness check. A namespace
   Policy may forbid either action. The binding still permits `secret::read` (ADR-0321).
2. **The owner.** `Secret.spec.owner`, optional: the Function of the Secret's namespace that may write it, besides the
   writer-role grants, as a KV table's and a SQL database's owner; a DNS label, not checked to exist. A Workflow
   step's Function is `<workflow>-<step>`, and a `ref` step writes as the Function it references. With no owner and no
   grant, no workload writes the Secret.
3. **The roles.** Built-in `Secret Data Reader` (`secret::read`) and `Secret Data Writer` (`secret::read`,
   `secret::write`); scope kind `Secret`, named `<secret>`; `secret::write` is a write action, so it feeds `writers`.
   `Reader`, `Contributor` and `Owner` stay without Secret actions (ADR-0233 Decision 6). Every `secret::read` question
   names a Secret its reader binds (ADR-0321 Decisions 3 to 5), so a role's `secret::read` opens no other Secret; a
   later reader that asks about an unbound Secret must decide what a role then grants.
4. **The write.** `PUT /secret/{binding}/{key}` with the body `{"value": "<text>"}` sets a key, and
   `DELETE /secret/{binding}/{key}` removes one; both answer 204, the delete of a missing key too. `{binding}` names a
   Secret of the caller's `spec.bindings.secret`; another is 403, as an unbound KV alias is. A body over
   `maxSecretBytes` (1 MiB, SQL's limit) is 413. Each attempt gets the Secret (a missing one is 404, as a read of it
   is), asks `secret::write` as the caller (403), sets or removes the key, checks the result as the API and a reader
   do (400), and updates it with the read's resourceVersion. A Conflict starts the attempt over, 5 attempts in all
   (`writeAttempts`), then answers 409. The write never creates or deletes a Secret, and no value is logged. A pool
   member writes as the member that `X-Funcd-Member` names (ADR-0158).
5. **The shims.** TypeScript `context.secret.put(binding, key, value): Promise<void>` and
   `delete(binding, key): Promise<void>`; Python `ctx.secret.put(binding, key, value) -> None` and
   `delete(binding, key) -> None`; each raises a failure as its KV client does. Both release first, tested against
   their own fake local API; funcd's PR adds the routes and pins both.
6. **The pool key.** A Secret a Function owns and binds joins its pool key as `secret/<name>`, as an owned KV table
   or database it binds does (`accessIn` and `poolKeyFor`, `internal/function/poolaccess.go:37`, `:162`): a write
   needs the binding. Secret joins the kinds whose change re-keys a namespace's pooled Functions
   (`pkg/funcd/funcd.go:888`), so an owner set on an existing Secret splits its pool within one pass, and an owner
   never shares a pool worker with a Function that does not own the same Secrets; a writer-role grant already splits
   one.
7. **Everything else stays.** A write is a store update like an API one: a Function's next read sees it (ADR-0249),
   unless a later bound Secret holds the same key and wins, as for any read; a `FUNCD_` key is stored, as the API
   stores one, and no reader loads it (ADR-0092). A Lake that binds the Secret replaces its engine, and a removed key
   acts as a person's removal does. A person's apply
   replaces the data, keys a Function wrote included. Readiness asks `secret::read` only. People's API access, Backup
   and Restore runs, the start check and the platform's own Secrets are unchanged.

## Design model

- **Spec**: [`docs/design/secret/key-write.fizz`](../design/secret/key-write.fizz) · state machine
  [`key-write.md`](../design/secret/key-write.md): Decision 4's read, decision and conditional update, two writers and
  a person's apply racing on one Secret.
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `AckedWritesStay` | always | `scenario: secret-concurrent-writes` | `TestScenarioSecretConcurrentWrites` |
  | `OnlyWritersWrite` | always | `scenario: secret-write-refused` | `TestSecretWriteDecidesAfterItsRead` |
  | `AttemptsAreBounded` | always | Decision 4's 409 | `TestSecretWriteGivesUpAfterItsAttempts` |
  | `WritesEnd` | always eventually | Decision 4's 409 | `TestSecretWriteGivesUpAfterItsAttempts` |

- **Bounds**: 2 writers (the owner's put and a grant's delete), 2 applies, 3 concurrent actions (one per actor), 16
  actions, `ATTEMPTS` 2: the smallest at which every mutant fails (`no-bound` needs three conflicts).
- **Faults**: none; a crash mid-write leaves the store as before or after the update, which is atomic.
- **Environment**: the store's `Update` compares the resourceVersion inside its transaction (`internal/store`); the
  decision reads the stored owner after the write's read; no grant changes during a run.
- **Mutants**: `blind-update` and `decide-first` fail `OnlyWritersWrite`; `stale-retry` fails `AckedWritesStay`;
  `no-bound` fails `AttemptsAreBounded`; `no-reread` fails liveness.
- **Result**: pass, 463 states, 0.5 s, FizzBee v0.5.4; wider (3 applies, 22 actions): pass, 990 states, 0.7 s.
- **Left to tests**: the entities a decision builds and the roles (`TestScenarioSecretWriterRole`), the checks and a
  missing Secret (`TestScenarioSecretWriteChecksTheValue`), the pool key (`TestScenarioSecretOwnerSplitsThePool`), and
  the routes and the shims (the e2e scenarios). A grant removed between a decision and its update is a Risks line.

## Behavior rows

- **The split ADR-0322 postponed**: a new chapter, Secrets (`docs/behavior/secrets.md`), takes `internal/secrets`,
  `internal/services/secret` and the `secrets` config section from Security in `chapters()`
  (`scripts/behavior/main.go`), and security.md's Secret rows unchanged; security.md keeps who may act and egress.
- **secrets.md**, new rows: "`ctx.secret.put` or `delete`": its writers, 403, 404, 400, 413 and 409, seen at the next
  read, `decided` (Decisions 1, 2, 4 and 7); "A RolesAssignment grants a Secret role": the writer joins the writers, and
  no role reads an unbound Secret, `decided` (Decision 3); "A person applies a Secret a Function wrote": the applied
  data replaces it, `accepted` (Decision 7).
- **security.md**: the pool row ("A grant changes what a pooled Function may do") names an owned Secret (Decision 6).

## Temporary workarounds

None.

## Contracts

| Package | Addition |
|---|---|
| `api/types/v1alpha1` | `SecretSpec.Owner ObjectName` (`json:"owner,omitempty"`), a DNS label in `Validate`; `ScopeKindSecret ScopeKind = "Secret"`, in the enum and `Validate` |
| `internal/auth` | `ActionSecretWrite Action = "secret::write"` |
| `internal/auth/cedar` | `actionSecretRead`, `actionSecretWrite`, in `isWriteAction` and `builtinRoles`; `scopeHead` gains `Secret`; `WriterLister.SecretWriters`; `SecretCapabilityWithWriters(w WriterLister) Capability`, `SecretCapability()` its nil form; `secretResource` reads `owner` and `writers` |
| `internal/services/roles` | `(*Lister).SecretWriters` |
| `internal/secrets` | `(*Resolver).CheckWrite(ctx context.Context, id auth.Identity, ns v1.NamespaceName, name string) error` |
| `internal/services/secret` | `Resolver` gains `CheckWrite`; `(*Facade).Put`, `(*Facade).Delete`; `const writeAttempts = 5` |
| `internal/workernode/local` | `Secrets` gains `Put` and `Delete`; the two routes; `const maxSecretBytes = 1 << 20` |
| `internal/function` | `accessIn` lists Secrets, `secret/<name>` for each owner; `poolKeyFor` keeps one the Function binds |
| `pkg/funcd` | the PDP registers `SecretCapabilityWithWriters(writerLister)`; `KindSecret` joins the `MapAccess` watch list |
| funcd-typescript, funcd-python `shim` | `SecretClient.put`, `SecretClient.delete` |

```cedar
@id("secret-write-base")
permit(principal, action == Action::"secret::write", resource);

@id("secret-single-writer")
forbid(principal, action == Action::"secret::write", resource)
  unless { resource has writers && resource.writers.contains(principal) };
```

```go
// WriterLister (internal/auth/cedar) gains:
SecretWriters(ctx context.Context, ns v1.NamespaceName, secret string) []cedartypes.EntityUID

// Secrets (internal/workernode/local) gains; a value is never logged:
Put(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key, value string) error
Delete(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string) error
```

```yaml
apiVersion: funcd.io/v1alpha1
kind: Secret
metadata:
  name: oauth
  namespace: default
  resourceGroup: rg1
spec:
  owner: refresher
  data:
    TOKEN: dDE=
---
apiVersion: funcd.io/v1alpha1
kind: RolesAssignment
metadata:
  name: api-writes-oauth
  namespace: default
  resourceGroup: rg1
spec:
  principal:
    kind: Function
    name: api
  assignments:
    - roleRef:
        kind: BuiltinRole
        name: Secret Data Writer
      scope:
        kind: Secret
        name: oauth
```

| Consumes | Exposes |
|---|---|
| the PDP, the metastore's conditional `Update` | `secret::write`; `Secret.spec.owner`; the two roles; the scope kind `Secret`; the two routes; the shims' `put` and `delete` |

## Implementation plan

1. The Contracts rows, in their order; `just generate`, and `go test ./internal/auth/cedar/ -update` for the grants
   golden.
2. The shims: a PR and a release in pyvvo/funcd-typescript and pyvvo/funcd-python, each with its own tests; then
   `go get` both tags here.
3. Tests, one per scenario, named after it: the decisions and the roles (`internal/auth/cedar`), the facade and its
   retry over a store that answers Conflict (`internal/services/secret`, with the spec's oracle markers), the routes,
   and the e2e scenarios on real Node workers (`pkg/funcd`), the pool split among them, since `pkg/funcd` wires the
   watch. No lane: nothing depends on how the runtime stops or restarts a worker.
4. The Secrets chapter in `chapters()`, its README line, the rows above, and F237's row text, which names the
   chapter; `just behavior-values`, `just check-behavior`.
5. At acceptance: blueprint.md's local API bindings (:116-119) and policy engine paragraph (:552) name the write.

Done when `just ci` and the e2e suite are green, the scenarios pass, `just check-specs` and `just check-behavior`
pass, both shims are released and pinned, and the PR shows `ResolveEnv` and `CheckRead` benchmarks (3 bound Secrets)
before and after.

## Review checklist

- [ ] `secret::write` is permitted only to the owner and writer-role grants; a Policy forbid revokes it.
- [ ] `Secret Data Writer` adds a writer, `Secret Data Reader` none; `Reader`, `Contributor`, `Owner` hold no Secret action.
- [ ] A write names a bound Secret, gets it, decides, and updates on the read's resourceVersion, at most 5 attempts.
- [ ] A write stores nothing the API or a reader's check refuses, never creates or deletes a Secret, never logs a value.
- [ ] An owned, bound Secret splits a pool, an owner set later too; every `secret::read` question names a bound Secret.
- [ ] The shims' `put` and `delete` are released and pinned; the Secrets chapter holds the Secret rows.

## Consequences

- **Positive**: a Function keeps a refreshed credential as a Secret that only its owner and granted writers change;
  Secrets have the reader and writer roles that blob, KV and SQL have.
- **Negative**: each Secret decision, a read's and a readiness check's included, now also reads the Secret and lists
  the namespace's RolesAssignments to build `owner` and `writers`, as a SQL decision does: more work per read and
  within the 50 ms `DependencyCheckBudget`, measured in the PR. Each Secret change, a write included, queues every
  pooled Function of its namespace, and each such pass lists and decrypts the namespace's Secrets, so a Function that
  refreshes a token often pays it each time. A person's apply drops the keys a Function wrote, so a manifest applied
  again resets a refreshed token. Both shims release.
- **Risks accepted**: a writer-role grant removed between a write's decision and its update lets that one write land,
  as for KV and SQL; until the pass an owner change queues, a mate in the owner's pool worker can still name the new
  owner and write (ADR-0158's re-keying window, as in ADR-0321's Risks); neither role gives an Identity anything it
  can use, since no path reads or writes a Secret for one.

## Open questions

None.

## References

- #1432 (F236), tracker #1430; the decisions of 2026-11-13, recorded on #1432.
- `internal/services/secret/secret.go:78`, `internal/workernode/local/secret.go:41`,
  `internal/auth/cedar/roles_compile.go:36`, `internal/auth/cedar/capabilities.go:536` (`sqlResource`),
  `internal/services/kv/resolver.go:85`, `internal/controlplane/handlers.go:267` (`ownReadAttempts`),
  `internal/store/store.go:434`, `internal/function/poolaccess.go:37`.
