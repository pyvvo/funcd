## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0198 implementation, model: claude-opus-5-5)

Branch `feat/adr-0198-presign-expiry-grammar`, three commits on `origin/main` (fc6d5379, up to date):
`1cef96a1 feat(blob)!: refuse a bad presign expiry or method with 400`, `9b46d23d test(blob): check the s3blob
presign lifetime offline`, `6215934c docs(adr): move ADR-0198 to Reviewing`. Diff: 8 files, +251/-29.

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **m1 — the signer tests are in a new file, not the one the Test plan names** · attribution: model ·
  `internal/blob/gocloud/presign_test.go` holds `TestScenarioValidExpiryHonouredS3Signer` and
  `TestScenarioAbsentExpiryDefaultsS3Signer`; the Test plan names `internal/blob/gocloud/gocloud_test.go`. The
  content matches the plan exactly (dummy credentials through `t.Setenv`, `AWS_PROFILE` cleared, the config files in
  `t.TempDir()`, endpoint `http://127.0.0.1:9`, `X-Amz-Expires` 600 / 5400 / 900). A separate file is a sound
  choice; no action is needed.

### ✅ Verified correct (keep it)

- **Checks run** (pinned toolchain through `scripts/agent/d`, all exit 0):
  - `go build ./...`: darwin 0, `GOOS=linux` 0.
  - `go vet ./internal/workernode/local/ ./internal/blob/gocloud/`: darwin 0, linux 0.
  - `golangci-lint run ./internal/workernode/local/... ./internal/blob/gocloud/...`: darwin "0 issues.",
    `GOOS=linux` "0 issues.".
  - `go test -race -count=1 ./internal/workernode/local/ ./internal/blob/gocloud/ ./api/types/v1alpha1/`: all ok.
  - `go mod tidy -diff` 0; `go mod verify` "all modules verified"; `just check-hygiene` "hygiene: clean".
- **Every Scenario has a same-name test, un-skipped and passing** (`-race -v`):
  `TestScenarioValidExpiryHonoured`, `TestScenarioBadExpiryRefused` (all 11 values of the scenario),
  `TestScenarioUnknownMethodRefused` (all 5), `TestScenarioAbsentExpiryDefaults`,
  `TestScenarioPythonShimTakesDurationString`, `TestScenarioTypescriptShimTakesDurationString`, and the signer pair
  above. The plan's extra rows are covered by `TestADR0198_SignCheckOrder` (unbound alias with a good expiry is 403,
  with a bad one 400; the method is checked before the expiry) and `TestADR0198_SignOptsFromQueryContract` (the
  bounds `1s` and `168h` accepted, `999ms` and `168h1s` refused, `2000ms` accepted as 2 s, exact detail strings,
  `fault.Invalid`, zero options on refusal).
- **Mutants** (applied with `go test -overlay`, the work untouched), all killed:
  - dropping the `opts.Expiry%time.Second != 0` check fails `TestADR0198_SignOptsFromQueryContract` and
    `TestScenarioBadExpiryRefused`;
  - `!q.Has("expiry")` changed to `q.Get("expiry") == ""` (empty taken as absent) fails the contract test, the
    bad-expiry scenario and both shim scenarios;
  - the upper bound loosened by one second fails the contract test and the bad-expiry scenario.
- **Contracts** (`internal/workernode/local/blob.go`): `const minSignExpiry, maxSignExpiry = time.Second,
  168 * time.Hour` and `func signOptsFromQuery(q url.Values) (blob.SignOptions, error)` match the Contracts block,
  doc comments included. The three `detail` strings match the Contracts table word for word, under the op
  `workernode.local.blob.sign`, written with `fault.WriteProblem` as 400 `urn:funcd:problem:invalid`.
- **Decision 1**: the refusal returns before `b.SignedURL`, so neither the PDP nor the driver is reached; the
  recording bucket proves no call on every refusal.
- **Decisions 2 and 3**: `url.Values.Has` separates absent from empty; the method match is exact and
  case-sensitive (`string(blob.SignGet)` etc.); the expiry goes through `v1.ParseDuration` (ADR-0194) with no copy of
  the pattern; `grep -n 'time.ParseDuration' internal/workernode/local/blob.go` finds nothing.
- **Decisions 4 and 5 (shims and pin)**: `go.mod` pins `funcd-python v0.6.0`, whose `signed_url` takes
  `expiry: str | None`, raises `TypeError` for a non-`str`, quotes and sends `""`, and whose CHANGELOG lists only the
  breaking `signed_url` change; `funcd-typescript v0.9.0` (the "next pin" the plan allows) sends `expiry` when
  `!= null`, names the grammar in the doc comment, and lists only that breaking change. A module-cache diff of each
  version pair shows only the blob files, their tests, the built bundles, and release metadata changed. The funcd
  commit carries `!`, `BREAKING CHANGE:` and `Fixes #823`.
- **Conventions (ADR-0002)**: `api/fault` errors, no `any` in signatures, no `panic`, `slog` only, no new
  dependency, the shadowing local `url` renamed to `signed` so `net/url` can be imported.
- **Tracking**: the ADR diff is the one `Accepted → Reviewing` status line; the F92 row moves only its
  "presign expiry" sub-status from `accepted` to `reviewing`.

### Definition of Done

19 / 19 hold (ADR Review checklist 6, ADR Definition of done 3, generic DoD 10). Two items hold on component
evidence: `just ci` was not run here because the gate runs it (its build, vet, lint, tidy, hygiene and touched-package
tests are green above), and the funcd-python shim suite could not be run here because the host Python has no
`pytest` (env); its tests at the pinned tag cover the scenario (`test_scenario_blob_signed_url`,
`…_sends_expiry_as_given`, `…_non_string_expiry_raises`), and the tag was released by that repo's CI.

### Model scorecard

Not recorded by this run (the caller records the ledger). Row below.

### Recommendation

Pass. Nothing loops back to the builder. The gate runs `just ci-full` and the Linux checks once for the PR.

```json
{
  "date": "2026-10-08",
  "adr": "0198",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 19,
  "dod_total": 19,
  "report": "docs/reviews/adr-0198-implementation-claude-opus-5-5.md",
  "notes": "loop 1 (6215934c on fc6d5379). signOptsFromQuery matches the Contracts (Has for absent vs empty, exact case-sensitive method, v1alpha1.ParseDuration, whole seconds 1s-168h, three detail strings verbatim), refusal before SignedURL; pins funcd-python v0.6.0 and funcd-typescript v0.9.0 (changelogs: only the presign change). 8 scenario tests + check-order + contract table pass with -race; hermetic s3blob signer gives X-Amz-Expires 600/5400/900. 3 mutants (drop whole-second check, empty as absent, upper bound +1s) all killed. Build, vet, lint darwin+linux clean; tidy, verify, hygiene clean. m1 [model] signer tests in presign_test.go, not gocloud_test.go as the plan names (no action). Python shim pytest not run here (env: no pytest)."
}
```
