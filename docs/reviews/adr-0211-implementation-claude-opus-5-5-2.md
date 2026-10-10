# ADR-0211 implementation review 2: claude-opus-5-5

## Verdict: pass, 0 blockers, 0 majors, 1 minor (ADR-0211 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0211`, head `1412a9c2`, on top of `2a03fc7f` (the #873 guard fix). This is the second review.
The first review ([adr-0211-implementation-claude-opus-5-5.md](adr-0211-implementation-claude-opus-5-5.md), at
`adc4c249`) requested changes for one Major and two Minors. The rework commit `1412a9c2` (3 files, +41/−23) bounds the
step loop of `parseField`, adds the three overflowing expressions to `TestParseAccepts`, lets the rapid generator draw
steps near `math.MaxInt`, and adds `TestCronSkipsMissedSlots`. All three earlier findings are closed. The one new
finding comes from the ADR's zone decision, not from the work, so it does not count against the model.

### Verification run

All commands ran in the review worktree through `scripts/agent/d`. Probes and mutants ran through `go test -overlay`
or in a scratch copy; the tree was not changed.

| Check | Result |
|---|---|
| `just ci` (tidy, generate, check-hygiene, fmt, golangci-lint plain and `dev`, `go test ./...` plain and `dev`, build, `go mod verify`) | `EXIT=0`; both lint runs `0 issues.`; `hygiene: clean`; `git status` clean afterwards, so the OpenAPI spec regenerates with no diff |
| `go test -race -count=1` on `api/cron`, `api/types/v1alpha1`, `internal/eventing`, `cmd/funcdctl`, `pkg/sdk` | all `ok` |
| `go test -count=1 -v` of the scenario tests, `TestCronSkipsMissedSlots`, `TestCronParseErrorKeepsPreviousEntry` and `TestIssue872_ZeroIntervalEventSkipped` | 8/8 `TestScenarioCron…` and the 3 others `--- PASS`, 0 skipped |
| `go vet -tags e2e ./pkg/funcd/... ./internal/eventing/...` | exit 0 |
| `go list -deps` of `./api/cron`, non-standard packages only | `api/fault` and `api/cron`; `time/tzdata` is absent |
| `go list -deps` of `./cmd/funcd` and `./cmd/funcdctl` | both include `time/tzdata`, imported by `cmd/funcd`, `cmd/funcdctl` and `fortio.org/duration` |
| `git diff 2a03fc7f..HEAD -- go.mod go.sum` | empty |
| Probe test in `cmd/funcdctl` | a raw `POST` of `0 0 */9223372036854775807 * *` gets 200; a `PUT` of `0 0 */99999999999999999999 * *` gets 400 `urn:funcd:problem:invalid`; `funcdctl apply` of `1-5/9223372036854775807 * * * *` is stored. Nothing panics |
| Four mutants (listed below) | 4 killed |
| `git tag --contains 2a03fc7f` | `v0.9.0` |
| The branch merged onto `origin/main` (`39e8d229`) with `git merge-tree`, then built from a scratch copy | the code merges with no conflict; `go build ./...`, specgen (same bytes as the merged spec), `go vet` and the tests of the touched packages and `internal/app` pass. Only `docs/reviews/model-ledger.json` and `model-scorecard.md` conflict, as append-only files do |
| `cron.LoadZone` on 10 names, on this host and in two Linux containers | Minor 1 |
| `git diff 2a03fc7f..HEAD` and `git diff origin/main HEAD` of `docs/adr/0211-cron-schedules.md` | one line: `Accepted (2026-10-10)` → `Reviewing (2026-10-10; accepted 2026-10-10)` |
| `docs/feat/0010-feat-apps.md` | the F124 row moved `accepted` → `reviewing`; nothing else changed |

Mutants:

- `dueTimers` advancing with `Next(e.next)`, the mutant that survived review 1: `TestCronSkipsMissedSlots` fails.
- `dueTimers` firing only when `now.After(e.next)`: 6 tests fail, among them every eventing scenario that fires.
- The old loop `for v := lo; v <= hi; v += step`: `TestParseAccepts` panics with `negative shift amount`.
  `TestNextMatchesOracle` alone finds the panic after 4 cases, so the generator now reaches this input class.
- The loop bound off by one (`step >= hi-v`): `TestParseAccepts` and `TestNextMatchesOracle` fail.

### Earlier findings

- **Major 1 (model), closed.** `parseField` (`api/cron/cron.go:163-168`) sets the bit of `v` and stops when
  `step > hi-v`. Because `v ≤ hi`, the next value is at most `hi`, so `v += step` cannot overflow. Every step `s ≥ 1`
  is accepted, as Decision 1 says, and a step that does not fit in `int` is still refused by `strconv.Atoi` (the
  probe above).
- **Minor 1 (model), closed.** `TestCronSkipsMissedSlots` (`internal/eventing/cron_test.go:195-202`) jumps the clock
  over four `*/15` slots and expects one fire, then the next slot. It kills the `Next(e.next)` mutant.
- **Minor 2 (env), closed.** Release `v0.9.0` contains #873. F124 is not merged, so it ships in a later release.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor 1: whether a zone name is valid depends on the host · attribution: adr

Decision 2 loads `timeZone` with `time.LoadLocation` (`api/cron/cron.go:69`), which reads the host's zoneinfo first and
the embedded copy last. The host lookup is a file lookup, so it inherits the file system's rules: case-insensitive on
macOS, `//` collapsed, and files such as `posixrules` that are not in the embedded database. A probe test calling
`cron.LoadZone`, run on this macOS host and, cross-compiled, in two Linux containers as a non-root user, gave:

| Name | macOS, host zoneinfo | Linux, host zoneinfo (Debian bookworm) | Linux, no zoneinfo (Alpine, embedded data only) |
|---|---|---|---|
| `europe/paris`, `EUROPE/PARIS`, `utc` | accepted | refused | refused |
| `Europe//Paris`, `posixrules` | accepted | accepted | refused |
| `Europe/Paris`, `US/Eastern`, `Etc/GMT+5`, `EST5EDT`, `Factory`, `UTC` | accepted | accepted | accepted |

So the admission answer depends on the host. A funcd that accepted such a name, for example a development funcd on
macOS or a Linux host with zoneinfo, stores it as written. If that metastore is restored or moved to a static image
without zoneinfo, the case the ADR embeds `time/tzdata` for, `registerTimer` fails for that event on every reconcile:
the event stays idle and `Reconcile` returns the error at each retry (`internal/eventing/eventing.go:150`, `:261`).
Decision 6 calls this path unreachable because admission refuses a bad zone; the probe shows that it is reachable
when the data moves between hosts. Nothing crashes and nothing is lost: the other events still fire, and changing
the name fixes it. In the other direction, `funcdctl apply` on macOS accepts offline a name that a Linux funcd refuses
with 400.

The implementation follows Decision 2 as written, so the model is not at fault. The Consequences accept that two
hosts with different zoneinfo versions can compute different instants, but not that a name valid on one host is
invalid on another. Owner: a follow-up issue or a superseding ADR that makes zone names host-independent. Possible
options, not decided here: accept only names that the embedded database knows, with the same spelling, or compare
the loaded name with a canonical list.

### Notes (not findings)

- **Binary sizes.** The ADR's Consequences expect both binaries to grow by about 450 KB. Commit `924b836c` records
  funcdctl +395,624 bytes and funcd +64 bytes, because `fortio.org/duration` already imports `time/tzdata` in funcd
  (confirmed with `go list -deps` above). The estimate for funcd is too high; nothing depends on it.
- **Ledger conflict.** The ledger rows of this branch conflict with `main`, as the project's notes on append-only
  files expect. The integrator resolves them, or moves the rows to the batch's ledger PR.
- **Release note.** Implementation plan step 4 asks for a release note: a funcd older than the guard release panics
  on a cron event, so a downgrade first deletes every cron event or changes it to an interval. No commit holds it,
  and it belongs in the F124 PR. The size change of step 4 is in the body of `924b836c`.

### ✅ Verified correct (keep it)

- **Rework.** The loop bound is the smallest fix: one condition moved, no new refusal, and the oracle's `span` in the
  test now counts with `v++` so the oracle itself cannot overflow. The new test is named after the rule it pins.
- **Grammar and day rule** (`api/cron/cron.go:78-120`): 5 fields on spaces and tabs, the 7 macros, the refusals of
  Decision 1 (`TestParseRefuses`, 37 cases, each quoting the expression and `Grammar`), the `*` prefix rule for the
  day fields and the no-date refusal only in the AND mode (`TestDayFieldRule`).
- **DST rule**: `instant` (`:255-269`) maps each slot with `ZoneBounds` and never relies on `time.Date` in the zone.
  The gap and fold tables for New York and Paris pass, and `TestNextMatchesOracle` checks `Next` against a
  minute-by-minute oracle.
- **Types** (`api/types/v1alpha1/eventsource.go:137`, `cron.go:10`): the three `omitempty` tags, the two Contracts
  messages word for word, `0s` counting as unset, `timeZone` refused without `cron`. `CheckCron` is the only admission
  call into `api/cron`; `internal/eventing` parses events that were already admitted. The OpenAPI `TimerEvent.required`
  is `[name]` and has no cron pattern.
- **Timer loop** (`internal/eventing/eventing.go`): the unchanged check compares kind, expression and zone (`:234`);
  a new entry is seeded with `Next(s.clock.Now())` (`:242`); `dueTimers` advances with `Next(now)` (`:395`); a parse
  error keeps the previous entry and is returned (`:261`). `registerTimer` and `dueTimers` make no store or KV call.
- **Scenarios**: all 8 have named, passing tests on the real `Reconcile`, `dueTimers` and `Fire` with
  `clock.NewManual`; `cron-invalid-refused` covers `funcdctl apply` offline and raw `POST` and `PUT`, with nothing
  stored; `cron-stored-unchanged` compares the bytes of an EventSource and an App spec.
- **Scope**: the plan's files, the two `time/tzdata` imports, the changed `pkg/sdk/manifest_test.go` key, and no
  `BackupSchedule`, catch-up or App code.

### Definition of Done

19/19 items hold: the 10 Review-checklist items and 9 applicable generic items. The generic item "contract suites
against every driver" does not apply, because the ADR adds no port. Minor 1 is outside the ADR's checklist, so it is
not a miss.

`just ci-full` (Implementation plan step 5) was not run here. Per the project rule it runs once in the PR gate; the e2e
files compile (`go vet -tags e2e`).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0211 (implementation) → pass, 0/0/1, 0 model-attributed, DoD 19/19. See
`docs/reviews/model-scorecard.md`.

### Status

ADR-0211 moves `Reviewing` → `Implemented` (2026-10-10), and the FEAT-0010 F124 row moves `reviewing` →
`implemented`. The board card was not moved in this run, which made no GitHub writes; its `In Progress → Done` move
is still due.

### Recommendation

Sign off. Before the F124 PR merges, resolve the ledger conflict and put the release note of Implementation plan step
4 in the PR. File Minor 1 as an issue (adr-attributed) so that a later ADR makes zone names host-independent.
