## Verdict: changes requested — 0 blockers, 0 majors, 1 minor (ADR-0352 implementation, model: claude-opus-5-5)

This report reviews ADR-0352's part of the branch `feat/tunnel-edge` at 03aa1e38: the commit 78eb3917, on the base
de9a0081 (ADR-0344 and ADR-0345 rebased on main). ADR-0353 and ADR-0354 are the branch's next two commits and have
their own reports, [adr-0353-implementation-claude-opus-5-5.md](adr-0353-implementation-claude-opus-5-5.md) and
[adr-0354-implementation-claude-opus-5-5.md](adr-0354-implementation-claude-opus-5-5.md). The reviewer did not write
the work, and every check below was run, not read.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

- **The guide says an add-on engine reaches nowhere under `funcdctl dev`** · model ·
  `docs/guide/develop/security-and-networking/networking/index.md:144-145`, written by 78eb3917. The page reads "Without
  it, and under `funcdctl dev`, workers connect anywhere, and an add-on engine nowhere". Under dev, ADR-0334 Decision 2
  applies no ADR-0269 fence to an engine, and the registry says so (`docs/behavior/funcdctl-dev.md:49`, "No ADR-0269
  fence"). The sentence holds only on containerd with egress off. Fix: say that without egress control an engine
  reaches nowhere, and that under `funcdctl dev` workers and engines both connect anywhere (ADR-0334 Decision 2).

### Routed (not this work's)

- **An intermittent failure in the first full run, on no path this work changes** · elsewhere: the flake tracker
  (#1142).
  - The failure: in the first `just ci-full`, on a host running two other test suites,
    `TestFunctionAnswersMatrix/invokeFunction/a_body_over_1_MiB` answered 400 "could not read request body" instead of
    413.
  - Why it is not this work's: that 400 is the envelope plugin's (`internal/dataplane/plugins.go:147`). Its read runs
    inside `s.chain.Before`, before any line the branch adds on that request. The pin reads no body, and the client
    middleware passes through when no proxy is trusted.
  - The test passed six runs alone and passed in the second full run.
  - It is surprising and not checked on the base yet: a question for the flake tracker, not a finding of this work.
- **An e2e failure under host load** · elsewhere: the flake tracker (#1142).
  - The failure: `TestScenarioKeyRenewalUnderLoad/rolling` failed in two runs of `just test-e2e` while other sessions'
    gates shared the host. It read "rolling renewals left s Degraded/DependencyNotReady", then "Degraded/Renewing".
  - It passed in the base de9a0081's full suite and in the head's full suite on a quiet host (413 s, exit 0), as well
    as three runs alone. Its `single` subtest had a load flake before (#1246, closed by #1250).
  - Why it is not this work's: the test sets no egress gateway, bridge address or trusted proxy
    (`pkg/funcd/key_renewal_e2e_test.go`), so the pin and the client middleware are inert there.
  - Its own comment calls it a smoke test at tight ratios. Load decides it, not this branch.

### ✅ Verified correct (keep it)

- **The checks, run on 03aa1e38 in a worktree of its own.**
  - `just ci` passed in full on its second run:
    - tidy, generate and fmt left nothing to commit;
    - `check-hygiene` was clean, `check-behavior` and `scripts/check-generated.sh` passed;
    - `golangci-lint run ./...` and the dev-tagged lint each reported 0 issues;
    - every package of `go test ./...` passed, and so did the dev-tagged tests;
    - `go build ./...` and `go mod verify` passed.
  - `just test-e2e` passed on a quiet host (413 s, exit 0). Its two earlier runs failed on
    `TestScenarioKeyRenewalUnderLoad/rolling` while other gates shared the host (routed above); the base de9a0081's
    suite passed as well. With `just ci`, that is `just ci-full` on 03aa1e38.
  - On Linux: `GOOS=linux go build ./...` and `go vet ./internal/... ./pkg/... ./cmd/...` passed, and `golangci-lint`
    reported 0 issues.
  - `just test-linux` passed in the funcd-linux-tests VM (exit 0, 49 s), including `internal/network` and
    `internal/runtime/containerd`.
  - `scripts/agent/stress.py --base de9a0081` ran the 47 Test functions the branch adds or changes, each alone under
    `-race`: 0 failed, 28 of them 20 times within the budget (342 s). The longest single run took 1.17 s, and every new
    scenario test calls `t.Parallel()`.
- **Mutants of the key lines, killed.** Each was one edit, applied with `go test -overlay` outside the tree:
  - the admission taking any Addon kind, not only an `Engine` entry's: `TestEgressAppliesToAdmission`,
    `TestScenarioAppliesToKindChecked`;
  - the Cedar item without `principal.kind ==`: `TestEgressItemNamesTheAddonKind`, `TestScenarioAppliesToNamesTheKind`,
    `TestScenarioForwarderAndPDPAgree`;
  - the forwarder matching an add-on item by name alone: `TestScenarioAppliesToNamesTheKind`,
    `TestScenarioForwarderAndPDPAgree`;
  - `Sources` taking the worker index's principal when the two differ: `TestSourcesNamesAWorkerElseAnEngine`,
    `TestScenarioUnknownCallerRefused`;
  - the engine fenced while egress is on: `TestScenarioEgressOffKeepsTheFence`.
- **Decision 1, the item.** `AppliesToRef` (`api/types/v1alpha1/egresspolicy.go`) has the Contracts' fields, the
  kind-name pattern and no enum. The OpenAPI changes only there: a new `AppliesToRef` schema and the field's `$ref`,
  with
  `WorkloadRef` unchanged (`git diff de9a0081..03aa1e38 -- api/openapi/`). `Validate` takes Function, Job or an
  `IsAddonKind` name. `NewEgressAppliesToAdmission(r StoreReader)` (`internal/controlplane/admission/egress.go`) runs on
  a create and an update, reads the Addons as `addonObjectQuota` does, takes only the kind of a runtime entry that
  serves
  `Engine`, spelled the same, and answers 400 naming `spec.appliesTo[i].kind`. It is registered in
  `pkg/funcd/funcd.go`. `TestScenarioAppliesToKindChecked` drives the API: 400 for an unregistered kind, a settings
  kind, an engineless kind and `TUNNEL`, and the schema's 422 for `tunnel`.
- **Decision 2, the path.** With egress on, the reconciler gives the engine a nil `EgressOnly`
  (`internal/services/addon/reconcile.go`, `Deps.Egress` from the egress substrate in `pkg/funcd/addon_provider.go`).
  `writeNetns` then writes the input chain alone, and still refuses a host-only worker without a fence while egress is
  off (`internal/runtime/containerd/containerd_linux.go`). `FenceNetns` writes no output chain for a nil allow list
  (`internal/network/fence_linux.go`). `TestScenarioEgressOffKeepsTheFence` checks both specs, the empty fence off and
  none on, and a host-only engine either way.
- **Decision 3, the source.** `egress.Sources` (`internal/network/egress/workerindex.go`) is the worker index's entry,
  else the engine index's, and refuses an address the two hold with different principals. `buildEgress` gives it to
  both the gateway and the forwarder (`pkg/funcd/executor.go`), and no index gains a writer.
- **Decision 4, the policy.** The `AddonObject` entity carries `kind` (`internal/auth/cedar/entities.go`), and an add-on
  item compiles to `principal == AddonObject::"<ns>/<name>" && principal.kind == "<kind>"`
  (`internal/auth/cedar/egress_compile.go`, `appliesToItem`). The forwarder's `EgressDomains` takes the stored object's
  kind, which `storeDomainRules` reads (`pkg/funcd/funcd.go`, `addonKindOf`). `TestScenarioEngineEgressNamed`,
  `TestScenarioEngineEgressDefaultDeny` and `TestScenarioAppliesToNamesTheKind` check the forwarder's answer and the
  real
  PDP's decision over stored objects. They cover the Function of the object's name, the item of another kind's object,
  and the audit under the object's namespace and name.
- **The lane.** The egress lane passed all 16 cases on containerd, `engine-egress-named` included: before the policy,
  REFUSED lookups and a failed connection; under it, the lookup of `example.com` answered and TLS completed; and no
  answer for the Function of the object's name. The run was on a31bfbf2, which differs from 03aa1e38 by two test
  comments only (`git diff a31bfbf2 03aa1e38`).
- **The docs.** The addons.md row "An object's engine" and the new egress.md rows match Decisions 1 to 5; the guide's
  egress and add-on pages were updated.
- **Tracking.** The ADR at `Reviewing`, with its substance unchanged since acceptance (`git diff 501ac9e4..03aa1e38`
  touches its status line only); F274 at `reviewing`; the back-links in ADR-0332, ADR-0175, ADR-0281 and ADR-0351.

### Definition of Done

14 / 14 items hold: the 5 of the ADR's review checklist, and the 9 generic items that apply (the one about every driver
of a port does not, as the ADR adds no port). The Minor is a sentence of the guide, which no item counts.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0352 (implementation) → changes-requested, 0/0/1, 1 model-attributed. See
docs/reviews/model-scorecard.md.

### Recommendation

The one finding is a Minor, which the skill alone would pass. The decider wants nothing left open in this review, so it
goes back to the builder, and the ADR and its feat row stay at `Reviewing` and `reviewing` until the next round. Fix the
guide's sentence on `funcdctl dev`; the next round reruns only what that touches and stamps the ADR. The PR closes #1552
(`Fixes #1552`).
