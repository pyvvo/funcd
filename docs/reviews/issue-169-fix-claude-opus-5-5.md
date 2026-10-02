## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #169 fix, model: claude-opus-5-5)

Change: branch `fix/i169`, commit `84fe3fc` "fix(kv): reject a KVStore value cap the local API cannot serve"
(5 files, +54/-11). Issue: a `KVStore.spec.maxValueBytes` above 1 MiB passed `Validate`, but the worker-node
local API reads every KV put body through a fixed 1 MiB `http.MaxBytesReader`, so the declared cap was dead
configuration and puts above 1 MiB failed with a transport error.

The fix takes the issue's second expected outcome ("the KVStore rejects a cap the platform cannot serve"):
one constant `v1alpha1.MaxValueBytesLimit` (1 MiB) is the ceiling. `KVStore.Validate` rejects a larger cap,
the OpenAPI schema carries it as `maximum`, and the local API uses the same constant as its body cap and
names it in the over-size error.

### 🟡 Major
None.

### Minor
- **The struct-tag literal `maximum:"1048576"` can drift from `MaxValueBytesLimit`** · attribution: model ·
  `api/types/v1alpha1/kvstore.go` (the `MaxValueBytes` field tag). A Go struct tag cannot reference a constant,
  so the literal is unavoidable. But no test pins the tag to the constant. If someone changes the constant
  later, `Validate` and the local API follow it, while the published schema keeps the old maximum. A one-line
  test that reads the tag with `reflect` and compares it to `MaxValueBytesLimit` would close this gap. This is
  not blocking.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** I ran `git revert --no-commit 84fe3fc`,
  kept the fix's `kv_test.go`, and ran `go test -run TestIssue169 ./internal/workernode/local/`. The result was
  `FAIL`: `expected: 204, actual: 400`, detail `workernode.local.kv.put: read value: http: request body too
  large`, for "store cap 1048577 bytes passed Validate". This is the exact symptom in the issue.
- **It passes with the fix under `-race`.** After `git reset --hard 84fe3fc`, I ran `go test -race -count=1` on
  `./internal/workernode/local/`, `./api/types/v1alpha1/` and `./internal/services/kv/`. All three returned
  `ok`. The test is not skipped. It loops over caps of 1 MiB, 1 MiB+1 and 4 MiB, and for every cap that passes
  `Validate` it sends a put of exactly that size through the real local-API handler and the facade.
  `require.Positive(served)` guards against a vacuous pass.
- **Mutants (3 of 3 killed):**
  - M1: the `Validate` upper-bound check disabled (`if false && …`). This failed both
    `TestIssue169_DeclaredValueCapIsServable` and `TestKVStoreValidate`.
  - M2: `>` changed to `>=` (off by one at the limit). This failed both tests, because the cap at the limit
    must be accepted.
  - M3: `maxKVBytes = v1.MaxValueBytesLimit - 1`, so the transport cap is below the declared ceiling. This
    failed `TestIssue169_DeclaredValueCapIsServable`.
- **The root cause is fixed, not masked.** The mismatch between the admission-time cap and the transport cap is
  removed by deriving both from one constant. No timeout, retry or swallowed error is added. The facade's
  per-store cap check (`internal/services/kv/kv.go`) is unchanged and still runs for caps at or below the
  limit.
- **Scope:** every hunk serves the issue. The test resolver gains an optional `maxValueBytes` field. When the
  field is 0 it uses 1 MiB, so the existing KV scenario tests behave as before (they pass). No test was
  weakened or deleted. The issue's side note on `maxKeyBytes` was correctly left alone, because the issue
  itself classifies it as not a defect under ADR-0072.
- **Reuse:** the fix reuses the existing `kvHandler`/`do`/`fakeKVResolver` harness and extends it instead of
  writing a new one. It reuses `fault.Invalidf`, the shared `validateMeta` path and
  `KVStoreSpec.EffectiveMaxValueBytes`. The former local magic number now refers to the API-level constant
  instead of repeating it. `DefaultMaxValueBytes` and `maxInvokeBytes` are separate concepts, so keeping them
  as distinct constants is correct.
- **Spec in sync:** I regenerated the spec with `go run ./internal/controlplane/cmd/specgen/ -out
  api/openapi/funcd.v1alpha1.yaml`, and the working tree stayed clean. The committed `maximum: 1048576` is
  the generator's output.
- **Checks on the touched packages:** `go vet` and `golangci-lint run` returned `0 issues.` on
  `./internal/workernode/local/` and `./api/types/v1alpha1/`, and `gofmt -l` printed nothing. As the task
  scoped, I did not run the e2e suite, the Linux lint or the Lima lanes here. The group gate runs them.
- **ADRs:** no ADR file was edited. ADR-0072 and ADR-0073 fix the default at 1 MiB and require the per-op caps
  in the facade. Neither promises caps above 1 MiB, so a ceiling at the default contradicts no Decision or
  Contract. ADR-0127 (blob, 64 MiB) remains the path for large values.
- **Conventions:** `api/fault` errors are used, the import graph is respected (`internal/workernode/local`
  already imports `api/types/v1alpha1`), imports are at the top level, and the comments explain why the
  ceiling exists without narrating the code.
- **Commit shape:** the subject is `fix(kv): …`, the body carries `Fixes #169`, the test name and the
  attribution trailer, and the commit covers one issue.
