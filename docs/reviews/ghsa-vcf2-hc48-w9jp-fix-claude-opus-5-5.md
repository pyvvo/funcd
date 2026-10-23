## Verdict: pass — 0 blockers, 0 majors, 0 minors open  (fix review, GHSA-vcf2-hc48-w9jp items 4 and 5, model: claude-opus-5-5)

This report reviews the private advisory branch `fix/ghsa-vcf2-engine-credentials`, merged with the advisory's other
fixes in 08eeea98. The review ran privately, in two rounds, before publication; this record follows it.

### Rounds
1. **Changes requested, 1 Major and 2 Minors.** Major: a bound ConfigMap or Secret still reached the engine through
   keys funcd does not own (`HTTP_PROXY`, `PYTHONPATH`, `AWS_CA_BUNDLE`…); botocore takes proxies from the environment.
   Minors: a lane assertion that an error also passed; a merge-order note with ADR-0264.
2. **Pass**, with a nit left (the dropped-key Warn logs on every reconcile). After ADR-0265 landed, a third point was
   closed: a binding can no longer move the engine's `DUCKDB_EXTENSION_DIRECTORY`.

### Verified correct (keep)
- funcd's own engine keys are laid over the guarded merge, and only `QUACK_TOKEN` and `DUCKDB_*` (but the extension
  directory) reach the engine from bound data. Revert check: `TestEngineEnvOwnKeysWinOverBoundData` fails on each
  earlier `reconcile.go`.
- The shim pops the key pair from `os.environ` before DuckDB starts and binds it only into
  `CREATE TEMPORARY SECRET funcd_lake_s3`, after `allow_persistent_secrets=false`; `lock_configuration` still follows.
  The boto3 client passes `proxies={}`. Revert checks fail on the old shim.
- The duckdb lane reads `secret=redacted` from `duckdb_secrets()` and no `s3_` setting holding a funcd key.

### Checks
- `go test -race ./internal/services/lake/`, the shim tests, vet and golangci-lint: clean.
- The duckdb lane and `scripts/agent/gate.sh`: PASS on the branch and on the combined advisory tree.
