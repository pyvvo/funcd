## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0260 implementation, model: claude-opus-5-5)

This report reviews branch `feat/adr-0260-loopback-ports` at 4e0db9c3, with four commits on main f7e40c89:

- 99ffa89f docs: ADR-0260 Accepted, the ADR-0235 back-link and the F132 row;
- aa4242f4 fix(sql): the change feed binds `127.0.0.1:0` in `New` on loopback with the port unset, `Run` serves it,
  `Shutdown` closes it, and `funcdctl dev` drops its feed reservation;
- a2a288c8 fix(dev): the catalog engine's start holds its port until the engine's `quack_serve` row, and the SQL
  engine is Ready only on its own `[http] … service listening on <addr>` line and is started again only on
  `address already in use`;
- 4e0db9c3 docs: ADR-0260 and the F132 row move to Reviewing.

The PR that carries this branch implements ADR-0260, and #1154 is the issue it ends. Its description names #1154
with the closing keyword, as the ADR's Implementation plan says.

### Minor
- **A hold left open makes the catalog hold test hang instead of fail** · attribution: model · evidence:
  `internal/catalog/devengine/devengine_test.go:131` calls `held.Accept()` to prove that the start closed its held
  listener.
  - Accept returns `net.ErrClosed` at once only when the listener is closed. If the listener stays open, Accept blocks.
  - Mutant: delete the `held.Close()` defer at `internal/catalog/devengine/devengine.go:279`. The test does not fail.
    It blocks at `:131` until `go test -timeout 30s` panics ("test timed out after 30s", 30.3 s). Under the default
    timeout, the same regression would hold a CI job for 10 minutes before it fails.
  - Fix: set a deadline in the past on the listener before `Accept`, for example
    `held.(*net.TCPListener).SetDeadline(time.Now())`. A closed listener still returns `net.ErrClosed`, and an open one
    returns a timeout at once.
- **The two dev-engine drivers carry the same line buffering** · attribution: model · evidence: `prefixLogWriter` in
  `internal/catalog/devengine/devengine.go:458-484` and `engineOutput` in `internal/sql/devengine/devengine.go:472-506`.
  - Both now have the same `partial` carry in `Write`, the same `flush` and the same three-line `last` ring.
  - The change adds that machinery to both drivers. Before it, the two drivers only had similar one-loop debug
    loggers.
  - Fix, optional: keep one line splitter that both drivers use, or accept the copy. The gate's bloat audit reports
    whether the copy crosses the dupl threshold.

### ✅ Verified correct (keep it)
- **The feed (Decision 1, Contracts).**
  - `Feed.Listen` (`internal/services/sql/changefeed.go:118`) binds once and allows port 0.
  - `Feed.ServeListener` (`:151`) treats `http.ErrServerClosed` and `net.ErrClosed` as a clean stop.
  - `Serve` (`:129`) keeps its retry each second for the bridge address and for a set port, and it is built on the two
    new functions. The signatures match the Contracts.
- **The platform (Decision 1, Contracts).**
  - `pkg/funcd/funcd.go:1174-1181` binds `127.0.0.1:0` in `New` only with no bridge address and an unset port, and
    the engines' URL takes the bound port. A set port is used as set. A bridge address keeps 7075.
  - Containerd without a bridge address binds port 0 too, and the warning at `:1168` stays.
  - `Run` serves the held listener (`:1687-1688`). `Shutdown` closes it unconditionally (`:1847-1848`).
  - A failed `New` runs `Shutdown` through its deferred cleanup, so a bind made before a later failure is released.
  - `sqlChangeFeedPortOrDefault` is gone. The four comments the plan names (`funcd.go:306`, `options.go:64`,
    `config.go:396-397`, `examples/funcdconfig.yaml:247`) now give the default for each case.
  - `cmd/funcd/main.go:1039` still names `cmp.Or(port, 7075)` in the allow list, and only with a bridge address, which
    agrees with Decision 1.
- **funcdctl dev (Decision 2).** The feed's `freeLocalAddr` reservation and its `WithSQLChangeFeedPort` call are
  removed. `cmd/funcdctl` never sets a bridge address, so dev gets the loopback bind. No reserve-then-release remains
  for the feed.
- **The catalog engine (Decision 3).**
  - `holdLoopbackPort` and `listenReusePort` (`internal/catalog/devengine/devengine.go:341-355`) bind with SO_REUSEPORT
    through `net.ListenConfig.Control` and `golang.org/x/sys/unix`, which `go.mod` already required.
    `go mod tidy -diff` is clean.
  - The deferred close at `:279` releases the hold on every return path of `launch`, which runs before `Converge`
    reports Ready.
  - `waitServing` (`:359`) does not dial the port. It waits for the row on stdout, for the engine's end (with its last
    error lines, colour codes removed), or for the 20 s timeout.
  - The `holdPort` field is the port-picker seam that the Contracts ask for; the SQL driver's is `pickPort`.
- **The SQL engine (Decision 3).**
  - `waitReady` (`internal/sql/devengine/devengine.go:399-403`) waits for an `[http] … service listening on <its HTTP
    address>` line and then polls `/readyz`.
  - `ended` (`:434`) returns `errPortTaken` only when the output said `address already in use`.
  - `launch` (`:270`) starts the engine again only on `errPortTaken`, three starts in all, and logs each retry at Warn.
  - The Contract's `lineLogger` is now `engineOutput`. The rename matches its larger job, which is to report both lines
    to the start.
  - `taken` and `last` are read only after `done`, which closes after `cmd.Wait` and `flush`. Both standard streams
    share one writer, so os/exec writes from one goroutine at a time.
- **Scenarios.** Each scenario has one test named after it, and each passes without a skip:
  - `TestScenarioPlatformsOwnFeeds`;
  - `TestScenarioSetPortKept`;
  - `TestScenarioBridgeKeeps7075`;
  - `TestScenarioCatalogEngineHoldsItsPort` (three subtests);
  - `TestScenarioSQLEnginePortTaken` (three subtests);
  - `TestIssue1154_PlatformsOnLoopbackDoNotShareTheFeedPort`.

  `TestShutdownReleasesTheLoopbackFeedPort`, `TestEngineIsReadyOnlyOnItsOwnListenLine` and
  `TestFeedServesTheListenerItBound` cover the rest of the Contracts. The pkg/funcd tests call `t.Parallel()` and
  take 0.3 to 0.6 s each. The forking dev-engine tests stay serial.
- **Revert and mutation checks** (`go test -overlay`; no file in the worktree changed). Each of these fails the test
  named after it:
  - main's `pkg/funcd/funcd.go` fails `TestIssue1154_…`: "todo-b's engine reaches the feed that knows its token",
    condition never satisfied after 5 s;
  - a catalog start that counts a TCP accept as ready (main's rule) fails all three subtests of
    `TestScenarioCatalogEngineHoldsItsPort`, including "Ready=true err=<nil>, want a failed start";
  - an SQL start that counts `/readyz` alone as ready fails `a_taken_port_starts_it_again` ("the server on the taken
    port does not count"), `three_starts_in_all` and `TestEngineIsReadyOnlyOnItsOwnListenLine`;
  - an SQL start that is retried on any end fails `another_end_fails_at_once` ("one start").
- **The real-engine guard in the plan.** The plan asks for "a new `-tags dev` test" that runs the bundled catalog
  engine and skips on the placeholder. The implementer extended `TestConvergeLaunchesEngineWhenBundled`
  (`internal/catalog/devengine/devengine_test.go:197-219`) instead. That test already runs the bundled engine through
  `Converge` and skips on the placeholder. `Converge` is now Ready only on the `quack_serve` row, so the test fails if
  an engine upgrade changes or drops the row. The added `requireTaken` also checks that the engine still holds its
  port after the start's hold closes. This satisfies the plan, and a second test would only repeat the same launch.
  rqlited's line is held in the same way by the real-engine tests in `cmd/funcdctl/dev_sql_test.go`, whose start now
  needs that line. The implementer's runs with the real rqlited v10.5.3 and the real DuckDB bundle, which belong in
  the PR description, were not repeated here.
- **Docs.**
  - ADR-0260's text is unchanged from 99ffa89f to 4e0db9c3 except its Status line.
  - ADR-0235 gains only its "Superseded in part by" back-link.
  - The F132 row reads `change feed: implemented · loopback port: reviewing` before this review's stamp.
- **Checks on 4e0db9c3.**
  - `go test -race -count=1` passes:
    - without tags: `./internal/services/sql/` (3.0 s), `./internal/platform/config/` (2.2 s) and `./pkg/funcd/`
      (20.8 s);
    - with `-tags dev`: `./internal/sql/devengine/` (9.5 s), `./internal/catalog/devengine/` (2.7 s) and
      `./cmd/funcdctl/` (173.8 s).
  - In the `funcd-linux-tests` VM (linux/arm64, non-root), the two dev-engine packages' scenario and Converge tests
    pass.
  - `go vet` exits 0, on darwin and linux, with and without `-tags dev`. `go vet -tags e2e ./pkg/funcd/` also exits
    0, which compiles the e2e-tagged tests without running them.
  - `golangci-lint` reports 0 issues on the touched packages, on darwin and linux, with and without
    `--build-tags dev`.
  - The tree is clean.

### Definition of Done
6 / 6: the 5 Review-checklist items plus Done-when. For Done-when, this review ran the build, vet, lint and test
checks on the touched packages, with and without `-tags dev`. `scripts/agent/gate.sh` runs once after this review, as
this run planned, and CI runs again on the PR.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0260 (implementation) → pass, 0/0/2, 2 model-attributed, DoD 6/6. See
docs/reviews/model-scorecard.md.

### Recommendation
Pass. The two Minors do not block the stamp. The hanging assertion is a one-line fix that the builder can make in this
PR before the gate runs.

### Follow-up
Before the stamp, the builder fixed the first Minor: the hold test now sets a deadline in the past before `Accept`.
With the `held.Close()` defer removed, the test now fails in 0.01 s ("the held port is still open after Ready")
instead of blocking. The second Minor stays: the bloat audit reports no cross-package clone.
