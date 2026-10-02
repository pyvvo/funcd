# ADR-0145 Implementation Review — Multi-arch function bundles and arch-aware placement (F106)

**Verdict**: **pass**. Every Contract matches the ADR, every non-e2e scenario has a named test that passes, the four
sub-checks, the Linux lint, the hygiene check and the e2e suite are green, and I re-ran `just act-bundle` myself: it
built, pushed and indexed both platforms and left the checkout unchanged. The findings are six Minors; none blocks
sign-off.

**Producing model**: claude-opus-5-5
**Reviewed against**: ADR-0145 Contracts / Scenarios / Implementation plan / Review checklist / Definition of done ·
ADR-0017 · ADR-0031 · ADR-0035 · ADR-0046 · ADR-0143 · ADR-0002 · blueprint.md · FEAT-0001/F106

The work is the uncommitted tree on `feat/toolchain-bundling-multi-arch` on top of `7d93eeb` (ADR-0144): 38 modified
files and 8 new ones (`api/types/v1alpha1/platform.go`, `internal/artifact/platform.go`, their tests,
`internal/function/platform_test.go`, `cmd/funcdctl/platform_test.go`, `.github/workflows/bundle-multiarch.yml` and
the ADR file). The blueprint's ADR-0145 sentence was already committed with `7d93eeb`.

## Verdict: pass — 0 blockers, 0 majors  (ADR-0145 implementation, model: claude-opus-5-5)

### Minor
- **Minor · model — `internal/function/pool.go` is not gofmt-clean.** The new `"errors"` import sits between
  `"context"` and `"crypto/sha256"`. `gofmt -l` over the changed Go files lists this one file. golangci-lint has no
  formatter enabled, so the four sub-checks pass, but `just ci` runs `go fmt ./...` and then fails on any Go diff.
  The lefthook pre-commit hook (`gofmt -w`, `stage_fixed`) would repair it at commit time. **Fix**: `go fmt ./...`.
- **Minor · model — no test covers `Platforms` on the solo `Schedule` call.** Decision 5 ("Later calls") and checklist
  item 6 require every solo `Schedule` call to carry the list; the code does (`function.go:895`). I mutated that call
  to send an empty list, and the whole `internal/function` platform suite still passed. The step-2b gate refuses a
  mismatch first, so the outcome cannot show the difference. **Fix**: assert the requests in a recording scheduler.
- **Minor · model — the QEMU step is not the one Decision 7 names.** Decision 7 says the job registers QEMU with
  `docker/setup-qemu-action`; `bundle-multiarch.yml` runs `docker run --privileged --rm tonistiigi/binfmt --install
  amd64,arm64`, the image that action wraps. It works (amd64 emulation was registered and the amd64 bundle carries
  `linux_amd64` extensions), but the deviation is not recorded anywhere. **Fix**: use the action, or record why the
  plain `docker run` is preferred under act.
- **Minor · adr — Decision 5's literal `gateFailure` contradicts the Contracts' status message.** Decision 5 sets
  `message: err.Error()` and `readyMessage: err.Error()`. The Contracts (and scenario function-no-matching-platform)
  require the message `artifact provides [<list>]; node <name> runs <platform>`. `err.Error()` of the single-node
  driver's fault is `scheduler.singlenode.Schedule: artifact provides [...]; node ... runs ...: no worker node matches
  the artifact's platforms`, so both cannot hold. The builder kept the reason, the phase and `message == readyMessage`,
  and took the text from the fault's `Msg` (`placementMessage`, `function.go:1550`), which meets the Contracts; the
  test asserts the exact Contracts text. **Fix (ADR)**: a superseding note should state the message source.
- **Minor · adr — after a restart, the gate needs the registry before an already-materialized Function reconciles.**
  The resolver caches platforms in memory per digest (`artifact.go:439`). Before ADR-0145 a reconcile of a Function
  whose artifact was already in the on-disk cache needed no registry. Now the first reconcile after a restart (outside
  ADR-0142's steady-state path) fetches the manifest, and a registry outage becomes a retried reconcile error, as
  Decision 5 prescribes. Local OCI layouts are unaffected. **Fix (ADR)**: a later ADR may persist the platforms beside
  the artifact cache or read them from the cached manifest.
- **Minor · env — there is no committed Accepted ADR text to diff.** The ADR file is untracked, as for ADR-0142,
  ADR-0143 and ADR-0144. The freeze check rests on the header (Accepted, then the `Reviewing` stamp of `adr-impl`);
  the Decision, Contracts and checklist match the code.

### ✅ Verified correct (keep it)
- **Checks**, run by me through the pinned dev shell:
  - `go build ./... && go tool golangci-lint run ./... && go test ./... && go mod verify`: exit 0, lint "0 issues.",
    79 packages `ok`, "all modules verified";
  - `GOOS=linux $(go tool -n golangci-lint) run ./...`: exit 0, "0 issues.";
  - `just check-hygiene`: exit 0 ("hygiene: clean");
  - `go test -tags e2e ./pkg/funcd/ ./tests/... -count=1 -timeout 30m`: exit 0 — `pkg/funcd` 111.8 s,
    `tests/chaos` 20.7 s, `tests/e2e` 5.0 s, `tests/lint-fixtures` 5.5 s. These run the unannotated single-manifest
    path end to end with the gate wired;
  - `just act-bundle` (re-run by me): exit 0, "Job succeeded"; the index and both children were pushed, the
    inspect step printed the contract through the index, and each pull holds its own platform's files
    (`_duckdb.cpython-314-x86_64-linux-gnu.so` + `duckdb-ext/v1.5.4/linux_amd64`; `…-aarch64-linux-gnu.so` +
    `linux_arm64`). `git status --short --ignored` was identical before and after the run. The builder's own log
    shows the same result;
  - `just lima-example duckdb` (run by me, a linux/arm64 Debian VM with containerd): exit 0, `final status: PASS`.
    The lane pushes catalog-quack without `--platform`, so it covers the existing single-manifest path on Linux with
    the gate wired and the new cache key. I did not re-run the other eight lanes.
- **Scenario → test**, every test un-skipped and passing:

  | Scenario | Test |
  |---|---|
  | push-records-platform | `TestScenarioPushRecordsPlatform`; `TestCLIPushPlatformAndIndex` (funcdctl.yaml and `--schema` paths) |
  | index-combines-platforms | `TestScenarioIndexCombinesPlatforms`, `TestIndexShape`; the CLI test checks the printed `<ref>@<digest>` |
  | index-rejects-inconsistent-sources | `TestScenarioIndexRejectsInconsistentSources` — repeated platform, missing annotation, different contract, runtime, kind, layout, missing source; all `fault.Invalid`, and the refused tag is not written |
  | pull-selects-node-platform | `TestScenarioPullSelectsNodePlatform` (two node platforms, one index digest; checks the bundle, the delivered contract and the cache key) |
  | pull-no-matching-platform | `TestScenarioPullNoMatchingPlatform` (`fault.NotFound`, "it provides [linux/amd64]") |
  | single-manifest-unchanged | `TestScenarioSingleManifestUnchanged` (no annotation, `Platforms` nil, pulls on amd64 and darwin/arm64) |
  | placement-filters-by-platform | `TestScenarioPlacementFiltersByPlatform`, plus the extended `schedulercontract.Run` |
  | function-no-matching-platform | `TestScenarioFunctionNoMatchingPlatform` (`Failed`, exact message, zero `Create`) |
  | redeploy-no-matching-platform-keeps-serving | `TestScenarioRedeployNoMatchingPlatformKeepsServing` |
  | pooled-member-no-matching-platform | `TestScenarioPooledMemberNoMatchingPlatform` (unlimited pool and a cap of one) |
  | inspect-index | `TestScenarioInspectIndex` (contract, index digest returned, runtime) |
  | multiarch-workflow-builds-index | `just act-bundle`, above |

  Also: `TestPlatformResolverErrorRequeues`, `TestIndexWithTheNodePlatformIsReady`, `TestOCIPlatformValidate`,
  `TestInvalidNodePlatformRejected`, `TestWithNodePlatform`, `TestCLIPushPlatformRefusals`.
- **Mutation check** (in a scratch copy, never the checkout): 10 mutations; 9 were killed by the named tests —
  removing the pool filter, removing the step-2b gate, dropping the platform from the cache key, selecting the first
  descriptor regardless of the node, skipping the contract or the repository check in `PushIndex`, ignoring
  `Platforms` in the single-node driver, not writing the annotation, and allowing `--platform` with `--site`. The
  survivor is the Minor above.
- **Contracts.** `v1.OCIPlatform` (constants, `Validate`, `OS`, `Arch`, `HostPlatform`); `PlatformAnnotation`,
  `IsArtifactPlatform`, `Push`/`PushBundle` with `platform`, `PushIndex`, `Pull` with `node`, `Platforms`,
  `NewOrasMaterializer(dir, node)` and the cached `(*OrasMaterializer).Platforms`; `scheduler.Request.Platforms`,
  `ErrNoMatchingPlatform`, `singlenode.New(local, node)` (`fault.Invalid` on an empty or malformed node);
  `schedulercontract.Run(t, s, node)` with the three required cases; `function.PlatformResolver` and
  `Deps.Platforms`; the three `funcdctl` command lines; `WithNodePlatform` — all match the ADR.
- **Decisions.**
  - Push: `--platform` is checked against `IsArtifactPlatform` before the path split, so it covers the single-file,
    bundle and funcdctl.yaml paths, and it is refused with `--site`.
  - Index: every source is read and checked before the single `Push` + `Tag`; the comparison uses the contract layer
    digest, the runtime annotation and the bundle layer; descriptors carry `platform` and the funcd `artifactType`,
    the index carries no funcd annotation.
  - Pull: selection by `os`/`architecture` (variant ignored), the child digest is checked, a miss lists the index's
    platforms (`unknown` entries excluded); the cache directory is `<digest>-<os>-<arch>`; `Inspect*` follow the
    first descriptor and return the index digest; `funcdctl pull --platform` defaults to `linux/<host arch>`.
  - Gate: step 2b sits after `ensureRevision` (and its drain) and before the shape gate and pooling's `assign`; the
    reason is `NoMatchingPlatform` with `phase: Failed` through `gateFailed`; a resolver error returns as a reconcile
    error and writes no status.
  - Pools: `sameKeyFunctions` — the single feed of `assign` and `admittedMembers` — skips a member whose
    `m.Spec.ImageDigest` lacks the node platform; the pool's own `Schedule` (`pool.go:264`) carries no list.
  - Composition root: one `nodePlatform` (default `v1.HostPlatform()`, validated by `WithNodePlatform`) feeds
    `singlenode.New` and `NewOrasMaterializer`; the materializer is wired as `Deps.Platforms` by type assertion, like
    the resolver.
  - Call sites: the only non-test callers of `Pull`, `NewOrasMaterializer` and `singlenode.New` pass the platform;
    the `Schedule` callers are the solo loop (with the list), the gate (with the list) and the pool (without).
- **Workflow.** `bundle-multiarch.yml` is `workflow_dispatch` only; neither `release.yml` nor `release-please.yml`
  names it; `just act-bundle` passes `--container-daemon-socket unix:///var/run/docker.sock` for this workflow only,
  and the layout lands in the ignored `.act-artifacts/`.
- **Conventions.** No new dependency (`go.mod`/`go.sum` untouched); errors are `api/fault`; no `any` in a new
  signature; no new `With*` option under `internal/` (the single-node driver and the materializer take explicit
  parameters); the only new package-level `var`s are the `ErrNoMatchingPlatform` sentinel and a compile-time
  interface assertion; the resolver cache is a mutex-guarded field.
- **Tracking.** ADR-0017 and ADR-0031 carry the `Superseded in part by: ADR-0145` back-links; ADR-0145 is at
  `Reviewing` and the F106 row at `reviewing`; the module path is unchanged.

### Definition of Done
13 / 13: the 12 Review-checklist items and the Definition of done hold. Checklist item 5 holds in the form the
Contracts require (see the `adr` Minor on the message); item 6 holds by reading the code (see the untested-call
Minor).

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0145 (implementation) → pass, 0/0/6, 3 model-attributed, DoD 13/13. See
docs/reviews/model-scorecard.md.

### Recommendation
Sign off: stamp ADR-0145 `Reviewing → Implemented`, move F106 to `implemented`, and move the board card to Done. Run
`go fmt ./...` before the commit (the hook would do it anyway). The other two `model` Minors (a recording-scheduler
assertion, the QEMU step) are small follow-ups; the two `adr` Minors belong to a later ADR.
