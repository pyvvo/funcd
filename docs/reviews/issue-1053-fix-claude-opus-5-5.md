## Verdict: changes requested — 0 blockers, 2 majors, 4 minors  (issue #1053 fix, model: claude-opus-5-5)

Change: branch `fix/1053-dev-persist-temp-bundle`, commit 736d581a `fix(funcdctl): serve a function isolated in its own
bundle again after a --persist restart`, on origin/main e7fcbe8d (`cmd/funcdctl/dev.go`,
`cmd/funcdctl/dev_phase2_test.go`; +79 −12). Governing ADRs: ADR-0125 (funcdctl dev, Decision 7), ADR-0124
(per-function manifests), ADR-0143 (the Revision switch), ADR-0167 (the dev state dir).

The fix makes the issue's own steps work: under `--persist`, an isolated function's bundle now has one path on every
run. But dev still deletes that bundle on stop, and it empties the bundle at boot before the session owns the persist
root. So a restart that gives the Function a new artifact path still fails for the issue's cause, and a second session
that fails on the held root now deletes the bundle of the session that runs.

The probes below are scratch test files added to the package with `go test -overlay`; nothing was written to the
worktree. `<persist-to>` stands for the probe's persist root.

### 🔴 Blockers

None.

### 🟡 Major 1 — a restart that changes the artifact path still answers 503 forever  ·  attribution: model

`prepareBundle` still removes the bundle on stop (`cmd/funcdctl/dev.go:1589`, `cleanup = func() { _ = os.RemoveAll(tmp) }`),
and that bundle is now `<persist-to>/bundles/<name>`. The path stays valid only if the next boot rebuilds the same
path. A restart that stamps a different artifact path finds the serving Revision's artifact gone, and the switch
(ADR-0143) never happens. This is the mechanism the issue describes. Adding `main` triggers it: it is the issue's own
workaround, and dev's reload warning asks for a restart after it (`cmd/funcdctl/dev_reload.go:258-259`, "restart
funcdctl dev to apply a changed main"). By reading, a changed entry name does too (for example a runtime change from
node to python turns `api.mjs` into `api.py`, `stemEntry` at `dev.go:1762`).

Probe P1, with the fix: boot an isolated `api` (the issue's handler and KV binding) under `--persist`, call it, stop,
add `main: api.mjs`, boot again over the same root:

```
P1 boot 1 (isolated): 200 {"n":1}; image under the persist root: true
P1 after stop, boot 1's artifact: stat <persist-to>/bundles/api/api.mjs: no such file or directory
P1 boot 2 (main set, in place): 503 {"type":"urn:funcd:problem:unavailable",…,"detail":"activator.activate: function default/api did not become ready within 30s"}
```

While boot 2 ran, the controller logged
`function.converge: materialize artifact: function.FileMaterializer.Materialize: artifact "<persist-to>/bundles/api/api.mjs" not found`
68 times. This is the #1053 loop, with the fix applied.

Fix (builder): under `--persist`, keep the bundle as state. Do not delete it on stop or when a boot fails, so the
serving Revision's artifact still exists at the next boot, whatever path the new Revision uses. A leftover bundle
under the gitignored persist root costs little. Add a restart that adds `main` to the regression test.

### 🟡 Major 2 — a failed second session over the same root deletes the running session's bundle  ·  attribution: model

`bootDev` prepares the bundles (`dev.go:576`) before `startPlatform` (`dev.go:580`) reaches `process.Open`
(`dev.go:701`). `process.Open` takes the persist root's single-owner lock, the flock in
`internal/runtime/procreg/procreg.go:54`. Every other writer under the persist root runs after that lock: the catalog,
SQL, Badger and fileblob drivers (`dev.go:721-768`). So a second `funcdctl dev --persist` over a root that another
session holds first empties that session's `<persist-to>/bundles/<name>` (`privateBundleDir`, `dev.go:1617`). Then
the lock refuses the second session, and its unwind (`dev.go:570-574`) runs the bundle cleanup (`dev.go:1589`), which
deletes the bundle again. Before the fix, each session had its own temp dir, so a refused second session touched
nothing of the first.

Probe P2: session 1 runs and serves; session 2 starts over the same root; then `api.mjs` is edited in session 1.

```
with the fix:
P2 session 2 start error: funcdctl dev: open the process runtime in … procreg.Open: registry … is held by another process
P2 session 1's artifact after session 2 failed: stat <persist-to>/bundles/api/api.mjs: no such file or directory
P2 session 1 after an edit of api.mjs: 200 {"n":2}
P2 session 1 hot-reload warnings: level=WARN msg="hot-reload failed" err="funcdctl dev: copy handler into bundle: open <persist-to>/bundles/api/api.mjs: no such file or directory"

origin/main's dev.go, same probe:
P2 session 1's artifact after session 2 failed: <nil>
P2 session 1 after an edit of api.mjs: 200 {"n":102}
P2 session 1 hot-reload warnings: []
```

Session 1 stops picking up edits until it restarts. By reading, a session-1 worker that is replaced (a crash, or the
restart of a hung worker) cannot materialize either.

Fix (builder): write the bundles only after `process.Open` holds the root, and leave them in place when a boot fails.
Cover it with a second session over a held root.

### Minors

- **Minor 1 — two key lines of the fix have no test** · attribution: `model`. Two overlay mutants leave
  `go test -tags dev -race ./cmd/funcdctl/` green. M-c: `privateBundleDir` no longer empties an existing dir
  (`dev.go:1617`): ok (30.8 s). M-d: the bundle dir is no longer excluded from the hot-reload walk (`dev.go:563`): ok
  (31.2 s). Emptying the dir lets a boot after a SIGKILLed session (no cleanup ran) start from a clean bundle; by
  reading, without it `linkSources` (`dev.go:1676`) fails with "file exists" on a linked bundle. The only crash test,
  `devRestartReaps` (`cmd/funcdctl/dev_crash_test.go:84`), runs an in-place function. The exclusion matters when
  `--persist-to` is inside the dir of an in-place function that runs beside an isolated one. Test both lines, or drop
  what the Major fixes make unnecessary.
- **Minor 2 — the regression test polls in a hand-written loop** · attribution: `model`. `dev_phase2_test.go:270`
  loops with `time.Sleep(200 * time.Millisecond)`, and the gate's audit lists that line as `sleep-test`. The package
  polls a call with `require.EventuallyWithT` (`dev_crash_test.go:112`), and `addNote` (`dev_sql_test.go:52`)
  already does the same call-and-read-`n`. The loop works, so this is cosmetic.
- **Minor 3 — a stale comment** · attribution: `model`. The `plannedFunc` doc (`dev.go:1157`) still says
  "isolate ⇒ materialize into a private temp bundle". Under `--persist`, the bundle is now `<persist-to>/bundles/<name>`.
- **Minor 4 — the change does not say that state from an earlier funcdctl stays broken** · attribution: `model`.
  The reporter's state was written by v0.14.0. CLI check B: run 1 with the unfixed binary (`{"n":1} 200`, `{"n":2} 200`,
  a clean stop), then run 2 with the fixed binary over that state. Run 2 answered
  `503 … "activator.activate: function default/api did not become ready within 30s"`, with 68 materialize errors that
  name the first run's deleted `<TMPDIR>/funcdctl-dev-fn-N/api.mjs`. Upgrading does not repair such state, and the
  commit message does not say so. Give the recovery in the PR, and so in the release note. A fresh `--persist-to` dir
  works, but it loses that dir's data.

### Observations (not scored)

- **The switch is the platform half of the cause.** `switchSolo` converges the serving Revision before the new one,
  and it returns that Revision's materialize error (`internal/function/function.go:1647-1651`). So no switch happens
  while the serving artifact is missing, and each missed case above stays failed instead of failing once. ADR-0143
  Decision 4.3 replaces a dead serving worker from its Revision. Whether a switch may skip a serving Revision that it
  cannot start is an ADR-0143 question, outside a `/fix`.
- **A pre-existing reload loop, also on origin/main.** An in-place function whose `--persist-to` is a non-dot dir
  inside its source dir rolls out a new Revision on every poll. Probe P3 (a generic `funcdctl.yaml`,
  `--persist-to <project>/state`) went from generation 1 to generation 21 in 6 s, both on this branch and with
  origin/main's `dev.go`. `<persist-to>/process` (the ADR-0167 worker registry, `dev.go:1011`) is the one dir under
  the persist root that the exclusion list at `dev.go:563` does not name. With that dir added by an overlay, the
  generation stayed at 1. This needs its own issue; this fix did not cause it.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** The overlay used origin/main's `dev.go` and the
  test file without the two `p.bundleDir` asserts, which do not compile against the old struct. Result:
  `--- FAIL: TestIssue1053_PersistRestartServesAnIsolatedFunction (61.48s)` — `boot 2 never served api; its last answer:
  503 … did not become ready within 30s`. The log names the first boot's deleted bundle 67 times
  (`materialize artifact: … artifact "<TMPDIR>/funcdctl-dev-fn-N/api.mjs"`), which is the issue's error.
- **It passes with the fix, un-skipped, under `-race`**: `--- PASS: TestIssue1053_… (1.59s)`, and the four
  `TestResolvePersistPlan*` tests pass. The test uses the issue's handler and KV binding, two boots over one persist
  root, and a counter that must reach 2.
- **The issue's own CLI steps are fixed** (check A, `go build -tags dev ./cmd/funcdctl`, then
  `funcdctl dev app --gport <port> --persist --persist-to state`): run 1 `{"n":1}`, `{"n":2}`; after a stop, run 2
  over the same state `{"n":3}`, with no materialize error.
- **Mutant M-b** (`bootDev` passes `""` as the bundle root, `dev.go:576`) fails the regression test (61.52 s, 503).
- **The new `RemoveAll` cannot leave the bundles dir**: the name comes from `sanitizeName` (`dev.go:1930-1937`,
  `[a-z0-9-]`, never empty), so `<persist-to>/bundles/<name>` never resolves to the persist root or above it.
- **Scope**: two files, and every hunk serves the issue. The `TestResolvePersistPlan*` changes are two added asserts;
  no test was weakened or deleted.
- **ADRs**: no ADR file changed. ADR-0125 Decision 7 persists the metastore, KV and blob. A bundle dir beside
  `process` (ADR-0167) and `sql` (ADR-0237) under the gitignored root contradicts none of it. ADR-0124's isolation (a
  private bundle and contract per isolated function) is kept. ADR-0143 is untouched.
- **Reuse and conventions**: no helper in the repo empties and recreates a dir (searched `internal/platform`, the dev
  engines and `cmd/funcdctl`). `privateBundleDir` keeps the temp-dir path for the ephemeral default.
  `fault.Wrapf(…, fault.Internal, op, …)` and `0o700` match `buildPersistDrivers`. The comments give the why
  (#1053), imports are at the top level, and the audit has 0 hard flags.
- **Checks run here**: `go test -race ./cmd/funcdctl/` ok (6.7 s); `go test -tags dev -race ./cmd/funcdctl/` ok
  (30.8 s); `go vet` ok with and without `-tags dev`; `golangci-lint` 0 issues on `./cmd/funcdctl/...` and with
  `--build-tags dev`. The gate on 736d581a was read, not rerun: ci-full with e2e (`pkg/funcd` 377 s), the Linux
  build, vet, lint and tests, an unchanged tree, and the audit all pass.
- **Shape**: `fix(funcdctl): …`, `Fixes #1053`, the Co-Authored-By trailer, and one issue in one commit.

### Definition of Done
9 / 12 hold. Misses: item 4 (two mutants survive, Minor 1, model); item 9 (Major 2 writes into the persist root before
the session holds its lock, unlike every other writer there, and Minors 2 and 3; model); item 12 (Major 1 and Minor 4:
a restart that changes the artifact path, and state from an earlier funcdctl, still fail for the issue's cause; model).

### Model scorecard
Recorded: claude-opus-5-5 on issue #1053 (fix) → changes-requested, 0/2/4, 6 model-attributed, DoD 9/12. See
docs/reviews/model-scorecard.md.

### Recommendation
Back to `/fix`. Keep the stable bundle path and the regression test. Under `--persist`, treat the bundle as state:
write it only after `process.Open` holds the root, and do not delete it on stop or when a boot fails. Add a restart
that adds `main` and a second session over a held root to the tests. Say in the PR that state written by v0.14.0 or
earlier needs a fresh `--persist-to` dir. The reload loop and the switch question in the observations belong to
their own issues.
