# ADR-0149: Runtime availability — an unserved runtime is reported unavailable

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, shim, function, containerd, process
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (function runtime behind the `runtime.Runtime` port)
- **Depends on**: [PR #597](https://github.com/pyvvo/funcd/pull/597) (merged, 1ca62fe): `ImageFor` is the
  single runtime→image mapping; `resolveImage` pulls the normalized ref only when `ctrmanager.Config.Pullable` allows.
- **Supersedes in part**:
  - [ADR-0049](0049-python-runtime-shim.md) Decision 7's selection "`python*` → python shim, else the default
    `WithRuntimeShim` — node" and its Contracts sentence "`shimFor(rt v1.RuntimeName)` returns the family match
    (longest prefix) else the default"; [ADR-0050](0050-python-worker-pooling-subinterpreters.md) Decision 3's "else
    the default node `poolShimCommand`" and its "remove the `shimByFamily` exclusion loop" (the loop that #371 restored
    stays, Decision 1). The default shim and pool host now serve only node-family runtimes.
  - [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md) Decision 2's rule that only a
    `--image <runtime>=<ref>` override "pulls from a registry … for a custom/updated runtime", and its Contracts
    sentence "a runtime in neither embed nor override is the Manager's fault.NotFound". A runtime under an operator-set
    `imagePrefix` (ADR-0036) is pulled from `<prefix><rt>:latest` too. A runtime with neither an embedded image nor an
    override is `fault.NotFound` with no pull under the default prefix, and under an operator-set prefix only when the
    pull finds the image absent.
  - The rest of the three ADRs stands. Each keeps status `Implemented` and receives a "Superseded in part by ADR-0149"
    back-link at acceptance (precedents: ADR-0097, ADR-0060).
- **Relates to**: ADR-0046 · ADR-0036 (`<prefix><rt>:latest`) · ADR-0032 · ADR-0087 (`duckdb` is a provider engine) ·
  ADR-0016 · ADR-0143 (Decision 4.6, extended by Decision 4; this ADR does not change 4.6) · ADR-0145 · ADR-0121 ·
  ADR-0161 (Proposed) amends 4.6's phase/`Ready`/`replicas` rule, and
  `registry-outage-stays-retryable`'s "the stored status is unchanged", Decision 3's "is returned from `Reconcile` and
  retried" and the risk "only logged": such an error is also written to the status as `StartFailed` · ADR-0169
  (Proposed) generalizes Decision 5 to every `Failed` Function (the `desiredReplicas` contract counts it as woken, with
  no reason or mode check) and voids the idle-reclaim Consequence

## Context & Need

Curated function runtimes: `nodejs22`, `python314`; the embedded `duckdb` is the CatalogService engine (ADR-0087), not a
function runtime. In containerd mode a worker runs `imageFor(rt)` = `ctrmanager.Config.ImageFor`. Validation checks
`RuntimeName` only as a DNS label. Verified (#457, at main 1193be6):

- Process mode runs `runtime: ruby3` on the **Node** shim: `shimFor` and `poolHostFor` fall back to the node defaults
  (`internal/function/function.go:1564-1569`, `internal/function/pool.go:61`).
- Containerd mode under the default prefix `funcd/runtime-` fails the pull of `funcd/runtime-ruby3:latest` with
  `lookup funcd: no such host` (since PR #597 / 1ca62fe: no pull, `fault.NotFound` "runtime image … is not
  embedded"). On a Docker Hub prefix a missing repository is 401 `insufficient_scope` → `docker.ErrInvalidAuthorization`
  → `fault.Internal` (containerd v2.3.1). Either way the controller only logs and retries at ≤1 s backoff; no status is
  written.
- The #371 gate (`runtimeUnavailable`, `function.go:1584`) covers only python-family runtimes in process mode.

Need: one reason, `RuntimeUnavailable`, for a runtime this node cannot serve, recovering on its own when it appears.

## Scenarios

- `scenario: unknown-runtime-process-solo` — Given a process-mode daemon, When a Function with `runtime: ruby3` is
  applied, Then it ends `Failed` with reason `RuntimeUnavailable` and message
  `runtime "ruby3" is not available on this node: no shim is registered for it`, and no worker is created.
- `scenario: unknown-runtime-process-pool` — Same with `spec.pooling.worker`; no pool worker is created.
- `scenario: engine-image-not-a-runtime` — Given a containerd-mode daemon, When a Function with `runtime: duckdb` is
  applied, Then it ends `Failed` with reason `RuntimeUnavailable` at the gate and no worker is created.
- `scenario: missing-image-containerd` — Given a containerd-mode daemon, When a `ruby3` Function with `replicas: 1` is
  applied and its image is absent (Decision 3), Then it ends `Failed` with reason `RuntimeUnavailable`, its message
  names the image reference and the cause, and the pass requeues after the supervision period.
- `scenario: missing-image-scale-to-zero` — Given a `ruby3` Function with default scaling, When it is applied, Then it
  goes `Idle` and creates nothing; When a call wakes it and its image is absent, Then it ends `Failed` with reason
  `RuntimeUnavailable`, and each later periodic pass checks again and keeps that reason.
- `scenario: published-image-recovers` — When the image then becomes available, the next periodic pass deploys it.
- `scenario: registry-outage-stays-retryable` — Given an operator-chosen registry, When the pull fails with a network
  error, a 5xx or a timeout, Then `Reconcile` returns that error (not `RuntimeUnavailable`), the stored status is
  unchanged, and the controller retries with its backoff. ADR-0161 (Proposed) amends this clause to write `StartFailed`;
  its test changes with it.
- `scenario: unavailable-runtime-keeps-serving-revision` — Given a containerd-mode Function in phase `Ready` with a
  running (listening) worker of revision S serving `nodejs22`, When `spec.runtime` changes to `ruby3` and its image is
  absent, Then `Ready` stays True, the phase stays `Ready`, `RevisionReady` is False with reason `RuntimeUnavailable` and
  the Decision 4 message, no worker of the new revision C remains, and the pass requeues after the supervision period;
  When a later `Create` succeeds, Then a later pass switches the calls to C.
- `scenario: node-runtime-unchanged` — In process mode a `nodejs22` or `node` Function runs on the Node shim, solo and
  pooled; in containerd mode no gate fires for `nodejs22` and its worker `Image` is `imageFor("nodejs22")`.

## Scope and drivers

In: Decisions 1–5, including the pull rule (the ADR-0054 supersession, implemented by PR #597). Out: an apply-time
allow-list; a new curated runtime; custom image behavior (ADR-0049); the mapping, override and normalization (PR #597);
registry credentials (containerd's default resolver has none); `RuntimeClass` (no reconciler reads it). An operator
runtime image is node-wide: FEAT-0000 V1's exclusion of arbitrary OCI-image functions stands.

Drivers: one reason and message prefix; availability differs by mode and node, by design, and is checked at reconcile
time (like the ADR-0145 platform gate; accept then requeue, ADR-0121 Decision 2); pull only from an operator-chosen
registry; a transient registry failure never shows `RuntimeUnavailable`, and an absent-image `RuntimeUnavailable` is
never permanent.

## Alternatives considered

Chosen: Decisions 1–5 — one reason, no registry call under the default prefix, recovery without a re-apply (cost: absence shows only after apply; a private image shows it too; a GHCR-style registry never does). Rejected:

- Pull every non-embedded ref, default prefix included — pulls from a registry the operator did not choose.
- Only a 404 means absent — Docker Hub never answers 404 for a missing repository.
- A 401 means absent, permanently — a published image needs a re-apply.
- ADR-0054 as written (only an override pulls) — an operator-set `imagePrefix` cannot add a runtime.
- A gate-time image probe through the port — a new port method and a second registry round trip per pass.
- Reject unknown runtimes at apply time — blocks operator runtimes; availability differs per node.
- Keep the Node fallback, document it — JavaScript runs under a false runtime name.

## Decision

1. **The default shim and pool host serve only node-family runtimes**: a `spec.runtime` that starts with `node`.
   `isNodeFamily` mirrors `isPythonFamily`. `poolHostFor` keeps today's exclusion: a runtime that a registered
   runtime-shim family matches gets no default pool host (`pool.go:56-59`).
2. **Availability per node.**
   - Process mode (a Materializer and `EndpointLoopback`; the legacy mode without a Materializer is untouched): a
     runtime is unavailable when `shimFor(rt) == nil` and the Function is not pool-eligible (`poolKeyFor` false).
   - Containerd mode (a Materializer and `EndpointNetnsFixedPort`): `duckdb` is unavailable as a function runtime; any
     other runtime is available when `imageFor(rt)` is in this node's image store, is the embedded image
     (`embedimg.TarForImageRef`), or is pullable (Decision 3) and its pull succeeds. A stored image is used as is (its
     `:latest` is not refreshed). Absence is learned only from a `Create`, so a scale-to-zero Function learns it at its
     first wake, and Decision 5 keeps it. Containerd mode runs solo only (`cmd/funcd` registers no pool shim there).
3. **What "absent" means.** The containerd driver wraps `runtime.ErrImageUnavailable`, kind `fault.NotFound`, in exactly
   two cases in `resolveImage`, after the store lookup and the embedded import:
   - a ref it may not pull (PR #597's not-embedded `fault.NotFound` under the default prefix), with no registry call;
   - a pull of a pullable ref that fails with an errdefs NotFound of any origin (manifest or blob 404, no platform
     variant, no resolve host, a local content-store or snapshotter NotFound) or with `docker.ErrInvalidAuthorization`
     (Docker Hub's 401 for a missing repository). A local NotFound shows the wrong reason only until the next re-check.

   The pull rule has one source: `ctrmanager.Config.Pullable` beside `ImageFor`, carried to the driver by
   `containerd.Config.Pullable`; `config.DefaultImagePrefix` is the one default-prefix constant. Any other pull failure
   (network, 5xx, timeout, a token endpoint's 403) keeps its `mapErr` kind, is returned from `Reconcile` and retried at
   the controller backoff; it never shows `RuntimeUnavailable`.
4. **One outcome.** An unavailable runtime fails the latest generation through `gateFailed` with reason
   `RuntimeUnavailable` and message `runtime "<rt>" is not available on this node: <cause>`. `<cause>` is
   `no shim is registered for it` (process), `it is the CatalogService engine image, not a function runtime`
   (`duckdb`), or the `Create` error for an absent image: under the default prefix
   `runtime.containerd.Create: runtime image "<ref>" is not embedded; set runtime.containerd.imageOverride for its
   runtime to pull it: image is not available`; on an operator-chosen registry
   `runtime.containerd.Create: pull image "<ref>": image is not available: <pull error>`. `<ref>` is `imageFor(rt)` as
   given; `<pull error>` names the normalized reference (`docker.io/...`), so tests and the lane assert the short
   ref only outside it. The hint names only the per-runtime option, because a custom `imagePrefix` moves every
   runtime image off the embedded ones (`nodejs22`, `python314` and the `duckdb` engine,
   `internal/runtime/embedimg/embedimg.go:44-46`). When nothing serves: phase `Failed`, zero replicas. When a revision
   serves, it keeps serving and `RevisionReady` carries the reason.
   - Process mode and `duckdb`: the step-3a gate (`function.go:422-426`), before pooling and the secret, data and
     catalog gates; `<rt>` is `spec.runtime`.
   - Absent image: learned at `Create`, after those gates and `Materialize`. `convergeRevision` wraps the error with the
     message prefix, so `<rt>` is the runtime of the revision whose worker failed; `Reconcile` maps
     `errors.Is(err, runtime.ErrImageUnavailable)` on the `convergeSolo` error only to the same `gateFailed`. A pooled
     converge error is returned as today (a containerd pool worker exists only for a library embedder; `createPool`'s
     `Image: key.Runtime`, `pool.go:367` at main, is ADR-0173 (#609)). A spec with two defects can show a
     different first reason per mode.
   - This extends ADR-0143 Decision 4.6 to a `Create` failure inside converge without changing 4.6 or its gate list;
     `gateFailed` sets phase, `Ready` and `replicas` per 4.6 in force (ADR-0161 amends them). In a switch, `switchSolo`
     converges S before C's `Create` fails, and `gateFailed` still stops C's workers.
5. **Re-check.** An absent-image failure requeues after `r.supervisionPeriod` (default `controller.SupervisionPeriod` =
   10 s), as the PoolFull gate does and as ADR-0143 Decision 7 re-checks a failed gate. In containerd mode
   `desiredReplicas` treats a `Failed` Function whose Ready reason is `RuntimeUnavailable` like a woken one
   (`maxInt(1, spec.replicas)`), so a scale-to-zero Function is not rewritten `Idle`. Each pass tries a `Create` again:
   under the default prefix a store lookup, on an operator-chosen registry a pull. An image that becomes available
   recovers the Function without a re-apply; while `Failed`, calls are refused (`activator.FailedFault`). A 401 is never
   permanent; a private image recovers only when anonymously pullable.
   Process-mode shims and the `duckdb` rule are fixed at daemon start, so those gates set no requeue (as #371 today).

## Temporary workarounds

None.

## Contracts

```go
// internal/runtime/runtime.go
// ErrImageUnavailable (new) is wrapped (fault.NotFound) by a driver's Create when the worker image is absent (Decision 3).
var ErrImageUnavailable = errors.New("image is not available")
// internal/runtime/containerd/image_ref.go (untagged)
// imageAbsent (new): any errdefs NotFound, or docker.ErrInvalidAuthorization.
func imageAbsent(err error) bool
// internal/runtime/ctrmanager/manager.go (PR #597, consumed unchanged)
// Pullable: an ImageOverride ref or a ref under an operator-set prefix; none under config.DefaultImagePrefix.
func (c Config) Pullable(prefix string) func(ref string) bool
// internal/function/function.go
// shimFor returns the longest registered family match, else the default shim when isNodeFamily(rt); nil otherwise.
func (r *Reconciler) shimFor(rt v1.RuntimeName) []string
// runtimeUnavailable applies Decision 2's gate predicate for any runtime.
func (r *Reconciler) runtimeUnavailable(fn *v1.Function) (string, bool)
// desiredReplicas adds the Decision 5 case.
func (r *Reconciler) desiredReplicas(fn *v1.Function) int
// isNodeFamily (new) reports a runtime that starts with "node".
func isNodeFamily(rt v1.RuntimeName) bool
// internal/function/pool.go
// poolHostFor: a registered pool family's host; else the default host when isNodeFamily(rt) and no runtime-shim
// family matches; nil otherwise.
func (r *Reconciler) poolHostFor(rt v1.RuntimeName) []string
```

| Direction | Item | Source / consumer |
|---|---|---|
| Consumes | `ctrmanager.Config.Pullable` → `containerd.Config.Pullable` (an `ImageOverride` ref or one under an operator-set prefix) | PR #597, unchanged; `resolveImage` (Decision 3) |
| Consumes | `config.DefaultImagePrefix` | the one default-prefix constant (PR #597) |
| Consumes | `catalog.DuckDBRuntime` | the `duckdb` gate (Decision 2) |
| Consumes | `r.supervisionPeriod` | the re-check requeue (Decision 5) |
| Exposes | `runtime.ErrImageUnavailable` (`fault.NotFound`) | `convergeRevision`, `Reconcile` |
| Exposes | reason `RuntimeUnavailable` + Decision 4 message | `Ready` (phase `Failed`) or `RevisionReady` (serving revision) |

```yaml
status:
  phase: Failed
  conditions:
    - type: Ready
      status: "False"
      reason: RuntimeUnavailable
      message: 'runtime "ruby3" is not available on this node: no shim is registered for it'
```

## Implementation plan

1. `internal/function/function.go`: add `isNodeFamily`; `shimFor` per Contracts; `runtimeUnavailable` drops the
   python-only guard and applies Decision 2 (`duckdb` via `catalog.DuckDBRuntime`) with the Decision 4 message;
   `desiredReplicas` gains the Decision 5 case.
2. `internal/function/pool.go`: `poolHostFor` per Contracts; its now-redundant `isPythonFamily` return goes.
3. `internal/runtime`: add `ErrImageUnavailable` and `imageAbsent`. In `resolveImage` the not-pullable branch returns
   `fault.Wrapf(runtime.ErrImageUnavailable, fault.NotFound, op, "runtime image %q is not embedded; set
   runtime.containerd.imageOverride for its runtime to pull it", ref)`; the pull branch returns
   `fault.Wrapf(fmt.Errorf("%w: %w", runtime.ErrImageUnavailable, err), fault.NotFound, op, "pull image %q", ref)` when
   `imageAbsent(err)`, else `mapErr`. `Tar`'s doc comment (`internal/runtime/embedimg/embedimg.go`, left unqualified
   by PR #597) states the pull rule; the `imagePrefix` line of `examples/funcdconfig.yaml` already does (PR #597).
4. `convergeRevision` (`function.go:1005-1007`) gives a sentinel `Create` error the prefix `runtime %q is not available
   on this node` (`tmpl.Spec.Runtime`); `Reconcile` (`:500-502`, `convergeSolo` only) calls `gateFailed` with
   `RuntimeUnavailable`, the error without its `function.converge` op as message and Ready message, phase `Failed`,
   replicas 0 when nothing serves (`zeroReplicas` today; ADR-0161's listening count if it lands first),
   `requeue: r.supervisionPeriod`; the `gateFailure` doc comment (`:508`) widens to "or at a worker create inside it".
5. `pkg/funcd/options.go`: `WithRuntimeShim` and `WithPoolShim` docs say they set the node-family default.
6. Tests (`internal/function`; `newShimHarness`, `newContainerHarness` whose fake gains a `Create` error and an attempt
   counter): `TestADR0149_UnknownRuntimeProcessSolo`, `TestADR0149_UnknownRuntimeProcessPool`,
   `TestADR0149_EngineImageNotARuntime`, `TestADR0149_MissingImageContainerd`, `TestADR0149_MissingImageScaleToZero`
   (two periodic passes, one `Create` attempt each), `TestADR0149_PublishedImageRecovers` (sentinel, then success),
   `TestADR0149_RegistryOutageStaysRetryable` (`fault.Unavailable`),
   `TestADR0149_UnavailableRuntimeKeepsServingRevision` (sentinel for `imageFor("ruby3")` only, then success),
   `TestADR0149_NodeRuntimeUnchanged`. `TestADR0149_ImageAbsentClassification` (`image_ref_test.go`, untagged): a 404,
   a wrapped `ErrInvalidAuthorization`, a no-platform NotFound, a timeout, and `docker.NewResolver(...).Resolve` against
   three `httptest` registries — 404 (absent), Docker Hub's 401 `insufficient_scope` (absent), GHCR's token 403 (not
   absent). `TestADR0149_ResolveImageClassifiesPullErrors` (`snapshotter_linux_test.go`, PR #597's `pullDriver` with a
   configurable `leaseCounter` error): `errdefs.ErrNotFound` → sentinel and `fault.NotFound`; `errdefs.ErrUnavailable`
   → no sentinel, `fault.Unavailable`. PR #597's `TestResolveImage_DefaultPrefixRuntimeIsNotPulled` also asserts the
   sentinel; its `TestResolveImage_PullsShortRefFromDockerHub` and `TestNormalizedRef_PullsShortRefFromDockerHub` pin the
   short-ref pull. `TestIssue371_PythonFunctionWithoutPythonShimIsRuntimeUnavailable` expects the new message.
7. Lane: fixture `e2e/fixtures/ruby3-echo.yaml` (`runtime: ruby3`, env-echo artifact, `replicas: 1`, `minReplicas: 1`),
   staged by env-echo in `scripts/lanes.yaml`; a last testcase in `e2e/env-echo.venom.yml` after
   `daemon-restart-recovers` asserts `Failed`, `RuntimeUnavailable` and a message containing
   `funcd/runtime-ruby3:latest` and `is not embedded`, then deletes `ruby3-echo` (default prefix: no registry call).
8. Documents: with this ADR, the F12 row of `docs/feat/0000-feat-v1.md` adds `(+ ADR-0149 — runtime availability)` and
   splits its status as F13's into `runtime: implemented · runtime availability: adr` (ADR-0152 adds its own
   sub-status), following this ADR. At acceptance ADR-0049, ADR-0050 and ADR-0054 get the back-link and F28's ADR cell
   adds ADR-0149, status unchanged. No blueprint change.
9. Done: `just ci` green; the lane case passes; the PR carries `Fixes #457`.

## Review checklist

- [ ] No non-node runtime gets the default shim or pool host; a registered runtime-shim family still gets no default host.
- [ ] Process solo, process pooled and containerd solo show the Decision 4 reason and message; a pooled converge error
      is not mapped.
- [ ] Only Decision 3's two cases wrap the sentinel; every other pull failure is returned from `Reconcile`.
- [ ] Decision 5 re-check, scale-to-zero and recovery hold; a serving revision keeps serving and C's workers stop.
- [ ] `nodejs22` and `python314` are unaffected; step 8 documents and `Tar`'s doc comment (step 3) are updated.
- [ ] Each scenario has one named, passing test.

## Consequences

- Positive: one reason and message prefix for every unserved runtime; no registry call under the default prefix, even
  offline; an image that appears recovers the Function without a re-apply; ADR-0054 no longer contradicts the driver.
- Negative: on an operator-chosen registry each absent-image Function costs one resolve per supervision period (three
  requests on Docker Hub; `Pull` caches no token). A Function re-applied out of `RuntimeUnavailable` comes up as woken.
  With `idleTimeout` set, idle reclaim may move a `Failed` Function to `Idle`; the next call learns the absence again.
- Risks accepted: a node that cannot reach the operator-chosen registry retries an unknown runtime at the ≤1 s backoff,
  only logged; a registry that denies the token for a missing repository (GHCR: 401, then a token 403) loops the same
  way. If the serving revision's image leaves the store and does not return, S's `Create` fails first on every pass, so
  a switch waits for it. A shim registered later is picked up only after a daemon restart.

## Open questions

None.

## References

- Issue [#457](https://github.com/pyvvo/funcd/issues/457); precedent [#371](https://github.com/pyvvo/funcd/issues/371).
- containerd v2.3.1: `core/remotes/docker/resolver.go:321-336` (401 → `ErrInvalidAuthorization`; 404 → NotFound),
  `authorizer.go:376` (`insufficient_scope`); a token 403 is `ErrUnexpectedStatus` (`auth/fetch.go:211-212`).
