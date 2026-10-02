## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #97 fix, model: claude-opus-5-5)

Change: branch `fix/i97`, commit `8ab0d79` — `fix(artifact): keep every tag when funcdctl pushes run in parallel into one oci-layout`.
Touched: `internal/artifact/artifact.go`, `internal/artifact/layout_internal_test.go` (new), `go.mod` (`gofrs/flock` indirect → direct).

The issue: two `funcdctl push` processes into one `oci-layout://` both exit 0, but `index.json` keeps only one
new tag, because each command opens its own oras-go `oci.Store`, which reads `index.json` once at open and
rewrites it whole on every manifest push and tag, with an in-process lock only.

The fix: a local layout resolves to a `layoutTarget`. Reads use the store opened by `resolveTarget`; every
`Push` and `Tag` reopens the store under an exclusive `flock` on the layout directory (`withLayout`), so each
index write starts from the `index.json` the previous writer left. This removes the named cause; it is not a
retry, timeout or swallowed error.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

None.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 8ab0d79`, test file restored from
  HEAD, `go test -race -count=1 -run TestIssue97 ./internal/artifact/` → `FAIL`, at
  `layout_internal_test.go:47`: "a push that succeeded lost its tag" — `index.json` held `fn-amd64` only,
  `fn-arm64` (the push that completed in between) was dropped. Exactly the reported interleaving.
- **Passes with the fix.** `git reset --hard 8ab0d79`, `go test -race -count=3 -run TestIssue97` → `ok`.
  Worktree left at `8ab0d79`, clean.
- **The user-visible behavior is fixed.** Rebuilt `funcdctl` and reran the issue's steps across real processes:
  10 trials of two parallel `push --platform linux/amd64|arm64` into a fresh layout, then `funcdctl index`
  over both tags → `trials=10 tag-lost=0 index-fail=0`, every push exited 0. No lock file lands in the
  layout (the directory itself is locked, `O_RDONLY` without `O_CREATE`): the layout holds only
  `blobs`, `index.json`, `ingest`, `oci-layout`.
- **Mutants — all killed by `TestIssue97_ParallelPushesKeepTheirTags`:**
  1. `layoutTarget.Tag` tags through the stale store opened by `resolveTarget` → FAIL ("a push that succeeded lost its tag").
  2. `withLayout` skips `lock.Lock()` → FAIL ("a parallel push that succeeded lost its tag"; in-process
     separate `flock` fds exclude each other, so the parallel part exercises the lock).
  3. `layoutTarget.Push` pushes through the stale store → FAIL ("a push that succeeded lost its tag").
- **Scope.** Every hunk serves the issue; no test weakened or deleted. All write paths that go through
  `resolveTarget` are covered at once: `Push`, `PushBundle`, `PushSite`, `PushIndex` (its `target.Push` +
  `target.Tag` now take the lock too). Read paths (`Pull`, `Inspect*`, `Resolve`) keep the store opened at
  resolve time, which takes the same lock, so it never reads a half-written `index.json`.
- **Reuse.** No lock helper exists in the repo (searched `internal`, `cmd`, `pkg`, `api` for flock/`LOCK_EX`);
  `gofrs/flock v0.13.0` was already in the module graph and `go.sum`, now promoted to a direct dependency
  (BSD-3-Clause, allowed). `go mod tidy -diff` is clean. The test reuses `resolveTarget`, `Push`,
  `ociLayoutScheme`, `artifactType` and oras-go's own `Tags` listing rather than parsing `index.json`.
- **Conventions (ADR-0002, CLAUDE.md).** Errors are `api/fault` wraps with an `op`; the store open error from
  `withLayout` is wrapped by every caller. Imports at top level; ctx-first; no `any` in signatures; the doc
  comments state the why (the oras-go behavior and the lock-the-directory choice), not the what. Naming
  follows the package (`resolveTarget`, `parseLocalRef`). `funcdctl` ships no Windows build, so a
  directory flock is portable to every target.
- **ADRs.** No ADR file touched. Consistent with ADR-0145, whose Alternatives present per-platform pushes plus
  an explicit index as the concurrency-safe shape — the fix makes that shape hold for a local layout.
- **Checks (touched packages).** `gofmt -l internal/artifact` empty; `go build ./...` exit 0;
  `go vet ./internal/artifact/` exit 0; `go test -race -count=1 ./internal/artifact/ ./cmd/funcdctl/...` →
  both `ok`; `golangci-lint run ./internal/artifact/...` → `0 issues.`; `just check-hygiene` → clean.
  Linux lint, the e2e suite and the lanes are left to the group gate.
- **Shape.** `fix(artifact):` subject, a body stating cause and fix, the regression test named,
  `Fixes #97`, the attribution trailer, one issue in one commit.

### Recommendation

Pass. Hand back to `/fix` Step 8. A non-blocking observation for later, not a finding: the lock is held for
the whole blob stream of a `Push`, so parallel pushes of large bundle layers into one local layout run their
layer writes one after another; correctness does not depend on that and the layout is disk-bound anyway.
