## Verdict: pass — 0 blockers, 0 majors  (ADR-0304 implementation, model: claude-opus-5-5)

Reviewed on branch `feat/pin-validator-free-shims` at the implementation commit, rebased on main `d66ebc3a` (#1347).
The reviewer ran every check below; it changed none of the work.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

None.

### ✅ Verified correct (keep it)

- **Pins (Decision 1).** `go.mod` names `github.com/pyvvo/funcd-typescript v0.24.0` and `github.com/pyvvo/funcd-python
  v0.19.0`, the releases of pyvvo/funcd-typescript#96 and pyvvo/funcd-python#111. No `go.work` and no pseudo-version
  are committed; `go mod verify` printed `all modules verified`.
- **funcd names no contract (Decision 2).** `writeOpenContract`, `openContractName`, `addContractEnv` with its two
  calls, and the pool manifest row's `Contract` field are gone (`internal/function/contracts.go`,
  `internal/function/function.go`, `internal/function/pool.go`). `contractFileIn` stays, and its comment and
  `contractDeliveryNames`' name their readers. No non-test file sets `FUNCD_CONTRACT_PATH` or writes
  `.funcd-contract-open.json`; only `TestScenarioTheShimsReadNoContract` sets them, to prove the shims ignore them.
- **The http refusal (Decision 3).** `materialize` returns `httpContractError` when an http Function's bundle root
  holds a delivered blob, and the reconcile pass maps it beside `ContractMissing` and `ContractInvalid` to a
  `gateFailed` with reason `ShapeInvalid` and `shapeInvalid: true`; its message names `spec.protocol: http`. Two
  mutants fail `TestScenarioAnHTTPFunctionWithAContractNeverStarts`: without the refusal, ShapeValid stays True;
  without the mapping, the reconcile pass returns an error. `TestScenarioHTTPMismatch` passes unchanged (0.40 s).
- **No fastjsonschema (Decision 4).** The python314 image's `requirements.txt` and its `COPY` and `pip install` are
  gone, the dev shell's Python lists only `pyyaml` (`flake.nix`), the Linux test VM's template installs no wheel and
  probes `python3.14 --version`, Dependabot's `pip` entry keeps only `/images/runtime/duckdb` (the image stays under
  `docker`), and `docs/DOCUMENTATION-PLAN.md` drops it. The startup messages (`cmd/funcd/main.go`,
  `cmd/funcdctl/dev.go`), the e2e harness's `requirePython`, the CI-lane assertion, `pypool_test.go`'s skip and the
  three fake-interpreter fixtures no longer name it. Outside the frozen docs, only `bench/contract-validation` imports
  it, as the ADR's Scope says.
- **The image check.** `build-runtime-images` runs `_check-python-image` after the python314 build: the image must
  print a marker after `import funcd_shim.shim, ssl` and then fail `import fastjsonschema` with `No module named
  'fastjsonschema'`. The marker is a deliberate strengthening of the plan's command: with an older shim that imports
  fastjsonschema at load, the plan's command fails with the same message, so it alone would pass an image that cannot
  run its shim. An image for an arch the docker host does not run skips the check with a SKIP line.
- **Scenarios, each with its named passing test:**
  - `the-shims-read-no-contract` → `TestScenarioTheShimsReadNoContract` (`internal/function/readnocontract_internal_test.go`):
    node and python, solo and pooled, all pass on the pins. On the old pins, node answers 422 and the old Python shim
    cannot load without fastjsonschema.
  - `funcd-names-no-contract` → `TestScenarioFuncdNamesNoContract`, in package `function` (process and container
    mode) and in package `function_test` (a pool member, its blob through `pushHandler` and the ORAS materializer);
    both fail on the old reconciler.
  - `an-http-function-with-a-contract-never-starts` → `TestScenarioAnHTTPFunctionWithAContractNeverStarts`
    (`internal/function/contract_missing_test.go`), beside `TestIssue1084_OCIArtifactWithoutContractDoesNotStart`;
    it fails on the old reconciler.
  - `the-python-image-runs-without-fastjsonschema` → `just lima-example kv` on the rebuilt images: `pycounter` reached Ready
    and served both of the suite's calls (final status PASS, 0 failures, 3m11s with the image builds), and
    `_check-python-image` passed in its build. The Linux test VM, recreated from the new template, runs Python
    3.14.4, and `import fastjsonschema` fails there with `ModuleNotFoundError`.
- **Replaced tests are removed, not skipped:** `TestScenarioTheShimsStopChecking`, `TestScenarioContractEnvProcessMode`,
  `TestScenarioContractEnvContainerMode`, `TestScenarioNoContractNoEnv` and `TestPoolManifestNamesTheOpenContract`.
- **Checks.** `scripts/agent/gate.sh` on the implementation commit `e96ee8cc`: `GATE PASS` in 18m48s — the audit
  (4 s), `just ci-full` with the e2e suite (813 s), the Linux build, vet and lint (18 s), `just test-linux` on the
  recreated VM (85 s), the stress run of the changed tests (208 s), and an unchanged tree. Before the pins, with a
  local `go.work` naming the shim branches: the touched packages' tests with `-race` (`internal/function`,
  `cmd/funcd`, `internal/testkit/bench`, and `cmd/funcdctl` with `-tags dev` in 188 s), the lane guard tests and
  lint (0 issues); on the pins, the four scenario tests again and `TestScenarioHTTPMismatch`.
- **Conventions.** No new exported API, `any`, `panic` or logging import; the new error type follows
  `contractMissingError`'s shape; no new config key, API field, CLI flag or dependency.
- **Tracking.** ADR-0304 went `Accepted → Reviewing` with its substance unchanged, and F88's shims part reads `reviewing`.

### Definition of Done

18 / 18 items hold: the ADR's six Review-checklist items, the four items of its Definition of done, and
the eight generic items that apply (the contract-suite item does not: this ADR adds no port). Misses: none.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0304 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 18/18. See
docs/reviews/model-scorecard.md.

### Recommendation

Stamp ADR-0304 `Implemented` and F88's shims part `implemented`.
