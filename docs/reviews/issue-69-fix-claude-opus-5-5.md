# Issue #69 Fix Review — a pooled member held Failed by the config gate is still loaded into the pool

**Verdict**: **pass**. The regression test fails on the pre-fix code for the reported reason and passes with the fix
under `-race`. A real-pool probe shows the user-visible harm is gone: a gated member whose module throws without its
Secret no longer takes its healthy sibling from Ready to Degraded. The change removes the cause the issue names,
reuses the gate itself, touches nothing else, and conforms to ADR-0046, ADR-0057, ADR-0093 and ADR-0145. There are
two Minors, and neither blocks sign-off.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #69 · ADR-0057 (fail closed, no worker with the secret absent) · ADR-0093 Decision 3 (the
pooled gate covers config or secrets) · ADR-0046 Decisions 3, 4 and 6 · ADR-0145 Decision 5 · ADR-0002 · `CLAUDE.md`
style rules

The fix is one commit, `e8a1d56`, on `fix/205-pooling`. Other issues of the pooling group have their own commits on
that branch; this review covers only `e8a1d56`. The commit touches `internal/function/pool.go` (`poolManifest`, 4
lines plus its doc comment) and `internal/function/pool_test.go` (one new test). The review ran at the branch head
`2500327`.

## Verdict: pass — 0 blockers, 0 majors  (issue #69 fix, model: claude-opus-5-5)

### Minor
- **Minor · model — one mutant of the fix line survives.** The fix calls `r.resolveBindingEnv(ctx, m, true)`. A mutant
  that passes `false` instead of `true` passes the whole `internal/function` suite. In the test, the Secret `db` and the
  ConfigMap `settings` do not exist, and the harness has no secret resolver. So the non-pooled path also returns an
  error for both gated members, and they are still left out. With a resolvable Secret or ConfigMap, that mutant would
  load the gated member's code into the pool again. Only the `true` argument makes the exclusion independent of whether
  the binding resolves. **Fix**: in `TestIssue69_GatedMemberNotLoadedIntoPool`, store a ConfigMap `settings` (the
  `createCM` helper in `config_internal_test.go` shows how; the test is in the external `function_test` package, so create it
  through `h.st`) so that only the pooled gate can exclude `c-config`.
- **Minor · issue — the issue's "Expected behavior" asks for more than its own Summary.** The Expected section says a
  gated member "does not occupy a slot". The Summary says that the cap facet is by design under ADR-0046 Decision 3
  (admission over all declaring functions, "Ready or not") and is not a defect on its own. The fix keeps admission
  unchanged and says so in the commit message. That matches the Implemented ADR-0046. ADR-0145 Decision 5 excluded
  platform-mismatched members from the cap through an ADR, so excluding gated members from the cap would also need an
  ADR. **Fix**: none in this change. If the cap facet is wanted, route it to `/adr`.

### Observation (not scored, outside this issue)
- The issue's Root cause section lists four gates whose result `sameKeyFunctions` ignores: secrets, config,
  data-reference and catalog. The issue title, Summary and Expected behavior cover only the config/secret gate, and the
  fix covers exactly that gate. A pooled member held Pending by the data-reference gate (ADR-0121, step 3c-bis) is still
  materialized into its siblings' pool manifest, and so its code still runs before its Bucket or KVStore exists. That
  is the same defect class as #69, for a different gate. It belongs in a separate issue.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix.** A revert of `e8a1d56` conflicts in `pool_test.go`, because the revert
  also removes the test that later commits sit next to. So I reverse-applied only the `pool.go` hunk of `e8a1d56`
  (`git show e8a1d56 -- internal/function/pool.go | git apply -R`) and kept the test. With that change,
  `go test -race -run TestIssue69_ ./internal/function/` fails at `pool_test.go:258`: the pool manifest is
  `[a-ok, b-secret, c-config]`, which "should not contain b-secret" ("the member gated on its Secret gets no worker").
  The earlier assertions pass on the pre-fix code: both gated members report `SecretResolveFailed` and `a-ok` is Ready.
  So the test fails for the reason the issue reports, which is that a gated member is loaded into the pool.
- **It passes with the fix**: after `git reset --hard 2500327`, `-race -count=3` gives 3/3. Nothing is skipped; the
  harness uses the fake runtime, so the test needs no `node`.
- **The user-visible harm is gone (real pool probe).** I added a scratch e2e probe next to `pooling_e2e_test.go`. It
  used the real process driver and `pool.mjs` with `PoolLimit 2`, and I deleted it afterwards. The probe starts `a-ok`
  Ready, then applies `b-gated` with `spec.secrets: db` and a module that throws at import when the secret is absent,
  then re-applies `a-ok` to force a pool rebuild.
  - With the fix: `b-gated` is Failed, `a-ok` stays `Ready`, and `POST /function/a-ok` returns `200`.
  - Without the fix (the same `pool.go` reverse-apply): `a-ok` goes `Degraded`, and `POST /function/a-ok` returns
    `503` ("did not become ready within 30s"). This reproduces the issue's second independent probe.
- **Cause, not symptom.** The issue names the cause: the manifest is built from every declaring member, whatever its
  gates decided. The fix makes `poolManifest` skip a member that the pooled config/secret gate fails closed. It does
  this before the member is materialized and before it counts toward the pool's desired replica, so the gated member
  neither runs nor keeps the pool up. Nothing is retried, swallowed or skipped to hide the defect.
- **Mutants.** Two of the three overlay mutants on the fix line fail `TestIssue69_`: limiting the skip to members with
  `spec.secrets` (the `c-config` assertion fails), and limiting it to `self` (the `b-secret` assertion fails). The third
  mutant survives; see the first Minor.
- **Reuse.** The fix calls the gate itself (`resolveBindingEnv(ctx, m, true)`, `internal/function/secrets.go`), not a
  copy of its condition. So the pool and the member's own reconcile (step 3c, `function.go`) cannot disagree on what
  the pooled gate rejects. With `pooled == true` the function returns before any resolver or store call, so it adds no
  I/O to the manifest loop. The test reuses `newShimHarness`, `withSwitch`, `withNodePool` and `h.rt.specFor`.
- **Placement fits the ADRs.** The skip is in `poolManifest`, not in `sameKeyFunctions`. So admission (the cap and
  `PoolFull`) stays a function of declared membership, as ADR-0046 Decision 3 requires. ADR-0046 Decision 4 builds the
  manifest "from the admitted members only"; leaving out a further member is consistent with it, and it follows the
  existing precedent in the same loop (a member whose artifact cannot be materialized is left out). It satisfies
  ADR-0057 ("no worker is started") and ADR-0093 Decision 3 (the pooled gate covers config or secrets), and it applies
  the reasoning of ADR-0145 Decision 5 (a member that cannot run is not materialized). A gated pooled member gets no
  route: `servingWorkerRuns` looks up instances by the member's own name, which a pool worker never has, so
  `gateFailed` writes Failed with zero replicas.
- **Scope.** Both hunks serve the issue. No ADR file, no other test and no living doc was changed. The `poolManifest`
  doc comment gained one sentence for the new rule; `sameKeyFunctions` and `convergePooled` comments stay true.
- **Conventions.** `api/fault` errors are unchanged, ctx is first, and there are no new imports, types or `any`. The
  test comment states the why and cites the ADRs.
- **Checks** (through `nix develop -c`): `gofmt -l internal/function` is empty; `go build ./...` and
  `GOOS=linux go build ./...` pass; `go vet ./internal/function/` passes on host and Linux; `golangci-lint run
  ./internal/function/...` reports 0 issues on host and on Linux; `go test -race -count=1 ./internal/function/...
  ./internal/pooling/...` passes; `go test -tags e2e -count=1 ./pkg/funcd/...` passes (105.5 s), including the
  pooling e2e scenarios; `just check-hygiene` is clean. A first e2e run with `TMPDIR` set to a long scratch path failed
  five tests with `FUNCD_INVOKE_SOCKET unset`, because the Unix socket path was too long. With the default `TMPDIR`
  those five tests pass and the full suite passes (env; not a finding). The Lima lanes were not run, by instruction.
- **Shape.** The subject is `fix(function): keep a pooled member gated on its Secret or ConfigMap out of the pool`; the
  body has `Fixes #69`, names the regression test and ends with the attribution trailer. The commit covers one issue.

### Definition of Done
11 / 11 fix-checklist items hold. Item 4 holds because the revert and two of three mutants fail a test; the surviving
mutant is the first Minor (model).

### Model scorecard
Not recorded by this stage. Ledger fields: claude-opus-5-5 on issue #69 (fix) → pass, 0/0/2, 1 model-attributed,
DoD 11/11.

### Recommendation
Sign off. Optionally strengthen the test with a resolvable ConfigMap so that the `pooled` argument is pinned. File the
data-reference gate gap as its own issue, and route the cap facet to `/adr` if it is wanted.
