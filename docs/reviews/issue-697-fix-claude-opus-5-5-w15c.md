## Verdict: changes requested — 0 blockers, 1 major  (issue #697 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i697`, commit aa7acb4c `fix(artifact): bound the wait for a registry's response headers`.
It sets `ResponseHeaderTimeout = 10s` on the registry transport in `registryClient` (`internal/artifact/artifact.go`)
and adds `TestIssue697_StalledRegistryCallEnds` (`internal/artifact/stall_test.go`).

### 🟡 Major 1 — The test's registry answers the retry, so it hides the real bound: about 67 s per call, not 10 s · attribution: model

The issue is about a registry that never answers. The stub in `stallingRegistry` stalls only the first request for
each repository and answers 404 to every later one. The test therefore passes after one header timeout plus one
retry (each subtest ends in about 10.3 s). The test comment says the bound is "the bound on one wait for response
headers, one retry and a margin", and the commit message says the same thing.

That is not how oras-go behaves. `retry.DefaultPolicy` has `MaxRetry: 5`, and `DefaultPredicate` retries any
`net.Error` whose `Timeout()` is true, including `timeout awaiting response headers`. I ran a scratch overlay probe
against a registry that stalls every request:

```text
PROBE Resolve took 1m7s, requests 6, err ... net/http: timeout awaiting response headers
```

So a stalled registry holds the only controller worker for about 67 s per artifact call. That happens again on
every rate-limited requeue of the Function, and the delays add up across every object that points at that
registry. The pull is now bounded, which fixes the defect the issue reports. But the issue's expected behavior
says objects of every kind "keep reconciling in the meantime", and that holds only after a delay of a minute or
more. The regression test cannot show this, because a stub that stalls every request makes the test fail with the
current fix: 67 s is more than `stallWait = 30s`.

Fix (builder):

- Make the stub stall every request, as in the issue's reproduction.
- Then do one of these:
  - bound the whole call, for example with a `context.WithTimeout` around each registry operation, or with a retry
    policy that does not retry a header timeout or retries it fewer times;
  - or keep the per-request bound, assert the real bound, and justify it.
- In both cases, correct the test comment and the commit message.

### 🟡 Minor
- **The platform case runs inside `internal/artifact`** · model. The `platform` subtest builds the whole daemon
  (`funcd.New`) from an `internal/artifact` test. It is the only test under `internal/` that imports `pkg/funcd`.
  The issue's whole-platform reproduction was written in `tests/chaos`, and that is where a cross-kind starvation
  test belongs. The subtest uses a short `os.MkdirTemp("", "funcd")` data dir as required. Lint passes. Fix: move
  the platform case to `tests/chaos` (or to `pkg/funcd`), and keep the unit cases here.
- **A stall after the headers is still unbounded** · issue. `ResponseHeaderTimeout` does not cover a registry that
  sends headers and then stops sending the body during a blob or manifest read. The issue's root cause asks to
  bound "every registry round trip". The issue's own example (a header timeout) does not do that. A deadline on
  each call, as proposed under Major 1, would cover this case too.
- Observation, not scored: `registryClient` also serves `Login` and pushes. On a slow registry, the final PUT of a
  very large blob can take more than 10 s before the registry sends its headers. Nothing in the issue or the tests
  exercises this. It is worth checking when Major 1 is reworked.

### ✅ Verified correct (keep it)
- **Proof on current main (the user's prove-first rule).** `internal/artifact/artifact.go` is identical at the
  branch's merge base and at the current `origin/main` (a394c6f1). An overlay of the `origin/main` file, with the
  test kept, fails all six subtests for the issue's reason:
  - `resolve`, `platforms`, `materialize`, `site` and `contract`: "the call still waits on the stalled registry
    after 30s";
  - `platform`: "a KVStore applied while a Function's reconcile waits on a stalled registry never reconciles".
- **The test passes with the fix under `-race`:** all six subtests pass in 10.25–10.44 s, with no data race.
- **Mutants: both fail every subtest with the stall messages above.**
  - M1 removes the `ResponseHeaderTimeout` assignment.
  - M2 raises `registryResponseTimeout` to 60 s.
- **Cause:** the change bounds the request that previously had no deadline. It is not a longer timeout, it adds
  no extra retry, and it swallows no error. The error surfaces as `net/http: timeout awaiting response headers`.
- **Coverage of the issue's cases:** the test covers every call path the issue names. These are `Resolve`,
  `Platforms`, `Materialize`, `ResolveSite` and `InspectContract`, plus the whole-platform starvation case
  (KVStore `after` reconciles, and the Function reports `Failed`).
- **Siblings:** `registryClient` is the only registry HTTP client in the codebase (`retry.NewTransport` and
  `auth.Client{` each appear once), so `Login`, pulls, resolves and pushes all get the bound.
- **Reuse:** the change keeps `httpx.Transport()` and sets one field. It adds no new helper or dependency.
- **Scope:** two files, every hunk serves #697, and no test was weakened.
- **ADRs:** no Accepted ADR is contradicted and none was edited. ADR-0031 and ADR-0035 set no deadline, and
  ADR-0143 accepts only a bounded block of the worker.
- **Checks on the touched package:** `go test -race ./internal/artifact/...` passes, `go vet` passes, and
  golangci-lint reports 0 issues.
- **Shape:** the subject is `fix(artifact):`, the body has `Fixes #697` and the attribution trailer, and the change
  is one commit for one issue.

### Definition of Done
11 / 12 items hold. Miss: item 12 (every case of the issue fixed and tested). The test does not exercise a
registry that never answers, and for that registry the fix gives a bound of about 67 s, not the 10 s that the test
and the commit message claim (model).

### Model scorecard
Not recorded by this gate (the ledger row is returned to the orchestrator): claude-opus-5-5 on issue #697 (fix) →
changes-requested, 0/1/2, 2 model-attributed, DoD 11/12.

### Recommendation
Return to `/fix`. Make the stub stall every request, then either bound the whole registry call or assert and
justify the real bound, and correct the test comment and the commit message. Move the platform starvation case to
`tests/chaos`.
