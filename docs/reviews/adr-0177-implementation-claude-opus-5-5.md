## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0177 implementation, model: claude-opus-5-5)

Work: branch `feat/adr-0177-policy-namespace-scope`, one commit `f2c10ff8 feat(auth): scope each Policy to its own
namespace`, 6 files, +415/-49 (`internal/auth/cedar/{cedar,policies,schema,policies_test}.go`,
`internal/controlplane/admission/{policy,policy_test}.go`). No doc, PEP, `auth.Request`, built-in policy text,
`go.mod` or `pkg/funcd` change. The ADR is not in the repo yet; its status moves and the F43 row are left to the
wave's docs PR, as instructed.

### Verification run (all through `scripts/agent/d`, in the worktree)

| Check | Result |
|---|---|
| `go build ./...` (darwin) | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet ./...` (darwin) | exit 0 |
| `GOOS=linux go vet ./...` | exit 0 |
| `go test -race -count=1 ./internal/auth/... ./internal/controlplane/admission/...` | exit 0 (`internal/auth`, `auth/cedar`, `auth/rbac`, `controlplane/admission` ok) |
| `golangci-lint run ./internal/auth/... ./internal/controlplane/admission/...` (darwin) | 0 issues, exit 0 |
| same, Linux (host-built linter binary, `GOOS=linux`, as `scripts/agent/gate.sh` does) | 0 issues, exit 0 |
| `gofmt -l` on the 6 changed files | none listed |
| `git status --short` after all runs | clean (0 lines) |
| Scenario tests, `-race -v -run TestScenario_` | 7/7 `--- PASS` (listed below) |

Overlay mutants (`go test -overlay`, nothing written to the worktree):

| Mutant | Line | Result |
|---|---|---|
| m1: `compiledPolicies.set` drops the `sameNamespace` guard (cross-ns request gets the principal's set) | `policies.go` `set` | killed: `TestScenario_cross_namespace_built_ins_only`, `TestScenario_unscoped_policy_stays_in_namespace` FAIL |
| m2: `scopeEntity.named` drops the `is … in` entity | `schema.go` `named` | killed: `TestScenario_policy_foreign_resource_refused` FAIL |
| m3: `denyReason` picks `max` instead of `min` PolicyID | `cedar.go` `denyReason` | killed: `TestScenario_deny_reason_names_forbid` FAIL 3/3 runs |

Not run, per the brief: `go test ./...`, e2e, Lima, full `just ci`; the per-PR gate runs those once.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **A deny by a built-in forbid now names a positional cedar-go id** · attribution: `adr` · evidence: an overlay
  probe test shows a non-owner `kv::write` on `team-b/orders/items` returns
  `cedar: forbidden by policy policy2` (before this change: `cedar default-deny: no permitting policy`). The
  built-ins are compiled with `cedar.NewPolicySetFromBytes("builtin", …)` (`policies.go`, unchanged line), which
  assigns ids `policy0..N` by position (cedar-go `policy_set.go:51`). Decision 4 names `<PolicyID>` for every
  matched forbid and only considered user ids (`team-a/revoke#0`), so the code follows the ADR. The id cannot be
  traced to a rule by an author and shifts when the built-in text is reordered. No consumer parses the reason
  (grep over `.go`/`.yml`/`.yaml`/`.sh`: only `cedar.go` and its test). Fix owner: a later ADR or issue that gives
  the built-ins stable ids (or a "built-in policy" wording); not the builder.
- **Two admit paths of Decision 3 have no test** · attribution: `model` · evidence:
  `internal/controlplane/admission/policy_test.go` covers the unconstrained `principal`/`resource` scopes but not
  `is T` scopes or entity literals inside `when`/`unless`. An overlay probe confirms both behave as Decision 3 says
  (`permit(principal is Function, …, resource is KVTable);` and
  `… when { principal == Function::"team-b/g" };` both return nil), so this is coverage only. Fix: one
  `require.NoError` line per shape in the existing admission tests.

Observation, not counted (pre-existing ADR-0074 code, outside ADR-0177's scope): `validateCedar` type-checks only
`scope.entity.type`, so an unknown type in an `is … in` scope is admitted
(`resource is KVTable in Foo::"team-b/x"` → nil in the probe). ADR-0177 now decodes that entity for the namespace
check; reusing it for the `KnownEntityType` check would close the gap. Candidate issue via `/issue-management`.

### ✅ Verified correct (keep it)
- **Contracts match exactly.** `compiledPolicies{revision, builtin, byNS map[v1.NamespaceName]*cedar.PolicySet}`;
  `(*policyCache).For(ctx, principalNS, resourceNS v1.NamespaceName) (*cedar.PolicySet, error)` replaces `Get`
  (no `Get` left; `go vet ./...` compiles every caller and test); `compile(builtins string, policies []v1.Policy)
  (builtin, byNS, err)` with named results as written; `ValidateCedarInNamespace(ns v1.NamespaceName, text string)
  error` runs the old checks, then Decision 3; `scopeEntity` gains `ID` and the `In` entity. `auth.Decision`,
  `auth.Request`, `PolicySource` and `policySource` are unchanged.
- **Decision 1** in `sameNamespace`: non-empty and equal; any other pair falls to `builtin`.
- **Decision 2**: `compile` groups by `Policy.Namespace`; each namespace set is created by `withBuiltins`, which
  adds the already-parsed built-in `*cedar.Policy` values, so the built-ins are parsed once per revision. A namespace
  with no Policy gets `builtin`. `TestScenario_synthetic_stays_in_namespace` asserts the exact membership: team-a =
  built-ins + 2 synthetics, team-b = built-ins + 1 Policy, the synthetics absent from team-b and from `builtin`.
- **Hot path**: still `src.Policies` (pre-existing poll), then one `atomic.Pointer.Load` and a map lookup; the
  recompile stays single-flighted under `mu` with the re-check. `TestScenarioPolicyCacheAtomicSwap` keeps its
  `require.Same`/`NotSame`/call-count assertions (only `Get` → `For`, which the Contract forces).
- **Decision 3**: refuses `==`, `in` and `is … in` entities of the 8 namespaced types whose id lacks
  `<ns>/`; the prefix includes the slash, so `team-ab/g` is refused for `team-a` (probe). The error is
  `fault.Invalid` naming the statement index and the entity (`Function::"team-b/g"`), wrapped by the admission as
  `fault.Invalid`; the admission tests assert HTTP 400 via `fault.ToProblem`.
- **Decision 4**: `denyReason` orders forbid → evaluation error → no-permit, picks the lowest PolicyID among
  several forbids (m3 proves the test pins it), and appends the cross-namespace suffix only on the no-permit path.
- **Decision 5**: no migration; `TestScenario_stored_foreign_policy_inert` feeds the foreign Policy straight to the
  driver and also checks that re-applying it is refused.
- **Implementation plan step 3 (PEP audit)**: every non-empty `auth.EntityRef{…}` literal in production code sets
  `Namespace` (grep over `internal/workernode/local`, `internal/catalog/gateway`, `internal/blob/s3gateway`,
  `internal/services/{kv,blob,catalog}`, `pkg/funcd`: no literal without it); the egress PEP builds the resource as
  `nd.Ref(ref.Namespace)` (`internal/network/egress/gateway.go:115`), the caller's namespace.
- **Scenarios**, one named test each, all passing under `-race`:
  `TestScenario_policy_foreign_principal_refused`, `TestScenario_policy_foreign_resource_refused` (admission);
  `TestScenario_unscoped_policy_stays_in_namespace`, `TestScenario_stored_foreign_policy_inert`,
  `TestScenario_cross_namespace_built_ins_only` (also the s3 case), `TestScenario_synthetic_stays_in_namespace`,
  `TestScenario_deny_reason_names_forbid` (cedar). No existing test weakened or deleted; the admission test file
  only gains lines.
- **Conventions**: `api/fault` for every error, ctx-first, no `any` in signatures, no `panic`, no new dependency,
  comments cite the ADR decision and explain why (set-iteration order for the lowest id), no narration.
- **Scope**: nothing beyond the ADR; ADR-0158's Decision 6 is left to its owner, as the ADR says.

### Definition of Done
14 / 14 hold: the 6 Review-checklist items; the ADR's 3 DoD items (scenario tests pass; existing cedar, egress,
roles and admission suites green, the only edit being the forced `Get` → `For` rename; the `just ci` sub-checks run
here are green, the repo-wide run belongs to the PR gate); and 5 applicable generic items (contracts honoured, real
behaviour, ADR-0002 conventions, no new deps, no scope creep). Tracking (ADR `Reviewing`, F43 row) is out of this
review's hands: the ADR is not in the repo, and the wave's docs PR moves it.

### Model scorecard
Row below: claude-opus-5-5 on ADR-0177 (implementation) → pass, 0/0/2, 1 model-attributed, DoD 14/14. Not
written to `docs/reviews/model-ledger.json` here; the wave's ledger PR records it.

### Recommendation
Pass. The builder may add the two admit-path assertions in passing; the built-in forbid id belongs to a later ADR or
an issue, and the `is … in` type-check gap to an issue. No status stamp here: the docs PR of the wave moves ADR-0177
`Reviewing → Implemented` and the F43 row.

```json
{
  "date": "2026-10-05",
  "adr": "0177",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 1,
  "dod_passed": 14,
  "dod_total": 14,
  "report": "docs/reviews/adr-0177-implementation-claude-opus-5-5.md",
  "notes": "all Contracts match (compiledPolicies, For replaces Get, compile, ValidateCedarInNamespace), 7/7 scenario tests pass under -race, build/vet/lint green on darwin and Linux, PEP audit clean (every EntityRef literal sets Namespace), 3/3 overlay mutants killed; is-T and when-literal admit paths untested (model); a built-in forbid deny names the positional cedar-go id policyN (adr); pre-existing is-in unknown-type admit gap noted, not counted"
}
```
