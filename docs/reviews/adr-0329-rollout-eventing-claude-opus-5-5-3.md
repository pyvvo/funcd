## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0329 rollout, the events and Workflow features, round 3, model: claude-opus-5-5)

Round 3 reviews commit `9cf04c05`, which follows [round 2](adr-0329-rollout-eventing-claude-opus-5-5-2.md). The
decider ruled on round 1's routed item about the shared 409 of `createWorkflowRun`: "let a kind reword a shared case".
The commit does that:

- `kindCases` gains `words`, a kind's own When or Fix for a shared case, named by a typed key
  (`internal/controlplane/describe.go`);
- `createWorkflowRun`'s 409 gets its own fix (`internal/controlplane/routes_rest.go`);
- the spec and the Workflow Spec tab are regenerated.

No finding is open.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### Routed (not this work's)

Round 1's item about the shared 409 of `createWorkflowRun` is closed by the decider's ruling and this commit. The
storage 500s that no described operation lists stay with the decider.

### ✅ Verified correct (keep it)

- **The mechanism is minimal and sound.**
  - `kindCases.words` (`describe.go:118`) maps a `sharedCase` key (`:122-127`) to a `wording` (`:131`).
    `restOperationsOf` hands the map to `verbCases` (`:153`), and its `worded` (`:227`) replaces the When or the Fix
    that the kind gives. An empty text keeps the shared one, and the status and the problem type never change.
  - Only the create 409 goes through `worded` (`:296`), and only WorkflowRun gives a wording (`routes_rest.go:311`).
    Every other kind has no map, so its cases stay as they were. The spec diff below shows it.
  - A misspelled key does not compile. An untyped integer constant, such as `1`, would compile as a key and do
    nothing, because Go converts it to `sharedCase`. Only package controlplane can write one, and a reviewer would
    see it.
  - Decision 1 still holds: `createWorkflowRun` keeps the shared case, with its status, its type and its When. Only
    the Fix is the kind's own.
  - The new comments say why. The comment of `registerWorkflowRun` ties the wording to workflowrun-spec-immutable.
- **The new fix is true to the code.**
  - workflowrun-spec-immutable refuses with 409 a replace that changes `spec.workflow`, `spec.input` or `spec.replay`
    (`internal/controlplane/admission/workflowrun.go:122-123`). Its message gives the same advice: "start a new run
    under a new name".
  - The When is unchanged. It still holds "already exists", which the matrix row matches.
- **The spec diff.**
  - Against round 2's head, `248b91c8`, the spec has 168 operations on both sides, and only `createWorkflowRun`
    changes: the description and the fix of its 409, two lines of the YAML. No schema changes.
  - The only other generated change is that 409's row on the Workflow tab.
- **The text is pinned.** A mutant that makes `worded` ignore the kind's wording fails `TestSpecGeneratedFromGo`:
  "generated spec differs from committed spec", with 717,777 bytes generated against 717,995 committed.

### Checks run in round 3

| Check | Result |
| --- | --- |
| `go test -race -count=3` of the two matrices alone | ok, 6.1 s |
| `go test -race -count=1 ./internal/controlplane/...` | ok |
| `go vet ./internal/controlplane/...`, on the host and with `GOOS=linux` | exit 0 |
| `golangci-lint run ./internal/controlplane/...` | 0 issues |
| `scripts/agent/d just generate` | exit 0; the tree stayed clean |
| The spec diff against `248b91c8` | only `createWorkflowRun`'s 409 |
| The mutant of `worded` | failed `TestSpecGeneratedFromGo` |
| `just ci`, the e2e suite and the full `pkg/funcd` run | not run, as asked: the batch gate runs them once |

### Definition of Done

15 of the 15 items hold, as in round 1. The commit changes the text of one case and adds a way to word a case, so no
status and no number of cases changes. Round 1's full run still stands for `TestMain`'s guard.

### Model scorecard

To be recorded by the batch's last commit: claude-opus-5-5 on ADR-0329 (implementation, the events and Workflow
rollout, round 3) → pass, 0/0/0, 0 model-attributed, DoD 15/15. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. The decider's ruling closes round 1's item about the shared 409, and the storage 500s stay with the decider.
Nothing is stamped: ADR-0329 is already `Implemented`.
