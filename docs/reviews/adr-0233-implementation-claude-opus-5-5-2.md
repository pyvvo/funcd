## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0233 implementation re-review, model: claude-opus-5-5)

Scope: the rework of the round-1 review (`docs/reviews/adr-0233-implementation-claude-opus-5-5.md`) in the uncommitted
working tree of `feat/sql-provider` against `2ff2f9d1`. The changed code is `internal/services/sql/client.go`, its
test `TestFacadeEngineFailures`, and a role-grant scoping fix in the writer lister
(`internal/services/roles/writers.go`) with its test.

**Stamped.** ADR-0233 `Reviewing → Implemented` and the FEAT-0011 F129 row `reviewing → implemented`.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...`; `GOOS=linux go build ./...` | exit 0 × 2 |
| hygiene | `just check-hygiene` | `hygiene: clean` |
| touched packages, race | `go test -race -count=1` on `internal/services/sql/...`, `internal/services/roles`, `internal/workernode/local`, `pkg/funcd` (e2e tagged out) | all `ok`, exit 0 |
| named tests | `TestFacadeEngineFailures`, `TestSQLWriters` and the new writer-lister test, `-v` | PASS × 3 |
| vet | `go vet` on the same packages | exit 0 |
| lint | `go tool golangci-lint run` on the same packages | `0 issues.`, exit 0 |
| gofmt | `gofmt -l` on the changed `.go` files | empty |
| engine address | the round-1 scratch test, rerun through `go test -overlay` | the error is `sql.engine: the database's engine does not answer`; it does not contain the address |

### 🔴 Blocker
None.

### 🟡 Major
None. Round-1 Major 1 is fixed: `call` no longer wraps a transport, read or request-build error
(`internal/services/sql/client.go:38-55`). It answers Unavailable with "does not answer", or "did not answer in time"
on a deadline, so neither the 503 body nor the log carries the engine's address. `TestFacadeEngineFailures` now asserts
that the address is absent from the error and the log, for a refused connection and for a call past
`sql.callTimeout`.

### Minor

1. **The Contract's `[][]any` rows and the missing `FacadeDeps.CallTimeout`** (`adr`). This is unchanged from round 1
   and stays on the record as an ADR deviation: the shipped `[][]json.RawMessage`
   (`internal/workernode/local/sql.go:40`, `:48`) and the `CallTimeout` field (`facade.go:32`) are right, and the ADR
   is frozen. The next ADR that cites these types should use the shipped shapes.

### ✅ Verified correct (keep)

- Everything verified in round 1 still holds: the 13 scenario tests pass in `pkg/funcd`, and the facade, Cedar
  capability, roles, bindings, pool key, deletion protection, dependency check, shims and lane are as reviewed.
- The 401 path is unchanged: Internal, and logged without the password.
- The writer-lister fix adds a test that passes with the fix and that the builder reports failing when the fix is
  removed through `-overlay`. `SQLWriters` is unchanged.

### Definition of Done

14 / 14 items hold (the ADR's 10 Review-checklist items and 4 Definition-of-done items). The repo-wide gate runs once
per PR; the touched packages' tests, vet, lint, the Linux build and `just check-hygiene` are green, and the `sql` lane
passed on containerd in round 1.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0233 (implementation) → pass, 0/0/1, 0 model-attributed, DoD 14/14.
See `docs/reviews/model-scorecard.md`.

### Recommendation

Done. The feature's board card was left for the coordinator to move.
