## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0235 implementation, round 2, model: claude-opus-5-5)

Scope: the rework of the uncommitted `feat/sql-provider` tree after
[round 1](adr-0235-implementation-claude-opus-5-5.md) (changes-requested, 0/1/2). The rework adds the boot gate
(`Feed.Loaded`, `Platform.openSQLFeed`, `Fanout.Subscribed`, `sqlFeedBootWait`), publishes changes while they are decoded
(`changes()` with an emit callback, the rows of unwatched tables skipped), and adds a start-up warning when engine
containers run without `catalog.proxyHost`.

**Stamped.** ADR-0235 is `Implemented` and the FEAT-0011 F132 row is `implemented`.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...`; `GOOS=linux go build ./...` | exit 0 × 2 |
| hygiene | `just check-hygiene` | `hygiene: clean` |
| touched packages, race | `go test -race -count=1` on `internal/services/sql/...`, `internal/eventing/...`, `pkg/funcd` (e2e tagged out) | all `ok`, exit 0 |
| vet, lint, gofmt | `go vet`, `go tool golangci-lint run`, `gofmt -l` on the same packages | exit 0, `0 issues.`, empty |
| scenario and feed tests | the 9 `TestScenario…` tests of `sql_changes_internal_test.go` and the 10 `TestFeed…` tests, `-v` | all PASS, each at or under 0.21 s |
| boot window | the round-1 scratch test through `go test -overlay` (the tree is unchanged): 2001 Sensors seeded, `Run` started, a valid delivery posted to the real listener until it answers 200 | 503 until the gate opened; the first 200 after about 160 ms, with all 2001 subscriptions in place (3 runs). Round 1 got 200 after 3 ms with none |
| torn group | a scratch test through `go test -overlay`: a group of two watched changes whose body ends after the first change, then the whole body again | 400, 1 event published; the redelivery publishes change 0 again with the same id, then change 1 (Minor 1) |
| real-engine lane | not rerun: the Sensor is applied in S8 after the lane's last restart, so the gate opens at once at each boot, and the round-1 run passed S1 to S8 | — |

### 🔴 Blocker
None.

### 🟡 Major
None. Round 1's Major is fixed (below).

### Minor

1. **A body torn in the middle of a group repeats that group's earlier changes** (`model`). Changes are now published
   as they are decoded (`changes()` → `emit` → `publish`), and the watermark is raised only at the end of the group.
   A body that ends or fails to decode after change *k* answers 400 or 503 with changes 0 to *k* already published.
   rqlite resends the whole group, so those changes fire again. The scratch test shows `…/1/any/0` published twice.
   Decision 3 says only a daemon crash between publish and raise can repeat a group. The ids are the same on the
   repeat, so a function's dedupe still works, and the dead-letter queue keeps Sensor publishes fast, so a cut
   connection is rare. This is the cost of fixing round-1 Minor 1. Fix, if wanted: record the last published change
   number with the index (`index`, `n`) and skip changes at or below it on a redelivery.
2. **Under containerd without `catalog.proxyHost`, the feed is still on loopback** (`adr`, carried from round 1). The
   new warning at `pkg/funcd/funcd.go:1148-1151` names the setting, which is the right mitigation while the ADR is
   frozen. The bridge address to use without `proxyHost` still needs a superseding note.

### ✅ Verified correct (keep)

- **Round-1 Major 1 is fixed.** Until `Loaded`, every valid token gets 503 and an unknown token still gets 404
  (`changefeed.go:140-154`). `openSQLFeed` lists the Sensors present at the start and waits until each
  `(ns, source, event)` they name has a Fanout subscriber (`Fanout.Subscribed`, under the read lock). With no Sensors
  it opens at once. After 30 s it opens anyway and warns, so a Sensor that never subscribes cannot stop every
  database's changes. Sensors subscribe while the platform is held (only delivery is refused), so a hold does not use
  up the 30 s. `TestScenarioChangesWaitForSensorsAtBoot` asserts the 503 before the subscription, the gate opening,
  and one firing on the redelivery. `TestFeedBootGate` covers the unit.
- **Round-1 Minor 1 is fixed.** A group's changes are no longer collected. The rows of a table no event watches are
  skipped token by token, which relies on rqlite encoding `op` and `table` before `before` and `after`, as the captured
  payload shows. `n` still counts every change of the group (`TestFeedSkipsUnwatchedTables`: a 1 MiB unwatched row,
  then the watched change with id `…/1/any/1`). A group already at or below the watermark, or with no index, is skipped
  unread.
- Everything round 1 verified still holds: the token, the epoch and CDC config, the epoch-keyed watermark under the
  per-database lock with its snapshot and delete, the holds, the drain of unwatched databases, truncation, the
  CloudEvent shape, and no token or endpoint in a log.
- The ADR's only edits are the status bumps.

### Definition of Done

12 / 12 items hold (the ADR's 6 Review-checklist items and 6 Definition-of-done items). The boot gate now answers 503
to every valid token until the Sensors present at the start are subscribed. The repo-wide gate runs once per PR.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0235 (implementation, round 2) → pass, 0/0/2, 1 model-attributed, DoD 12/12. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Stamped `Implemented`. Minor 1 can go into a follow-up fix (a change number kept beside the index). Minor 2 waits for
a superseding note on the bridge address.
