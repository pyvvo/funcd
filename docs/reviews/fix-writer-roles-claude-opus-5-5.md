## Verdict: pass — 0 blockers, 0 majors, 0 minors  (writer roles fix, model: claude-opus-5-5)

This review covers a78020da on `fix/limits-followup`: "a writer role writes only the data kinds its actions name". The
checks ran in a worktree detached at a7cccf39, the branch's tip. `scripts/agent/gate.sh` runs once on the branch, in the
integration session, and did not run here.

### Checks run
- **Revert check**: with `origin/main`'s `internal/services/roles/writers.go` overlaid, `TestWriterRoleWritesOnlyItsKinds`
  fails.
- **Mutants**, with `go test -overlay`: the KV filter removed, the blob filter removed, and the KV filter asking
  `s3::write`. Each one fails `TestWriterRoleWritesOnlyItsKinds`.
- `go test -race -count=1 ./internal/services/roles/ ./internal/auth/cedar/`: ok, with the other packages the branch
  touches.
- `go vet` and `golangci-lint run` on the touched packages, on darwin and with `GOOS=linux`: ok, 0 issues.

### ✅ Verified correct (keep)
- **The rule**: `BlobWriters` keeps a grant only when its `Writes` holds `s3::write`, and `KVWriters` only when they
  hold `kv::write`. That is the filter `SQLWriters` already applies to `sql::write` (ADR-0233). The removed `sqlOnly`
  helper is subsumed: a grant writing only SQL holds neither action. `TestSQLWriterMakesNoKVOrBlobWriter` still passes.
- **Every case**: the test grants each built-in single-kind writer role namespace-wide. It checks both directions, plus
  Contributor writing both kinds. Grants scoped to a bucket prefix or a KV store were already limited to their own kind
  by the scope `switch`.
- **No siblings**: `internal/services/roles.Lister` is the only `cedar.WriterLister`. Its three methods, the KV, S3 and
  SQL capabilities' only callers (`internal/auth/cedar/capabilities.go:172`, `:328`, `:536`), now each filter on their
  own action.
- **Reuse**: the fix reads `WriterGrant.Writes`, which `cedar.WriterGrants` already fills. Nothing new was added.
- **Scope and shape**: two files, one commit, a `fix(iam):` subject and the attribution trailer. The text describes
  behavior only.

### Fix checklist
12 of 12. The test is named for its behavior rather than `TestIssue<N>_…`, and the commit has no `Fixes #N`, because
the fix has no public issue. Lanes and the gate run once on the branch.

### Model scorecard
Recorded: claude-opus-5-5 on writer-roles (fix) → pass, 0/0/0, DoD 12/12. See docs/reviews/model-scorecard.md.

### Recommendation
Ship it with the branch.
