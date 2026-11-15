## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0329 rollout, the App and Site features, round 2, model: claude-opus-5-5)

Round 2 reviews commit `182ec998`, which follows [round 1](adr-0329-rollout-apps-claude-opus-5-5.md). It changes:

- `internal/controlplane/routes_rest.go`: the case of a ref to an object that the App of its name controls moves from
  `replaceApp`'s own cases into `appWrites()`, which `createApp` and `replaceApp` share. Its When and What to do are
  reworded, and the comment of `appWrites()` says why a create meets it.
- `pkg/funcd/apps_answers_test.go`: a `createApp` row POSTs `keeper` with `kv: [{ref: kept}]` and expects 400 with
  `is controlled by App/keeper`. The `replaceApp` row keeps its place with a shorter `when` match, and the two rows
  share `keptRef`.
- The generated spec and the App Spec tab.

Round 1's Minor is closed, and no finding is open.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### Routed (not this work's)

- `retryApp`'s 503 "the App reconciler has not run yet" stays with the decider, as round 1 records. The pull request
  names the gap.

### ✅ Verified correct (keep it)

- **Round 1's Minor is closed.**
  - `createApp` lists the case as a 400 `urn:funcd:problem:invalid`, between the undeclared Secret and the cycle.
  - The new row passes, and `TestAppAnswersMatrix` has 76 subtests.
  - Round 1's probe gets the same answers. A create and a dry-run create of `keeper` with the ref get 400
    `admission.app-parts: spec.kv[0].ref: KVStore/kept is controlled by App/keeper; only a store set to deletion:
    retain can become a ref`. The plain spec gets 409. `createApp` now documents the case of that 400.
- **The texts match the rule.**
  - The When, "A ref entry names an object that the App of this name controls", matches the admission's check, which
    compares the controller's kind and name (`internal/app/admission.go:81`), and its refusal (`:82`).
  - The What to do, "Declare it by name instead; to keep a store as a ref, apply the App with the store's deletion
    set to retain first", matches the refusal. It also matches ADR-0199 Decision 3: "a store becomes a `ref` only
    after `deletion: retain`".
  - The new comment of `appWrites()` gives the two reasons why a create meets the rule. The admissions run before the
    store refuses a taken name (`internal/controlplane/handlers.go:116`, then `:129`), and the rule matches the
    controller by kind and name. It says why, not what.
  - No description names an ADR: the kindref tests pass.
- **The mutants fail.** Each is an overlay on `182ec998`, run against both matrices:
  - the case dropped from `appWrites()`: `runMatrix` fails with `createApp: one case holds "A ref entry names an
    object"`;
  - app-parts checks the ref only on an update: the new row fails, 400 expected and 409 received. The row exercises
    the create path's admission, not only the document.
- **Only the documentation changed.** A diff of the spec at `9586fb79` and at `182ec998` finds `createApp` and
  `replaceApp` changed, and only in their responses. Their statuses, parameters, bodies and successes are the same,
  and the other 166 operations are unchanged.

### Checks run in round 2

| Check | Result |
| --- | --- |
| `go test -race -count=3 -run '^(TestAppAnswersMatrix\|TestSiteAnswersMatrix)$' ./pkg/funcd` | ok, 5.4 s |
| The same with `-v -count=1` | 76 and 46 subtests, PASS |
| Round 1's probe of the create | 400 for a create and a dry-run create; 409 for the plain spec |
| Two overlay mutants | each failed the matrix |
| `go vet`, on the host and with `GOOS=linux`, and `golangci-lint` on `./internal/controlplane/` and `./pkg/funcd/` | exit 0; 0 issues |
| `go test -race ./internal/controlplane/kindref/ ./internal/controlplane/cmd/specgen/` | ok |
| `scripts/agent/d just generate` | exit 0; the tree was unchanged |
| The spec diff across the round | 2 operations changed, in their responses only |
| The full `pkg/funcd` run, `just ci`, the e2e suite | not run in this round: the commit changes registration texts and one test file, and the batch gate runs them |

### Definition of Done

15 of the 15 items checked hold, as in round 1. "`just ci` passes" stays with the batch gate and is not counted.

### Model scorecard

To record: claude-opus-5-5 on ADR-0329 (implementation, the App and Site rollout, round 2) → pass, 0/0/0,
0 model-attributed, DoD 15/15. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. The batch gate runs next. The pull request names the gap of the retry's startup 503, and the decider rules on
it. Nothing is stamped: ADR-0329 is already `Implemented`.
