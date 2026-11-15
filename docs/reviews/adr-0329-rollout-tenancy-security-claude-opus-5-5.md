## Verdict: pass — 0 blockers, 0 majors, 3 minors  (ADR-0329 rollout, the Namespaces, Security and Networking features, model: claude-opus-5-5)

The work is one item of ADR-0329's rollout: commits `132d500e` and `9ff5a0cc` on `94f1821f`, branch
`feat/rollout-0329-tenancy-security`. It describes the 40 operations of Namespace, ResourceGroup, Identity, Role,
RolesAssignment, Policy, EgressPolicy and Route. It moves Namespace, ResourceGroup and Route from hand-written routes
to the generic route helpers, adds the kinds' own cases, and proves every case in three matrix tests in
`pkg/funcd/tenancy_security_answers_test.go`. Every check below ran on `9ff5a0cc`. ADR-0329 is already
`Implemented`, so this review stamps nothing.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

- **Minor 1: the fix of Policy's second Cedar case says that the detail lists both vocabularies** · attribution:
  model.
  - The fix ends with "the detail lists both" (`internal/controlplane/routes_rest.go:280`).
  - Each refusal of the case lists one vocabulary at most (`internal/auth/cedar/schema.go`). An unknown action lists
    the actions that funcd knows (`:186`). An unknown entity type lists the entity types (`:191`). A statement that
    names no single action lists neither and gives one example (`:183`).
  - The probe saw all three: `kv::fly` answered `(curated: egress::connect, kv::read, …)`, `Robot` answered
    `(curated: BlobPrefix, Bucket, …)`, and `action in [...]` answered `must name a specific action (e.g. action ==
    Action::"kv::read")`.
  - Fix: end the sentence with "the detail lists the actions or the entity types that funcd knows", or drop the
    clause.
- **Minor 2: the "still has members" case leaves out members that another object owns** · attribution: model.
  - The when says that a member is an object "that no other object owns" (`internal/controlplane/routes_rest.go:41`).
  - `admission.Members` leaves out only an object that has a controller reference
    (`internal/controlplane/admission/resourcegroup.go:30`).
  - A store that another object made can carry only its maker's marker, an owner reference with `controller: false`
    (`internal/ownedstore/ownedstore.go:39-53`). A Site's Bucket always does (`internal/site/materialize.go:34-37`).
    So does a store of a Function or a Workflow whose `deletion` is `retain`. Such a store takes its maker's resource
    group (`internal/function/rootentries.go:32`, `internal/site/materialize.go:36`), so it is a member, and it blocks
    the delete.
  - The guide describes `ownerReferences` as "The objects that own this one"
    (`docs/guide/develop/common-definitions.md:20`). A reader of the case would therefore leave a Site's Bucket out of
    the members.
  - Fix: say "that no other object controls". The guide describes the `controller` field as "Whether this owner is
    the one that manages the object" (`docs/guide/develop/common-definitions.md:31`).
- **Minor 3: the path case of `replaceNamespace` names only `metadata.name`** · attribution: model.
  - For a cluster-scoped kind, the when reads "The body's metadata.name is not the path's, or If-Match and
    metadata.resourceVersion name two versions" (`internal/controlplane/describe.go:280` and `:286`).
  - The same check also refuses a body that sets `metadata.namespace`. `replaceObjIf` matches the body's namespace
    against the path's, which is empty for a cluster-scoped kind (`internal/controlplane/handlers.go:220`). The
    probe's Namespace replace with `"namespace":"x"` answered 400 with the detail `body namespace "x" does not match
    path ""`.
  - The answer is listed: its status and problem type are those of the path case. The generic "A field breaks a rule"
    case covers it loosely too. So the work does not document this answer wrongly, but no when names its trigger.
  - The wording comes from the pilot's `verbCases.replace`. Namespace is the first described kind that uses it.
  - Fix: for a cluster-scoped kind, say "The body sets metadata.namespace, or its metadata.name is not the path's, …".
    The row's `"is not the path's"` still selects the case.

### Routed (not this work's)

- **The detail of that refusal names an empty path** · home: elsewhere · destination: the decider.
  - A create of the same body is refused by the metadata rule `namespace must be empty for cluster-scoped kind
    "Namespace"` (`api/types/v1alpha1/metadata.go:188`). A replace gets the path-match detail above.
  - The behavior is surprising and not checked yet. Changing the detail is a wire change, which the decider's ruling
    leaves out of this rollout. Only Minor 3's wording stays here.
- **The rollout list conflicts with two sibling items** · home: elsewhere · destination: the integration step.
  - `git merge-tree` of this branch with `feat/rollout-0329-eventing` and with `feat/rollout-0329-services` conflicts
    only in `internal/controlplane/kindref/testdata/undescribed-operations.txt`. `routes_rest.go` and the spec merge
    cleanly.
  - The integrator regenerates the list with the `-update` flag after the merge, then runs `just generate`.

### The implementer's choices that the ADR does not state

- **A kind whose only rules are its `Validate`'s gets no case of its own**: sound.
  - Decision 1 gives an operation its own cases, with an admission's refusal as its example. The Function rollout does
    the same: its admissions' refusals are cases of their own (`routes_rest.go:66-88`), and its `Validate` rules fall
    under the shared 400.
  - No admission other than `validate` handles Namespace, Identity, Role, RolesAssignment, EgressPolicy or Route. The
    lists checked: the pipeline's (`pkg/funcd/funcd.go:1053-1086`), the providers'
    (`pkg/funcd/binding_providers.go:102`, `:240` and `:429`) and the App admissions (`internal/app/admission.go:50`,
    `internal/app/requires.go:175`).
  - The probe agrees: a second Route on the same host and path answers 200, so the conflict is a status reason that
    the reconciler sets.
- **Policy's Cedar refusals as three cases, by what the caller fixes**: sound.
  - The policy-validity admission refuses in six places that a request can reach (`internal/auth/cedar/schema.go`).
    Two more, at `:173` and `:177`, refuse a parsed policy that does not marshal, which no request text causes.
  - Case 1 covers a parse error (`:164`) and a text with no statement (`:167`). Case 2 covers no single action, that
    is none or a list (`:183`), an unknown action (`:186`) and an unknown entity type (`:191`). Case 3 covers a scope
    entity of another namespace (`:144`).
  - The probe sent seven texts, one or two for each place, through create and replace. Each answered 400 with the
    matching detail.
- **The answers left out as unreachable**: each claim holds.
  - **The 503 of a forced delete with no garbage collector.** `New` always builds the collector, and an error stops
    `New` (`pkg/funcd/funcd.go:881`). It passes the collector to the only production `controlplane.NewServer` call
    (`:1107-1117`). Only tests build a server without one, such as `cmd/funcd/backup_test.go:79`.
  - **A member's 403 during a forced delete.**
    - A caller that may delete the group is an admin or a developer of the namespace. Both may delete every
      namespaced kind there (`internal/auth/rbac/rbac.go:23-57`). A viewer, whose Secret rule is the only rule per
      kind, may delete nothing.
    - CRUD requests carry no action, so the routing authorizer sends them to RBAC (`internal/auth/routing.go:30-38`).
    - `WithAuthorizer` has no caller outside the tests.
  - **The 503 when a request ends while it waits for the namespace admission lock.**
    - The lock waits on the request's context (`internal/controlplane/nslocks.go:35-44`).
    - The control-plane server sets no `BaseContext` and no `WriteTimeout` (`pkg/funcd/funcd.go:1136-1137`). Its
      router adds only `LogCauses` and `Authn` (`internal/controlplane/server.go:86-87`). Its stop calls `Shutdown`,
      never `Close` (`pkg/funcd/funcd.go:1344`).
    - `ReadTimeout` does not end a running handler's context. A scratch program on go1.26.3 showed it: a handler that
      waited 3 s behind a 1 s `ReadTimeout` kept its context and answered 200.
    - So the context ends only after the client has gone, and no client reads that 503.
- **Identity's shared 400 row uses a missing `resourceGroup`**: sound.
  - `Identity.Validate`'s own rule is `spec.type` (`api/types/v1alpha1/identity.go:62`). The schema's enum refuses an
    unknown type first: the probe got 422 `expected value to be one of "external" (body.spec.type)`.
  - The metadata rules are the remaining source of a 400. The row's answer is `resourceGroup is required for kind
    "Identity"`.
- **ResourceGroup's unknown-query row sends `?bogus=true`**: sound. `force` is a parameter of that delete, and
  `?bogus=true` answers 422 `unknown query parameter (query.bogus)`.

### ✅ Verified correct (keep it)

- **No wire change.**
  - **The spec.** A Python diff of the spec at `94f1821f` and at `9ff5a0cc` finds 168 operations on both sides, with
    49 changed.
    - The 40 operations of the item gain a summary, `security` and their responses. Their parameters change only in
      descriptions, and none keeps a `default` response.
    - The other 9 are the get, replace and delete of RuntimeClass, WorkerNode and Gateway. Each differs only in the
      description of `name`, on the parameter and in its schema.
    - Unchanged in all 49, descriptions aside: the paths, methods, operation IDs and tags, the parameters' names,
      places, flags and schemas, the request bodies and the success responses. No component or top-level key changed.
    - huma and funcd read `security` only to write the document and the pages.
  - **The live answers.** A scratch probe sent 417 requests in order, both to `94f1821f`, built from a `git archive`,
    and to `9ff5a0cc`, through `go test -overlay`.
    - The requests: the matrices' 366, plus 51 more. The additions include forced deletes (one that deletes its
      member, one with a stale If-Match, `force=1`, an empty `force`), dry runs, `generateName`, `If-Match: *`, two
      versions, a body of another namespace on a replace, and unknown parameters on reads. They also include PATCH,
      HEAD and POST on items (405 with `Allow`), a trailing slash, and the gets of RuntimeClass, WorkerNode and
      Gateway.
    - The statuses, the `Content-Type` and `Allow` headers, the set of header names and the bodies are identical.
      Volatile metadata was removed, along with the random suffix of generated names.
    - On the branch, `TestMain` found none of the 417 answers unlisted.
  - **The generic helpers** do what the removed routes did.
    - The input types match the removed ones field by field.
    - A create checks the path's namespace and applies `dryRun`. A replace checks If-Match against the metadata.
    - The ResourceGroup delete keeps `deleteResourceGroupInput`, with If-Match, the path and `force`. Only two
      descriptions are new.
    - `registerNamespacedCRUD` skips the delete only when `remove` is nil. ResourceGroup is the one kind that leaves
      it nil, and at the base every caller set it.
- **The matrices.**
  - 90, 186 and 90 rows. These are exactly the documented cases of their operations, counted from the spec: 40
    successes and 326 cases.
  - `runMatrix` runs with no exemption. Each row takes its status and its problem type from the document, and its
    detail names the refusal's message.
  - The rows are deterministic. The stale rows send `"1"`, which no stored version equals. The refused Policies are
    never stored, and each delete row deletes its own `-doomed` object.
  - Each matrix runs in 0.07 s.
- **Mutants.** All three failed a matrix:
  - an untested 409 case on `deleteNamespace` fails `TestNamespacesAnswersMatrix`:
    `cases with no row [deleteNamespace 409 "The Namespace still holds objects."]`;
  - the ResourceGroup delete passing `false` for `force` fails the forced-refusal row on its detail: the answer is
    `still has 1 member(s): Function/anchor`;
  - `ValidateCedarInNamespace` without its namespace check fails both "another namespace" rows: 400 expected, 200
    received.
- **The guard.**
  - The full non-e2e run without `-race` passed with both halves of `TestMain`, in 65.2 s.
  - The full run under `-race` failed one test, `TestDependencyCheck_AForbiddenLinkFails`, with the report kind
    `socket` instead of `link`. That test writes its Policy straight to the store, not through the API. Alone, under
    `-race`, it passed 5 runs of 5. It is a `TestDependencyCheck*` load flake that the brief names.
- **The pages.**
  - The Namespaces, Security and Networking Spec tabs show the HTTP API sections of their 10, 20 and 10 operations.
  - `just generate` changes nothing.
  - Each of the 326 cases has a problem type, a when and a fix. No added line of the spec or the pages names an ADR,
    and `TestScenarioNoADRInDescription` passes.
- **The rollout list** loses exactly these 40 operations. `TestEveryOperationIsDescribed` passes, and so does
  `scripts/check-list-only-shrinks.sh` against `94f1821f`.
- **The two edited tests keep their coverage.**
  - In `TestScenarioUnlistedAnswerFails`, `compareAnswers` takes the same branch for an operation that the document
    lacks as for one that it holds undescribed: neither is in `listed` (`pkg/funcd/main_test.go:117`).
  - `TestOnlyDescribedKindsLoseTheDefault` uses Service, an `Omitted` kind
    (`internal/controlplane/kindref/kindref.go:60`), which no item describes.
- **Conventions.**
  - The bloat audit passes with no flag. Production code shrinks by 176 lines (84 added, 260 removed).
  - Comment ratios: 9 comment lines to 67 code lines in production, 23 to 260 in tests. The comments give reasons.
  - The new helpers stay in the item's file, and `api_answers_test.go` is unchanged. `clusterFixture.sharedRows`
    mirrors `kindFixture.sharedRows` for a cluster-scoped kind, as the brief's rule on shared helpers leads to.
  - The matrices call `t.Parallel()`, and the file builds outside the e2e build.
  - Its helper names collide with none in the new test files of the sibling branches (eventing, services,
    blob-secret, sql-calls).

### Checks run

| Check | Result |
| --- | --- |
| `go test -race -count=3` of the three matrices alone | ok, 2.1 s; once more with `-v`: 366 subtests pass (90, 186 and 90) |
| `go test -race ./internal/controlplane/...` | ok: `controlplane` 16.1 s, `admission` 2.1 s, `specgen` 2.9 s, `kindref` 5.4 s |
| `go vet` on `./internal/controlplane/...` and `./pkg/funcd/`, also with `-tags e2e` | exit 0 |
| `golangci-lint run ./internal/controlplane/... ./pkg/funcd/...` | exit 0, 0 issues |
| `GOOS=linux go build ./...`, then `GOOS=linux go vet` on the touched packages | exit 0, exit 0 |
| `go test -race -count=1 ./pkg/funcd`, no filter | FAIL in 98.9 s, only `TestDependencyCheck_AForbiddenLinkFails`; alone, `-race -count=5`: ok |
| `go test -count=1 ./pkg/funcd`, no filter: both halves of the guard | ok, 65.2 s |
| `just generate`, twice | exit 0 both times; the tree was clean after each |
| `just check-hygiene`; `gofmt -l` on the six Go files; `scripts/check-list-only-shrinks.sh 94f1821f` | clean; clean; exit 0 |
| The spec diff, and the live probe of 417 requests on both commits | identical where it must be |
| Three overlay mutants | all three failed a matrix |
| `scripts/agent/audit.py --base 94f1821f --head HEAD --report-only` | PASS, 0 hard flags |
| A scratch program: a running handler's context under `ReadTimeout` (go1.26.3) | the context survived |
| `just ci`, `just ci-full`, the e2e suite, `go test ./...` | not run, as asked: the batch gate runs them |

### Definition of Done

18 of the 18 items checked here hold:

- ADR-0329's 7 Definition-of-done items and 9 Review-checklist items, applied to this item;
- three rulings of the decider that the ADR does not state: one row per case with no exemption, answers that only a
  shim writes left out, and the item's tests in a file of their own.

The items about `invokeFunction` and the recorder hold because this work leaves them unchanged. The 19th item,
"`just ci` passes", is left to the batch gate, as asked. The parts of it that this change reaches pass.

### Model scorecard

To be recorded in the batch's ledger commit: claude-opus-5-5 on ADR-0329 (implementation, the tenancy and security
rollout) → pass, 0/0/3, 3 model-attributed, DoD 18/18. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. The builder can reword the three texts (Minors 1 to 3) in this pull request, then run `just generate`. The
empty-path detail goes to the decider, and the list's merge goes to the integrator. Nothing is stamped: ADR-0329 is
already `Implemented`.
