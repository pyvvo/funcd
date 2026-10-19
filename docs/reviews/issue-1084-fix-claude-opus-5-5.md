## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #1084 fix, model: claude-opus-5-5)

This report reviews branch `fix/1084-oci-missing-contract`, commit 8d2ddf89 `fix(function): refuse a cloudevent
Function whose OCI artifact carries no I/O contract` (`git diff origin/main...HEAD`: `internal/function`
`function.go`, `pool.go`, `materializer.go` and the new `contract_missing_test.go`; `internal/artifact/artifact.go`;
`internal/testkit/bench` `bench.go` and `containerd_linux.go`; and the tests that pushed contract-less cloudevent
artifacts). Issue: "A cloudevent Function whose OCI artifact carries no I/O contract starts and serves
unvalidated". The review ran in the fixer's session, not in a separate agent, as the user asked for on 2026-10-11;
the DR session's check before merge is the independent look.

### Minor
- **A sibling was found only by the gate** · attribution: model · evidence: the first gate run failed
  `TestBenchSmoke` (`bench.Run: all 2 substrate(s) failed`): the `funcd bench` harness (`internal/testkit/bench`
  `bench.go:176` and `containerd_linux.go:116`) pushed its handler with no contract, so the fix stopped its
  Functions. A grep for contract-less `artifact.Push` in non-test code before the first gate would have found it.
  Fixed in the same commit: both pushes carry an open contract, and `TestBenchSmoke` passes on the host and in the
  Linux VM.
- **A held revision's boot failure reads `StartFailed`, not `ContractMissing`** · attribution: model · evidence: a
  first version also mapped the error in `heldBootFailed`, with no test; it was removed rather than kept untested.
  A held revision whose artifact has no contract still never starts (fail-closed); only its reason is the generic
  one, with the full message. No stored revision can be in that state once this fix runs, since none starts.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** With `origin/main`'s `function.go`,
  `pool.go`, `materializer.go` and `artifact.go` overlaid, `TestIssue1084_OCIArtifactWithoutContractDoesNotStart`
  fails on `cloudevent-without-contract` and `pooled-cloudevent-without-contract`: expected `Failed`, actual
  `Ready`. The three control cases (http without contract, cloudevent with contract, solo and pooled) pass.
- **It passes with the fix**, un-skipped, under `-race`.
- **Mutants:** dropping the protocol check fails `http-without-contract`; replacing the contract-file probe with
  `false` fails both missing-contract cases; disabling the reconciler's `ContractMissing` mapping fails them too.
- **Cause, not symptom.** The reconciler treated a missing contract file as a legacy artifact. It now asks the
  materializer whether its artifacts carry their contract (`ContractRequirer`, implemented only by the oras driver),
  and fails a cloudevent Function without one closed, through the same `gateFailed` path as `RuntimeUnavailable`
  (ADR-0149), with no requeue: only a new artifact, so a new generation, brings a contract.
- **Scope.** OCI only: the file driver is not a `ContractRequirer`, so `funcdctl dev` and the many `file://` tests
  are unchanged, as the issue asks. An http Function (ADR-0243) is unchanged. The test edits only give
  contract-less cloudevent pushes an open contract (`artifact.ContractBlob({}, {})`, the existing inline pattern);
  `TestScenarioHTTPMismatch`'s `app-as-event` case now reuses the contracted push so it still reaches the shape check
  it tests. No assertion was weakened.
- **One path for every materialize.** `Reconciler.materialize` wraps the solo, pooled and serving-member calls, so
  no path skips the check; a pooled member other than self is left out of the manifest, as for any materialize error
  (ADR-0046).
- **Name.** `ContractMissing` and `ContractRequirer` collide with nothing on main (grepped). The message names the
  artifact by digest, as `NoMatchingPlatform` does.
- **ADRs.** The fix enforces ADR-0090's mandatory contract and ADR-0247's accepted risk; ADR-0123 Decision 4 is
  unchanged. No ADR file was edited. The blueprint already calls the contract mandatory.
- **Checks.** `go vet` and `golangci-lint` clean on the touched packages (default, `-tags e2e`, Linux). Tests pass in
  `internal/function`, `internal/artifact`, `internal/testkit/bench`, `cmd/funcd` and the affected `pkg/funcd` e2e
  tests. `just test-linux` passes in the VM. `scripts/agent/gate.sh` on the final commit: audit, ci-full, linux, linux-tests and tree-unchanged all PASS.

### Definition of Done
12 / 12 items hold, after the bench sibling was fixed in the same commit (first Minor).

### Model scorecard
Recorded: claude-opus-5-5 on #1084 (fix) → pass, 0/0/2, 2 model-attributed, DoD 12/12.

### Recommendation
Ready for the PR.
