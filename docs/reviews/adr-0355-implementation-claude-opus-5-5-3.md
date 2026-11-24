## Verdict: pass — 0 blockers, 0 majors, 0 minors (ADR-0355 implementation, round 3, model: claude-opus-5-5)

This round reviews the last part of ADR-0355: its two lane cases on funcd's real platform. The work is funcd's branch
`feat/infrastack-outputs` at 1d583a11. It holds two commits on the unmerged ADR-0357 branch `feat/infrastack-apps`
(8d7721d0):
- 7cc4e69d pins pyvvo/funcd-addons v0.6.0 and pyvvo/funcd-functions v0.29.0;
- 1d583a11 adds the cases to `e2e/addon-infrastack.venom.yml`, with `scripts/lanes.yaml`, on the add-on's examples
  `token-stack` and `random-stack-reader` (pyvvo/funcd-addons PR #19, released in v0.6.0).

Rounds 1 and 2 passed the plugin and the runtime ([round 1](adr-0355-implementation-claude-opus-5-5.md),
[round 2](adr-0355-implementation-claude-opus-5-5-2.md)). v0.6.0 holds that code: its runtime equals the reviewed
655d686's, and its plugin differs only in the `IMAGE` line that the release stamps. The reviewer did not write the work.
Every check below was run, not read. ADR-0355 is stamped `Implemented`, and F143 `implemented`.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

None.

### Routed (not this work's)

- **A module whose every variable is sensitive admits a run that sets one** · home: the decider (ADR-0355 Decision 3),
  then pyvvo/funcd-addons' plugin · surprising, and not a defect of this work.
  - Decision 3 says "A record that would have no member is the empty schema `{}`", and the plugin's `record()` follows
    it. I ran the plugin's `contract()` offline on a module whose only variable is the sensitive `token`: its
    `variables` is `{}`. With the plain `prefix` beside it, the record is closed.
  - The empty schema admits `{"token": "lane"}`, and the runtime writes a run's variables into `run.tfvars.json`
    (`infrastack/runtime/handler.py:289`). The run's input would then hold the value, against the same Decision's "A
    sensitive variable is left out, so no run's input holds it".
  - The lane's token-stack keeps `prefix`, so it does not reach this case. Closing an empty `variables` record would
    change Decision 3, so it is the decider's ruling, as an amendment.
- **A run started before its Job holds a contract is checked at its start** · accepted: ADR-0347 Decision 3,
  `docs/behavior/jobs.md:32` and `:43`.
  - A stack applied together with its Job can therefore meet a wrong variable as a Failed run with
    `InputSchemaMismatch`, not as a 422 with `InvalidVariables`.
  - The lane applies the stack once its Job holds the contract, and its comment says why. A change would be the add-on's
    reconciler waiting for its Job's contract (F141).
- **nerdctl** stays untested: both lanes run the plugin on Docker (env).

### ✅ Verified correct (keep it)

**The lane, on this stacked branch, with ADR-0357's App rules in it**
- `scripts/agent/d timeout 1700 scripts/agent/lanes.sh feat/infrastack-outputs:addon-infrastack` printed
  `feat/infrastack-outputs:addon-infrastack @ 1d583a11: PASS (280s); final status: PASS`. Its log has no `ERRO` line.
- The plugin pushed both modules from the VM, on Docker 20.10.24, each as an index of `linux/amd64,linux/arm64`, and
  random-stack's Job took the plugin's contract: `PHASE=Ready,VARIABLES=prefix,REQUIRED=prefix`.
- `scenario infra-variables-refused` matches its Given, When and Then:
  - the module the plugin pushed has the string variable `prefix`, and the stack's reconciler Function starts each run;
  - `prefx: a` gives `REASON=InvalidVariables RUN=random-stack-g1-4545ad10 STATUS=422
    PATHS=[/variables/prefix,/variables/prefx]`, and `prefix: 3` gives `RUN=random-stack-g2-4545ad10 … STATUS=422
    PATHS=[/variables/prefix]`;
  - each refusal reads `RUNS=0`, so no run exists.
- `scenario infra-sensitive-variable-refused` matches its scenario:
  - token-stack's contract leaves `token` out (`VARIABLES=prefix`);
  - a run that sets `token` answers `STATUS=422 PATHS=[/variables/token]`, with `RUNS=0`;
  - without it, `token-stack-g2-d5d5764e` plans with `TF_VAR_token` from the Secret its Job binds
    (`ACTION=apply,ADD=1,DESTROY=0`), and the stack turns Ready.

**The static checks**
- `scripts/agent/d go test -run 'Lane' ./cmd/funcd/ ./internal/testkit/langmod/ ./scripts/` exits 0, with 15 tests.
- `go mod verify` and `go build ./...` exit 0, `go mod tidy -diff` prints nothing, and the whole of
  `./internal/testkit/langmod/` and `./scripts/` exits 0.
- `just check-hygiene` and `just check-behavior` exit 0 with this stamp in place.
- pyvvo/funcd-addons PR #19's `ci`, `e2e` and `infrastack` checks passed.

**The builder's decisions: keep all four of this ADR's**
- **The image built from the pinned add-on's `runtime/`**, served by the lane registry and named by
  `INFRASTACK_OPENTOFU_IMAGE`. Decision 1 names that variable for the add-on's lanes. The add-on's release pins, in a
  follow-up, the digest that an earlier release published (its `release.yml`, #13, #17), so the Addon's pin can lag the
  pinned version's runtime. At v0.6.0 the runtime equals v0.5.0's, whose published image the Addon pins. The cost: no
  funcd lane runs the published image itself.
- **Debian's `docker.io` 20.10.24**, from the VM's apt snapshot, so its version is pinned. Docker runs its own daemon
  beside funcd's private containerd (`scripts/lane-provision.sh:46-47`), and `just lima-example` deletes the VM when the
  lane ends (`justfile:359`). The lane is `optional`, so it never shares the VM of `lima-example-all`
  (`scripts/lane.py:57`).
- **The stack is applied once its Job holds the plugin's contract.** Decision 6's check runs at a run's creation only
  then (Routed, the second item).
- **token-stack keeps the plain variable `prefix`.** The scenario's Given needs only a sensitive `token`. The case that
  it leaves out is Routed, the first item.

**Mutant controls**, each new assertion weakened in turn, by reading the steps:
- `PATHS=[…]` catches a 422 whose details name no path, and an open `variables` record, which would name only
  `/variables/prefix`.
- `STATUS=422` catches another refusal, such as 400, 409 or 413.
- `RUNS=0` catches a run that is created and then fails at its start; `REASON=InvalidVariables` catches it too, since
  the stack would read `ApplyFailed`.
- `RUN=…-g1-` and `RUN=…-g2-` tie each refusal to its generation, so a stale message cannot pass.
- `VARIABLES=prefix` on token-stack catches a contract that keeps `token`. Without it, `PATHS=[/variables/token]` and
  `RUNS=0` still catch that contract, since it would admit the run.
- `ACTION=apply,ADD=1` on `token-stack-g2-` catches a runtime that drops `TF_VAR_token`: OpenTofu's plan would stop on
  the missing variable, and no run would await an approval.
- `PLATFORMS=linux/amd64,linux/arm64` is the only check that catches a push of one platform, since the VM runs its
  own.
- I found no broken platform that passes these two cases.

### Definition of Done

14 of 14 items hold: ADR-0355's Review checklist (5) and the generic DoD (9). Round 2's two routed misses are closed:
the lane scenarios pass, and the status moves at this stamp.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0355 (implementation, round 3) → pass, 0/0/0, 0 model-attributed, DoD 14/14. See
docs/reviews/model-scorecard.md.

### Recommendation

Stamped: ADR-0355 `Implemented`, F143 `implemented`. The PR that merges this branch carries `Fixes #1021`. The decider
rules on the first routed item.
