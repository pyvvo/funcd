# Call length fix review — claude-opus-5-5

## Verdict: pass — 0 blockers, 0 majors, 0 minors (fix with no issue, model: claude-opus-5-5)

This reviews the commit "an external call reaches its worker with its length": one line in `internal/dataplane/plugins.go`, the test
`TestChunkedCallReachesTheWorkerWithItsLength` and one row of `docs/behavior/calls.md`. The change has no issue, so the `TestIssue<N>` name and the
`Fixes #N` line do not apply.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

None.

### Routed (not this work's)

- **The Lake proxy sets a length the same way** · home: above · destination: the decider, as a question. `internal/lake/gateway/proxy.go:160` sets
  `r.ContentLength` and the `Content-Length` header on a body it read whole, and leaves the caller's coding. A scratch test through `newLakeProxy` shows a
  chunked call reaching the engine chunked, with its whole body. No ADR, row, issue or card speaks of the framing at the engine, and a sound design could
  want either, since that proxy forwards a body opaquely apart from the handshake's token. Verdict: `uncovered`. It is no sibling this fix must take.
- **How a shim reads a chunked body** · home: elsewhere · destination: pyvvo/funcd-python#114 and the later bump of the pinned shim. A nested call keeps its
  own framing, as ADR-0134 Contracts says of `internal` requests.

### Not run here

`scripts/agent/gate.sh`, `just ci-full`, the Linux checks, the lanes and the whole e2e suite did not run in this review. The session that drives this
work runs one gate and one lane pass over the branch at the end. None of this is a finding.

### ✅ Verified correct (keep it)

**It is a bug: both checks of `.claude/CLAUDE.md`, done here.**

1. **It happens.** With the change reverted by an overlay of `internal/dataplane/plugins.go`, `TestChunkedCallReachesTheWorkerWithItsLength` fails at
   `internal/dataplane/normalize_e2e_test.go:105`: `Should be empty, but was [chunked]`, message `the worker's request is not chunked`.
2. **Nothing decided it.**
   - `docs/behavior/`: `chunked` and `Transfer-Encoding` hit only this commit's row. The row `A body over the cap` speaks of the cap.
   - ADR-0134: Alternatives, Decision, Contracts, Scope, Consequences and Open questions. ADR-0181: Scope, Decision 2, the Contracts table, Consequences
     and Open questions. ADR-0302: its envelope row. ADR-0112 and ADR-0148: they speak of a chunked body's cap. ADR-0013 speaks of answers, not of requests. A
     grep of `docs/adr`, `docs/design` and `blueprint.md` for `chunked` and `Transfer-Encoding` finds nothing that accepts a chunked request at a worker.
   - Issues, open and closed, searched for `chunked`, `Transfer-Encoding` and `Content-Length`: no hit on this. Board cards: none.
   - Tests: `TestScenarioUpgradeWithBodyStillCapped`, the scenario `edge-chunked-body-over-cap` and the `chunked body` row of
     `internal/dataplane/normalize_test.go:107` pin the cap and the classification. None pins the framing of the worker's request.

   Verdict: `bug`. ADR-0134 Contracts, Wiring, says to "replace `out.Body`/`out.ContentLength`/`Content-Length` with the envelope", and ADR-0181 Decision 2
   applies that to a chunked request, which "is read through `http.MaxBytesReader`" and "is wrapped". In `net/http`'s request writer a chunked coding takes
   precedence over the length, so the clause holds on the worker's request only when the coding goes too.

**Cause, not symptom.** The line sits where the body and the length are replaced (`internal/dataplane/plugins.go:157`), on the Call's clone of the request. It
adds no timeout, retry or tolerance, so it adds no policy and needs no ADR.

**The test.** `TestChunkedCallReachesTheWorkerWithItsLength` passes un-skipped under `-race`. It sends a chunked POST through the real handler to a recording
worker, with `t.Parallel()`, and uses the file's rig and the repo's `io.MultiReader` way of making a body chunked.

**Overlay mutants of `internal/dataplane/plugins.go`:**

| Mutant | Tests that fail |
|---|---|
| The `TransferEncoding` line removed | 1: `TestChunkedCallReachesTheWorkerWithItsLength` |
| The `ContentLength` line removed, the `TransferEncoding` line kept | at least 17, this test among them (one test hung, so that run was stopped) |
| The coding cleared only when the caller sent a length | 1: `TestChunkedCallReachesTheWorkerWithItsLength` |

**The behavior on real workers.** A scratch e2e test, not in the tree, made 20 calls through the edge, five per Function, mixing chunked and sized bodies, on a
solo and a pooled Function of the real Node and Python shims. Each answered 200 with the data it was sent.

**Commands, each through `scripts/agent/d`:**

| Command | Exit |
|---|---|
| `go test -race -count=1` of `TestChunkedCallReachesTheWorkerWithItsLength` in `./internal/dataplane` | 0 |
| `go test -race -count=1 ./internal/dataplane ./internal/activator` | 0 |
| `go vet ./internal/dataplane ./internal/activator` | 0 |
| `go tool golangci-lint run ./internal/dataplane/... ./internal/activator/...` | 0 (0 issues) |
| `just check-behavior`, `just check-hygiene` | 0, 0 |

**Scope, reuse and shape.**
- Every hunk serves the fix: the line, its test and its row. No test is weakened or deleted, and no ADR file is edited.
- The change adds no helper and no dependency. The test reuses `authDoorUpstream`, `warmAPI`, `seedFunction` and `creds`.
- The line's comment says why it is there. The subject is `fix(dataplane):`, and the message carries its `Test:` line and the attribution trailer.
- The row `A chunked call` (`docs/behavior/calls.md:43`) cites the two clauses above and the test, as `decided`, and `just check-behavior` takes it.

**Siblings.**
- `internal/contractcheck/contractcheck.go:68` puts a read prefix back on the same body and changes no length.
- `internal` requests are streamed, as ADR-0134 Contracts says, so they are outside the clause.
- The Lake proxy is routed above.
- No design spec under `docs/design` models a request's framing.
- A chunked call's trailers do not reach the worker, because `net/http` drops them from a request with no chunked coding (ran). No ADR on the path, row, test in
  `internal/dataplane` or `internal/activator`, or pinned shim reads a request's trailers (grep).

### Definition of Done

12 of 12 hold: the `/fix-review` fix checklist without its item 13, which does not apply because no design spec models this. Item 1 holds with no issue number
in the test's name, item 8 holds for the host checks that ran here, and item 11 holds with no `Fixes #N`. The Linux part of item 8, e2e and the lanes are the
driving session's one pass.

### Model scorecard

Recorded: claude-opus-5-5 on the call length fix (fix): pass, 0 blockers, 0 majors, 0 minors, 0 model-attributed, DoD 12/12. See
docs/reviews/model-scorecard.md.

### Recommendation

The commit needs no rework. It merges with the branch after the driving session's one gate and lane pass.
