## Verdict: pass — 0 blockers, 0 majors, 2 minors  (fix `fix-dev-workflow-when`, model: claude-opus-5-5)

This report reviews branch `fix/dev-workflow-when`, commit c660f5d78 on origin/main 1d1a2d28, `fix(funcdctl): funcdctl dev
checks a Workflow against its steps' contract files` (`cmd/funcdctl/dev.go`, `cmd/funcdctl/dev_app.go`,
`cmd/funcdctl/dev_phase3_test.go`). It has no issue: the decider asked for defects to be fixed without public issues, so the
commit body states the defect, the `TestIssue<N>` name and the `Fixes #N` line do not apply, and no ledger row is recorded.
The defect: under `funcdctl dev`, a Workflow whose `when` reads a parent's output never became Ready (`WhenTypeError`, "not
rooted"), because dev's `devWorkflowContracts` reported every step as untyped.

### Minor
- **Two behaviors that the commit names have no test, and one mutant survives** · attribution: model · evidence: the new
  test uses image steps only, so nothing asserts that a ref step is now typed (TestScenarioDevWorkflowSteps fails mutant M1
  only because an unregistered ref image keeps the Workflow from Ready), or that a run whose input misses a required field
  is refused at apply. Mutant M3 (`Contract` answers an image that the session does not know as untyped instead of
  `NotFound`) passes the whole package (`go test -tags dev ./cmd/funcdctl/`, ok, 171 s). Scratch probes, not committed,
  show that all three behave as the commit says: a mistyped ref edge is `EdgeTypeMismatch`, and the input `{}` for the
  TypeScript example's `orders` is refused with `admission.workflowrun-contract: … (InputSchemaMismatch): "amount" (want
  number) is missing`. Fix: add a ref step and a refused run to the test, and check `devImages.Contract` on an unknown
  image.
- **The commit's account of what changes leaves out the stale verdict of a ref step** · attribution: model · evidence: in
  a probe, Workflow `pipe` (image step `a`, then ref step `b` to Function `sink`, whose contract file requires `rows`)
  reaches `EdgeTypeMismatch`. After `sink.contract.yaml` is corrected, dev reloads `sink` (its `imageDigest` changes), but
  `pipe` keeps `EdgeTypeMismatch` for the next 15 s: its resourceVersion does not change, and a rewrite of `pipe.yaml` that
  only adds a comment does not change it either. Only an edit of its spec or a restart derives it again. On main a ref
  step was untyped, so dev could not reach this state. Name it in the PR body.

### Routed (not this work's)
- **A ref step's verdict does not follow its Function's contract** · destination: the board card "Pin workflow ref: steps
  to a Function Revision (immutable typed edges, no dependency watch)" · verdict: `deferred`. The card names this state and
  decides against a watch. Its "Why deferred" says that ref steps have no real use yet; dev's ref steps (ADR-0247
  Decision 7, the dev Workflow of releve-lakehouse) are now one. Add the probe above to the card.

### ✅ Verified correct (keep it)
- **Design check: `bug`, and the fix changes no decision.** ADR-0125, ADR-0247, ADR-0098, ADR-0095 and ADR-0166 were read
  whole. ADR-0125 says that dev "must run the real reconcile/materialize/shim/invoke path, swapping only drivers. No fakes."
  (Constraints), and its Scope Out lists the fidelity boundary without the typed-edge gate. ADR-0247 says that dev applies
  production's files and runs steps as production does (Constraints, Consequences); its Decision 1 requires both contract
  sides, and its Decision 7 already reads the runtime from the same file. ADR-0098 Decision 1 types every function step, a
  ref step from its Function, sets Ready only when every edge, `when` and root merge type-checks, and requeues on
  `NotFound`, which `devImages` mirrors. A relaxed checker would contradict ADR-0095 (roots are context-scoped, and untyped
  JS was rejected) and ADR-0166 (the gate types what the engine sends). The platform's untyped producers (builtins, http
  refs, ADR-0166 Scope Out 6) stay untyped (`stepImage`). No issue, card or test accepts the untyped resolver: it came with
  the ADR-0125 implementation (145f86af9), and the #712 review assumed that dev Workflows still reach Ready.
- **The test fails without the fix, for the reported reason.** With main's `dev.go` and `dev_app.go` overlaid: `orders is
  WhenTypeError: step "hi" when: expr.check: reference "step.score.output.score" is not rooted in this context`, FAIL in
  20.3 s. With the fix, under `-race`: PASS in 0.98 s, not skipped.
- **The user-visible defect is fixed.** funcd-typescript v0.28.0 `examples/workflow`, run as dev runs a folder: on main,
  `orders` is `WhenTypeError`; with the fix, `scorer`, `orders` and `pipeline` are Ready, `orders` with `{"amount":40}`
  succeeds with `hi` run and `lo` skipped, and `pipeline` succeeds.
- **Mutants.** M1 (no registration in `deliverFunction`) fails TestScenarioDevWorkflowSteps: the run never succeeds. M2 (the
  contract without its input) fails the new test: `miswired` reaches `EdgesTypeChecked`.
- **What changes, and the examples.** A mistyped edge now sets `SchemaMismatch`, and a run that misses a required field is
  refused at apply (both probed). Read, not run: funcd-python v0.23.0 sql-readings (`ingest → summarize` and `run.yaml`
  type-check) and releve-lakehouse (its step inputs require nothing), and funcd-typescript's s3-roundtrip (one builtin
  step). funcd-functions has no Workflow.
- **Siblings.** The workflow contract and runtime resolvers are dev's only resolver options, and both now read the build.
  App mode keeps the production OCI resolvers (`devPlatformOptions`). `cmd/` and `pkg/funcd` hold no other untyped
  stand-in.
- **Reuse and conventions.** `devRuntimes` is extended, not duplicated. The contract is `contractBlob`, the
  `artifact.ContractBlob` that push bakes and that dev delivers to the gateway, and its decode mirrors `pkg/funcd`'s
  `contractResolver`. Errors use `api/fault`, the methods take ctx first, the test's YAML is block style, the imports are at
  the top, and the comments cite their ADRs. The test reuses the package's harness.
- **Checks.** `go test -race -tags dev ./cmd/funcdctl/`: ok (202.9 s). `go vet` and golangci-lint with and without `-tags
  dev`: 0 issues. The gate on c660f5d78 passed (audit, `ci-full`, the Linux build, vet and lint, stress, clean tree).
- **Shape.** A `fix(funcdctl):` subject, the attribution trailer, one commit, and a body that states the defect.

### Definition of Done
11 / 12 items hold. Item 13 does not apply: no spec under `docs/design` models workflows or dev. Miss: item 12 (the first
Minor). Items 1 and 11 hold in their form without an issue. Item 4 holds on the fix's key lines: the revert, M1 and M2 fail.

### Model scorecard
Not recorded: this fix has no issue, so no ledger row is written.

### Recommendation
Ready for the PR. Name the stale verdict of a ref step in the PR body, and consider extending the test as the first Minor
says.
