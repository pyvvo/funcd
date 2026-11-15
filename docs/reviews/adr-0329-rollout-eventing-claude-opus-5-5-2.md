## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0329 rollout, the events and Workflow features, round 2, model: claude-opus-5-5)

Round 2 reviews commit `248b91c8`, which follows [round 1](adr-0329-rollout-eventing-claude-opus-5-5.md). It changes
two texts and the files that are generated from them:

- the 503 hold case of `replayDeadLetter` (`internal/controlplane/deadletters.go:140-143`);
- the description of DeadLetter's `payload` (`internal/eventing/deadletter/deadletter.go:33`);
- the regenerated `api/openapi/funcd.v1alpha1.yaml` and the events Spec tab.

Round 1's two Minors are closed, and no finding is open.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### Routed (not this work's)

Round 1's two routed items stay with the decider: the storage 500s that no described operation lists, and the shared
409 of `createWorkflowRun`, whose fix says to replace the run. This commit does not change either one.

### ✅ Verified correct (keep it)

- **Round 1's Minor 1 is closed.**
  - The When now names each hold that `Replay` refuses: the platform's hold, and a namespace's hold for its data or
    while a Restore loads it. `held` asks the platform's hold and the namespace's markers
    (`internal/sensor/sensor.go:716`, `internal/platform/hold/namespace.go:55`). A namespace holds only those two
    reasons (`internal/platform/hold/namespace.go:23`).
  - The fix now names each release.
    - `funcdctl hold release` releases the platform's hold (`internal/controlplane/hold.go:251-270`).
    - `funcdctl hold release --namespace` releases a namespace's data marker and its restore marker
      (`internal/controlplane/hold.go:276`). It answers 409 while the Restore that holds the namespace is Pending or
      Running (`hold.go:297`), which is what "once any Restore that loads it has ended" says.
  - The text follows the wording of the two hold cases of `invokeFunction` (`internal/controlplane/spec.go:95-99`),
    without their Retry-After clause. Only the data plane and the edge's limiter set that header
    (`internal/dataplane/`, `internal/edge/limit/limit.go`), and the replay's route sets none.
  - The matrix row's `is held` still matches exactly one case, and the matrix passes.
- **Round 1's Minor 2 is closed.**
  - `payload` now says that it is the CloudEvent that fired the action, not the input that the action builds from it.
    `deadLetter` marshals the firing event (`internal/sensor/sensor.go:370`), and `deliver` sets the input on a copy
    of the event (`sensor.go:324`).
  - "A replay runs the Sensor's current action on it again": `Replay` decodes the stored event and delivers it through
    the action that it finds on the live Sensor (`sensor.go:432-441`).
- **Nothing else changed.** A diff of the spec against round 1's head, `910e459f`, finds 168 operations on both sides.
  Only `replayDeadLetter` changes, in the texts of its 503 response, and only DeadLetter's schema changes, in its
  descriptions. No parameter, request body or success response changes.

### Checks run in round 2

| Check | Result |
| --- | --- |
| `go test -race -count=3` of the two matrices alone | ok, 6.2 s |
| `scripts/agent/d just generate` | exit 0; the tree stayed clean |
| The spec diff against `910e459f` | only `replayDeadLetter`'s 503 texts and DeadLetter's descriptions |
| `just ci`, the e2e suite and the full `pkg/funcd` run | not run, as asked: the batch gate runs them once |

### Definition of Done

15 of the 15 items hold, as in round 1. This commit changes two texts, not a status or a number of cases, so the
full run of round 1 still stands for `TestMain`'s guard.

### Model scorecard

To be recorded by the batch's last commit: claude-opus-5-5 on ADR-0329 (implementation, the events and Workflow
rollout, round 2) → pass, 0/0/0, 0 model-attributed, DoD 15/15. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. Round 1's two Minors are closed, and its two routed items stay with the decider. Nothing is stamped: ADR-0329 is
already `Implemented`.
