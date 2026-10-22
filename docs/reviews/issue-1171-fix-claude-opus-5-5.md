## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #1171 fix, model: claude-opus-5-5)

Commit b7d2985a on `fix/stress-flakes-1` (main f7e40c89) changes `cmd/funcdctl` (`-tags dev`):
- `prepare` (`dev_app.go`) reports a Site push that fails while `siteHoldsIndex` misses as that miss;
- `startDevApp` waits for the builds again (`waitForBuilds`) when `prepare` returns a `buildMiss` under watch commands;
- a `beforeSitePush` seam on `devConfig` (`dev.go`);
- `TestIssue1171_StartWaitsWhenASitesBuildVanishesBeforeItsPush` (`devwatch_test.go`), with an options parameter on
  `startLoopAsync`.

### Minor 1 — a narrower window remains: a build that empties and rewrites the folder during the push  ·  attribution: model
- **The case.** A Site's watch build can succeed while dev pushes: Vite empties `dist` and writes it again within a few
  milliseconds. The push can then fail on a file that vanished, while the folder holds its index again by the time of
  the check. The start then still stops with the push error.
- **Why it is Minor.** This case is rarer than the reported one, and it is not the one the issue describes. A failed
  build leaves the folder empty, and that is now waited on.
- **An optional hardening.** Under watch commands, also treat a failed Site push as a miss when the inputs' stamp moved
  since the lookup.

### ✅ Verified correct (keep it)
- **It fails without the fix, for the reported reason.**
  - The overlay is main's `dev_app.go` plus only the seam call. `TestIssue1171_…` fails after 90.1 s ("the error
    shows"). Dev printed the same lines as in the issue: the `watch web` line, then a `pushed images.*` line for each
    function, and no `web | build error`.
  - A diagnostic test on the same overlay gets the start error
    `funcdctl dev: push images.web: artifact.PushSite: site dir "<dir>" is empty`.
- **Each key line is needed.** Each of these mutants fails the test after 90 s:
  - without the loop in `startDevApp`;
  - without the miss conversion in `prepare`.
- **It passes with the fix**, un-skipped, under `-race`:
  - `TestIssue1171_…` takes 5.3 s;
  - the issue's own `TestScenarioDevWatchStartFailure/an_earlier_good_build` takes 4.1 s;
  - the `-tags dev` package passes in 178 s.
- **The reload path stays within ADR-0257.**
  - Decision 1 already counts a failed push as a build that cannot be pushed, and keeps the last build.
  - The conversion changes only the reason's wording: `<dir> is empty` instead of the refusal's message. The Line
    contract allows both.
  - With live reload, the new wording names the user's folder, not dev's stage copy.
  - Without watch commands, the start still stops. `TestDevSiteKeepsLastImage` passes.
- **The loop cannot spin.**
  - Every `buildMiss` that `prepare` returns comes from `resolveImage` or `siteHoldsIndex`. `lookupBuilds` runs both,
    so the loop goes around only when the folder changed after a settled lookup.
  - Each pass waits at least two polls (300 ms each) for a settled lookup.
  - The pass ends on a refusal, on a miss once every command has exited 0, or when ctx ends.
  - Images that were already pushed are not pushed again: their fingerprint matches `s.images`. `s.stamp` is updated
    as after the first wait.
- **ADR-0255 Decision 2.** Under watch, dev waits while a Site folder is empty. A folder that empties after the lookup
  is now treated the same way.
- **The seam is acceptable.** It follows the `pickS3Addr` precedent (#627) in the same struct, it is nil in production,
  and its comment names the issue. It makes the race deterministic: the seam holds the push until `dist` is empty.
- **No sibling in another path.**
  - Folder mode has no watch commands (ADR-0255 keys are App-only).
  - A reload keeps the last build.
  - A function build that vanishes before `prepare`'s lookup returns a `buildMiss`, and the loop waits on it, as the
    commit message says.
- **Checks.**
  - `go test -race -count=1 -tags dev ./cmd/funcdctl/` passes (178 s).
  - `go vet` passes with and without `-tags dev`, on darwin and Linux.
  - `golangci-lint --build-tags dev` reports 0 issues on darwin and Linux.
- **Shape.** The subject is `fix(dev):`, with `Fixes #1171` and the trailer. The commit holds one issue.

### Definition of Done
12 / 12. Linux under `-race` was not rerun (env). The seam reproduces the race on darwin.

### Model scorecard
Recorded: claude-opus-5-5 on issue #1171 (fix) → pass, 0/0/1, 1 model-attributed, DoD 12/12.
See docs/reviews/model-scorecard.md.

### Recommendation
Pass. Minor 1 is optional hardening.
