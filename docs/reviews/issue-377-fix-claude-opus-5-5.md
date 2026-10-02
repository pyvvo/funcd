## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #377 fix, model: claude-opus-5-5)

Change: `fix/i377`, commit 984c589 `fix(kv): reject a KVStore key cap the durable engine cannot store`
(`api/types/v1alpha1/kvstore.go`, `api/types/v1alpha1/kvstore_test.go`, `api/openapi/funcd.v1alpha1.yaml`,
`internal/services/kv/kv_test.go`).

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **The KV Facade's Delete still sends an over-cap key to the driver** · attribution: issue · evidence:
  `internal/services/kv/kv.go` `Delete` checks no key cap, and Badger's `Txn.Delete` goes through
  `Txn.modify`, which rejects a key over 65000 bytes with `exceedsSize` (the hex dump). So a delete with a
  key of about 65 KB still returns `Internal` with the dumped internal key. This gap exists for every cap
  (the default 1 KiB too), so it is not caused by the unbounded `maxKeyBytes` that the issue reports, and
  the issue names only the Put path. It is recorded for a follow-up issue (apply the store's key cap in
  `Delete`, as `Put` does), not scored against the fix.

### ✅ Verified correct (keep it)
- **The regression test fails on the pre-fix code for the reported reason.** With
  `git revert --no-commit 984c589` and the two test files restored from HEAD,
  `TestIssue377_DeclaredKeyCapIsStorable` fails: a 65000-byte cap passes `Validate`, and the Put of a
  65000-byte key fails in Badger with the `exceeded 65000 limit` error and the hex dump of the key, as in the
  issue. The worktree was reset to 984c589 and is clean.
- **It passes with the fix under `-race`**: `go test -race` for `api/types/v1alpha1` and
  `internal/services/kv` is green.
- **Mutants are killed** (each run with `-run 'TestIssue377|TestKVStoreValidate'`, then restored):
  `MaxKeyBytesLimit` 64000 → 65000 fails both tests (the schema-tag check and the Badger write);
  removing the upper-bound check fails both (`want Invalid, got <nil>`, and the Badger write);
  `>` → `>=` fails both (`maxKeyBytes at the limit rejected`).
- **The cause is fixed, not masked.** `Validate` now bounds `spec.maxKeyBytes` from above, so a cap the
  engine cannot store is rejected at admission, and the existing facade check (`len(key) > b.MaxKeyBytes`)
  turns every over-cap Put into `Invalid` before the driver. The 64000 limit is correct: namespace, store
  and table are DNS-1123 labels of at most 63 bytes (`api/types/v1alpha1/ids.go` `DNSLabel`), so the prefix
  `<ns>/<store>/<table>/` adds at most 192 bytes, and 64192 < 65000. The regression test proves this with
  three 63-byte labels against a real Badger engine.
- **The published schema matches.** The `maximum:"64000"` tag is pinned to the constant by a reflection
  check, and regenerating the spec with `internal/controlplane/cmd/specgen` gives a file identical to the
  committed `api/openapi/funcd.v1alpha1.yaml`.
- **Reuse.** The fix mirrors the existing `MaxValueBytesLimit` pattern from #169 (constant, `Validate`
  check, schema tag) and reuses the test file's `fakeResolver`, `bkey` and `allowAll` and the existing
  `kvbadger.Open` options. No new helper, type or dependency.
- **Scope and conventions.** Every hunk serves the issue. The error is `fault.Invalidf` with an op name;
  imports are at top level; comments explain the why (the Badger limit and the prefix budget). No test
  was weakened. No ADR file was edited, and the change agrees with ADR-0072 and ADR-0073 (per-op caps give
  `Invalid`).
- **Checks.** `go vet` and `golangci-lint` for the two touched packages: clean (0 issues).
- **Shape.** The subject is `fix(kv): …`, the body has `Fixes #377` and the attribution trailer, and the
  commit holds one issue.

### Definition of Done
10 of 10 applicable items hold (item 8 counted on the touched packages; the repo-wide, Linux-lint and e2e
checks run in the group gate).

### Model scorecard
claude-opus-5-5 · fix · pass · blockers 0 · majors 0 · minors 1 · model-attributed 0 · DoD 10/10.

### Recommendation
Pass. Open a follow-up issue for the unchecked key size in the KV Facade's `Delete` (minor, attribution:
issue).
