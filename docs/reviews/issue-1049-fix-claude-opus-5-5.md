## Verdict: pass — 0 blockers, 0 majors, 3 minors  (issue #1049 fix, model: claude-opus-5-5)

Self-review: the fixer ran this review, because the decider asked that fixes be finished without review agents. Every
claim below comes from a command that ran.

Change: branch `fix/1049-bundle-push-entry`, commit 78f4a706 `fix(funcdctl): take a bundle push's entry from its
funcdctl.yaml when --entry is not given`, on `origin/main` c47fc1a6 (4 files, +82/−24; 48 of the added lines are the
test). With a funcdctl.yaml in the bundle directory, `pushCmd` passed the `--entry` value, `handler.py` by default, to
`pushFromManifest` and on to `artifact.PushBundle`, and nothing read the manifest's `main` or its runtime. Now
`pushEntry` (`cmd/funcdctl/manifest.go`) returns `--entry` when the command line sets it, else the file `funcdctl dev`
runs for the same manifest. It calls dev's `manifestEntry` and `defaultEntry`, which move unchanged from the dev-tagged
`dev.go` to `manifest.go`, so the release build has them.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1 — `pushFromManifest`'s `entryFlag` parameter now carries the resolved entry** · attribution: `model`.
  The name stays because the ADR-0243 implementation (not merged yet) rewrites that function's body and passes
  `entryFlag` to a second push call, so a rename here conflicts with it. `git merge-tree` of the two commits is clean,
  and the merged tree passes `TestIssue1049_BundlePushTakesEntryFromManifest` and `TestScenarioPushHTTPFunction`.
  Rename the parameter once both are on main.
- **Minor 2 — `pushEntry` also runs for a single-file push, which ignores the entry** · attribution: `model`. The call
  is pure and cheap. Keeping it in one expression in `pushCmd` leaves that function's size and complexity unchanged:
  the first version branched there, and the bloat audit reported funlen 83 (over 80), gocognit 40 → 43 and gocyclo
  25 → 26 for `pushCmd`.
- **Minor 3 — dev and push rank an explicit `--entry` differently when `main` is set** · attribution: `adr`. Push
  lets `--entry` win, as the issue asks. `funcdctl dev` passes its `--entry` to `manifestEntry` as the default, which
  `main` overrides (`resolveDirFunctions`, `cmd/funcdctl/dev.go:1260-1264`). A Node bundle pushed without a
  funcdctl.yaml also keeps ADR-0089's `handler.py` default. Changing either is a decision, so both are recorded here
  and left as they are.

### ✅ Verified correct (keep it)

- **The issue reproduces and the fix removes its cause.** A funcdctl built from c47fc1a6 failed both of the issue's
  pushes with `entry "handler.py" is not a regular file in bundle`. The fixed build pushes both, and the Node bundle's
  digest (`sha256:fb6fa087…`) equals the one the `--entry handler.mjs` workaround gives on main.
- **The test fails without the fix, for the reported reason.** An overlay of `cli.go` and `manifest.go` from
  `origin/main` (with `dev.go` too under `-tags dev`) fails the node runtime default, main and nested main cases with
  that error, in both builds. The python runtime default, explicit entry and no-manifest cases pass on both trees:
  they guard what must not change.
- **The mutants are killed.** M1 inverts `Changed("entry")` and fails 4 cases. M2 returns `handler.py` for every
  runtime and fails the node runtime default. M3 drops the main's directory and fails nested main. M4 ignores `main`
  and fails main and nested main.
- **Reuse.** The entry rule exists once: push calls the `manifestEntry` and `defaultEntry` that dev calls. The test
  reuses `execCLI`, `writeFuncdctl` and the push-then-pull path of `platform_test.go`. `pullBundle` resolves the
  handler from the bundle layer's entry annotation, so the pulled path names the pushed entry. No new dependency.
- **Scope and siblings.** Every hunk serves #1049: the helper, the moved functions, one call in `pushCmd`, the
  `--entry` help and the test. The other bundle push (`cli.go`, without a manifest) keeps the default ADR-0089
  decides. `pullBundle`'s `handler.py` fallback covers only an artifact without an entry annotation, which
  `PushBundle` never writes. The lanes and `bundle-multiarch.yml` pass `--entry`, so their pushes do not change.
- **ADRs and docs.** ADR-0122 decides nothing about the bundle's entry file. ADR-0144 Decision 2 (the bundle's
  manifest `main` names the bundled handler) now holds for push as well. No ADR file is touched, and the `--entry`
  help names the new default.
- **Checks** (all through `scripts/agent/d`): `go test -race` of `./cmd/funcdctl/` passes in the default and the
  `dev` build, `go vet` passes in both, and `golangci-lint` reports `0 issues.` on `./cmd/funcdctl/...` and, with
  `--build-tags dev`, on the Justfile's `_dev-packages`. `scripts/agent/gate.sh` passed on 78f4a706: the bloat
  audit (0 hard flags, no lint deltas), `just ci-full` with e2e (470 s), the Linux build, vet and lint, the Linux
  tests in the Lima VM (37 s), and an unchanged tree.

### Definition of Done

12 / 12 items hold.

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue1049_…` reproduces the issue | yes: six cases, three of them the issue's failures |
| 2 | Fails on the pre-fix code, for the reason | yes: `entry "handler.py" is not a regular file`, both builds |
| 3 | Passes with the fix, un-skipped, `-race` | yes |
| 4 | Reverting or mutating the key lines fails a test | yes: revert and M1–M4 |
| 5 | Root cause fixed, not masked | yes |
| 6 | Scope only, no test weakened | yes |
| 7 | No ADR contradicted or edited; living docs true | yes |
| 8 | Build, vet, lint (host and Linux), tests green | yes |
| 9 | Conventions | yes |
| 10 | Reuse | yes |
| 11 | Commit shape | yes: `fix(funcdctl):`, `Fixes #1049`, trailer |
| 12 | Every case fixed and no sibling left | yes |

### Model scorecard

To record: claude-opus-5-5 on issue #1049 (fix) → pass, 0/0/3, 2 model-attributed, DoD 12/12.
See docs/reviews/model-scorecard.md.

### Recommendation

Ship it. The rename in Minor 1 can follow once the ADR-0243 implementation is merged.
