# ADR-0286 implementation review — funcdctl backup plan prints an example and edits no file

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0286 implementation, model: claude-opus-5-5)

Reviewed by coworker-2 (claude-opus-5-5) on 2026-10-25, in its own checkout of `feat/adr-0286-backup-plan-example`:
docs commit fe558de0 (Accepted, the four back-links, F200), implementation commit 0a9fe924 (ADR → Reviewing,
F200 → reviewing), on 1cb84fe2.

### Verification run

| Check | Command | Result |
|---|---|---|
| build | `scripts/agent/d go build ./...` | exit 0 |
| vet | `go vet ./cmd/funcdctl/ ./internal/backup/plan/ ./internal/platform/config/`; `go vet -tags dev ./cmd/funcdctl/` | exit 0, exit 0 |
| lint | `go tool golangci-lint run` on the three packages; `--build-tags dev ./cmd/funcdctl/...` | 0 issues, 0 issues |
| tests | `go test -count=1 -race ./cmd/funcdctl/ ./internal/backup/plan/ ./internal/platform/config/` | ok, ok, ok |
| tests, dev tag | `go test -count=1 -race -tags dev ./cmd/funcdctl/` | ok (190 s, the package's dev suite) |
| scenarios | `-run 'TestScenarioPlanEditsNothing\|…ShowsVerifyJob\|…LakeSetsNothing\|…LakeNamesTheOptIn' -v` | 4 × `--- PASS`, none skipped |
| tracking | `git diff fe558de0..0a9fe924 -- docs/` | the ADR's Status line and F200's status cell only |

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **Minor 1 — the key-set variant through the environment has no test** · attribution: `model`. Decision 3 shows the
  `lake.saveInterval <v>` comments "when the loaded config sets the key (in the file or as `FUNCD_LAKE_SAVE_INTERVAL`,
  the loader's overlay)". The code reads `cur.Lake.SaveInterval` from `config.Parse`, which applies the `FUNCD_*`
  overlay (`internal/platform/config/config.go:596`), so the behavior holds. But `TestScenarioPlanLakeNamesTheOptIn`
  covers the file only. Fix: a subtest with `t.Setenv("FUNCD_LAKE_SAVE_INTERVAL", "2s")` and no key in the file.
- **Minor 2 — `--write` as an unknown flag sits outside `TestFlagRules`** · attribution: `model`. Implementation plan
  step 3 says `TestFlagRules` "gains `--write` as an unknown flag". The assertion exists, in
  `TestScenarioPlanEditsNothing` (exit 1, "unknown flag: --write", nothing printed, the file unchanged), so the
  coverage is the same; only its place differs from the plan. Fix: one row in `TestFlagRules`, or nothing.

### Routed (not this work's)

- `docs/PROJECT-SUMMARY.md` still describes ADR-0223's `--write` (row ADR-0223) and ADR-0274's "`plan lake --rpo R`
  writes min(R ÷ 2, 1h)" (row ADR-0274) · home: elsewhere · destination: the DR session's integration step, as
  implementation plan step 4 assigns it (once, with the batch, through the project-summary skill).

### ✅ Verified correct (keep it)

- **No write path is left.** `commitFile`, `planResult`, the `write` flag field and the `path/filepath`/`syscall`
  imports are gone from `cmd/funcdctl/backup.go`. `planFinish` ends in `a.writef("%s", printed)`. Cobra refuses
  `--write` with exit 1, and nothing is printed (asserted).
- **The check is still the start's.** `planConfig` splices the settings into the located file's bytes in memory
  (`plan.Apply(data, "", settings)`), runs `plan.CheckConfig` on the result, and prints only after it passes.
  `TestScenarioRefusedAsTheDaemonRefuses` and `TestScenarioRTOImpossible` run without `--write` and still assert
  the file unchanged.
- **Decision 2 is exact.** `verifyJob` computes V = (R − R ÷ 2) ÷ 2, truncated to the ms, and gives the ADR's line
  word for word (`15m` for `--rpo 1h`, `7m30s` for `30m`). stderr no longer carries the cadence:
  `TestScenarioPlanShowsVerifyJob` and `TestScenarioRTOLowerBoundOnly` assert stderr whole.
- **Decision 3 is exact.**
  - `Derive(Lake)` returns no setting and has no floor (`--rpo 800ms` exits 0).
  - `lakeNote`'s two variants match the ADR's text blocks line for line, with `<R>` in ADR-0194's form.
  - `--keep` and `-f` stay refused for `lake` (`TestFlagRules` rows kept).
- **`Apply`'s contract** is `([]byte, error)`; `Change` is gone from the package; a key already at its value returns
  the bytes unchanged.
- **Docs follow the decision.** `examples/backup-lifecycle.md`, the `lake.saveInterval` comment in
  `examples/funcdconfig.yaml`, and the `SaveInterval` doc in `internal/platform/config/config.go` say the key is set
  by hand. F112 drops "or writes them", F200 is at `reviewing`, and the four back-links are in place.
- **The test renames carry the scenarios.** `TestScenarioPlanShowsVerifyJob` names both ADR-0223's
  platform-from-objectives and ADR-0286's plan-shows-verify-job. `TestEngineReadsTheLakeKey` keeps ADR-0274's
  engine-key contract under an honest name.
- The `MinLakeSaveInterval`/`MaxLakeSaveInterval` bounds stay in use (`internal/platform/config/lake.go:31`,
  `pkg/funcd/options.go:567`): nothing dead is left behind.

### Definition of Done

7 / 7 hold: the six Review-checklist items and implementation plan step 5's checks (race tests, vet and lint on both
tags, build). Misses: none; the two Minors are coverage placement, not a missing behavior.

### Model scorecard

Not recorded here: the DR session records the batch's ledger rows in one commit (claude-opus-5-5 on ADR-0286,
implementation → pass, 0/0/2, 2 model-attributed, DoD 7/7).

### Recommendation

Pass: stamp ADR-0286 `Reviewing → Implemented` and F200 → `implemented`. The two Minors can be folded before the stamp
or left; neither changes the verdict.
