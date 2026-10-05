# ADR-0175: A catalog engine is its own principal

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (judged twice by three lenses; held from publication)
- **Deciders**: green-0-rabbit
- **Tags**: authorization, cedar, s3, egress, catalog, identity
- **Realizes**: [FEAT-0003/F58](../feat/0003-feat-data-platform.md) (add-on provider identity in the F47/Cedar model)
- **Builds on**: [ADR-0152](0152-runtime-worker-owner-kind.md) (Proposed; `Instance.OwnerKind`), which lands first.
- **Supersedes (in part)**:
  - [ADR-0088](0088-add-on-provider-s3-identity.md): the scenario `function-takes-precedence`, the Function-first
    precedence rule in Decision and Scope, the Contracts comment on it, the Out item that keeps the provider principal
    Function-shaped, and the clauses that keep the principal/owner UID name-based (`functionUID(ns, name)`) and the
    keypair untouched. Lookup is by kind instead.
  - [ADR-0085](0085-s3-in-platform-identity-funcd-keypair.md): the fixed in-platform key format (`accessKey = "FUNCD" + base32(ns + "\x00" + fn)`,
    `secretKey = HMAC(master, "s3:" + ns + "/" + fn)`, repeated in its plan) and decoding a key to `(ns, fn)`. Its
    "deterministic across restart" property stands.
  - [ADR-0117](0117-egress-policy-enforcement.md): §4(b)'s literal whole-namespace permit
    (`principal.namespace == "<ns>"` gains `principal is Function`), and the `(namespace, function)` Ref of §5 and of
    the `WorkerIndex` comment, which now carries the owner kind. `appliesTo` is unchanged. Each gains a back-link.
- **Relates to**: ADR-0137, ADR-0153 (catalog tokens) · ADR-0158 (pool `DeriveKeypair`) · ADR-0177 (shares
  `egress_compile.go`; the second to land rebases) · ADR-0080 (refined by Decision 6's CatalogService writer)

## Context & Need

Three authorization inputs map a CatalogService engine to the principal of a same-named Function (origin/main 915f342):

- Egress: `reconcileEgressWorkers` (`pkg/funcd/funcd.go:1624`) lists the workers of each namespace that has a
  Function and indexes every one as `auth.EntityRef{Type: v1.KindFunction, …}` (`:1652`). An engine beside a
  Function `lake` gets that Function's egress grants; an engine in a namespace with no Function is not indexed.
- S3 key: `DeriveKeypair(master, ns, name)` (`internal/blob/s3gateway/iam.go:49`) is used for Functions
  (`internal/function/function.go:1629`) and engines (`internal/services/catalog/reconcile.go:255`), so Function
  `lake` and engine `lake` hold the same access key and secret. `principalFor` (`internal/blob/s3gateway/auth.go:40-47`)
  always returns `Type: v1.KindFunction`.
- Bindings: the Cedar entity provider tries `FunctionPrincipalSource` then `CatalogServicePrincipalSource` whatever
  the ref's type (`internal/auth/cedar/entities.go:73`); `principalUID` models no CatalogService principal (`:149-159`).
- Prefix writers: `s3Resource` (`internal/auth/cedar/capabilities.go:303-322`) builds a prefix's `writers` from the bare
  `owner` name as a Function UID.

Observable effect: with Function and CatalogService `lake` in one namespace, the engine reads with the Function's
`spec.blob` bindings and egresses with the Function's grants, and the Function can sign as the engine.

Purpose: an engine is authorized as its CatalogService and a Function as itself, decided by the worker's owner kind.

## Scenarios

- `scenario: engine-s3-bindings-are-its-own` — Given Function `lake` with no `spec.blob` and CatalogService `lake`
  bound to prefix `raw/`, When the engine reads `raw/`, Then 200; When Function `lake` reads `raw/`, Then 403.
- `scenario: function-s3-bindings-are-its-own` — Given Function `lake` bound to `a/` and CatalogService `lake` bound
  only to `raw/`, When the engine reads `a/`, Then 403; When the Function reads `a/`, Then 200.
- `scenario: engine-writes-owned-prefix` — Given a Bucket prefix `raw/` whose `owner` is `lake` and CatalogService
  `lake` whose `spec.blob` binds `raw/` and no Function `lake`, When the engine writes and checkpoints there, Then 200;
  Given a CatalogService `lake` that does not bind `raw/`, or a Function `lake` beside it, When the engine writes
  `raw/`, Then 403.
- `scenario: function-key-cannot-sign-as-engine` — Given Function `lake`'s injected keypair, When it signs a request
  with the access key derived for CatalogService `lake`, Then 403 and nothing is read.
- `scenario: pre-adr-key-refused` — Given an access key and secret in the pre-ADR format and no ExternalKeys entry with
  that id, When a request is signed with them, Then 403 `InvalidAccessKeyId`.
- `scenario: keys-reissued-on-upgrade` — Given a fresh runtime with no workers, When the function and catalog
  reconcilers re-create Function `lake` and engine `lake`, Then each worker's env key equals
  `DeriveKeypair(master, kind, ns, "lake")` for its own kind and authenticates at the gateway.
- `scenario: engine-egress-is-catalogservice` — Given egress enforcement on, Function and CatalogService `lake` in one
  namespace and an `EgressPolicy` (whole-namespace, and one with `appliesTo: [lake]`) allowing `example.com:443`,
  When each connects to `example.com:443`, Then the Function is allowed and the engine is denied with
  `AuditRecord.Function == "lake"`; and (own assertion) in a namespace with only CatalogService `lake`, the engine
  is indexed, denied and audited, not an unknown source.

## Scope

In: the egress worker index, the in-platform S3 key and its decoding, kind-scoped principal sources, the CatalogService
principal UID, the S3 prefix `writers` resolution. Out: catalog tokens (ADR-0137/0153; only Functions and Identities
hold them); `removeRoute` and log labels (ADR-0152 issues); a typed prefix owner (Open questions); admission of a
shared name; operator-written Cedar `Policy` objects (see Decision 2).

## Constraints & Decision drivers

- Settled by the decider: every input that maps a worker to a principal uses the worker's owner kind (ADR-0152
  `Instance.OwnerKind`); ADR-0088's Function-first precedence becomes lookup by kind; existing keys are re-issued.
- Default-deny holds: no input may give an engine a grant written for a Function.
- Least new surface. An engine needs no external egress today: its DuckDB extensions are preinstalled with
  autoinstall off (`images/runtime/duckdb/shim.py:106-114`), and the S3 gateway and catalog proxy are `InternalAllow`
  endpoints, never redirected (`internal/network/network.go:36`).
- Names grepped: `estIs` (EST `is` node helper) is new; no new field, label, reason or constant. `OwnerKind` is
  ADR-0152's; `catalogServiceUID` (ADR-0137) and `KindCatalogService` exist.

## Alternatives considered

- **Owner kind everywhere: the egress index maps `Instance.OwnerKind` to the entity type and the in-platform S3
  access key carries the owner kind** — chosen: one rule closes all three inputs.
- A separate key prefix for engines only — rejected: fixes S3 alone, and leaves the egress index and the
  principal sources keyed by name.
- Forbid a Function and a CatalogService from sharing a name in one namespace (admission), index by name — rejected:
  racy across kinds (ADR-0152), and a name is not a principal.
- Keep Function-first precedence and document it — rejected: the engine keeps the Function's grants and keypair.
- Prefix `writers` with both kinds for an owner name — rejected: anyone who may create a CatalogService (or holds its
  catalog token, ADR-0137) could write any prefix owned by a Function of that name.

## Decision

1. **Egress index by owner kind.** `reconcileEgressWorkers` covers every namespace that has a Function or a
   CatalogService and indexes each running worker as `EntityRef{Type: in.OwnerKind, Namespace, Name}`; a worker whose
   `OwnerKind` is neither kind is not indexed (unknown source, denied). A failed CatalogService list returns `prev`,
   as the Function list does (`pkg/funcd/funcd.go:1625-1628`), so a store error drops no engine from the index.
2. **No EgressPolicy grant reaches an engine.** The whole-namespace permit becomes `principal is Function &&
   principal.namespace == "<ns>"`; `appliesTo` stays a Function list (it may not name a CatalogService). An engine's
   external egress is denied and audited. An operator-written Cedar `Policy` on `egress::connect` is outside this
   guarantee: one written on `principal.namespace` alone grants every principal type in the namespace.
3. **The in-platform S3 key carries the owner kind.** The kind is in the access body (a one-byte code plus a NUL:
   about four more base32 characters than today) and in full in the MAC input, so a Function cannot compute the
   engine's secret. `decodeAccess`, `principalFor` and `GetUserAccount` all use the decoded kind. Format: Contracts.
4. **Re-issue on upgrade is the restart.** Keys are derived, never stored, and reach a worker only through its env
   at create. After the upgrade restart the runtime lists no worker (ADR-0152 Decision 4), so every Function and
   engine is re-created with a new key. A pre-ADR key fails `decodeAccess`; the request then falls through to the
   ExternalKeys lookup, which misses (no entry carries an in-platform id): 403 `InvalidAccessKeyId`. Containers left
   from before the upgrade (until ADR-0167's boot sweep) fail closed the same way; no sweep is needed here.
   `funcdctl dev` derives at each start. No grace window.
5. **Principal lookup by kind.** `principalUID` maps `KindCatalogService` to `catalogServiceUID`;
   `FunctionPrincipalSource` resolves only `Type == KindFunction`, `CatalogServicePrincipalSource` only
   `Type == KindCatalogService`. A name held by both kinds resolves each to its own object.
6. **Prefix owner resolved by kind at evaluation.** A Bucket prefix `owner` is a name. `s3Resource` adds
   `Function::"<ns>/<owner>"` to `writers` by name, as ADR-0080 does, and `CatalogService::"<ns>/<owner>"` only if
   CatalogService `<owner>` exists, its `spec.blob` binds that bucket and prefix, and no Function `<owner>` exists.
   `s3Resource` is the only gate: the catalog reconciler checks only that each bound prefix exists, not its owner
   (`resolveBucketRefs`, `internal/services/catalog/reconcile.go:282-322`), and the name match holds because the
   CatalogService is looked up by the owner name. `builtin_s3.cedar` permits `s3::write` unconditionally and its
   `writers` forbid is the only write gate, so a write needs no `blobBindings` membership and the `spec.blob` check
   here carries the weight; admission already forces `spec.catalog` into `spec.blob`
   (`api/types/v1alpha1/catalogservice.go:146-156`), so checkpoint writes pass it. Any of the three lookups that errors or misses adds no
   `CatalogService` writer; a Function lookup error other than NotFound counts as "a Function may exist". When both
   kinds hold the name the owner is the Function, and the engine writes only through an explicit writer-role grant
   (ADR-0136). The `owner` attribute stays `Function::"<ns>/<owner>"` (ADR-0080) and may name a Function that does
   not exist; it is not resolved by kind.

## Temporary workarounds

Until this ADR is implemented (after ADR-0152): never give a Function the name of a CatalogService in the same
namespace. Exit: the seven scenario tests pass on main.

## Contracts

```go
// internal/blob/s3gateway/iam.go
// DeriveKeypair returns the stable per-(kind, namespace, name) keypair (ADR-0085, ADR-0175):
//   AccessKey = "FUNCD" + base32-noPad-upper(code + "\x00" + ns + "\x00" + name), code "F" Function, "C" CatalogService
//   SecretKey = base64(HMAC-SHA256(master, "s3:" + kind + ":" + ns + "/" + name)), kind the full v1.Kind
func DeriveKeypair(master []byte, kind v1.Kind, ns, name string) Keypair

// decodeAccess maps an in-platform access key to its (kind, ns, name). ok=false unless the body is three
// non-empty parts with code F or C (a pre-ADR-0175 two-part key is refused).
func decodeAccess(access string) (kind v1.Kind, ns, name string, ok bool)
```

```go
// internal/function/function.go (S3GatewayInjection) and internal/services/catalog/catalog.go (Deps, Reconciler)
Derive func(kind v1.Kind, ns, name string) (access, secret string)
```

| Input | Before | After |
|---|---|---|
| Egress index (`pkg/funcd/funcd.go:1652`) | `Type: KindFunction`, Function namespaces only | `Type: in.OwnerKind`, Function or CatalogService namespaces |
| Whole-namespace egress permit (`egress_compile.go` `namespaceScope`) | `principal.namespace == "<ns>"` | `principal is Function && principal.namespace == "<ns>"` |
| `principalFor` (`auth.go:40-47`) | `Type: KindFunction` | the decoded kind |
| Principal sources (`capabilities.go:378-415`) | Function-first, type ignored | by `Type` |
| Prefix `writers` (`s3Resource`) | `Function::"<ns>/<owner>"` by name | plus `CatalogService` if it exists, binds the prefix and no Function has the name |

| Direction | Items |
|---|---|
| Consumes | `runtime.Instance.OwnerKind` (ADR-0152); `v1.KindFunction`, `v1.KindCatalogService` |
| Exposes | the kind-bearing in-platform key format; `CatalogService::"<ns>/<name>"` as an s3/egress principal |

## Implementation plan

1. Lands after ADR-0152; rebase onto ADR-0177 if it lands first (`egress_compile.go`). Edit `internal/blob/s3gateway/{iam,auth}.go` (`decodeAccess`, `principalFor`,
   `GetUserAccount` at `iam.go:132`), `internal/function/function.go` (`KindFunction` at `:1629`),
   `internal/services/catalog/{catalog,reconcile}.go` (`KindCatalogService` at `:255`; also correct the
   `engineEnv` comment at `:253-254`, which claims an `owner == cs.Name` check nothing enforces), `pkg/funcd/funcd.go` (`Derive`
   at `:601`, `reconcileEgressWorkers`), `cmd/funcdctl/dev.go` (`:482`, `:853`, `KindFunction`),
   `internal/auth/cedar/{entities,capabilities,egress_compile}.go` (`estIs`, `s3Resource`), and the pool manifest's
   `Derive` call if ADR-0158 has landed (pool members pass their owner's kind).
2. Tests, one per scenario, failing on main:
   - `internal/blob/s3gateway`: `TestScenarioFunctionKeyCannotSignAsEngine`, `TestScenarioPreADRKeyRefused`
     (decode refuses the two-part body; SigV4 with it and empty ExternalKeys is 403), and a table test that keys of
     the two kinds differ.
   - `internal/auth/cedar` over the assembled registry: `TestScenarioEngineS3BindingsAreItsOwn`,
     `TestScenarioFunctionS3BindingsAreItsOwn`, `TestScenarioEngineWritesOwnedPrefix` (subtests: a store error on
     the Function lookup gives 403; deleting Function `lake` gives the engine 200).
   - `pkg/funcd`: `TestScenarioKeysReissuedOnUpgrade` (fresh fake runtime, both reconcilers, env key checked against
     `DeriveKeypair` and signed through the gateway); `TestScenarioEngineEgressIsCatalogService` (fake runtime with
     both kinds, `reconcileEgressWorkers` plus the compiled policies through the cedar authorizer, as
     `decideConnect` calls it; the only-CatalogService namespace as its own subtest).
   - Lima lane `duckdb` unchanged: its existing read and checkpoint cases exercise the new engine key end to end.
3. Documents: the F58 row in `docs/feat/0003-feat-data-platform.md` drops "(Function-first)", adds
   `(+ ADR-0175 — engine is its own principal)` and splits its status as F12 does (`provider identity: implemented ·
   own principal: adr`), then follows this ADR. ADR-0152 (Proposed) names ADR-0175 where it defers the egress index
   and S3 principal mapping (its `reconcileEgressWorkers` row, Consequences and Open questions). ADR-0158 cites
   ADR-0175 for the new `DeriveKeypair` signature. At acceptance ADR-0085, ADR-0088 and ADR-0117 gain
   `Superseded in part by: ADR-0175`. Board: `driver.py list` for an F58 card; reuse it or create one titled
   "… — ADR-0175 / FEAT-0003 F58" in Backlog, and move it with the lifecycle. No blueprint change.
4. Done: the seven scenario tests pass, the `duckdb` and `egress` lanes pass, `just ci` green.

## Review checklist

- [ ] No `Type: v1.KindFunction` literal remains in `reconcileEgressWorkers` or `principalFor`.
- [ ] The key's MAC input carries the full kind; a two-part key decodes to ok=false.
- [ ] Each principal source returns ok=false for a ref of another type; `principalUID` accepts `KindCatalogService`.
- [ ] The whole-namespace egress permit carries `principal is Function`; `appliesTo` validation is unchanged.
- [ ] `s3Resource` adds a `CatalogService` writer only when it exists, binds the prefix and no Function has the name;
      it checks the `spec.blob` binding itself at evaluation (nothing upstream checks the owner, and the `writers`
      forbid is the only write gate since `s3::write` is permitted unconditionally), and any lookup error adds no
      writer; the `engine-writes-owned-prefix` negative cases cover an unbound prefix.
- [ ] A test pins that a policy comparing `principal == resource.owner` does not match an engine.
- [ ] Every `Derive` caller passes the worker's kind; each scenario has one named, passing test that fails on main.

## Consequences

- Positive: a shared name no longer merges a Function's and a CatalogService's grants or keys; an engine in a
  namespace without Functions is indexed and audited.
- Negative: every in-platform key changes at upgrade; an external copy of one (a `funcdctl dev` shell export) must
  be taken again. A user policy comparing `principal == resource.owner` does not match an engine.
- Risks accepted: a prefix `owner` stays a bare name, so a Function created with an engine-owned prefix's owner name
  gains write there as a Function (and the engine loses it), as under ADR-0080 today; and while no Function has that
  name, whoever creates a CatalogService of that name and binds the prefix writes it, as a Function creator can
  today. Neither path uses another principal's key. Entities are built per request (`s3Resource`; only the
  PolicySet is cached), so creating a Function `<owner>` removes the engine's write at once (fail closed), and
  deleting it, including the gap while a Function is re-created or a Workflow re-materializes one
  (`internal/workflow/reconcile_workflow.go:253`), gives the same-named bound CatalogService write on that prefix. The workaround avoids both until a typed owner lands.

## Open questions

| Question | Answered by |
|---|---|
| A kind on the Bucket prefix `owner` reference | a follow-up ADR on the Bucket API |
| An `EgressPolicy` grant form that names a CatalogService | an ADR when an engine needs external egress |
| The egress `AuditRecord` names the caller in its `Function` field; add the kind | an issue |
| The SigV4 128-character access-key limit some clients enforce (ADR-0085's format already passes it for long names) | an issue |

## References

- Issue [#18](https://github.com/pyvvo/funcd/issues/18) (same-name Function and CatalogService)
- ADR-0080, ADR-0085, ADR-0088, ADR-0117, ADR-0137, ADR-0152, ADR-0158, ADR-0167
