# ADR-0211 implementation review: claude-opus-5-5

## Verdict: changes requested, 0 blockers, 1 major, 2 minors (ADR-0211 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0211`, head `adc4c249`, on top of `2a03fc7f` (the #873 guard fix). The branch holds 7 commits:
the `api/cron` package, the `TimerEvent` fields, the eventing timer loop, the `time/tzdata` imports, a test cleanup, a
refactor of the field refusals, and the status move to `Reviewing`. The diff is 17 files, +1245/−36, of which 7
non-test Go files carry +376/−22. `go.mod` and `go.sum` are unchanged. The tree is clean after `just ci`, whose
`generate` step rewrote the OpenAPI spec with no diff.

The work is close. The parser, `Next` and the timer loop do what the ADR says, and every Scenario has a passing test.
One input class makes `cron.Parse` panic instead of returning `fault.Invalid`, and that must be fixed before sign-off.

### Verification run

All commands ran in the review worktree through `scripts/agent/d`.

| Check | Result |
|---|---|
| `just ci` (tidy, generate, check-hygiene, fmt, golangci-lint plain and `dev`, `go test ./...` plain and `dev`, build, `go mod verify`) | `EXIT=0` (33 s); `git status` clean afterwards |
| `go test -race -count=1` on `api/cron`, `api/types/v1alpha1`, `internal/eventing`, `cmd/funcdctl`, `pkg/sdk` | all `ok`, uncached |
| `go test -count=1 -v -run TestScenarioCron` on `internal/eventing`, `api/types/v1alpha1`, `cmd/funcdctl` | 8/8 `--- PASS`, 0 skipped |
| `go vet -tags e2e ./pkg/funcd/... ./internal/eventing/...` | exit 0 (the e2e files compile against the new `registerTimer` signature) |
| `go list -deps ./api/cron` | only standard-library packages, `api/fault` and `api/cron`; no `time/tzdata` |
| `go list -deps ./cmd/funcd` and `./cmd/funcdctl` | both include `time/tzdata` |
| `TestNextMatchesOracle` with `-rapid.checks=3000`, plus the same property through `go test -overlay` in 8 more zones (Lord Howe's 30-minute shift, Troll's 2-hour shift, midnight changes in Santiago, Havana, São Paulo and Gaza, Apia's skipped day, Dublin) | `ok`: `Next` equals the brute-force oracle in every zone |
| A probe test through `go test -overlay` with a step of `9223372036854775807` | `cron.Parse` panics: `runtime error: negative shift amount` (Major 1) |
| Ten mutants through `go test -overlay` (listed below) | 7 killed, 2 equivalent, 1 survived (Minor 1) |
| `git diff 2a03fc7f..HEAD -- docs/adr/` | one line in `docs/adr/0211-cron-schedules.md`: `Accepted (2026-10-10)` → `Reviewing (2026-10-10; accepted 2026-10-10)` |
| `docs/feat/0010-feat-apps.md` | the F124 row moved `accepted` → `reviewing`, nothing else changed |
| `git tag --contains 2a03fc7f` | no tag; the manifest reads `0.8.0`, and `git describe` gives `v0.8.0-11-g2a03fc7f` (Minor 2) |

Mutants:

- `instant` replaced by `time.Date` in the zone: `TestNextDaylightSaving` and `TestNextMatchesOracle` fail.
- The `*` prefix rule replaced by "either day field restricted": `TestParseAccepts`, `TestParseRefuses`,
  `TestDayFieldRule` and `TestNextMatchesOracle` fail.
- The no-date refusal removed: `TestParseRefuses` fails.
- The unchanged check without the zone (`internal/eventing/eventing.go:234`):
  `TestScenarioCronChangeReseeds/a_changed_zone_reseeds_from_now` fails.
- A new cron entry seeded with `Next(created)` instead of `Next(s.clock.Now())` (`:242`):
  `TestScenarioCronUTCDefault`, `TestScenarioCronInZone` and `TestScenarioCronDSTGap` fail.
- A parse error that drops the previous entry: `TestCronParseErrorKeepsPreviousEntry` fails.
- Equivalent: the interval unchanged check without `e.table == nil`, and the cron check without `e.table != nil`. A
  cron entry's interval is always 0 and an interval entry's cron is always empty, so the other comparison already
  decides. This is not a finding.
- Survived: `dueTimers` advancing with `Next(e.next)` instead of `Next(now)` (`:395`). Every test passes (Minor 1).

### 🔴 Blockers

None.

### 🟡 Major 1: `cron.Parse` panics on a step that overflows `int` · attribution: model

`parseField` (`api/cron/cron.go:155-165`) accepts any step `s ≥ 1` that `strconv.Atoi` can read, then runs
`for v := lo; v <= hi; v += step { set |= 1 << v }`. When `lo ≥ 1` and the step is close to `math.MaxInt64`,
`v += step` wraps to a negative number, the loop goes on, and `1 << v` panics with
`runtime error: negative shift amount`.

Evidence, from probe tests added through `go test -overlay` (nothing in the tree changed):

- `cron.Parse` panics on `0 0 */9223372036854775807 * *`, `1-5/9223372036854775807 * * * *` and
  `0 0 * * 1-7/9223372036854775807`.
- `funcdctl apply -f` of an EventSource with the first expression panics, instead of returning `fault.Invalid`.
- A raw `POST` of the same EventSource panics in the control-plane handler, through `EventSource.Validate`,
  `TimerEvent.validate` and `CheckCron`. net/http recovers the panic and drops the connection, so funcd stays up and
  nothing is stored, but the client gets no `400 urn:funcd:problem:invalid`.

This breaks the `Parse` contract ("fault.Invalid naming expr, the failing field and Grammar") and the
`cron-invalid-refused` rule that a bad schedule gets a 400. Every program that validates with `pkg/sdk` has the same
crash. The rapid generator draws steps only up to the field's span (`api/cron/cron_test.go:198`), so it cannot find
this case.

Fix (builder): bound the loop so that `v` never overflows, for example by stopping when `step > hi-v`. Decision 1
allows any step `s ≥ 1`, so such an expression should be accepted and match only `lo`, not refused. Add the three
expressions to `TestParseAccepts`, and let the generator also draw a step near `math.MaxInt64`.

### Minor 1: the skip-not-burst rule of `dueTimers` has no test · attribution: model

Decision 6 says that `dueTimers` sets `next = Next(now)`, "so slots passed during a slow publish are skipped, not
burst". The Risks section says the same for a forward clock jump: one missed slot fires and the rest are skipped. The
code does this (`internal/eventing/eventing.go:395`), but a mutant that advances with `Next(e.next)` passes every
test, so a later change can make a cron event fire a burst. The interval path has the matching test
(`TestIssue114_TimerFiresOncePerInterval`).

Fix (builder): add a test that jumps the manual clock over several slots of a `*/15 * * * *` event between two
ticks and expects one fire, then the next slot after the jump.

### Minor 2: the guard fix is not in a release yet · attribution: env (release sequencing)

The last Review-checklist item says the guard fix (#873, `2a03fc7f`) must be in a release older than the one that
ships F124. The fix is on `main`, but no tag contains it: the latest release is 0.8.0. The implementation cannot
satisfy this item. A release that contains #873 must be published before the F124 PR merges.

### ✅ Verified correct (keep it)

- **Grammar** (`api/cron/cron.go:78-120`): exactly 5 fields split on spaces and tabs, the 7 macros, and every
  refusal of Decision 1 (`n/s`, names, `?`, `L`, `W`, `#`, `@every`, other macros, `CRON_TZ=`, 6 or 7 fields, a
  sign, an empty item). `TestParseRefuses` checks 37 refusals, and each message quotes the expression and `Grammar`.
  Day of week 7 maps to 0.
- **Day-match mode**: a day field whose text starts with `*` is unrestricted (`:113-115`). The OR mode applies only
  when both day fields are restricted, and the no-date refusal applies only in the AND mode (`:116`). `hasDate`
  counts 29 February. `TestDayFieldRule` pins the three cases of the Implementation plan and a `*/7` AND case.
- **DST rule**: `instant` (`:252-266`) walks the zone's periods with `ZoneBounds` from 30 hours earlier and never
  uses `time.Date` to choose an instant in a gap or a fold. A gap slot maps to the end of the gap and a fold slot to
  its first occurrence. The comment on `Next` gives the proof that skipping the slots up to the local minute of
  `after` is safe: the map from slot to instant never decreases. The gap and fold tables for New York and Paris pass,
  and the oracle matched in 11 zones.
- **Purity**: `Next` reads no clock, keeps no state, and `Timetable` is immutable. `maxScanDays` bounds the walk
  by one 400-year Gregorian cycle.
- **Zone**: `LoadZone` maps `""` to UTC, refuses `Local` and unknown names, and stores the name as written.
  `api/cron` does not import `time/tzdata`. `cmd/funcd`, `cmd/funcdctl` and the three zone test files do.
- **Types**: the tags are `interval,omitempty`, `cron,omitempty` and `timeZone,omitempty`. `TimerEvent.validate`
  (`api/types/v1alpha1/eventsource.go:137`) treats an interval of `0s` as unset, uses the two messages of the
  Contracts word for word, refuses `timeZone` without `cron`, and keeps the interval bounds. `CheckCron` is the only
  admission call into `api/cron`. The other caller, `internal/eventing`, parses an event that was already admitted.
- **OpenAPI**: `TimerEvent.required` is `[name]`, the App section uses the same `$ref`, and the schema has no cron
  pattern.
- **Stored bytes**: `TestScenarioCronStoredUnchanged` decodes and encodes an interval-only EventSource and an App
  spec and gets the same bytes.
- **Timer loop**: the unchanged check compares the kind, the expression and the zone (`:234`). A new or changed cron
  entry is seeded with `Next(s.clock.Now())` (`:242`). A parse error keeps the previous entry, lets the other events
  register and prune, and is returned by `Reconcile` (`:150`, `:261`). `registerTimer` and `dueTimers` make no store
  or KV call.
- **Scenarios**: all 8 have named, passing tests. `cron-invalid-refused` checks `funcdctl apply` offline (with no
  client) and a raw POST and PUT, and checks that nothing is stored. The eventing scenarios run the real
  `Reconcile`, `dueTimers` and `Fire` on `clock.NewManual`, with a new `Source` for each daemon life.
- **Guard test**: `TestIssue872_ZeroIntervalEventSkipped` now stores an event with an unknown key, because a cron
  event registers since this ADR. The guard path is still tested. The commit message explains the change.
- **Scope**: the plan's files and nothing else. `pkg/sdk/manifest_test.go` now uses `jitter` as the unknown key, the
  deferral in `internal/eventing/cloudevent.go` is dropped, and the binary size change is recorded in the body of
  `924b836c` for the PR. `BackupSchedule` and catch-up were not added.
- **ADR**: only the status line changed.

### Definition of Done

17/19 items hold: 10 Review-checklist items and 9 applicable generic items. The generic item "contract suites against
every driver" does not apply, because the ADR adds no port. Misses:

- Contracts honoured: `Parse` panics instead of returning `fault.Invalid` (Major 1, model).
- The guard fix in an earlier release (Minor 2, env).

`just ci-full` (Implementation plan step 5) was not run here. Per the project rule, it runs once in the PR gate. The
e2e files compile (`go vet -tags e2e`).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0211 (implementation) → changes-requested, 0/1/2, 2 model-attributed, DoD 17/19.
See `docs/reviews/model-scorecard.md`.

### Recommendation

Return to `adr-impl` for Major 1 and Minor 1. Both are small changes in `api/cron/cron.go` and the tests. The ADR
stays `Reviewing`. Before the F124 PR merges, publish a release that contains #873 (Minor 2). Also carry the release
note of Implementation plan step 4 into the PR, because no commit holds it yet: a funcd older than the guard release
panics on a cron event, so a downgrade first deletes every cron event or changes it to an interval.
