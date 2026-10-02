## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #341 fix, model: claude-opus-5-5)

Change: `fix/i341`, commit f767c8d — `examples/funcdconfig.yaml` (+79/-5) and a new
`internal/platform/config/example_test.go`.

### Minor 1 — the header's "EVERY key also has a FUNCD_* env override" line is false for some newly documented keys  ·  attribution: model
`examples/funcdconfig.yaml:8` still says every key has a `FUNCD_*` override. Several keys this fix adds
have no `env` tag in `internal/platform/config/config.go`: `server.tls.hosts`, `server.shaping.cors.*`,
`server.shaping.headers.*`, `server.network.internalAllow`. The issue is about the header claiming more
than the file delivers; that sentence now over-claims for the keys the fix brought in. Fix: say "most
keys" or mark the file-only keys. Non-blocking.

### Verified correct (keep it)
- **Regression test reproduces the issue.** With the `origin/main` example restored and the new test kept,
  `go test -run TestIssue341 ./internal/platform/config/` → `FAIL`: 53 × `is missing from the example`
  plus `runtime.containerd.imageOverride shows no default` — exactly the issue's list.
- **Passes with the fix under -race**: `--- PASS: TestIssue341_ExampleDocumentsEveryKeyWithDefault`; the
  whole package `ok` with `-race -count=1`.
- **Revert check**: `git revert --no-commit f767c8d` removes the test with the fix (single commit), so the
  meaningful revert is the yaml-only restore above, which fails; worktree reset to f767c8d, clean.
- **Mutants (3/3 killed)**: dropping `eventing.deadletter.maxEntries` → `is missing`; changing
  `workflow.defaultStepTimeout` to `5m` → `does not show its default`; stripping `default:` from
  `server.shaping.compression` → `shows no default`.
- **The file still loads**: an overlay probe calling `config.Load("examples/funcdconfig.yaml")` succeeds
  (`[default] memory`), so the block-style `auth.namespaces` rewrite is equivalent.
- **Defaults are true to the code**: `defaults()` values (workflow, eventing, s3gateway, site) are
  checked by the test; consumer-applied ones were checked by hand — backup interval 30s / rebaseline 24h
  and cdc retention 24h (`cmd/funcd/main.go` `parseDurationOr`), chunk 64 MiB
  (`internal/kvstore/badger/backup.go` `defaultChunkBytes`), stateDir `<dataDir>/run` (`Load`), burst
  ⇒ ratePerMin (`internal/edge/limit`), `clientIP` / `selfsigned` constants, quota 100 (config doc).
- **Root cause fixed**: the file now lists every leaf of `Config`, and the test walks the struct by
  `json` tag, so a key added later without documenting it fails CI — the drift that caused the issue
  cannot silently recur.
- **Scope**: only the example and its test; no Go behavior change; no test weakened; no ADR touched.
- **Reuse**: no existing struct walker or example-file check in the package, `internal/testkit` or the
  test harnesses; the test uses stdlib `reflect`/`regexp` and the repo's testify.
- **Conventions**: block-style YAML (the flow-style `[default]` and `{}` were removed), top-level
  imports, no comment bloat; vet clean, `golangci-lint run ./internal/platform/config/` → 0 issues.
- **Shape**: `fix(config): …`, `Fixes #341`, attribution trailer, one commit.

### Recommendation
Pass. Optionally soften the env-override header sentence (Minor 1) before the PR; it does not block.
