## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0319 implementation, model: claude-opus-5-5)

This report reviews branch `feat/funcdctl-plugins`: the implementation commit `feat(funcdctl): funcdctl runs
kubectl-style plugins found on PATH` and the review's test fix `test(funcdctl): a plugin's FUNCDCTL is funcdctl's own
path when argv[0] is a bare name`, on the acceptance commit.

**This review is not independent.** The model that built the implementation ran it, in the same session, because the
decider's standing rule is no review agent. Every check below was run, not read, but the judgement calls are the
builder's own.

### 🟡 Major / Minor
- **Minor 1: a mutant reading `FUNCDCTL` from argv[0] survived** · attribution: `model` · fixed in the test commit.
  The overlay mutant `exe, err := os.Args[0], error(nil)` in `findPlugin` passed every `TestPlugin` test, because each
  test started funcdctl by its absolute path. A shell starts funcdctl by its bare name, and the plugin would then be
  handed `funcdctl` instead of the binary it must call back (ADR-0319 Decision 4). The environment test now starts
  funcdctl with a bare argv[0] and expects the binary's absolute path. The mutant now fails it with `FUNCDCTL is
  "funcdctl" when argv[0] is a bare name`.

### Routed (not this work's)
- The blueprint's `### funcdctl (separate CLI)` says funcdctl never depends on `internal/`. On main it already imports
  `internal/artifact`, `internal/backup/…`, `internal/blob` and more, and it now reads the version stamp from
  `internal/platform/version`, as the ADR states · home: elsewhere, a drift of the blueprint · destination: the
  stale-docs sweep.

### ✅ Verified correct (keep it)
- **Builds and lint**: `go build ./...` and `go build -tags dev ./cmd/funcdctl` exit 0. `go vet` on `cmd/funcdctl`
  (default and `-tags dev`) and `scripts/behavior` is clean, and `golangci-lint` reports 0 issues on both builds.
- **Tests**: `go test -race ./cmd/funcdctl/` passes for the whole package on the default build, and the plugin tests
  pass on the `-tags dev` build. Each of the ten scenario tests runs un-skipped and carries its `// scenario:` marker.
- **Mutants** on `cmd/funcdctl/plugin.go`, each run as an overlay, and each killed:
  - no plain-word check: three tests fail;
  - no built-in check: `TestPlugin_BuiltInCommandsWin`;
  - interspersed flag parsing: two tests;
  - an inherited token kept after `--token ""`: the environment test;
  - no earlier-entry warning: `TestPluginList_…`;
  - a child process instead of the exec: the PID check of `TestPlugin_ReplacesTheFuncdctlProcess`;
  - after the fix, the argv[0] mutant.
- **Contracts**: `findPlugin`, `pluginEnv`, `runPlugin`, `plainWord`, `isBuiltin`, `pluginCmd` and `listPlugins` match
  the ADR's signatures. `main` resolves a plugin before `Execute`, and only `runPlugin` execs. The four variables match
  the Contracts table, an empty token removes `FUNCD_TOKEN`, and a failed exec reads `funcdctl <name>: run the plugin
  …: exec format error` with exit 1.
- **The real binary on two systems**: experiment 0017 ran this exact code (its `funcd.patch` equals the branch's
  implementation byte for byte) with the built funcdctl on macOS and on the lane VM's installed funcdctl against a real
  funcd. Every check passed.
- **Tree against the Implementation plan**: every listed file, plus the chapter entry in `scripts/behavior/main.go`,
  where the registry defines its chapters.
- **Docs and tracking**: `docs/behavior/funcdctl.md` holds seven `decided` rows, and the index lists the chapter.
  `just check-behavior`, `just check-hygiene` and the lane guards (`go test -run Lane ./cmd/funcd/
  ./internal/testkit/langmod/ ./scripts/`) pass. F233 is at `reviewing`, and the ADR's only edit since its acceptance
  is the status line.
- **Lanes**: `scripts/agent/lanes.sh feat/funcdctl-plugins:all` at the implementation commit: all 14 lanes passed on one shared VM, `funcdctl-plugin` included, with the VM's reset and leak check after it (1003 s). The app-todo lane runs before the merge, and the PR records its result
- **Gate**: `scripts/agent/gate.sh` on the final code: GATE PASS in 1134 s. Its steps were the audit, `ci-full` with the e2e suite (865 s), the Linux build, vet and lint, the stress step (each of the ten plugin tests alone under `-race`, up to 20 runs, 246 s) and an unchanged tree. `linux-tests` printed SKIP because the funcd-linux-tests VM was not running; CI runs the tests on Linux

### Definition of Done
16 / 16 items hold: the ADR's seven Review-checklist items and nine items of the generic DoD. The contract-suite item
does not apply, because the ADR adds no port.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0319 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 16/16. See
docs/reviews/model-scorecard.md.

### Recommendation
Stamp it `Implemented`. The PR carries `Fixes #1421`.
