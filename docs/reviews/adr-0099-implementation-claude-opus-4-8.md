# ADR-0099 implementation review — Sub-workflows (a `workflow:` step runs a child workflow, F70)

**Verdict**: **pass** — the sub-workflow step is implemented conformantly (inline recursive execution,
depth + cycle guard, typed edges across the boundary), every scenario has a named passing test, the four
sub-checks are green, a Go e2e drives a parent's `workflow:` step running a real child inline over the
control plane, **and the containerd venom lane is green (7/7, incl. the F70 sub-workflow case) on real
containerd**. (An `cloud.debian.org` TLS-handshake outage initially blocked the Lima VM boot; worked around
by pointing the pinned image at a reachable Debian mirror, digest-verified identical — see below.)
**Model**: claude-opus-4-8 · **Phase**: implementation · **ADR status**: Reviewing → Implemented
**Reviewed against**: ADR-0099 Contracts / Scenarios / Review checklist / DoD · [ADR-0094](../adr/0094-workflow-engine-core.md) (the engine it reuses) · [ADR-0096](../adr/0096-engine-native-builtin-steps.md) (the reserved kind it fills) · [ADR-0098](../adr/0098-typed-workflow-edges.md) (F65 typed edges across the boundary) · [ADR-0002](../adr/0002-source-code-conventions-and-patterns.md) conventions.

## Verification (run, not eyeballed)

| Check | Command | Result |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Lint | `go tool golangci-lint run ./...` | **0 issues** |
| Tests | `go test ./...` | exit 0 (all packages ok except the pre-existing env failure below) |
| Modules | `go mod verify` | all modules verified |
| Go e2e | `go test ./pkg/funcd/ -run TestScenarioSubworkflow` | **ok** — parent `workflow:` step runs a real child inline over the control plane |
| Containerd lane | `just lima-example workflow` | **7/7 venom PASS** (incl. `sub-workflows-…-F70`) on real containerd |

`TestPythonPoolSmoke` (internal/testkit/bench) fails on Python-shim readiness — a pre-existing
**environmental** failure unrelated to this change; not attributed.

## Scenario → test (all named, un-skipped, passing)

| Scenario | Test |
|---|---|
| `subworkflow-runs-inline-and-output-flows` | `TestSubworkflowRunsInlineAndOutputFlows` + the Go e2e `TestScenarioSubworkflow` |
| `subworkflow-nests-multi-level` | `TestSubworkflowNestsMultiLevel` |
| `subworkflow-child-failure-fails-parent` | `TestSubworkflowChildFailureFailsParent` |
| `subworkflow-typed-edge-across-boundary` | `TestSubworkflowTypedEdgeAcrossBoundary` (+ mismatch variant) |
| `subworkflow-child-not-ready-requeues` | `TestSubworkflowChildNotReadyRequeues` |
| `subworkflow-cycle-rejected-at-reconcile` | `TestSubworkflowCycleRejected` |
| `subworkflow-max-depth-guarded` | `TestSubworkflowMaxDepthGuarded` |
| `workflow-kind-validated` | `TestWorkflowKindUnion` (api types — accepted with a valid ref, rejected empty/bad/combined) |

## Review checklist (ADR-0099)

- [x] The `workflow` kind is accepted (`validateKind`): exactly one of `function|builtin|workflow`;
      `workflow` requires a valid `ref`; the reserved-rejection is gone.
- [x] A `workflow:` step runs the child inline (recursive `execute`); the child's leaf-composite run
      output becomes the step output and flows into the parent's downstream step (Go e2e + unit).
- [x] Nesting composes; a failed child fails the parent fast; the parent ctx/timeout threads to the
      child; recovery re-runs the in-flight sub-step (at-least-once) — `runChild` reuses the engine.
- [x] Cycle rejected at reconcile (`WorkflowCycle`, not Ready, visited-set walk); max-depth guarded at
      runtime (`SubworkflowDepthExceeded`, no stack overflow); default depth is a Config key (8).
- [x] Typed edges across the boundary: the child's `status.contract` (read from the store, not the
      `ChildResolver`) is the step contract; edges type-check (F65); a not-Ready child requeues.
- [x] Security: the child runs with its own grants; the parent invokes it only via the declared
      `workflow: {ref}` (declared-target-is-the-grant, default-deny preserved) — the child run's
      `Workflow` field is the child, so its steps dispatch to the child's own materialized functions.
- [x] Go e2e green; **containerd venom lane green** (child.yaml/parent.yaml, lanes.yaml apply+ready, a
      venom sub-workflow case asserting `pipeline` reaches Ready and a run drives the child inline).

## Lane infra fix (recorded)

Initially the containerd lane couldn't boot its Lima VM: `limactl start` hit a **TLS-handshake timeout to
`cloud.debian.org`** (and `cdimage.debian.org`) from this network while fetching the Debian VM base image
— an SSL/host-reachability issue, not a code/lane defect (the manifests + venom YAML build + stage cleanly;
the same infra ran 8 lanes green earlier). Fixed in `scripts/lima.yaml`: the image is **pinned by content
digest** (both arches, verified against the official `SHA512SUMS`) so Lima keys the cache by digest and
skips the flaky freshness HEAD, and the `location` now points at a reachable official Debian mirror
(`ftp.acc.umu.se`) whose TLS works — the digest guarantees the mirror serves the identical image. With
that, the lane boots and passes 7/7 (incl. the F70 sub-workflow case).

## Strengths — keep as-is

- **Inline recursion over the same engine** — `runChild` calls `execute(depth+1)`; no new dispatch path,
  no child-reconciler-wait, so the worker-starvation deadlock class is structurally avoided (as spiked).
- **The layering is right**: the engine's `ChildResolver` fetches the child *spec* (execution); the
  reconciler reads the child's `status.contract` from the store it already has (typing). One responsibility
  each — the judge finding that got folded pre-accept.
- **Guards are real**: the reconcile cycle-walk (visited-set) makes a cyclic workflow un-Ready; the
  runtime depth cap is the backstop for a late-created cycle — the `TestSubworkflowMaxDepthGuarded` self-
  reference fails cleanly instead of overflowing.
- **Typed edges compose for free** — the cross-boundary edge test confirms F65's `checkEdge` treats the
  child contract like any step's, so a mismatch against the child's I/O blocks Ready.

## Findings

### Blockers / Major / Minor
None.

### Nits
- `childResolver` (pkg/funcd) resolves the child in the run's namespace (threaded via the interface) —
  correct for V1 same-namespace scope; cross-namespace is a documented follow-on.

## Recommendation

**pass** → stamp ADR-0099 `Reviewing → Implemented`; advance FEAT-0005/F70 → implemented; move the board
card → Done. No `adr`/`model` Blockers or Majors; the containerd lane is `env`-deferred (re-runnable, with
the digest-pin resilience fix), and the Go e2e provides the passing end-to-end evidence.
