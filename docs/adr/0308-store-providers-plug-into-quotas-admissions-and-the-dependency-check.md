# ADR-0308: Store providers plug into quotas, admissions and the dependency check through shared hooks

- **Status**: Accepted (2026-11-03, under the decider's delegation: "pick whatever ADR you want and ship them";
  judged twice under the adr-judge skill in an independent session: round 1's Major, the admissions' timing, and
  five Minors folded; round 2 accept)
- **Date**: 2026-11-03
- **Deciders**: green-0-rabbit
- **Tags**: provider, quota, admission, health, refactor
- **Realizes**: [FEAT-0001/F222](../feat/0001-feat-v1.1.md) (a store provider's quota, rules and checks in one place)
- **Relates to**: [ADR-0305](0305-providers-start-and-stop-through-one-lifecycle.md) (its open question: shared hooks) ·
  [ADR-0307](0307-a-providers-boot-reclaim-is-a-lifecycle-hook.md) (the first shared hook) ·
  [ADR-0292](0292-stores-get-a-quota-block-and-a-quota-framework.md) (size quotas) ·
  [ADR-0072](0072-kv-as-a-declarative-resource.md), [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md),
  [ADR-0232](0232-sql-database-resource-and-rqlite-engine.md) (count quotas, deletion protection) ·
  [ADR-0215](0215-built-in-health.md) Decision 3 (the dependency check) ·
  [ADR-0233](0233-sql-access-from-functions.md) Decision 8 (sql in the check, between blob and links) · #1370

## Context & Need

Every store provider takes part in three cross-cutting families, wired by hand per kind in `buildControlPlane`
(`pkg/funcd/funcd.go`): its size quota (ADR-0292; three `p.quotas.Register` calls), its store rules at admission (the
count quota and the deletion protection of ADR-0072, ADR-0080 and ADR-0232; three default blocks and three admission
pairs), and its kinds in the dependency check (ADR-0215 Decision 3; one typed loop per kind in
`internal/health/checker.go`). That hand order hid #1370: the KVStore quota was registered before the default KV driver
existed, so a platform on the in-memory driver never enforced it. The next store providers (vector stores, FEAT-0013)
would edit all of these places again.

Purpose: each store provider names its quota, its store rules and its dependency checks in one place, and the Platform
puts them in place in list order. An experiment (2026-11-03, unmerged) inventoried every cross-cutting family on top of
ADR-0305's frame, turned these three into hooks, and kept every test passing; it found #1370 on the way.

## Scenarios

- **scenario: quotas-cap-every-store** — *Given* a platform built without `WithKVStore`, *When* a 9-byte write goes to a
  Bucket, a KVStore or a SQLDatabase capped at 8 bytes, *Then* the Bucket and the KVStore refuse it with
  `InsufficientStorage`, and the SQLDatabase, whose engine is not Ready, with `Unavailable`. Test:
  `TestIssue1370_EveryStoreKindIsCappedOnTheDefaultDrivers` (#1370's regression test).
- **scenario: store-rules-on-the-write-path** — *Given* a namespace at its KVStore count cap, *When* one more is
  applied, *Then* it is refused (400); *Given* a store that holds data, *When* it is deleted, *Then* it is refused
  (409); the same rules hold for Buckets and SQLDatabases. Tests: `TestKVStoreRulesRunOnTheWritePath`,
  `TestIssue58_BucketWithObjectsIsDeletionProtected`, `TestScenarioApplyDryRunRefused` (the Bucket count cap, e2e),
  `TestScenarioDatabaseQuota`, `TestScenarioDeleteRefusedWhileHoldingData`.
- **scenario: dependency-check-in-list-order** — *Given* a Function with a kv, a blob, a sql and a link binding, none
  resolvable, *When* its worker asks `GET /health/dependencies`, *Then* the 503 names the kv binding; once its store
  exists, the blob one; then the sql one; and a link to a missing Function fails. Test:
  `TestDependencyCheck_ReadsTheProvidersInListOrder`.
- **scenario: boot-log-names-shared-hooks** — *Given* an in-memory platform, *When* `Run` boots, *Then* the hook list
  it logs names each provider's shared hooks after its lifecycle hooks, e.g. `kv: start, reclaim, stop, quotas,
  admissions, dependencies` and `invoke: dependencies`. Test: `TestPhases_ListTheProvidersWithTheirHooks` (the list
  `Run` logs).

## Scope

**In**: three shared hooks (quotas, store admissions, dependency checks), where each runs, how the boot log shows
them, and the list form of the dependency check.

**Out**, each staying hand-wired for the reason the experiment's inventory gives:

| Family | Why it is not a hook now |
|---|---|
| Storage probes and their probe-flip requeue | two users (kv, blob) |
| Restore → DataMissing mapping (ADR-0234, ADR-0262) | two one-line users; the duplication is two near-identical `MapRestore` bodies, a helper's job |
| Workload backup members | the backup service keeps per-kind code behind typed fields; a member interface is its own decision |
| Platform backup inputs | the Platform's own parts, no provider |
| The hold | a dependency each of 13 components receives; `TestEveryRunnerConsultsHold` guards it |
| Hold evidence (orphans) | the control plane's interface and the hold status are typed per kind |
| Event sources | two kinds with different shapes |
| Dev mode | options the caller chooses; nothing runs per provider |

Also out: providers building their own parts (an experiment is studying it).

## Constraints & Decision drivers

- Behavior-preserving: the same quotas, the same store rules in the same place in the admission chain, and the
  dependency check in today's order (kv, blob, sql, links). #1370's fix is assumed.
- `internal/lifecycle` stays a leaf: typed hooks that name quota, admission and health types live in `pkg/funcd`.
- The idiom of ADR-0302, ADR-0305 and ADR-0307: optional one-method interfaces found by a type assertion; no `init()`
  registry (ADR-0002); no new dependency.

## Alternatives considered

| Option | Why it lost |
|---|---|
| Keep the per-kind wiring | Each new store provider edits three places in `buildControlPlane` and `internal/health`; that hand order hid #1370. |
| A provider registers itself (`p.quotas.Register` in a hook) | The provider would hold the Platform's registries; returning its part keeps it passive, as the catalog's `describer` does. |
| One `Store` interface with every store method | invoke takes part only in the dependency check and lake only in backups; empty methods, as ADR-0305 rejected for one lifecycle interface. |
| Typed hooks in `internal/lifecycle` | It would import the quota, admission and health packages and stop being a leaf. |
| Unify the backup members now | A hook would only move wiring while the backup service keeps per-kind code; that is a separate decision. |

## Decision

1. **A shared hook** is an optional one-method interface in `pkg/funcd`, beside `describer`. It returns the provider's
   part; the Platform puts every provider's part in place, in list order. A provider never holds a Platform registry.
2. **Three hooks.**
   - `Quotas()`: kv, blob and sql each return their size-quota source by kind (ADR-0292).
   - `Admissions(r)`: kv, blob and sql each return their store rules, the count quota and the deletion protection;
     they stay after the link admissions in the chain, as today.
   - `Dependencies()`: kv, blob, sql and invoke each return their dependency-check binding; the check runs them in
     list order, which is today's order.
3. **When.** The quotas and the dependency checks are put in place once, while `New` builds the platform, after the
   last store facade exists. The admissions are built when the control-plane chain is built and again for every
   App write, over the App's view (ADR-0199 Decision 3), so `Admissions` only builds values from parts `New` set:
   no I/O, no state, safe to call concurrently. No hook runs at boot or at stop.
4. **The boot log** names each provider's shared hooks after its lifecycle hooks; the golden test pins every set, and
   a new shared hook joins the list the log walks.
5. **The dependency check takes a list** of bindings, one per provider kind, instead of one typed loop per kind.
6. **The families in Scope's table stay hand-wired**, each for the reason given.

## Design model

None: the quotas and the dependency checks are put in place once, inside `New`, and `Admissions` builds stateless
values on each App write; they add no state, timer or actor (ADR-0272 Decision 1).

## Temporary workarounds

- The hooks read the store facades and the policy decision point from four Platform fields (`kvFacade`, `blobFacade`,
  `sqlFacade`, `pdp`). Exit: the ADR that moves construction into the providers, after which each hook reads its own
  provider's parts.

## Contracts

```go
// pkg/funcd, unexported, each found by a type assertion over the provider list.
type quotaHook interface {
	lifecycle.Provider
	Quotas() map[v1.Kind]quota.Source // the size-quota source of each kind the provider stores (ADR-0292)
}

type admissionHook interface {
	lifecycle.Provider
	Admissions(r admission.StoreReader) []admission.Admission // count quota, deletion protection
}

type dependencyHook interface {
	lifecycle.Provider
	Dependencies() []health.Binding // checked in list order (ADR-0215 Decision 3)
}

// internal/lifecycle: walking and naming a hook another package defines.
type Hook struct{ /* a name, and whether a provider implements the hook */ }
func Each[H Provider](ph *Phases) []H           // the providers that implement H, in list order
func Named[H Provider](name string) Hook        // a hook's name for the boot log
func (ph *Phases) Hooks(shared ...Hook) []string // lifecycle hooks, then the shared ones named

// internal/health: one binding per checked kind; a kind with no constructor fills the fields itself.
type Binding struct {
	Kind    string
	Aliases func(fn *v1.Function) []string
	Check   func(ctx context.Context, caller local.Ref, alias string) *local.DependencyReport
}
func KV(rc ReadChecker, p *Prober) Binding
func Blob(rc ReadChecker, p *Prober) Binding
func SQL(rc ReadChecker) Binding
func Links(r local.Resolver, authz auth.Authorizer, st local.FunctionStore) Binding
```

| Provider | `Quotas` | `Admissions` | `Dependencies` |
|---|---|---|---|
| kv | KVStore | KVStore count cap, deletion protection | kv |
| blob | Bucket | Bucket count cap, deletion protection | blob |
| sql | SQLDatabase | SQLDatabase count cap, deletion protection | sql |
| invoke | — | — | links |

Changes to ADR-0305's shapes, so the review reads them as decided: `describer` embeds `lifecycle.Provider`,
`Phases.Hooks` takes the shared hooks as variadic arguments, and invoke becomes a provider type of its own (it has
a `Dependencies` hook) instead of a catalog-only entry.

| Dependencies & I/O | |
|---|---|
| Consumes | the store facades, the quota and admission packages, the dependency check's readers |
| Exposes | the three hooks; the boot log lines that name them |
| Config keys, dependencies | none new |

## Implementation plan

On top of the ADR-0305 and ADR-0307 implementations and #1370's fix, as the experiment built it (commits `6f2733aa`
and `26bfb6bd`, unmerged):
1. `internal/lifecycle`: `Each`, `Named` and `Hooks(shared ...Hook)`, with `TestPhases_EachWalksASharedHookInOrder`.
2. `internal/health/checker.go`: `Binding` and its four constructors; the check walks the list, and the typed
   `CheckerDeps` fields go: the two test literals that set them (`internal/health/checker_test.go`) move to bindings.
3. `pkg/funcd`: the three hook interfaces and `sharedHooks()`; kv, blob, sql and invoke implement theirs; `New`
   calls `registerQuotas`, `providerAdmissions` and the dependency list at the points of Decision 3, in place of the
   per-kind wiring.
4. Tests: `TestKVStoreRulesRunOnTheWritePath` and `TestDependencyCheck_ReadsTheProvidersInListOrder` (new), the
   golden hook list extended, every other test unchanged apart from step 2's two `internal/health` literals; a
   `// scenario:` line above each Scenario's tests.
5. Run `scripts/agent/d go test -race` on the touched packages, lint, `scripts/agent/gate.sh`, `just lima-example-all`
   and `just lima-example app-todo`.

Done when the gate, the 13 lanes and the app-todo lane pass.

## Review checklist

- [ ] The three hooks are optional, live in `pkg/funcd`, and each implementer asserts it at compile time.
- [ ] No per-kind `p.quotas.Register`, store-rule admission or typed dependency loop is left in `buildControlPlane`
  or `internal/health`.
- [ ] `Quotas` and `Dependencies` run once, in `New`, after the parts they read exist; `Admissions` runs when the
  chain is built and on each App write, with no I/O and no state; no hook runs at boot or stop.
- [ ] The admission chain keeps the store rules after the link admissions; the dependency check keeps kv, blob, sql,
  links.
- [ ] The golden hook list names every shared hook of every provider.
- [ ] `internal/lifecycle` imports no quota, admission or health package.

## Consequences

- **Positive** (measured in the experiment): a new store provider names its quota, rules and checks in one type;
  `internal/health`'s four per-kind loops become one; `buildControlPlane` 394 → 384 statements and cognitive
  complexity 163 → 158; 14 of 14 mutants caught.
- **Negative**: 143 more production code lines (hooks, constructors, assertions); four Platform fields until
  construction moves.
- **Risks**: a hook run before its parts exist reads a nil facade and panics in `New` (Decision 3 and the golden test
  pin where each runs); a hook interface left out of `sharedHooks()` still runs but leaves the boot log.

## Open questions

- Storage probes as part of `Dependencies` → when a third store probes.
- Hold evidence: `Reclaimer` gaining `Orphans` → when the hold status takes a list.
- Backup members through one hook → a member interface in `internal/backup/workload`, its own ADR.
- A shared helper for the two `MapRestore` bodies → a cleanup task, no ADR.
- Providers building their own parts → the construction ADR, after its experiment.

## References

- [ADR-0305](0305-providers-start-and-stop-through-one-lifecycle.md) · ADR-0307 · ADR-0292 · ADR-0215 · ADR-0302 ·
  ADR-0002 · #1370 (the quota defect the hand order hid).
