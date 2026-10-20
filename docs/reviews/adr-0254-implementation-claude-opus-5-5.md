## Verdict: changes requested — 0 blockers, 1 major, 1 minor  (ADR-0254 implementation, model: claude-opus-5-5)

This reviews pyvvo/funcd#1113, branch `feat/adr-0254-dev-config` at `d99752c8`, stacked on #1112 at `aa79f197`:
+1039 −27 over 10 files, the code in `cmd/funcdctl` (`devconfig.go` and its tests are new).

### 🟡 Major 1 — App mode runs production's cadences  ·  attribution: model

- Decision 4: dev's defaults are `250ms`, `1s` and `1s` "in both modes".
- `devPlatformOptions` lists `WithPacing`, `WithBlobPollInterval` and `WithFunclog` in folder mode's options
  (`cmd/funcdctl/dev.go:985-988`). The App-mode branch then replaces the whole list with its oras one
  (`:990-997`), so an App never gets them.
- Probe on `d99752c8`, a throwaway test that was not committed: in the todo App, a TS build saved right after another
  serves after 2.01 s, production's 2 s `handOutSettle`.
- The live check (experiment 0001, coworker-3, posted on the PR) shows the same on all three: upload-to-intake median
  14.91 s, a back-to-back save 2.05 s, a `funcdctl logs` line about 30 s after the attach.
- Why the tests missed it: the scenario's Given is the todo App, but `TestScenarioDevConfigCadences` and
  `TestScenarioDevConfigCadencesLogsAndSaves` run a folder (`devProject`), so they time folder mode only.
- Fix: pass the three options after the mode switch, and add a cadence test in App mode.

### Minor

1. Three comment lines and ADR-0254's Status line (148 columns) run past the 120 columns these files wrap at:
   `dev.go:343-344` (the `declaredSecrets` comment) and `devconfig_test.go:49`. · model

### ✅ Verified correct (keep it)

- **Checks on `d99752c8`:**
  - `go build ./...` passes, and so does the `-tags dev` build of `cmd/funcdctl`.
  - `go vet` passes with and without `-tags dev`.
  - `golangci-lint` reports 0 issues with and without `--build-tags dev`.
  - `go mod tidy -diff` is clean: `spf13/pflag` moved to a direct requirement.
  - `go test ./cmd/funcdctl/` (4.5 s) and `go test -race -tags dev ./cmd/funcdctl/` (79.8 s) pass.
- **Decision by decision:**
  - Decision 1: the file is read from the directory dev starts in. The decode is strict (a root key other than `dev`,
    an unknown `dev` key, a `platform` key other than the three), and its paths are relative to the file's
    directory. Folder mode skips the file as a document that is not funcd's.
  - Decisions 2 and 3: each key acts as its flag, and cobra's `Changed` lets a given flag replace its key, a
    repeatable one the whole list. `values`, `build`, `hooks` and `apply` set without a template `dev.dir` stop dev,
    naming the key. A non-template dir argument ignores them.
  - Decision 4: `platform` takes only the three keys, each a positive duration. A zero `Pacing` field is its
    component's default (`pkg/funcd/options.go:810`), so setting only `HandOutSettle` changes nothing else.
    Production's 2 s, 15 s and 10 s are gone in folder mode.
  - Decision 5: one lookup (`devSecretSources.lookup`) for both modes: the shell, then `envFile`, then `env`. The
    message names the three places, and a missing `envFile` reads as empty. The `envFile` format follows the ADR.
  - Decision 6: the file and its `envFile`'s contents are reload inputs in both modes. `build`, `values`, `apply`,
    `env` and the `envFile` reload, another key prints the restart line, and a file that fails to decode is printed
    while the last good keys stay. A probe of the decoder on empty, comment-only, null and multi-document inputs
    found no panic.
- **Tests:** one per scenario, named after it, plus table tests for the strict decode, the precedence and the
  `envFile` format. `funcdctl dev --help` names the file and every key.
- **Tracking:** ADR-0254 changed only its status line, F165 is `reviewing`, and the PROJECT-SUMMARY row says
  Reviewing.

### Definition of Done

7 / 8 hold (Review checklist 6 + Done-when 2). Miss: checklist item 3, the defaults in both modes (Major 1, model).
The live check is recorded on the PR, and it shows Major 1.

### Model scorecard

To record at the stamp: claude-opus-5-5 on ADR-0254 (implementation) → changes-requested, 0/1/1, 2 model-attributed,
DoD 7/8.

### Recommendation

Fix Major 1 on the branch, and the Minor if cheap. I re-review the delta, and stamp on a pass once the live check
shows dev's cadences in App mode.
