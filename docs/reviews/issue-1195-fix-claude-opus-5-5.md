## Verdict: pass — 0 blockers, 0 majors, 1 minor  (pyvvo/funcd issue #1195 fix, model: claude-opus-5-5)

Branch `fix/1195-engine-spec`, commit 96776b69 `fix(provider): an engine created from another spec is replaced, not
adopted` (`git diff origin/main...HEAD`: `internal/provider/runtime.go`, `internal/provider/runtime_test.go`). Issue:
"A Lake re-created or changed between two reconciler passes keeps its old engine".

### Minor
- **No test covers a failing retire** · attribution: model · evidence: overlay mutant M2 (record the fingerprint
  before the retire loop and drop the retire's error) leaves `go test ./internal/provider/` ok. The shipped order is
  right: the fingerprint is recorded only after every old engine is retired, so a failed retire is retried on the
  next pass. With M2, one failed retire would record the new spec and the next pass would adopt the old engine for
  good, which is the bug again. Fix: a test whose fake runtime fails one `Stop`/`Remove`, then checks that the next
  `Converge` replaces the engine.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** `origin/main`'s `runtime.go` overlaid →
  `TestIssue1195_ConvergeReplacesAnEngineOfAnotherSpec` fails at `require.Len(t, rt.created, 2, "an engine of another
  spec is replaced")`: one engine created, the old one adopted. With the fix it passes under `-race`.
- **Mutants.** M1 (never retire) → the regression test fails the same way. M2 survives (Minor above). M3 (`Teardown`
  does not forget) and M4 (the fingerprint keeps the replica index) survive as equivalent mutants: after a
  `Teardown` no instance is left to retire, so a stale entry only costs a map slot; and `specFingerprint`'s only
  caller passes replica 0, so the zeroing is defensive.
- **The fix addresses the cause.** `Converge` adopted any running engine whatever spec created it; ADR-0087 has it
  run the engine from the `ProviderSpec` it is given. It now compares a sha256 of the `WorkerSpec` (image, env,
  limits, mounts, owner, name) with the one it created the engine from, and retires every replica on a mismatch.
  Both of the issue's cases reach `Converge` with a new spec: a coalesced delete and re-create, and an in-place
  change (`spec.resources`, a ConfigMap's new hashed name in the env). The test changes both env and resources.
- **No per-pass churn.** Lake: `engineEnv` holds constants, `lakeURI(spec.location)`, a keypair from
  `s3gateway.DeriveKeypair` (HMAC of the master key), the fixed S3 endpoint and the resolved ConfigMap/Secret
  values; resources come from the spec; there is no storage. SQL: `engineSpec` holds the name, `EnginePassword`
  and `ChangeToken` (HMACs), the feed host and port fixed at startup, spec resources and a fixed storage path.
  `encoding/json` sorts map keys, so the env's digest is stable. Nothing holds a time or a random value. A changed
  Secret value now restarts the Lake engine, which is the intended effect.
- **Scope.** Both hunks serve the issue; no test was weakened. Keeping the fingerprint out of `WorkerSpec.Revision`
  keeps the engine IDs and container names (`lake-r0`) that other code and the lanes look up.
- **Reuse.** No existing spec-hash helper in `internal/provider` or `internal/runtime`; the retire path reuses
  `retire`. The SQL reconciler's teardown on a generation change (ADR-0232 Decision 2) still works and now overlaps
  the runtime's check without conflict.
- **Siblings.** The other worker-adopting paths (`internal/function/function.go`, `internal/function/pool.go`) key
  workers by an immutable Revision (ADR-0143), so a changed spec gets a new worker; only the provider runtime adopted
  by name alone. Both provider runtimes (Lake and SQL, `pkg/funcd/funcd.go`) share the fix. After a daemon restart
  the map is empty and a running engine is adopted as before, which the commit states.
- **ADRs.** The change realizes ADR-0087's contract and contradicts none; no ADR file was edited.
- **Conventions.** `api/fault` wrapping, a why-comment citing #1195, top-level imports, the mutex guards the map.
  Subject `fix(provider):`, `Fixes #1195`, the attribution trailer, one commit.
- **Checks.** `go test -race -count=1 ./internal/provider/ ./internal/services/lake/ ./internal/services/sql/` ok;
  `go vet` clean; `golangci-lint run ./internal/provider/` 0 issues.

### Definition of Done
11 / 12 items hold. Pending: item 8 (the repo-wide gate and the duckdb lane, run once by the implementer). Item 4
holds on the key lines (revert and M1 fail); M2 is recorded as a Minor test gap.

### Model scorecard
Recorded: claude-opus-5-5 on #1195 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/12.

### Recommendation
Ready for the PR once the gate and the lane pass. Optionally add the failing-retire test, which kills M2.
