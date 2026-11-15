## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0329 rollout, the tenancy and security item, round 2, model: claude-opus-5-5)

Round 2 reviews commit `b6b4c76e`, which follows [round 1](adr-0329-rollout-tenancy-security-claude-opus-5-5.md) on
branch `feat/rollout-0329-tenancy-security`. It changes:

- `internal/controlplane/routes_rest.go`: the fix of Policy's second Cedar case, and the when of the ResourceGroup's
  members case;
- `internal/controlplane/describe.go`: the when and the fix of the path case of a cluster-scoped kind's replace;
- the regenerated spec and the Namespaces and Security Spec tabs.

Round 1's three Minors are closed, and no finding is open.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### Routed (not this work's)

Round 1's two routed items stand unchanged. The empty-path detail of a Namespace replace goes to the decider, and the
merge of the rollout list goes to the integration step.

### ✅ Verified correct (keep it)

- **Minor 1 is closed.**
  - The fix now reads "Name exactly one action that funcd knows, such as kv::read, and only entity types that it knows;
    when the detail names an unknown action or entity type, it lists the ones that funcd knows"
    (`internal/controlplane/routes_rest.go:280-281`).
  - Each refusal of the case matches it (`internal/auth/cedar/schema.go`). An unknown action is named, and the actions
    that funcd knows are listed (`:186`). An unknown entity type is named, and the entity types are listed (`:191`). A
    statement with no single action names neither and lists nothing (`:183`). Round 1's probe saw all three details.
  - "Exactly one action" now matches the case's when.
- **Minor 2 is closed, and the implementer's addition holds.**
  - The when now reads "The ResourceGroup still has members: objects of the namespace, other than ResourceGroups,
    whose metadata.resourceGroup names it and that no other object controls" (`routes_rest.go:40-41`).
  - `admission.Members` skips every cluster-scoped kind and ResourceGroup
    (`internal/controlplane/admission/resourcegroup.go:21`), as its doc comment and ADR-0170 Decision 7 state
    (`:15-17`). It leaves out only an object that has a controller reference (`:30`).
  - The addition matters. A ResourceGroup must name a group in its own `metadata.resourceGroup`: the matrix's
    broken-rule row answers `resourceGroup is required for kind "ResourceGroup"`. Without the words, a reader would
    count a group that names this one among its members.
- **Minor 3 is closed.**
  - For a cluster-scoped kind, the when now reads "The body sets metadata.namespace, which the detail compares with the
    path's empty namespace, or its metadata.name is not the path's, or If-Match and metadata.resourceVersion name two
    versions". The fix reads "Leave metadata.namespace empty, and send the object to its own path, with at most one
    version" (`internal/controlplane/describe.go:283-290`).
  - Each trigger has its raise site:
    - `withReplaceVersion` in the route (`routes_rest.go:551`);
    - `replaceObjIf`'s namespace check, against the path's empty namespace (`internal/controlplane/handlers.go:220`);
    - its name check (`handlers.go:223-224`).
  - Round 1's probe got the three details: `If-Match "998" and metadata.resourceVersion "999" differ`,
    `body namespace "x" does not match path ""` and `body name "x" does not match path "tenant"`.
  - The namespaced text is byte-identical. The `when` and `fix` strings rebuild the old sentence, and no namespaced
    replace operation changed in the spec.
  - The text does not reach RuntimeClass, WorkerNode or Gateway, which have no cases. Their replaces call `replaceObj`
    with an empty namespace (`handlers.go:1176`, `:1198` and `:1220`), so the text would hold if they were described.
  - The new comment gives the reason for the branch.
- **No wire change, and nothing else changed.**
  - A leaf-by-leaf diff of the spec at `cffa2b68` and at `b6b4c76e` finds 9 changed leaves in 4 operations:
    `createPolicy`, `replacePolicy`, `deleteResourceGroup` and `replaceNamespace`.
  - The leaves are the four changed whens and fixes, and the response descriptions that are written from them.
  - The other 164 operations and the components are unchanged.
  - The Go change is the cases' text and one comment, and no test changed. The statuses are the same, so round 1's
    full run of the guard still holds for them.
- **The matrices** still hold each row to one case and each case to one row. All 366 rows pass. Each row's selector
  still selects exactly one case: "still has members", "exactly one action" and "is not the path's".

### Checks run

| Check | Result |
| --- | --- |
| `go test -race -count=3` of the three matrices alone | ok, 3.0 s; once more with `-v`: 366 subtests pass |
| `go test -race ./internal/controlplane/...` | ok: `controlplane` 16.8 s, `admission` 2.8 s, `specgen` 1.7 s, `kindref` 4.9 s |
| `just generate` | exit 0; the tree was clean after it |
| `gofmt -l` on the two Go files; `go vet` and `golangci-lint` on `./internal/controlplane/` | clean; exit 0; 0 issues |
| The leaf diff of the spec, `cffa2b68` against `b6b4c76e` | 9 leaves in 4 operations, all texts of the three fixes |
| The full `pkg/funcd` run, `just ci`, the e2e suite, `go test ./...` | not run, as asked: the batch gate runs them |

### Definition of Done

18 of the 18 items checked here hold, as in round 1. "`just ci` passes" is left to the batch gate, as in round 1.

### Model scorecard

To be recorded in the batch's ledger commit: claude-opus-5-5 on ADR-0329 (implementation, the tenancy and security
rollout, round 2) → pass, 0/0/0, 0 model-attributed, DoD 18/18. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. No finding is open. Nothing is stamped: ADR-0329 is already `Implemented`.
