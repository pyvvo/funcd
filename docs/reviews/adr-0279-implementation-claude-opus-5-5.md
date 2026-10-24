## Verdict: changes requested — 1 blocker, 0 majors, 6 minors  (ADR-0279 implementation, model: claude-opus-5-5)

This report reviews ADR-0279's implementation, `git diff 8368b054..5aca61c8` (stacked on ADR-0278's branch), against
the ADR's Decision, Contracts, Scenarios, Review checklist and Definition of done. The checks below ran in a worktree
detached at 5aca61c8. The gate, the Lima lanes and experiment 0010's failure phase are the builder's runs and did not
run here.

### 🔴 Blocker 1 — every Lake query under `funcdctl dev` gets 503  ·  attribution: model
`funcdctl dev` never sets `lake.saveInterval` (`devLakeOptions`, `cmd/funcdctl/dev.go:1103`), so the facade hands the
Lake reconciler `SaveInterval` 0 (`pkg/funcd/funcd.go:1167`). `saveURL` (`internal/services/lake/reconcile.go:390-396`)
then points the dev Lake's proxy at `http://127.0.0.1:8081/save`, because the dev engine's address is
`127.0.0.1:<port>` (`internal/lake/devengine/devengine.go:348`). The dev engine serves only Quack, so the barrier's
first save call fails, and the proxy answers every request 503 "lake catalog not saved". On a host where another
service holds port 8081 (funcd's default data-plane port, or a Lima port forward), the call goes to that service.

Evidence: a test added to `pkg/funcd` through `go test -overlay` assembles the platform as `funcdctl dev` does
(`InMemory`, `WithLakeProviderRuntime` with an engine that serves no save endpoint) and sends one request through the
Lake's proxy. It gets 503 `lake.gateway.lakeProxy: lake catalog not saved`. With `WithLakeSaveInterval(time.Second)`,
the same request gets the engine's 200. The six reconciler tests that this change moved to interval mode ("the fake
engine serves no save endpoint") show the same failure.

The ADR puts the dev engine out of scope (Scope, `devengine.go:270-282`), and F95 (ADR-0130, implemented) runs Lake
queries under `funcdctl dev`. No test or lane covers that path, so the gate does not catch it. The fix stays inside the
ADR's scope and needs no new decision: the dev Lakes keep answering without a barrier (for example, `devLakeOptions`
sets a save interval, which the dev engine ignores, or the facade passes no save URL for a process-mode Lake runtime).
Add a test that sends a query through the proxy of a Lake assembled as `funcdctl dev` assembles it.

### Minor
- **The barrier test cannot fail on the release order** (`model`): `TestBarrierHoldsTheLastBytes` sends a 9-byte
  answer (`internal/lake/gateway/barrier_test.go:97`), and that answer stays in the proxy server's write buffer until
  the handler returns. A mutant that returns the last bytes with the read that reaches EOF, and makes the save call on
  the next read, passes every test of the package. With a 200 kB answer, the same mutant fails ("the answer completed
  before its save call answered"), and the real barrier holds the answer (three runs). A `heldTail` that holds no byte
  passes at both sizes: Go's transport returns a Content-Length body's last bytes together with `io.EOF`, so the read
  that makes the save call holds them, and Quack frames every answer with Content-Length (see the probe under Checks).
  The one-byte hold protects answers in other framings.
- **Three barrier rules have no test** (`model`). Each of these mutants passes every test of `internal/lake/gateway`:
  an empty answer sent through `heldTail`, so its headers go out before the save and a failed save breaks the
  connection instead of answering 503 (`TestScenarioRefusedSaveIsAnError` accepts either result in both subtests); a
  release that retries a failed save call once (only `TestBarrierReleasesOnlyTheSameInstance` counts calls, and all of
  its calls answer 200); and a save call made on `context.Background()`, so the wait no longer ends with the client's
  request.
- **The opt-in scenario does not test the wiring** (`model`): `TestScenarioIntervalIsTheOptIn` checks the `saveURL`
  helper through `export_test.go` (`internal/services/lake/reconcile_test.go:284`), not what `Reconcile` passes to
  `Ensure`. A `Reconcile` that passes `""` passes every test of `internal/services/lake`; in production, that leaves no
  barrier in front of an engine that runs no watcher. Only the duckdb lane would catch it.
- **The stop with a save call waiting is tested only with a stub** (`model`): `test_stop_answers_waiting_saves`
  (`images/runtime/duckdb/shim_test.py:878`) counts a call with `begin` and `end` and blocks nothing on the save lock.
  The refused-save calls are stub cases too. The implementation plan put both in `engine_test.py`, on a real engine.
  No lane case has a call waiting at a stop either: the commits of `issue1227` are answered before the redeploy.
- **`saveURL` fails open** (`model`): an address that `net.SplitHostPort` refuses gives `""` (`reconcile.go:391-393`),
  so the proxy holds nothing while the engine, which gets `FUNCD_LAKE_SAVE_PORT`, runs no watcher. The provider
  reports `host:port` today, so this case does not occur, but a durability barrier should fail closed.
- **Stale comments** (`model`): `internal/platform/config/config.go:320-321` still says that `lake.saveInterval` is
  "1s when empty"; `images/runtime/duckdb/shim.py:70-71` says that the platform sets `FUNCD_LAKE_SAVE_INTERVAL` "for
  every Lake"; and the docstrings of `_Checkpointer` (`shim.py:307-308`) and `_stop` (`shim.py:581-584`) describe only
  the watcher.

### Routed (not this work's)
- `docs/PROJECT-SUMMARY.md` still says that ADR-0274's default is 1 s · home: elsewhere · destination: the next
  `/project-summary` run after ADR-0279 is Implemented.

### ✅ Verified correct (keep)
- **Decision 1, the switch:** an empty key is 0 (`internal/platform/config/lake.go:20-35`, `TestLakeSaveInterval`), and
  `cmd/funcd` passes it on (`main.go:554-558`). The reconciler sets `FUNCD_LAKE_SAVE_INTERVAL`, or
  `FUNCD_LAKE_SAVE_PORT=8081` without the key (`reconcile.go:417-421`), and gives `Ensure` a save URL only without the
  key. `Ensure` retargets the proxy when the save URL changes. The shim takes its mode from `FUNCD_LAKE_SAVE_PORT` and,
  with neither key, keeps the 1 s watcher.
- **Decision 2, the endpoint:** the instance id is drawn at start, and the endpoint listens before `quack_serve`
  (`TestMainServesSavesBeforeQuack`). The saved id starts at the catalog's latest snapshot. Each call reads on its own
  cursor and answers at once when the last good save covers it. Otherwise it takes the save lock: it shares a save
  that ended while it waited, answers 503 behind a failed save, or else saves with the id read before the copy. A
  failed save records nothing and updates the report. The start-up save of a recovered catalog is retried every second
  until it succeeds or the stop begins. The stop records the snapshot id of the closed catalog, lets the waiting calls
  answer for up to 1 s, then calls `quack_stop`. Interval mode is unchanged: `quack_stop` now runs after the lock is
  released, but the watcher has been joined before the stop takes the lock, so no other thread can take it.
- **Decision 3, the barrier:** the proxy learns the instance with a save call before the first request, which gets
  503 when that call fails. Each answer gets one save call, made on the request's context. An empty answer waits in
  `ModifyResponse`, so a failed save gives a 503 before the headers. Any other answer is held by its last byte, and a
  failed save breaks it, because ReverseProxy aborts the handler on a copy error. An engine error in the middle of a
  body never triggers the save call. A report from another instance fails the answer and teaches the proxy the new
  instance. A mutex guards `known`, and a stale value can only fail an answer. No transport or server timeout cuts a
  long save (`httpx.NodeTransport`; net/http clears the read deadline once the body is read). A 200 kB answer is held
  until its save call answers (overlay test).
- **Contracts:** the 200 and 503 shapes of `POST /save`, `EngineTarget.SaveURL`, the `saveURL` parameter of
  `Manager.Ensure`, `LakeSaveInterval` and `WithLakeSaveInterval` match the ADR.
- **Scenarios:** `acknowledged-commit-survives-crash`, `waiting-commits-share-a-save` and
  `read-waits-for-nothing-saved` are real-engine tests in `engine_test.py`; the first two carry the oracle markers of
  `AckedCommitsAreSaved` and `NoAnswerStaysHeld`. `refused-save-is-an-error` is `TestScenarioRefusedSaveIsAnError`,
  and the lane's `issue1233` case shows `CatalogSaved` False. `interval-is-the-opt-in` is
  `TestScenarioIntervalIsTheOptIn`. The two Quack facts have their tests. With a `/save` that never saves, the crash
  test and `test_answer_ends_after_its_commit_is_visible` both fail (`Table with name t does not exist` after the
  SIGKILL).
- **Mutants killed:** in Go, a release that ignores the instance, a request forwarded before the instance is learned,
  and a save call whose status is ignored. In the shim, all eight that were tried: the id read after the copy, no
  re-check under the lock, a refused save recorded as saved, no failure shared with the calls behind it, no wait for
  the calls at the stop, a `/save` that never saves, a stop that does not record its snapshot id, and a start-up save
  that is tried only once.
- **Tracking:** the ADR's substance is unchanged since its acceptance (only the status line moved); F192 is at
  `reviewing`; ADR-0274 and ADR-0086 carry their back-links.

### Checks
- `go build ./...` on the host and with `GOOS=linux`: exit 0. `go vet` on the touched packages, for both systems:
  exit 0.
- `golangci-lint run` on `internal/lake/...`, `internal/services/lake/...`, `internal/platform/config/...`,
  `pkg/funcd/...` and `cmd/funcd/...`: 0 issues.
- `go test -race -count=1` on `./internal/lake/... ./internal/services/lake/... ./internal/platform/config/...
  ./internal/runtime/embedimg/... ./cmd/funcdctl/`: ok.
- `just check-specs`: 8 specs, 0 failing. `commit-save.fizz` passes at 7,277 states, each of its six mutants fails
  what it names, and `commit-save.md` is current.
- `shim_test.py`: 40 tests, OK.
- `engine_test.py` in `funcd/runtime-duckdb`, the four new classes: 5 tests, OK, 17.4 s.
- A probe through a logging proxy: one `quack_query` makes three requests (the handshake, the statement and the
  close), and Quack frames each answer with Content-Length (83, 97 and 19 bytes for an INSERT; one 157,822-byte
  answer for a 2,000,000-row SELECT).

### Definition of Done
7 of 8 items hold: the five Review-checklist items, the tree, the tracking, and no change outside the ADR's scope.
Miss: the scope, through Blocker 1 (`model`). The ADR's "Done when" runs (`just ci-full`, `just test-linux`, the
duckdb lane and experiment 0010's failure phase) are the builder's and did not run here.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0279 (implementation) → changes-requested, 1/0/6, 7 model-attributed, DoD 7/8. See
`docs/reviews/model-scorecard.md`.

### Recommendation
Changes requested. Keep the `funcdctl dev` Lakes answering without a barrier, and test it (Blocker 1); the Minors are
small test and comment fixes. ADR-0279 stays `Reviewing` and F192 stays `reviewing`; the ADR needs no change. The PR
that implements the ADR closes #1255.
