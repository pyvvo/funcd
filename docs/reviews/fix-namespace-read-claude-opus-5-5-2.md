## Verdict: pass — 0 blockers, 0 majors, 0 minors (namespace-read, re-review, model: claude-opus-5-5)

This report reviews `50df8ee9` on `claude/optimistic-jang-6b6af2` against `origin/main` `5b27e74a`: one commit and two files. It answers the first review
(`docs/reviews/fix-namespace-read-claude-opus-5-5.md`). Since `056355ac`, the read of the Namespace and the by-name read of the Function take
`context.WithoutCancel(r.Context())`, `TestNamespaceRead_ContextThatEndedChangesNoAnswer` is new, and the commit message changed.

The Major and the Minor of the first review are closed, and nothing it marked correct has regressed. What comes before the PEP now depends on the store alone,
a real store fault still answers as a fault, and no clause is broken. The pass holds if the gate and the lanes are green: they run on the batch's PR and did
not run here.

### ✅ The first review's findings

| Finding | Now |
|---|---|
| Major: one check on a real daemon | Closed. I rebuilt `origin/main`, `056355ac` and this tip, and repeated the measurement on each. The case reproduces on the first two and not on this tip, where every answer in it is the one the design rules. |
| Minor: a phrase in the commit message | Closed. It now says "share one function". |

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

None.

### ✅ Verified correct (keep)

- **Checks on `50df8ee9`, through `scripts/agent/d`:**
  - `go build` of `internal/dataplane`, `pkg/funcd` and `cmd/funcd`, on the host and with `GOOS=linux`, and of `cmd/funcdctl` on the host: exit 0.
  - `go vet` of `internal/dataplane` with no tag, with `GOOS=linux`, and with the tags `e2e`, `integration` and `dev`: exit 0.
  - `golangci-lint` on `internal/dataplane`, on the host and with `GOOS=linux`: 0 issues.
  - `go test -race -count=1 ./internal/dataplane/`: ok. `TestNamespaceRead_*` under `-race -count=5`: 110 passes, no failure and no skip.
  - `just check-hygiene`: clean.
- **Not run, as agreed:** the gate, the e2e suite, `go test ./...` and any lane. They run on the batch's PR and did not run here.
- **The revert check.**
  - With `origin/main`'s `dataplane.go` overlaid, `TestNamespaceRead_ContextThatEndedChangesNoAnswer`, `TestNamespaceRead_FailedReadRefusesTheCall` and
    `TestNamespaceRead_RefusalNamesOnlyAnUnavailableEdge` fail, in all 17 cases of the last two. `TestNamespaceRead_AbsentNamespaceIsUnset` and
    `TestNamespaceRead_OwnStanceNeedsNoRead` pass there, as they pin what stays.
  - With `056355ac`'s `dataplane.go` overlaid, only `TestNamespaceRead_ContextThatEndedChangesNoAnswer` fails: it expects the 401 and gets the 503.
- **Mutants.**
  - Each `WithoutCancel` line put back on the request's context, each alone and both together: `TestNamespaceRead_ContextThatEndedChangesNoAnswer` fails.
  - The first review's 20, rerun on this tip: 18 fail a `TestNamespaceRead_*` test. One fails `TestExposure_ByNameStance` and
    `TestScenarioTheByNameFormKeepsItsStance`. The last adds a read and changes no answer, so no test sees it, and the count below shows that this tip has no such read.
- **`WithoutCancel` and the clauses.**
  - ADR-0002 §5 has every blocking call take a context and honor it. `store.Get` still does, and its `context-cancellation` scenario still holds. The data
    plane passes it a context that does not end, as ADR-0156 Decision 5 does for the metastore.
  - Both engines check the context only at the start of `View`, so these reads last as long as they did. Nothing in `internal/gateway`, `internal/edge` or
    `internal/dataplane` sets a deadline on the request's context, so no deadline is dropped. ADR-0151's deadline comes later and the activator applies it.
  - `WithoutCancel` keeps the context's values, so nothing that rides on the context is lost.
  - The order of ADR-0241 Decision 4 and ADR-0302 Decision 3 is unchanged, and the store read that ADR-0241 accepts before the PEP is the same read.
- **A real store fault still answers as a fault.** On a real Badger engine, I corrupted a Namespace record, corrupted a Function record and closed the engine. Each
  call before the PEP answers the same with a live context and with one that has ended: the 503 where the read of the Namespace fails, and the by-name path's
  existing 500 where the read of the Function fails.
- **Answers on the paths of the first review's matrix.** In an authenticated namespace, with no token and no open stance, a call answers 401 whatever its request's context
  has become: 84 of 84 rows, on the memory and Badger engines alike. A context that has ended still changes an answer only past the PEP, where the activator and the
  Route form's Function read follow the request's context as they did.
- **Nothing wakes on a refusal.** With a spy scaler, in three namespace configurations, no call that the PEP refuses and no missing, closed or own-address name starts a wake,
  as ADR-0113 and ADR-0241 Decision 4 want.
- **No added read.** The 60 rows of status and read counts are identical on `origin/main` and this tip.
- **The cost.** Allocations: one more per read, 16 bytes each. By name 279 becomes 282, and static 101 becomes 102. Time, on a shared host, with a fixed call count on one CPU, in
  alternating rounds: by name −1.5 % on the best round and −0.8 % in CPU per call, which is no measurable cost; static, in 12 pairs, a median difference of +0.10 µs (+1.1 %).
- **The refusal and what stays.** The 503's body is the one the first review read, identical on all 22 calls that get it. A refused call takes no rate token, and in a held namespace
  it names no hold. By-name calls with odd `X-Funcd-Namespace` values, such as a slash, `..` or 70,000 bytes, answer as on `origin/main`.
- **Siblings.** The read of the Namespace and the by-name read of the Function are the only store reads before the PEP, and the PEP is the only plugin at Admit. The
  Route form's Function read comes after the PEP. A failed read in `s3BucketFor` still ends in a 404, on its own context.
- **Scope, reuse and conventions.** Since `056355ac` the change adds 26 lines in the same two files: the two reads, their comments and one test on the existing `namespaceReadDoor`. No
  new helper, dependency or import. No ADR file is edited, no test is weakened or deleted, and no design spec models this path, so item 13 of the checklist does not apply.
- **Shape.** One `fix(dataplane):` commit with the attribution trailer. The commit message, the code comments and the test names describe behavior only.

### Fix checklist

11 of 12. Item 8 stays open until the gate and the lanes run: they run on the batch's PR and did not run here. The tests are named for their behavior, and the commit carries no
`Fixes #N`, because the fix has no public issue.

### Model scorecard

Not recorded by this report: the row goes into the ledger with the batch. It reads: claude-opus-5-5 on namespace-read (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/12.

### Recommendation

Ship it with the batch, once the gate and the lanes pass on the batch's PR.
