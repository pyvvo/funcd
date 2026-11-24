## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0345 implementation, round 3, model: claude-opus-5-5)

Round 3 reviews two commits on `feat/addon-runtimes`, `3ce5f0ff..cf664716`, against round 2's report
(`docs/reviews/adr-0345-implementation-claude-opus-5-5-2.md`):

- `8203dba7`, the engine mark;
- `cf664716`, ADR-0345 amended in place with the rulings of 2026-11-24.

Every check below was run at `cf664716`, not read.

### Round 2's finding

- **Minor 1, an engine object did not bar an Engineless Addon of its kind: closed.**
  - The engine path stores `status.workload: Engine` (`internal/services/addon/reconcile.go:125`). The hold keeps the
    mark: it writes only the address, the phase and the condition (`:387-390`).
  - `objectsKeepTheirWorkload` reads the mark alone (`internal/controlplane/addon.go:735-738`), and the revision
    lookup is gone.
  - Round 2's probe now gets 409 (`the IdentityProvider object default/login ran an engine`), with `rauthy-1`
    collected. `login` waits `AddonNotFound` and keeps its mark.
  - Every other read of the mark compares with `Engineless`: the collector, a group's list, the routing, an App's last
    part, `MapJobRun` and the Function watch.
  - Mutant G1, the mark's write removed, fails `TestEngineKindKeepsItsWorkloadAcrossDeletion`.

### The amendment

- **What it changes:** an `Amended in place` header line under the stamp, Decisions 1, 3, 4 and 6, a new Decision 9,
  the status contract comment and two checklist lines. Each change is marked `(amended 2026-11-24)`. The Scenarios,
  the Alternatives, the Design model and the Consequences are unchanged.
- **Each amended line against the code:**
  - Decision 1: the 400 on a replace (`addonRules`) and the 409 naming the object (`objectsKeepTheirWorkload`).
  - Decision 3: the engine fields refused, as on a settings object (`engineFields`).
  - Decision 4: `MapJobRun` takes any event of an ended run.
  - Decision 6: the forced ResourceGroup delete, through `deleteMemberAt`, with 409 naming the member.
  - Decision 9: the quota and its wording.
  - The checklist's two lines, each with its test.
- All of these hold, but one phrase does not (Minor 1).

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

- **Minor 1 — "once its engine converged" says less than the code does** · model.
  - The words: Decision 1's amended sentence and the contract comment say the Engine mark is stored "once its engine
    converged". The field's doc (`api/types/v1alpha1/addon.go:703`) says "once its engine has run".
  - The code stores the mark after every call of the provider runtime's `Converge` except a `RuntimeUnavailable`
    answer, whatever the outcome (`internal/services/addon/reconcile.go:114-125`).
  - A probe test, not committed, ran an engine object whose `Converge` fails. The object is `Pending`,
    `EngineConvergeFailed`, with no address, and `workload: Engine`. So an object whose engine never started bars an
    Engineless Addon of its kind.
  - The stricter bar is defensible: a failed start may have made the engine's storage. Fix: say so in the three places
    ("once its pass has asked for its engine, whether or not it started"), or mark only an engine that started.
  - Also, the amendment's header line (446 characters) and Decision 6's edited line (159) are not wrapped at 120, as
    the rest of the ADR is.

### ✅ Verified correct (keep it)

- **The checks**, through `scripts/agent/d`:
  - `go build ./...` passes.
  - `go vet` is clean on `api/types/v1alpha1`, `internal/controlplane/...`, `internal/services/addon`,
    `internal/app`, `internal/gc`, `internal/backup/workload`, `internal/services/backup`, `cmd/funcd` and
    `pkg/funcd`, and also with `-tags e2e` and `-tags dev`.
  - `go tool golangci-lint run` reports 0 issues, and gofmt is clean.
  - The three workload tests pass under `-race`.
  - `go test` passes on those packages (`pkg/funcd` 157 s, `cmd/funcd` 173 s) and on `cmd/funcdctl -tags dev`
    (227 s).
  - `go run ./scripts/behavior -check`, `just check-hygiene` and `go mod tidy -diff` are clean, and the spec generator
    leaves no drift.
- **Tracking**: ADR-0345 stays `Implemented`, with the stamp line above the amendment line. This round refreshes the
  stamp's note to this report.

### Definition of Done

17 of 17 items hold: the 8 Review-checklist items as amended, and 9 generic items.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0345 (implementation, round 3) → pass, 0 blockers, 0 majors and 1 minor,
model-attributed, DoD 17/17. See `docs/reviews/model-scorecard.md`.

### Recommendation

Nothing blocks the PR, which closes #1533. Minor 1 is a wording fix in the amendment and the field's doc, or a narrower
mark.
