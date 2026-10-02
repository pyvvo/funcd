## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #191 fix, model: claude-opus-5-5)

Change: commit 75c8d9e `fix(funcd): keep the KV in memory under --memory when kvstore.engine is badger`
(`cmd/funcd/main.go`, `internal/platform/config/config.go`, `cmd/funcd/kvbackup_test.go`; +28/−2).

The issue: `buildKVStore` checked only `cfg.Kvstore.Engine` and never `cfg.Storage.Mode`, so
`funcd --memory` with `kvstore.engine: badger` opened a durable Badger KV at `<dataDir>/kv`, against
ADR-0043 ("`funcd --memory` writes nothing to disk") and against the metastore/workflow/DLQ, which are
in memory in that mode. The fix adds a `Storage.Mode == "memory"` branch that returns the in-memory
driver with a warning — the same precedence `main.go` already applies to the workflow and dead-letter
stores (`cfg.Storage.Mode != "memory"` guards at `main.go:309`/`:324`).

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The override warning does not say that `kvstore.backup` / `kvstore.cdc` are also skipped** ·
  attribution: model · evidence: `cmd/funcd/main.go:387-390` returns before the backup/CDC wiring, so a
  config with `engine: badger` + `backup.enabled` or `cdc.enabled` under `--memory` silently runs without
  the DR export or the change-feed; the warning only mentions the KV being in memory. Consistent with
  "memory mode wins", and the example config already says the per-service stores are unused in memory
  mode, so not a defect of the fix — but naming the skipped seams in the warning (or one extra `slog`
  attr) would make the override fully visible. Non-blocking.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 75c8d9e` with the new test
  kept → `go test -race -run TestIssue191 ./cmd/funcd/` → `--- FAIL: TestIssue191_MemoryFlagKeepsKVOffDisk`,
  `directory ".../kv" exists` / "--memory must not open a durable Badger KV" (exit 1). That is exactly the
  issue's observed `<dataDir>/kv` Badger instance.
- **Passes with the fix under `-race`.** After `git reset --hard 75c8d9e`: `TestIssue191_…` and all five
  `TestScenarioDaemon*` KV tests PASS; `go test -race -count=1 ./cmd/funcd/ ./internal/platform/config/` → ok, ok.
- **The test drives the real path.** It goes through `config.Load(path, config.Flags{MemoryOnly: &true})`
  — the same flag plumbing as `funcd --memory` — then `buildKVStore`, writes a key, and asserts the
  derived `Kvstore.DataDir` does not exist. This reproduces the issue's CLI steps without a daemon, so a
  separate daemon probe was not needed.
- **Mutants killed (2/2).** M1: `Storage.Mode == "memory"` → `== "ephemeral"` → `TestIssue191_…` FAILS.
  M2: `==` → `!=` → `TestIssue191_…`, `TestScenarioDaemonBackupEnabledRequiresTarget` and
  `TestScenarioDaemonCDCEnabledRequiresSink` FAIL. Both restored; tree clean.
- **Root cause, not symptom.** The missing `Storage.Mode` check named in the issue is the line fixed; no
  timeout, retry, swallowed error or skip. The chosen resolution ("memory mode wins") is one of the two
  the issue lists as expected, and matches ADR-0043 and the existing workflow/DLQ precedence.
- **Scope.** Every hunk serves the issue: the guard, the `buildKVStore` doc comment, the `Kvstore.Engine`
  field comment (now matching the workflow/deadletter "ignored (in-memory) when Storage.Mode is memory"
  wording), and the regression test. No test weakened or deleted.
- **Reuse, no duplication.** Reuses `kvmemory.New()` and the existing `noop` start func; the test reuses
  `config.Load`, `buildKVStore` and the file's `io.Closer` cleanup idiom. Not using the file's
  `kvBadgerCfg` helper is correct: that helper bypasses `config.Load`, while this test must exercise the
  `--memory` flag path. No new helper, type or dependency.
- **Conventions.** `log/slog` only; the warning follows the `"funcd: …"` message idiom of the other
  `Warn` calls in `main.go`; the embedded YAML is block style; imports at top level (`os` added); comments
  are short and say *why* (ADR-0043). No `any` in signatures, no import-graph change.
- **ADRs.** Conforms to ADR-0043 (`--memory` is fully ephemeral) and ADR-0066/0069 (memory default,
  Badger opt-in); no ADR file touched. The example config's existing note ("In storage.mode: memory
  these are unused") is now true for the KV.
- **Checks (touched packages).** `gofmt -l` clean; `go build ./...` ok; `go vet ./cmd/funcd/
  ./internal/platform/config/` ok; `golangci-lint run` on both → 0 issues; race tests green. Linux lint,
  e2e and lanes are left to the group gate.
- **Shape.** `fix(funcd):` subject, `Fixes #191`, the attribution trailer, one issue in one commit.

### Recommendation
Pass. Optionally fold the skipped-backup/CDC detail into the warning in a later touch; it does not block.
