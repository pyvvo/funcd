## Verdict: pass — 0 blockers, 0 majors, 0 minors open  (ADR-0256 implementation, model: claude-opus-5-5)

This report reviews branch `feat/adr-0256-silent-failure`, built in the one-PR mode, in two rounds. Round 1 did not
pass; round 2 passed.

- **Round 1** reviewed 602e31d4 on main 28303008 and found 1 Minor (model). The live check was not yet recorded, so
  the Definition of Done was not met.
  - The live check then found a defect in the rule itself: Vite empties its outDir at the start of every rebuild, also
    one that then fails. That changed the owned build's stamp, so a failing save read as "led to a build" and its error
    was dropped.
  - The ADR was re-judged (sixth draft) and the branch was force-pushed.
- **Round 2** reviewed 2f04e057: 9e306fb7 docs (Accepted) and 2f04e057 feat (`Fixes #1131`, Reviewing).

The change is in `cmd/funcdctl/devwatch.go` (devBurst, follow, the exit hooks in supervise), in
`cmd/funcdctl/dev_app.go` (earlyChecks returns the used images; watchFiles, watchBuilds, ownedPaths, walkBuild) and in
`cmd/funcdctl/devwatch_test.go`.

### Round 1 findings (closed in 2f04e057)
- **Minor: Decision 5's exit wiring in `supervise` had no test** · attribution: model · evidence: mutant M6, which
  drops `b.exited(true)` from the `finished` line, left every watch test and `TestDevBurst` passing (51.8 s).
  `TestDevBurst` calls `exited` directly, and dev-watch-restart's one-shot command builds its owned `web/dist`, so its
  burst prints nothing either way. The non-zero `b.exited(false)` call was uncovered the same way.
  - Closed by `TestDevWatchExits`. A one-shot command must print `once | hello` before `once finished`, and a failed
    run shows `fail | oops` once only, after a 3 s wait.
  - Both mutants now fail it.
- **The rule's defect** (found by the live check, attributed to the ADR, not the model) was fixed by the sixth draft:
  a build was written when a file under it was added or rewritten, so removals never count.

### ✅ Verified correct (keep it)
- **The verdict.** `devBurst.wrote` is true only for a file now present that is absent from the baseline or whose
  stamp (size and mtime) differs, so removing files writes nothing. Both the end of a burst and the 1 s window key on
  it.
  - The baseline is the map of the poll before the burst's first line: `poll` reads the files before `read` opens the
    burst with `base = last`. `started` resets it at each start, which is "the command's start when that came later".
  - Each poll replaces the maps; none is mutated.
  - `walkBuild` is factored out of `treeStamp`, which is unchanged, so the same entries are kept and skipped.
- **Ownership.** A `key=path` value, plus `<folder>/<key>.mjs` and `<folder>/dist/<key>.mjs` for each image that no
  `key=path` names, skipping Sites and Python runtimes. Paths are compared absolute through `filepath.Rel` (`.` counts,
  `..` does not), present or not, and taken at start.
- **Locking.** `started`, `poll`, `exited` and `close` take `b.mu`. `follow` defers `close`, and `stop` waits on the
  `WaitGroup`, so the read handle is closed when stop returns. An `exited` that races the close gets only a `Stat`
  error.
- **ADR-0255's tests** keep every assertion on the moved layout. Only paths changed, and dev-watch-quiet also forbids
  `build started`. ADR-0255's file gains only the permitted "Superseded in part by" back-link.
- **The fixture** empties `dist` on `BROKEN`, as Vite does. Both start-failure cases, with no build yet and with an
  earlier good build, show the error. There is one test per scenario, named after it, and they run serially.
- **Mutants.**
  - Round 1: M1 (no built window), M2 (baseline taken at read time), M3 (a non-zero exit judging), M4 (a prefix
    compare in ownership) and M5 (Python owned) all failed their tests.
  - Round 2 killed 5 of 5:
    - removals counting (`len(files) != len(base)`) → `TestDevBurst`;
    - no built window → `TestDevBurst`;
    - the baseline taken at read time → `TestDevBurst`;
    - no `b.exited(true)` → `TestDevWatchExits`;
    - no `b.exited(false)` → `TestDevWatchExits`.
- **Checks on 2f04e057.** gofmt is clean, `go build ./...` passes, and vet passes with and without `-tags dev`.
  `golangci-lint` reports 0 issues with and without `--build-tags dev`. `go test -tags dev -race ./cmd/funcdctl/`
  passes (172 s), and so does `go test ./cmd/funcdctl/` (4.3 s). The gate runs once on the PR.
- **The live check** is recorded: experiment 0001 at 418c92c, 62 of 62 checks, at 2f04e057 with Vite 5.4.11
  (`emptyOutDir`), 5 samples each.
  - Broken saves: a broken save shows its error prefixed 5 of 5 (median 3.45 s), with no push, while Vite emptied
    `dist` each time. A quick failure 1.5-2.5 s after a good build's last line shows 5 of 5, and the error shows at
    start with or without an earlier build.
  - Good saves: the fix and the good saves print no `web |` line, 0 of 5.
  - `tsc --noEmit --watch` in `src` shows its error.
  - Measured against the windows: a rebuild takes a median 0.125 s, the gap from emptying to the write a median 6 ms
    (at most 13 ms), and the write to the last line a median 8 ms.
  - ADR-0253 to ADR-0255's checks still pass.

### Note (not scored, for the decider)
Under `watch`, a build that empties a Site's folder makes dev's next reload push an empty Site, which fails the reload
and blocks the other parts' reloads until the Site builds again. ADR-0255 treats an empty Site folder as a miss only
at start. This belongs to ADR-0253/0255's reload, and the decider files it as a `needs-adr` issue.

### Definition of Done
- Round 1: 6 / 7, counting the 5 checklist items plus Done-when's build/vet/lint/test and its live check. The live
  check was not yet recorded.
- Round 2: 7 / 7.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0256 (implementation):

- round 1 → changes-requested, 0/0/1, 1 model-attributed, DoD 6/7;
- round 2 → pass, 0/0/0, DoD 7/7.

### Recommendation
Pass. ADR-0256 is stamped `Implemented` and F167 `implemented` in this commit; the gate and CI run on the PR.
