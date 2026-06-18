## Verdict: pass — 0 blockers, 0 majors  (ADR-0042 implementation, model: claude-opus-4-8)

ADR-0042 adopts spf13/cobra for `funcdcli` (cobra root + 8 verb subcommands, persistent
`--server`/`--token`, `*sdk.Client` built lazily and only for the 4 control-plane verbs) and
`funcd` (cobra root `RunE`=serve + `version` subcommand), refining ADR-0024's stdlib CLI. The
implementation is faithful, fully verified by running build/vet/lint/test + behavior smoke, and
meets the Definition of Done.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **Stray untracked `funcdcli` binary** · attribution: model/env · evidence: `git status --short`
  showed `?? funcdcli` (a 10 MB build artifact, timestamp 12:08, `git check-ignore` → NOT-IGNORED).
  Not part of the implementation surface and not committed, but an un-gitignored compiled binary
  risks an accidental commit. Removed during review. Suggest adding `funcdcli`/`funcd` build
  outputs to `.gitignore` (a hygiene nit, not a code defect). Does not affect the verdict.
- **ADR prose says "promote both [cobra + pflag] to direct"** · attribution: adr · evidence:
  `go.mod:265` keeps `spf13/pflag v1.0.10 // indirect`. This is *correct* Go module hygiene — the
  cmds import only cobra (which wraps pflag); they never import pflag directly, so `go mod tidy`
  correctly leaves it indirect (`TIDY_CLEAN=yes`, no diff). The model did the right thing; the
  ADR's "promote both" is at most a doc nit on an already-Implemented-bound ADR. Not the model's
  fault, not scored, no rework owed.

### ✅ Verified correct (keep it)
- **Builds / vet**: `go build ./...` → `BUILD_EXIT=0`; `go vet ./cmd/...` → `VET_EXIT=0`.
- **Lint**: `go tool golangci-lint run ./cmd/funcdcli/... ./cmd/funcd/...` → `0 issues` (`LINT_EXIT=0`).
- **Tests**: `go test ./cmd/funcdcli/ ./cmd/funcd/ -count=1` → both `ok` (`TEST_EXIT=0`); full
  suite `go test ./... -count=1` → `WIDE_TEST_EXIT=0` (no wider regression; node-gated tests run
  green here).
- **Modules**: `go mod verify` → `all modules verified`; `go mod tidy` → no diff (`TIDY_CLEAN=yes`).
  `spf13/cobra v1.10.2` is direct (`go.mod:18`); `spf13/pflag` correctly stays indirect.
- **Scenario coverage** (all ADR-0042 scenarios → passing tests, none weakened):
  - `cli-verbs` → `TestScenarioCLIApplyThenGet`, `TestScenarioCLIDelete`,
    `TestScenarioCLIValidatesBeforeApply`, `TestScenarioCLIPushPullRoundtrip` — the ADR-0024 verb
    scenarios re-expressed through the cobra root; the `fault.Invalid`/`fault.NotFound` kind
    assertions are preserved verbatim (`cli_test.go:102,115,121`).
  - `cli-flag-compat` + `cli-unknown-command` → `TestScenarioCLIFlagCompatAndUnknown` (long
    `--namespace` works alongside `-n`; unknown verb errors).
  - `cli-help-and-completion` → `TestScenarioCLIHelpAndCompletion` (asserts all 8 verbs +
    `completion` in `--help`).
  - `daemon-version-and-serve` → `TestDaemonVersion`; the serve half (root `RunE`=`serve`,
    unchanged startup) is exercised by the existing `TestDaemonExecutesFunction`.
- **Behavior smoke** (the real proof cobra didn't regress UX):
  - `go run ./cmd/funcdcli --help` lists the 8 verbs + `completion` + persistent
    `--server`/`--token` (`HELP_EXIT=0`).
  - `go run ./cmd/funcd version` → `funcd dev (commit none, built unknown, go1.26.4, darwin/arm64)`
    (`VERSION_EXIT=0`).
  - `go run ./cmd/funcdcli push /tmp/smoke.js oci-layout://…:v1` (NO `--server`) →
    `…:v1@sha256:225d78…` (`PUSH_EXIT=0`) — the artifact-verb-needs-no-server scoping is real: the
    SDK client is built in each control-plane verb's `RunE` (`cli.go:72,91,143,169`), not in a
    blanket `PersistentPreRunE`, so push/pull/login/logout never require a server.
  - A control-plane verb honors `-n`/`--namespace`: `get function --server http://127.0.0.1:1 -n
    team-a` issues `GET …/namespaces/team-a/functions` (path carries the namespace) before failing
    on the unreachable server; unknown verb → `unknown command "frobnicate"`, exit 1.
- **Contracts**: `newRootCmd(out io.Writer) *cobra.Command` present in both cmds
  (`cmd/funcdcli/cli.go:28`, `cmd/funcd/main.go:39`); `newRootCmdWith(out, client)` is the test
  seam; one subcommand per verb reusing the existing SDK/artifact logic; daemon root
  `RunE`=`serve`, `version` subcommand → `version.Get().String()`.
- **No SDK/API change**: `pkg/sdk` is unchanged by this branch's working tree (the `pkg/sdk` diff
  vs `main` is entirely ADR-0024's already-landed work, not ADR-0042's); the changed-file set is
  exactly `cmd/funcd/{main,main_test}.go`, `cmd/funcdcli/{cli,main,cli_test}.go`, `go.mod`, and the
  feat row.
- **ADR-0002 conventions**: no bare `fmt.Print*` (verbs use `fmt.Fprintf`/`Fprintln`; the `writef`
  helper is the sanctioned printf form, `cli.go:309`); no `panic(`; no globals/`var` blocks;
  `api/fault` errors preserved and wrapped (`fault.Invalidf`/`fault.Wrapf`/`fault.KindOf`); the
  `//nolint:gosec` on operator-supplied `os.ReadFile` kept (`cli.go:270`); `SilenceUsage`/
  `SilenceErrors` set so funcd's own error text prints once.
- **`isVersionArg` removed**: `grep -rn isVersionArg cmd/` → not present.
- **Hygiene**: identity grep on all changed files clean (no local username/paths). ADR-0024 is
  frozen — `git diff HEAD -- docs/adr/0024-funcdcli-and-sdk.md` shows no change (refined via this
  new ADR, not edited). ADR-0042 substance unchanged at `Reviewing` (only the status bump remains).
  No blueprint sync owed: this is a CLI-shell dispatch/parse change, not an architecture change.
- **License**: cobra Apache-2.0, pflag BSD-3-Clause — both already vendored.

### Definition of Done
5 / 5 ADR Review-checklist items hold (cobra root + 8 verbs + persistent `--server`/`--token` +
testable `newRootCmd`; short flags + long forms + `--help` + `completion`; `funcd` cobra root
`RunE`=serve + `version`, `isVersionArg` gone; cobra direct + pflag, `go mod verify`/`tidy` clean,
no new runtime SDK dep; ADR-0024 verb scenarios pass through the cobra root, no SDK/API change, no
identity/path leak). Generic DoD: full suite green, every scenario un-skipped and passing, real
behavior (no stubs), contracts honoured, tree matches surface, conventions hold, deps sanctioned,
no scope creep, tracking consistent. No misses. The single `adr`-attributed prose nit (pflag
"promote both") does not reduce the count — the model's choice is the correct one.

### Model scorecard
Recorded: claude-opus-4-8 on ADR-0042 (implementation) → pass, 0 blockers / 0 majors / 2 minors,
0 model-attributed (1 minor model/env hygiene nit + 1 adr doc nit, neither blocking), DoD 5/5.
See docs/reviews/model-scorecard.md.

### Recommendation
**Pass.** Stamp ADR-0042 `Reviewing → Implemented`; F18 + F19 stay `implemented` (multi-ADR rows
already linked to ADR-0042). Optional cleanup (not a gate): add the `funcdcli`/`funcd` build
outputs to `.gitignore`. The "promote both deps" wording in ADR-0042's prose is a harmless doc
artifact — no superseding ADR is warranted for a one-word hygiene nit.
