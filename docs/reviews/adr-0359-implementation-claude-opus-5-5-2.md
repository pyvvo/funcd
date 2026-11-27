## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0359 implementation, round 2, model: claude-opus-5-5)

This round reviews the three commits on branch `feat/dev-containers` that come after the round-1 review (21b15262):

- 8a0a32c2 amends ADR-0359 and ADR-0346 in place.
- 82f59747 bind-mounts, read-only, a directory that the bundle links to and that the engine can see.
- 8aeab392 copies such a directory instead of binding it when one of its links leaves it.

The decider chose option A on 2026-11-26, and the fallback was settled on 2026-11-27. The work was not edited.

### Blocker

None.

### Major

None.

### Minor

1. **The amendment dates the fallback 2026-11-26, but it was settled on 2026-11-27** · here · model. The header line
   of ADR-0359 reads `Amended in place: 2026-11-26 … unless the directory holds a link that leaves it`. The
   Decision 5 text marks the fallback sentence `(amended 2026-11-26)`, and so do the Review checklist and the tests.
   The fallback ("Such a directory is copied whole instead…") is the 2026-11-27 ruling. It should be dated with
   that day and its own clause, as ADR-0346's header lists each of its amendments with its own date and source.
2. **The behavior row is in a different file from the one the ADR names** · here · model. This is carried from round 1
   and is still open. The new row for the bind is also in `docs/behavior/workers.md:40`, next to the round-1 row, and
   `docs/behavior/funcdctl-dev.md` has neither. The content is correct.

### Routed (not this work's)

None.

### ✅ Verified correct (keep it)

- **The amendments follow ADR-0346's in-place form.**
  - Each ADR gains a header line `- **Amended in place**: <date>, by the decider ("<quote>"): <what> (<decisions>)`,
    the same shape as ADR-0346's 2026-11-25 line.
  - Each changed clause carries an inline `(amended 2026-11-26)`, as the 2026-11-25 clauses do.
  - ADR-0346 has a second amendment line rather than an extended first one. This matches its two separate
    `Superseded in part by` lines.
- **Nothing else in the frozen text changed.** In ADR-0346, `git diff 21b15262..HEAD -- docs/adr/` touches only the
  new header line, Decision 4's "never a bind mount" clause and the Review checklist's bind item.
- **ADR-0359's own amendment is limited to the bind.** In ADR-0359 the diff touches only:
  - the header line
  - a measured paragraph in Context, marked as amended
  - Decision 5
  - the `Mount.Bind` contract, marked `// new`
  - plan step 5
  - Review checklist item 5
  - the Consequences lines about saves. The "not measured" line is replaced by the measured (+) line and two (−) lines.

  The status, Scenarios, Decisions 1–4, 6 and 7, and the other contracts are byte-identical.
- **Build, vet and lint.**
  - `go build ./...` and `go build -tags dev ./...` passed.
  - `go vet ./internal/provider/...` and `go vet -tags dev ./cmd/funcdctl/` passed.
  - golangci-lint reported `0 issues` on `internal/provider/...`, and `0 issues` with `--build-tags dev` on
    `cmd/funcdctl`.
- **Tests.** `go test -race -count=1` passed (`ok`) on `internal/provider/docker` and `internal/provider/nerdctl`. The
  real-engine run on colima's Docker is in the next section.
- **The bind, unit side.**
  - `TestTarTreeBindsTheDirectoriesTheEngineSees`: an engine on the host's network binds every directory. An engine
    in a VM binds only directories under the home directory, and copies the rest as before. A link to a file is
    still copied.
  - `TestTarTreeCopiesADirectoryWhoseLinksLeaveIt`: a `.bin` link or an alias link inside the directory keeps the
    bind. A workspace link, plain or under `@scope`, makes the directory a whole copy, with the workspace package in
    it.
  - `TestWorkersBindTheDirectoriesTheirBundleLinksTo`: the bind mounts after the bundle volume, the volume holds an
    empty mount point, and the worker reads the module through the bind.
  - At the Engine API, a bind is `Type: bind`, read-only, with no volume options (`TestRunSpecAtTheEngineAPI`).
  - Through nerdctl, the bind mounts after its volume and needs no volume lookup, and a helper takes it with `-v`
    (`TestBindsThroughNerdctl`).
- **Design fit.** `sees` reuses ADR-0346 Decision 5's classification (`net.host`), and `under` replaces `inBundle`
  and is shared with `linksStayIn`. Within the copy of a linked directory, nothing is bound (`bind=false`), which
  matches Decision 5's "any other link … is copied". The fallback checks only the top level and `@scope` entries,
  as the amended Decision 5 states.

### Real-engine run (colima's Docker)

`go test -tags dev -race -count=1 ./cmd/funcdctl/ -run 'TestScenarioDevContainers|TestScenarioDevAddonWorker|TestLoadOwnImagesOnce|TestDeliverBuild' -v`:
`ok` (63.5 s).

- **The new scenario.** `TestScenarioDevContainersBindsNodeModules` passed in 2.1 s. The handler imports from the bound
  `node_modules`, a write into it fails `EROFS`, and `docker inspect` shows exactly one `bind` mount, read-only, from
  the folder's `node_modules` to `/var/funcd/artifact/node_modules`.
- **The other `TestScenarioDevContainers*` scenarios** passed:

  | Scenario | Time |
  |---|---|
  | `…DevContainers` | 17.8 s |
  | `…NoHostRuntime` | 4.2 s |
  | `…Save` | 4.6 s |
  | `…LoadOnce` | 14.9 s |
  | `…LinkedBuild` | 2.2 s |
  | `…Key` | under 0.1 s |
  | `…Refused` | under 0.1 s |

- **Earlier tests still pass.** ADR-0346's `TestScenarioDevAddonWorker*` still pass, because a worker on an Addon's
  runtime takes the same bind. `…Linux` was skipped, as on main. Round 1's fix test,
  `TestDeliverBuildLinksNodeModulesOfARelativeFolder`, still passes.

### Definition of Done

13/13 items hold: the 6 ADR Review-checklist items, with item 5 as amended, plus the 7 generic items. The PR's gate,
which runs next, covers `just ci-full`.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0359 (implementation, round 2) → pass, 0/0/2, 2 model-attributed, DoD 13/13. See
docs/reviews/model-scorecard.md.

### Recommendation

The ADR stays `Implemented`; this round changes no status. The two Minors are a date fix and a row placement, and
either can go in the PR. The PR closes #1585 (`Fixes #1585`).
