## Verdict: changes requested — 0 blockers, 1 major, 0 minors  (ADR-0329 rollout, the Function feature, round 2, model: claude-opus-5-5)

Round 2 reviews commit `74f5f0e7`, which follows [round 1](adr-0329-rollout-function-claude-opus-5-5.md). It changes
only `pkg/funcd/api_answers_test.go`:

- the cases of the two pilot invoke tests move into `TestFunctionAnswersMatrix`, and the matrix's exemption goes;
- `answerRow` gains a `send` hook, and `ask.run` is split into `do` and `check`;
- the in-flight 503 row holds the gateway's one slot with a call that its worker keeps;
- each platform of the matrix closes its client's idle connections before it stops.

Round 1's Minor is closed. One of the three asks that the commit drops was the only test of a decided behavior.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

- **Major 1: the 404 of a Function with a path of its own lost its only test** · attribution: model.
  - The commit drops the pilot's ask "a Function at a path of its own": `POST /function/routed`, where the Function
    `routed` sets `ingress.path: /routed`, which answers 404. The reason given: the row of a missing Function proves
    the same case.
  - The case is the same, but the code path differs. `servedByName` refuses a Function whose ingress names a host or
    a path (`internal/dataplane/dataplane.go:223`, ADR-0241 Decision 4). `TestExposure_ByNameFollowsEnabled`
    (`internal/dataplane/namespacehost_test.go:112`) covers the host only.
  - A mutant drops `|| ing.Path != ""` from line 223.
    - On `74f5f0e7`, it passes `internal/dataplane`, `internal/function` and the Function matrix.
    - On `1e4175b4`, it fails the dropped ask: the Function is served by name and answers 503 `the contract of
      default/routed cannot be read`.
    - No e2e test or lane suite calls a Function with a path of its own by name.
  - Fix: add the path to `TestExposure_ByNameFollowsEnabled`. Seed a Function with `&v1.Ingress{Path: "/routed"}`,
    and add `{"localhost", "/function/routed"}` to its 404 table. The matrix keeps one row per case.

### Routed (not this work's)

None open. The decider ruled on 2026-11-15 on round 1's three routed items:

> - The Node pool heap 503 stays out, and the PR names the gap.
> - The shim's own 400 and 500 stay out.
> - The Function matrix stays in api_answers_test.go.

The pull request's description owes the gap of the heap 503.

### ✅ Verified correct (keep it)

- **Round 1's Minor is closed.**
  - `runMatrix` has no exemption left.
  - `invokeFunction` has 20 rows: its 19 cases and its success. The matrix's own check holds each case to one row.
  - Round 1's mutant, a 503 case with no row, now fails the matrix: `cases with no row: invokeFunction 503 "An
    unproven case that no test provokes."`.
- **The other two dropped asks stay covered.**
  - A credential that funcd does not know, at the edge: `TestScenarioInvalidToken401` (`internal/edge/authn`)
    answers a bad bearer token with 401.
  - A Function whose ingress is off: `TestExposure_ByNameFollowsEnabled` answers `POST /function/off` with 404 and
    wakes nothing. The e2e `TestScenario_exposure_function_off` covers it too.
- **The in-flight row is deterministic.**
  - The fake worker of `held` signals when it gets the call, then keeps it until the row's cleanup closes
    `release`. The row sends its ask only after that signal, while the held call takes the gateway's one slot. No
    time window is left to race.
  - A mutant that leaves room for two calls fails the row: it gets 404 instead of 503.
- **The idle-connection close is correct.**
  - `newAnswerEnv` registers `c.CloseIdleConnections` after the platform's stop. Cleanups run last in, first out,
    so it runs first.
  - `http.Server.Shutdown` treats a connection that never sent a request as idle only after 5 s (Go issue 22682).
    Closing the client's idle connections first ends that wait.
  - It changes only how long a stop takes. Five runs took 13.2 s. Round 1's ten runs took 30.2 s, with 15 fewer
    rows.
- **The moved rows keep the pilot's setup.**
  - They run on platforms with the pilot's options: the store that fails reads, edge authentication and the 300 ms
    read timeout; the rate limit; the in-flight limit.
  - The edge-off 401 moved to the shared platform, which has no edge authenticator. The rate row's setup takes the
    only token first.
- **`ask.run` keeps its behavior.** It is now `do` and then `check`. The `send` hook serves `stalledBody` and
  `pastInFlight`.
- **No dangling reference.** The removed test names appear only in earlier review reports.

### Checks run in round 2

| Check | Result |
| --- | --- |
| `go test -race -count=5 -run '^TestFunctionAnswersMatrix$' ./pkg/funcd` | ok, 13.2 s |
| `go test -v` on the matrix | 20 `invokeFunction` subtests, PASS |
| `go vet`, `golangci-lint` and `gofmt -l` on `pkg/funcd` | exit 0; 0 issues; clean |
| Mutants: a 503 case with no row; room for two calls in flight; the path half of `servedByName` | failed; failed; survived (Major 1) |
| `just ci` and full-suite runs | not run in this round, as asked: another agent is measuring flaky tests on this machine |

### Definition of Done

16 of the 17 items hold.

- The ruling's item 3 now holds: by the ruling, the heap 503 stays out.
- "`just ci` passes" was not rerun in this round. It passed in round 1 on `e5497f4d`, this round changes one test file
  that passes vet, lint and gofmt, and the implementer reports exit 0 on `74f5f0e7`.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0329 (implementation, the Function rollout, round 2) → changes-requested, 0/1/0,
1 model-attributed, DoD 16/17. See `docs/reviews/model-scorecard.md`.

### Recommendation

Add the path case to `TestExposure_ByNameFollowsEnabled` (Major 1), then the rollout passes. The pull request names
the gap of the heap 503, as the ruling asks. Nothing is stamped: ADR-0329 is already `Implemented`.
