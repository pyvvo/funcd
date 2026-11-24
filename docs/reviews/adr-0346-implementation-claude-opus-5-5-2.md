## Verdict: changes requested — 0 blockers, 0 majors, 1 minor (ADR-0346 implementation, round 2, claude-opus-5-5)

Round 2 reviews six commits on `feat/addon-dev-workers`, 22c299fc..a51e81a1, against the round-1 report
([adr-0346-implementation-claude-opus-5-5.md](adr-0346-implementation-claude-opus-5-5.md)). A second builder wrote
them. Every check below was run at a51e81a1, not read. Round 1's three findings and its two routed items are closed.
One commit of this round, a51e81a1, drops more than the rule it implements, so ADR-0346 stays `Reviewing`.

### Minor 1 — a dev container worker loses a config key named `DUCKDB_EXTENSION_DIRECTORY` · attribution: model

- **The code.** `imageWorkerSpec` deletes the key whatever set it (`internal/function/addonruntime.go:154`). The rule
  that a51e81a1 records leaves out dev's folder of DuckDB extensions, a host path. It does not leave out a value that
  the developer binds.
- **The probe.** A Function on `deno2` binds a ConfigMap whose key `DUCKDB_EXTENSION_DIRECTORY` names a path in its
  image, and it binds no Lake. Its container worker gets no such key, while `FUNCD_CONFIG_KEYS` still lists it. The
  process worker beside it keeps the value.
- **Why it is a defect.** containerd keeps that key, because a bound config key is the developer's and wins over a
  provider's (ADR-0306 Decision 3). So the row's "as no containerd worker gets it" holds for dev's folder only.
- **The fix (builder).** Delete the key only when it holds dev's folder (`r.lakeExtensionDir`). Add a row to
  `TestDevAddonWorkerSpec` with a ConfigMap key of that name.

### Round 1's findings

- **Major 1, the real-engine scenarios on CI: closed.**
  - `workerImage` now finds a digest on Docker's classic store. It serves the engine's `docker save` from
    `imageregistry` over TLS on 127.0.0.1, which Docker trusts there without verifying, and pulls the image by that
    digest (`cmd/funcdctl/dev_worker_test.go:109-127`).
  - `mustRunOnCI` turns a skip under `CI` into a failure in each of the six scenarios. The Linux one registers it
    after its OS check.
  - This host has no Docker 28 engine, and bringing one up means downloading an image, so the builder's run on
    docker-ce 28.0.4 was not repeated here. A skip on the PR's CI now fails the run, so that CI is the check.
  - The `workers` helper now counts only worker containers, not the helpers that read a port file.
- **Minor 1, the behavior row: closed.** "The add-on runtime under dev" names the workers and cites ADR-0346 Decision 4
  and `TestScenarioDevAddonWorker`. Its two other cases became rows of their own, each with its test, and
  `just check-behavior` passes.
- **Minor 2, four rules that no test reached: closed.** Each of the four surviving mutants is now caught by a named
  test: M3e, M6a, M9 and M13.

### Routed in round 1

- **The ADR-0351 back-link: closed.** ADR-0346's header carries `Superseded in part by: ADR-0351 (2026-11-23) — the
  Contracts' ListenerFor takes the caller's ref`. This is the header line that ADR-0272 Decision 7 allows, and it is
  the only change to the ADR. ADR-0351 is `Implemented`, so it is frozen and keeps ADR-0346 under "Relates to".
- **The extension folder: decided** on the decider's delegation, in a51e81a1 and its `decided` row. Minor 1 above is
  about how that commit implements the rule.

### The registry package

`internal/testkit/imageregistry` adds no duplication.
- Its body is `e2e/laneregistry`'s code moved line for line, with exported names: `Read`, `Digest` and `Handler`.
  The lane keeps its certificate and the files it writes.
- `stallregistry` does a different job: it is a registry that never answers.
- The package imports only the standard library, so the lane's static Linux binary takes no test code.
- Its test covers a plain and a gzipped archive, each read, a HEAD, and the refusals.
- Its scope is test support, for the lane and the dev scenarios. The audit of 22c299fc..a51e81a1 raises no hard flag.
- The addon-runtime lane was not rerun: its registry is the same code, which the unit test covers.

### ✅ Verified correct (keep it)

- **The checks**, run through `scripts/agent/d`:
  - `go build ./...` exits 0.
  - `go vet` exits 0, also with `-tags dev` and for Linux.
  - `golangci-lint` reports 0 issues on the touched packages, also with `--build-tags dev` and for Linux.
  - gofmt, `go mod tidy -diff`, `check-hygiene` and `check-behavior` are clean.
  - `go test -race` passes on `imageregistry`, `internal/function`, `internal/provider/docker` and `nerdctl`.
  - `e2e/...` and `cmd/funcdctl` pass.
  - The six dev scenarios on colima's Docker: five pass, and the Linux one skips by OS, both without `CI` and with
    `CI=true`.
  - The rest of the `-tags dev` suite passes.
- **Mutants.** Round 1's 32 mutants and a new one were run at a51e81a1; each one is caught except M5d, which is
  equivalent. The new one, M14, keeps the host path, and `TestDevAddonWorkerSpec` catches it.

### Definition of Done

19 of 20 items hold, against round 1's 17. Both items of round 1's Major hold now. Plan step 8's Linux run is enforced
on CI by `mustRunOnCI`, and the builder ran it on CI's Docker version. Miss: real behavior, for the deleted config key
(Minor 1, model).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0346 (implementation, round 2) → changes-requested, 0/0/1, 1 model-attributed, DoD
19/20. See `docs/reviews/model-scorecard.md`.

### Recommendation

The builder makes the deletion conditional and adds its test row. A third round then stamps ADR-0346 `Implemented`
and F139's dev-workers part `implemented`. The PR references #1017 and does not close it.
