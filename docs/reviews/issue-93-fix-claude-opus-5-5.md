## Verdict: pass — 0 blockers, 0 majors, 4 minors  (issue #93 fix, model: claude-opus-5-5)

Change: branch `fix/i93`, commit f58cf40 `fix(funcd): fail startup when the stored Secrets do not match
secrets.encryptionKeyFile` — `cmd/funcd/main.go` (+21/−1) and `cmd/funcd/main_test.go` (+51).

`buildStore` now lists every stored Secret once after opening the durable Badger metastore. On a decode
failure it closes the store and fails startup with an error that names `secrets.encryptionKeyFile` and
says whether the key is wrong or missing. The issue's expected behavior ("reported clearly, ideally at
startup") is met for all three facets: a different key, a removed key, and a key added over plaintext
Secrets.

### 🟡 Major / Minor

- **Minor 1 — the test does not tell the two error messages apart** · attribution: `model`.
  Every case asserts only `ErrorContains(err, "secrets.encryptionKeyFile")`, which both branches contain.
  Mutant M3 (`case keyed:` → `case !keyed:`, which swaps the "wrong key" and "key missing" messages)
  survived: `ok github.com/pyvvo/funcd/cmd/funcd`. Fix: assert a distinct phrase per case ("do not
  decrypt" for a different key or a key over plaintext, "is not set" for a removed key).
- **Minor 2 — `context.Background()` inside a call path that has a ctx** · attribution: `model`.
  `checkSecretsDecode` (`cmd/funcd/main.go:489`) builds its own context, while its caller chain starts in
  `buildOptions(ctx, …)` (`main.go:154`). ADR-0002 is ctx-first; threading `ctx` through `buildStore`
  would let a startup cancel stop the scan. Cosmetic at today's store sizes.
- **Minor 3 — every List error is reported as a key mismatch** · attribution: `model`.
  `store.List` also returns the store's `initErr` (`internal/store/store.go:216`) and Badger read errors,
  and the scan stops on any single undecodable record. Those cases now produce "stored Secrets do not
  decrypt with secrets.encryptionKeyFile …". The cause stays visible through `%w`, so this does not hide
  anything, but the headline can mislead. Optional: check `initErr`-type failures separately, or phrase the
  message as "could not read the stored Secrets (likely a key mismatch)".
- **Minor 4 — no migration path for plaintext Secrets when a key is added** · attribution: `adr`.
  The no-key startup warning tells the operator to set a key file. With this fix, doing so on a store that
  holds plaintext Secrets stops startup (correct and fail-closed — before, it broke every Secret
  operation silently). Re-encrypting existing Secrets is key rotation/migration, which ADR-0022 defers
  ("Key rotation / per-namespace keys … a follow-up"). This is not in the scope of a fix; it needs an ADR
  or a board item. The warning text could also mention that existing Secrets must be re-applied.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** With `cmd/funcd/main.go` reverted to
  the pre-fix version and the new test kept: `--- FAIL: TestIssue93_KeyMismatchFailsAtStartup` at
  `main_test.go:364`, "An error is expected but got nil", case "key removed" — startup accepts a
  mismatched key setting.
- **Passes with the fix under `-race`**: `--- PASS: TestIssue93_KeyMismatchFailsAtStartup (0.14s)`, and
  the neighbouring `TestScenarioSecretsKeyfileActivatesEncryption` passes. The whole `cmd/funcd` package
  passes with `-race` (`ok … 6.318s`).
- **Mutants on the key lines**: M1 (`case err == nil:` → `case true:`, the check never fails) → FAIL;
  M2 (drop `_ = st.Close()` on the error path) → FAIL, because the next `buildStore` on the same
  directory cannot open the Badger lock, so the close-on-failure is covered. M3 survived (Minor 1).
- **Cause, not symptom**: the issue names two causes — no encoding marker in `decode`, and no startup
  check in `buildStore`. The fix closes the second. It does not swallow errors, add retries or skip tests.
  `store.List` (`internal/store/store.go:215`) scans the full bucket and decodes every record before
  filtering, so an empty `ListOptions{}` really reads every stored Secret. The memory mode is correctly
  skipped (it has nothing persisted). The original key still opens the store (asserted).
- **Scope**: two hunks, both for the issue; no test weakened or deleted.
- **Reuse**: the check reuses `store.Store.List` and the existing decode path. No new helper, type,
  dependency or harness. The test follows the existing `cmd/funcd/main_test.go` pattern (a real
  `buildStore` with temp-dir key files), and there is no existing verify or probe API in `internal/store`
  that it duplicates.
- **ADRs**: no ADR file changed. The fix fits ADR-0022 (one configured key, fail-closed, no silently weak
  crypto) and ADR-0065 (durable Badger metastore). It does not invent rotation.
- **Checks (touched packages)**: `gofmt -l cmd/funcd` is empty; `go build ./...` passes; `go vet ./cmd/funcd`
  passes; `golangci-lint run ./cmd/funcd/...` reports `0 issues.`. The Linux lint, e2e and the full test
  suite are left to the group gate.
- **Shape**: the subject is `fix(funcd): …`, the body explains the cause and the fix, `Fixes #93` is
  present, the attribution trailer is present, and the commit covers one issue. Comments are short
  and state the *why*. Imports are at the top level.
- **User-visible path**: `buildOptions` returns the `buildStore` error before any server starts
  (`main.go:164`), so the daemon stops at startup. The test drives this real function against a real
  Badger directory. No live daemon probe was run.

### Recommendation

Pass. Optional follow-ups for the fixer: assert distinct messages per case (Minor 1), and thread `ctx`
(Minor 2). Minor 4 belongs on the board or in an ADR about key migration and rotation.
