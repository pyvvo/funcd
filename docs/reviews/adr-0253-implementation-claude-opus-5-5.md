## Verdict: changes requested — 0 blockers, 1 major, 3 minors  (ADR-0253 implementation, model: claude-opus-5-5)

This reviews pyvvo/funcd#1112, branch `feat/adr-0253-app-dev-mode` at `aa79f197` on main `26b406c7`: `cmd/funcdctl`
only, +1620 −24 over 9 files.

### 🟡 Major 1 — a dot-entry inside `resources/` stops dev  ·  attribution: model

- Decision 3: dev "skips only dot-entries and its persist dir inside the template directory; any other entry stops
  it, as `template.Load` refuses it".
- `loadTemplate` (`cmd/funcdctl/dev_app.go:258-289`) skips dot-entries at the template's top level only, then copies
  `resources/` whole with `os.CopyFS` (`:273`). `template.Load` refuses any entry of `resources/` that is not a `.yaml`
  file (`internal/app/template/load.go:103-104`).
- Probe on `aa79f197`, a throwaway test that was not committed: with `resources/.api.yaml.swp` in a copy of the todo
  App, `loadTemplate` fails with `app.load: resources/.api.yaml.swp: resources/ holds only .yaml files, one level`.
- Effect: while an editor holds a resource open, its swap file in `resources/` fails every reload. A Finder
  `.DS_Store` in `resources/` stops dev at start. `TestAppDevTemplateEntries` (`dev_app_test.go:620`) covers only
  top-level entries.
- Fix: skip dot-entries when copying `resources/` into the snapshot, and add that case to `TestAppDevTemplateEntries`.

### Minor

1. `loadTemplate` copies `app.yaml` through `os.CopyFS` on a single file, then again with `ReadFile` and `WriteFile`
   when the entry is not a directory (`dev_app.go:272-283`). That is two copies of one file in a branch that reads
   backwards. Copy the file one way and the directory the other. · model
2. A failed reload that is not an admission refusal, such as a render error or a Secret key gone from the
   environment, has no test. The checklist's "a failed reload leaves dev running and the last App serving" is shown
   only by `TestScenarioAppDevRefusedApply`. · model
3. Two comment lines (`dev_folder.go:65`, `dev_test.go:197`) and ADR-0253's Status line run past the 120 columns these
   files wrap at. · model

### ✅ Verified correct (keep it)

- **Checks on `aa79f197`:**
  - `go build ./...` and `go build -tags dev ./cmd/funcdctl` pass.
  - `go vet` passes with and without `-tags dev`.
  - `golangci-lint` reports 0 issues with and without `--build-tags dev`.
  - `go test ./cmd/funcdctl/` (4.4 s) and `go test -race -tags dev ./cmd/funcdctl/` (56 s) pass.
  - The builder's `scripts/agent/gate.sh` passed, its Linux tests skipped because the VM was down (env).
- **Decision by decision:**
  - Decision 1: the template check, with the App-mode flags refused on a folder.
  - Decision 2: the build lookup, `key=path`, a Site only by `key=path`, and the stand-in render.
  - Decision 3: a push per fingerprint change, `0.0.0-dev` for a range, a Python entry pushed from a copy, `app.lock`
    skipped, and other top-level entries refused.
  - Decision 4: hooks dropped unless `--hooks`, and no synthesized grant.
  - Decision 5: the oras materializer, with catalogs and SQL wired through the platform folder.
  - Decision 6: only the Secrets that no `--apply` document defines (`withoutDefined`).
  - Decision 7: `--apply` documents read as folder documents, applied first, pruned after a full apply, and pruned
    across `--persist` by the dev tag.
  - Decision 8: the apply as `app deploy` does it (the stored `spec.paused` kept, an unchanged spec skipped), the
    100 ms follow, and the newest revision winning.
  - Decision 9: the 300 ms reload over every input.
  - Decision 10: `requires` and SQL refused.
  - Decision 11: the App banner, and `--print-env` with the `dev` identity for an App with no function part.
- **The builder's two calls hold.** `waitDeployEvery` keeps `app deploy`'s 1 s poll, since `waitDeploy` wraps it,
  and stays inside `cmd/funcdctl`. Pruning an `--apply` document only after everything applied keeps a document that
  a failing apply still needs.
- **Scenarios:** one test per scenario, named after it, on copies of the pinned todo and site-config Apps. The
  assertions are observable: `/api`'s answers, the hook events, the revision numbers, the banner, the grant's tag and
  its deletion, and the refusal messages.
- **Tracking:** ADR-0253 changed only its status line, F164 is `reviewing`, and the change stays in `cmd/funcdctl`.

### Definition of Done

9 / 11 hold (Review checklist 9 + Done-when 2). Misses: Decision 3's rule (Major 1, model), and the live check, which
coworker-3 is running (pending, not scored).

### Model scorecard

To record at the stamp: claude-opus-5-5 on ADR-0253 (implementation) → changes-requested, 0/1/3, 4
model-attributed, DoD 9/11.

### Recommendation

Fix Major 1 on the branch, and the Minors if cheap. I re-review the delta, and stamp on a pass once the live check is
recorded.
