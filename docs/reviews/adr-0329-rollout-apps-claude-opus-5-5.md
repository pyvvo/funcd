## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0329 rollout, the App and Site features, model: claude-opus-5-5)

The work is the App and Site item of ADR-0329's rollout: commit `9586fb79` on `6ac8d855`, on branch
`feat/rollout-0329-apps`. It describes the 13 operations tagged App, AppRevision and Site:

- App, AppRevision and Site already registered through the generic route helpers, so the work only passes their
  `kindCases`. `appWrites()` holds the cases that an App's create and replace add, and the Site's replace adds the
  prefix that cannot change.
- `retryApp` is described in place with `Describe`, and its two path parameters gain descriptions.
- `TestAppAnswersMatrix` (75 rows) and `TestSiteAnswersMatrix` (46 rows), in `pkg/funcd/apps_answers_test.go`, prove
  each case.

Every check below ran on `9586fb79`. ADR-0329 is already `Implemented`, so this review stamps nothing.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

- **Minor 1: `createApp` leaves out the case of a ref to an object that the App controls, which a create reaches** ·
  attribution: model.
  - The work lists the case on `replaceApp` only, on the ground that a create reaches it only in a garbage-collector
    window. A create of a taken name reaches it too, and deterministically:
    - `createObj` runs the admissions before the store refuses a taken name (`internal/controlplane/handlers.go:116`,
      then `:129`);
    - app-parts handles Create (`internal/app/admission.go:50-51`), and its check compares the controller's kind and
      name, not its UID (`:81-82`).
  - A scratch test probed it with `go test -overlay`. It sent a POST of App `keeper`, which exists and controls the
    KVStore `kept`, with `kv: [{ref: kept}]`. The answer is 400 `urn:funcd:problem:invalid`, `admission.app-parts:
    spec.kv[0].ref: KVStore/kept is controlled by App/keeper; only a store set to deletion: retain can become a ref`,
    for a create and for a dry-run create. The same name with a plain spec gets 409 `App "keeper" already exists`.
  - ADR-0199 Decision 3 decides the rule for the admission, which handles both writes.
  - `createApp` lists 400 already, so the guard passes, and its shared 400 for a broken rule fits the answer loosely.
    funcdctl's apply replaces first and creates only after a 404 (`pkg/sdk/sdk.go:92`), so it meets the case only in
    the window after a delete. A client that creates first and replaces on a 409 meets this 400 instead of the 409
    that it expects.
  - Fix: move the case into `appWrites()`, so that create and replace share it, and add a `createApp` row that POSTs
    `keeper` with the ref.

### Routed (not this work's)

- **`retryApp`'s 503 "the App reconciler has not run yet" stays out** · home: elsewhere · destination: the decider,
  and the pull request's description · attribution: adr.
  - `Retry` answers it when no pass has bound the hook runner's context (`internal/app/hooks.go:522-524`). The first
    pass of an App that is neither held nor paused binds it (`internal/app/reconcile.go:258`), so the window is the
    time between a start, or the release of a hold at boot, and that pass.
  - No test provokes it: `git grep 'has not run yet' -- '*_test.go'` finds none. A row would need a store that holds
    a failed hook before the platform's first pass, and no helper of the matrices builds one. ADR-0214's Contracts
    and the behavior row `docs/behavior/apps.md:39` list only the hold's 503.
  - 503 stays listed through the hold case, so the guard passes.
  - The decider's rulings do not cover a startup window: the heap 503 of the Function feature was a shim's answer.
    That ruling kept the case out and had the pull request name the gap. The commit message does not name this gap,
    so the pull request's description must.

### The five choices that the ADR does not state

1. **The App's part refusals as one 400 case and one 409 case: kept.**
   - It meets Decision 1: each status that the operations answer has one response, and each case has a type, a
     When and a What to do.
   - The statuses are complete. app-parts wraps a part's refusal with its own kind (`internal/app/admission.go:100`).
     Each admission that it runs on a part's create or update refuses with Invalid or Conflict, or with Internal
     when a read fails. Those admissions are link-validity, binding-declared, function-volumes and its booker, the
     KV, Bucket and SQL count quotas, the KV and Bucket protections on update, site-prefix-immutable and
     backup-schedule. The two store-owner admissions mutate and never refuse.
   - The reader's need is met. The detail names the entry and the rule of the part's kind, for example
     `admission.app-parts: spec.functions[0] (Function/linked-api): admission.link-validity:
     spec.bindings.links[l].target "ghost" does not exist in namespace "default"`. The shared 400 for a broken rule
     already defers to the detail in the same way.
   - One case per rule would copy the cases of a dozen part kinds into the App's tab. Most of those kinds are not
     described yet, so their cases would be decided twice.
   - The two rows prove the group: each provokes one rule through the same wrap and checks the status, the type and
     the entry in the detail. Two mutants of that wrap fail them (below). Each rule's own status is proven by its
     kind's tests.
2. **The ref to a controlled object on `replaceApp` only: the stated reason does not hold.** Minor 1.
3. **One `retryApp` 503 left out: routed above.** The window is real and short, but no ruling covers it.
4. **One 503 for every hold: kept.** `Retry` gives one answer, `funcd is held: app <app> calls no hook`, for the
   platform's hold and for a namespace's data or restore marker (`internal/app/hooks.go:482-483`, `HeldIn` in
   `internal/platform/hold/namespace.go`). One remedy fits all three, and `funcdctl hold status` lists the platform's
   hold and each namespace's (`internal/controlplane/hold.go:83`). `invokeFunction` splits its two holds because the
   gateway answers them with two messages and two remedies.
5. **No case for the dry-run plan: kept.** `withPlan` adds only the planner's errors:
   - an Internal error on a failed read or marshal;
   - a 409 when the revision that the plan names is stamped between the planner's list and its read, a race;
   - a 503 when no planner is wired, which a platform never answers, since `pkg/funcd/funcd.go:1119` wires it.

   409 is listed. A dry-run create and a dry-run replace answer 200 with `status.plan`, identical on both commits.

### Surprising, not checked yet

The work documents both behaviors correctly. Neither is called a bug here, and neither was checked against the
design.

- **A Site's delete with a stale If-Match** answers 409 `store.Delete: Site "plain" resourceVersion mismatch`, where
  a replace answers `controlplane.precondition: Site default/plain has changed since resourceVersion "1"`.
  `deleteObjIf` compares the version itself only for a kind whose delete an admission handles, and no admission
  handles a Site's delete (`internal/controlplane/handlers.go:370-371`). The status and the type are the shared
  case's.
- **A retry in a held namespace answers 503 before 404.** `Retry` checks the hold before it reads the App
  (`internal/app/hooks.go:482`, then `:495`). ADR-0214 Decision 7 lists the errors in no order. The 503 row asks for
  App `any`, which does not exist, so it relies on this order. An App created in `held` would keep the row valid if
  the order changes.

### ✅ Verified correct (keep it)

- **No wire change.**
  - **The spec.** A Python diff of the spec at `6ac8d855` and at `9586fb79` finds 168 operations on both sides and
    the 13 of the item changed. Unchanged: the operation IDs, the tags, the request bodies, the success responses and
    every parameter but the two new descriptions of `retryApp`. The components and the top-level keys are unchanged.
    No described operation lists `default`.
  - **The answers.** A scratch test sent the same asks to `6ac8d855`, built from a `git archive`, and to `9586fb79`,
    through `go test -overlay`: the 121 asks of the two matrices, one at a time, and 12 more. The 12 were five 405s
    with their `Allow`, three dry runs, a retry with a body, a retry by API key, a retry with a bad token and a list
    with an unknown parameter. The statuses, the `Content-Type` and `Allow` headers and the bodies are identical, with
    volatile metadata and status removed. The one difference is the temporary directory of the test's own artifact
    in two list answers.
- **Shared code is unchanged.** `describe.go`, `api_answers_test.go`, `crudKind` and the signatures of
  `registerNamespacedCRUD` and `registerNamespacedRead` do not change. The only new function is `appWrites()`.
- **The cases match their raise sites.**
  - The undeclared Secret matches `undeclaredSecret` (`internal/app/admission.go:110`), and the cycle matches `cycle`
    (`internal/app/requires.go:251`).
  - The controlled ref matches `internal/app/admission.go:81-82`.
  - The version range matches app-requires on update, which skips a dependent that has not started.
  - The required App on delete and the Site's prefix match their admissions.
  - The retry's 409s and its 503 match `Retry`, and its 403 is the update authorization of
    `authorizeAppRetry`.
  - Each What to do names something that exists: `funcdctl hold status`, `spec.paused`, `deletion: retain`,
    `volumes.capacity` (`cmd/funcd/main.go:1623`). No description names an ADR.
- **The rows that #1463 deferred are covered.** The drafts of #1463 list them: `4e82ea9576` for the Site page and
  `ba4181bdb4` for the App page.
  - The App's server answers are a rule that names its entry, a part's undeclared Secret and the retry's 409. They
    are the shared 400, the group and its own cases.
  - The Site's five apply refusals are `Site.Validate` rules, all `fault.Invalidf` (`api/types/v1alpha1/site.go`),
    so the shared 400 covers them. Its prefix has its own case.
  - The Site's edge 404, 405, 401 and 403 come from the gateway that serves the Site's own Route
    (`internal/gc/gc.go:38`, `api/types/v1alpha1/site.go:177`). ADR-0329's Scope leaves calls through a Route out,
    and its In list holds only the control plane's operations and `invokeFunction`.
- **The matrices.**
  - 75 and 46 rows. `runMatrix` holds each row to one case and each case to one row, with no exemption.
  - They are deterministic. The setup waits for each state that a row needs: `plain-1` stamped, `kept` made, `user`
    started, `hooked-1` Failed. The failed pre-hook call releases the App right after it records the call
    (`internal/app/hooks.go:383-397`), and a later pass marks the revision Failed, so the retry row finds no call
    running. The running-call row sends its ask only once the stalled worker holds the call.
  - Under `-race`, three runs of both matrices take 3.8 s.
  - The tests sit in a file of their own, outside the e2e build, and each calls `t.Parallel()`.
- **The mutants fail.** Each is an overlay on `9586fb79`, run against both matrices:
  - app-parts wraps every part refusal as Invalid: the two volume rows fail, 409 expected and 400 received;
  - app-parts drops the entry from its wrap: the four rows of the two groups fail on the detail;
  - site-prefix-immutable handles nothing: the prefix row fails, 400 expected and 200 received;
  - a fourth 409 case on `retryApp` with no row: `cases with no row: retryApp 409 "An unproven case that no row
    provokes."`.
- **The rollout list.** `undescribed-operations.txt` loses exactly the 13 operations, and `TestEveryOperationIsDescribed`
  passes.

### Checks run

| Check | Result |
| --- | --- |
| `go test -race -count=3 -run '^(TestAppAnswersMatrix\|TestSiteAnswersMatrix)$' ./pkg/funcd` | ok, 3.8 s |
| The same with `-v -count=1` | 75 and 46 subtests, PASS |
| `go test -race -count=1 ./internal/controlplane/...` | ok |
| `go vet` and `golangci-lint` on `./internal/controlplane/...` and `./pkg/funcd/...`; `go vet -tags e2e ./pkg/funcd` | exit 0; 0 issues |
| `GOOS=linux go build ./...`; `GOOS=linux go vet` on the touched packages | exit 0 |
| `go test -race -count=1 ./pkg/funcd`, no filter | runs 1 and 2 failed `TestDependencyCheckReadsThePolicies` and then `TestScenarioMissingDataReported`, both known load flakes, and each passed alone; run 3 ok, 101.0 s, with both halves of the guard |
| `scripts/agent/d just generate` | exit 0; the tree was unchanged |
| The spec diff, and the 133 asks on both commits | identical where it must be |
| The `createApp` probe of Minor 1 | 400 for a create and a dry-run create; 409 for the plain spec |
| Four overlay mutants | each failed a row |
| `just ci`, the e2e suite | not run: the batch gate runs them |

### Definition of Done

15 of the 15 items checked here hold:

- 6 of the 7 items of ADR-0329's Definition of done, applied to this feature. "`just ci` passes" is left to the batch
  gate and is not counted.
- Its 9 checklist items. The two that concern `invokeFunction` and the recorder hold unchanged: the commit does not
  touch them, and their scenario tests pass in the full run.

The decider's rulings hold too: no wire change, one row per case with no exemption, no shim answer, no ADR in the
text, and a file of its own.

### Model scorecard

To record: claude-opus-5-5 on ADR-0329 (implementation, the App and Site rollout) → pass, 0/0/1, 1 model-attributed,
DoD 15/15. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. The builder can add the create case of Minor 1 in this pull request. The pull request names the gap of the
retry's startup 503, and the decider rules on it. Nothing is stamped: ADR-0329 is already `Implemented`.
