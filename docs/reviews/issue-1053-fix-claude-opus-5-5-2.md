## Verdict: changes requested — 0 blockers, 1 major, 1 minor  (issue #1053 fix, round 2, model: claude-opus-5-5)

Change: round 2 of branch `fix/1053-dev-persist-temp-bundle`, `git diff ad37f73f..HEAD` (`cmd/funcdctl/dev.go`,
`cmd/funcdctl/dev_phase2_test.go`; +127 −50) in two commits:

- 57f62bd8 `fix(funcdctl): keep a --persist bundle across runs and write it under the state lock`;
- c28df3ac `fix(funcdctl): stop reloading an in-place function whose --persist-to is inside its source`.

They sit on 736d581a (round 1) and ad37f73f (the round-1 review files, unchanged since). Round 1:
[issue-1053-fix-claude-opus-5-5.md](issue-1053-fix-claude-opus-5-5.md).

Round 2 fixes every round-1 finding and the reload loop that round 1 observed, and each fix is verified below. One
change of this round causes a new regression, though. The bundles and contracts are now written after the platform
starts, so after a `--persist` restart an in-place function can serve without its contract.

The probes are scratch test files added to the package with `go test -overlay`; nothing was written to the
worktree. `<persist-to>` stands for a probe's persist root.

### 🔴 Blockers

None.

### 🟡 Major 1 — after a --persist restart, an in-place function can serve without its contract  ·  attribution: model

57f62bd8 moved `prepareHandlers` after `startPlatform` (`dev.go:593`, after `dev.go:579`). Over a durable metastore,
the platform reconciles the stored Function as soon as it runs (`dev.go:636`), before dev delivers anything. The worker
spec sets `FUNCD_CONTRACT_PATH` only if the contract file exists in the bundle root at that moment
(`addContractEnv`, `internal/function/function.go:3087-3097`, process mode at `function.go:3169`). An in-place
function's `.funcd-contract.json` is gone at that point: the previous run's stop removed it (`dev.go:1591`). Before
delivering it, `prepareHandlers` fingerprints the source (`dev.go:665`), which walks the whole source dir of an
in-place function. If that takes longer than the controller needs to start the stored replica, the replica runs with
no contract. Nothing in the spec changed, so no Revision is stamped, and that replica keeps serving. By reading, this
lasts until an edit rolls out a new Revision.

Probe P6: an in-place `funcdctl.yaml` with a strict input contract, plus n plain files in `assets/`; boot, stop, and
boot again over the same `--persist-to`; then post a payload that violates the contract.

```
HEAD:                    500, 2000 and 5000 files → boot 2 bad input: 200 {"greeting":"hi undefined"}
origin/main's dev.go:    500, 2000 and 5000 files → boot 2 bad input: 422 {"error":"event data does not match the input contract",…}
round 1's dev.go:        the same 422 for each size
HEAD, delivery moved back before startPlatform (mutant m2): the same 422 for each size
```

Boot 1 answered 422 in every run. With no extra files the margin is thin, but in this run the delivery came first:
422 in 20 of 20 restarts, and in 10 of 10 under `-race`. A 20 ms delay before `prepareHandlers` (an overlay) loses the
contract in 5 of 5 restarts. An isolated function keeps its contract, because its kept bundle still holds the
previous run's contract file. ADR-0125's scenario dev-run-function requires a 422 before the handler for a payload
that violates `contract.input`.

The move is not needed for round 1's Major 2. Mutant m2 (`prepareHandlers` back before `startPlatform`) passes the
whole `-tags dev` package, `TestIssue1053_FailedSecondSessionKeepsTheBundle` included. A refused second session
only rewrites the same bytes now, because a kept bundle is no longer emptied and its cleanup does nothing. So no test
pins the lock-first order either.

The bootDev doc comment (`dev.go:537-542`) also still says that the bundles are delivered before the platform boots.

Fix (builder): deliver the bundles and contracts before `startPlatform` again, as origin/main did. Add a --persist
restart of an in-place function, with a source tree, whose bad input must answer 422.

### Minors

- **Minor 1 — two new guards have no test** · attribution: `model`. Each mutant below leaves
  `go test -tags dev -race ./cmd/funcdctl/` green:
  - m3: `deliverBundle` no longer replaces a link at the entry (`dev.go:1646`). Probe P5 makes that case. Two
    functions have `main` files in one dir, so both run linked. Then `main` is removed from `a`, and dev restarts
    over the same root. With the mutant, the copy writes through the kept link into the source: `a.mjs`'s mtime
    changes, and both functions reload without end (`a` went from generation 2 to 12 in 3 s, `b` from 2 to 11). With
    the fix, both generations stay at 2, and the source is untouched.
  - m4: `linkSources` no longer removes a different file at a link's name (`dev.go:1698`). By reading, the reverse
    change (a copied bundle becomes linked) then fails the boot with "file exists".

  Each guard needs a two-boot test of its transition.

### Round-1 findings — status

- **Major 1 (a restart that changes the artifact path): fixed.** `TestIssue1053_PersistRestartServesAnIsolatedFunction`
  now has a third boot that sets `main`. With round 1's `dev.go` it fails at that boot:
  `503 … did not become ready within 30s`, with 68 materialize errors naming `<persist-to>/bundles/api/api.mjs`. On
  HEAD it passes (1.73 s). Round 1's probe P1 now gives `200 {"n":2}` after the restart that sets `main`; the kept
  artifact survived the stop. Mutant m1 (remove the kept bundle on stop again) fails the test at boot 3.
- **Major 2 (a refused second session deletes the bundle): fixed.** `TestIssue1053_FailedSecondSessionKeepsTheBundle`
  fails with round 1's `dev.go` ("the running session's bundle stays") and passes on HEAD (1.38 s). Round 1's probe P2
  now shows session 1's artifact intact, its edit served (`{"n":102}`) and no hot-reload warning.
- **Minor 1 (untested lines): resolved.** Nothing is emptied any more. Mutant m7 (the bundles dir dropped from the
  skip list) fails `TestWatchSkipsTheSessionState`.
- **Minor 2 (the hand-written poll): fixed.** The tests use `addNote` (`require.EventuallyWithT`) and
  `internal/testkit/poll.Never`, the helper the #950 hygiene rule asks for, already used in `cmd/funcd`.
- **Minor 3 (the stale comment): fixed** (`dev.go:1168`).
- **Minor 4 (state from v0.14.0): the recovery works.** CLI check with `-tags dev` builds: the unfixed binary
  counted `{"n":1}`, `{"n":2}`. HEAD over that state answered `503 … did not become ready within 30s`, with 68
  materialize errors. Then `funcdctl delete function api -n default --server <control URL> --token funcd-dev-token`
  answered `deleted Function/api`, and after a restart HEAD answered `{"n":3}`: the KV data was kept. No PR is open,
  so the PR body that is to carry this recovery could not be checked.
- **Observation (the reload loop): fixed in c28df3ac.** `TestDevPersistRootInsideTheSourceDoesNotReload` fails with
  57f62bd8's `dev.go` (0.86 s: "the function is re-applied though nothing was edited") and passes on HEAD. Mutant m6
  (the process dir dropped from `watchSkips`) fails it and `TestWatchSkipsTheSessionState`. Round 1's probe P3 now
  keeps generation 1 for 6 s.

### ✅ Verified correct (keep it)

- **The regression tests fail without the fixes, for the right reasons.** With origin/main's `dev.go` (the test file
  without the `p.bundleDir` asserts and `TestWatchSkipsTheSessionState`, which do not compile there), all three fail:
  - the restart test, at boot 2: `503 … did not become ready within 30s`;
  - the second-session test: no bundle exists at the stable path;
  - the reload test: the reload loop.

  With round 1's `dev.go`, the restart test fails at boot 3 and the second-session test fails. With 57f62bd8's
  `dev.go`, only the reload test fails.
- **They pass on HEAD under `-race`**: restart 1.73 s, second session 1.38 s, reload 3.66 s, watch skips 0.00 s, and
  the four `TestResolvePersistPlan*`.
- **Kept bundles are sound.** `privateBundleDir` keeps `<persist-to>/bundles/<name>` and never empties it, and the
  ephemeral default still uses a temp dir that is removed on stop. The entry-link guard stops a write-through into the
  source. `linkSources` keeps a correct link and replaces anything else. The new skip list covers
  `<persist-to>/process` (ADR-0167), which `devStateDir` returns for the process driver.
- **Scope**: c28df3ac fixes the defect that round 1 found, in its own commit with its own tests, as the repo's rule for
  defects found while fixing asks. No test was weakened: the restart test keeps its boots and adds one.
- **Reuse and conventions**: `addNote`, `poll.Never` and `resolvePersistPlan` are reused. `fault.Wrapf` and `0o700`
  match the surrounding code, the comments give the why, and imports are at the top level. The audit's soft note
  `discard-prod` at `dev.go:1698` is benign: if that `Remove` fails, the `Symlink` on the next line returns the error.
- **ADRs**: no ADR file changed; Major 1 is the one behavior conflict (ADR-0125's dev-run-function scenario).
- **Checks run here**: `go test -race ./cmd/funcdctl/` ok (6.3 s); `go test -tags dev -race ./cmd/funcdctl/` ok
  (35.8 s); `go vet` ok with and without `-tags dev`; `golangci-lint` 0 issues on `./cmd/funcdctl/...` and with
  `--build-tags dev`. The issue's CLI steps on a `-tags dev` build of HEAD: run 1 `{"n":1}`, `{"n":2}`; run 2 over
  the same state `{"n":3}`.
- **The gate on c28df3ac** (I read its logs and did not rerun it): the audit has 0 hard flags; ci-full passed, with
  the `pkg/funcd` e2e result cached because this change does not touch that package; Linux build, vet, lint and tests
  passed, and the tree was unchanged.
- **Shape**: both commits are `fix(funcdctl): …` and carry the trailer. 57f62bd8 has `Fixes #1053`; c28df3ac has no
  issue to name.

### Definition of Done
9 / 12 hold. Misses, all model:
- item 4: mutants m2, m3 and m4 survive (Major 1 and Minor 1);
- item 7: Major 1 breaks ADR-0125's dev-run-function scenario after a --persist restart;
- item 9: the bootDev doc comment is stale (part of Major 1).

### Model scorecard
Recorded: claude-opus-5-5 on issue #1053 (fix, round 2) → changes-requested, 0/1/1, 2 model-attributed, DoD 9/12.
See docs/reviews/model-scorecard.md.

### Recommendation
Back to `/fix`. Keep the kept bundles, the link guards, the skip list and the tests. Move the bundle and contract
delivery back before `startPlatform`; the kept bundles already protect a running session. Then add a test that pins
the contract after a --persist restart, and two-boot tests for the two link transitions.
