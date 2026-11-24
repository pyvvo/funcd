## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0346 implementation, round 3, model: claude-opus-5-5)

Round 3 reviews d6890ffa, the one commit on a51e81a1, against round 2's report
([adr-0346-implementation-claude-opus-5-5-2.md](adr-0346-implementation-claude-opus-5-5-2.md)). The commit changes
one condition in `imageWorkerSpec`, a test and a behavior row. So the checks of rounds 1 and 2 stand for the rest of
the work, and every check below was run at d6890ffa, not read.

### Round 2's finding

- **Minor 1, a developer's config key named `DUCKDB_EXTENSION_DIRECTORY`: closed.**
  - `imageWorkerSpec` now drops the key only when it holds dev's folder (`internal/function/addonruntime.go:154-156`).
  - `TestDevAddonWorkerSpec` adds `configured`, a Function on `deno2` that binds a ConfigMap whose key holds
    `/srv/duckdb`, and requires the container spec to keep that value.
  - The row now says that a bound config key of that name stays (ADR-0306 Decision 3), and `just check-behavior`
    passes.
- **The probes.**
  - Round 2's probe, a ConfigMap key and no Lake: the container worker keeps the key, as the process worker does.
  - Round 1's probe, a Lake with dev's folder: the container worker drops the folder, and its Lake URL names
    `funcd.host`.
  - A new probe, an empty-valued key with no dev folder: the container worker keeps the key.

### Mutants of the new condition

- Dropping the equality check, inverting it, or never deleting each fails `TestDevAddonWorkerSpec`.
- **Dropping the non-empty guard survives the package, and is equivalent in practice.**
  - It differs only for a bound config key of that name whose value is empty, under a dev that extracted no extension
    folder. There, the mutant would drop the empty variable, and the shipped code keeps it, as the third probe shows.
  - No folder is dev's usual case when it starts without a Lake. Then the key is absent, and the mutant deletes
    nothing.
- Round 2's 33 mutants were caught at a51e81a1, except the equivalent M5d. This commit changes none of their lines but
  M14's, which the third mutant above replaces.

### ✅ Verified correct (keep it)

- **The checks**, run through `scripts/agent/d`:
  - `go build ./...` exits 0.
  - `go vet` exits 0, also with `-tags dev`.
  - `golangci-lint` reports 0 issues, also with `--build-tags dev` and for Linux.
  - gofmt is clean, `check-behavior` passes and `check-hygiene` is clean.
  - `go test -race ./internal/function/` passes.
  - On colima's Docker, the six dev scenarios give five passes and one skip: the Linux one, by OS.
- **The whole work**, from rounds 1 and 2:
  - Decisions 1 to 8, with the builder's eight choices sound.
  - Six scenario tests, which fail under `CI` when they cannot run.
  - The registry package, which adds no duplication.
  - The ADR-0351 back-link, and the delegated rule on the extension folder.

### Definition of Done

20 of 20 items hold:
- the 5 Review-checklist items;
- the 5 plan items: one test per scenario, the docs, `just ci-full` through the checks of the three rounds (the gate
  runs it in full), a Job on Docker on macOS, and the Linux run, which `mustRunOnCI` enforces on the PR's CI;
- the 10 generic items.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0346 (implementation, round 3) → pass, 0/0/0, DoD 20/20. See
`docs/reviews/model-scorecard.md`.

### Recommendation

ADR-0346 is stamped `Implemented`, and F139's dev-workers part reads `implemented`. Its shim-protocol part stays
`idea`. The PR that ships the branch references #1017 and does not close it: the last of F139's ADRs closes it.
