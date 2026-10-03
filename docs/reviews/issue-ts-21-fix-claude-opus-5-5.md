## Verdict: pass — 0 blockers, 0 majors, 2 minors  (pyvvo/funcd-typescript#21 fix, model: claude-opus-5-5)

This reviews a **pyvvo/funcd-typescript** change: commit `875d48b` on branch `fix/r21-ts`
("fix(shim): reject context.invoke, kv and blob when the reply drops mid-body"), diffed against `origin/main`.
Issue: *context.invoke, kv and blob never settle when the reply drops mid-body*.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Commit trailer says `Refs #21`, not `Fixes #21`** · attribution: model · evidence: `git log -1 --format=%B`
  ends with `Refs #21`; the fix skill's commit shape (Step 6) asks for `Fixes #<N>`. The repo squash-merges
  with the PR title, so the PR body must carry `Fixes #21` to close the issue. Fix: amend the trailer, or make
  sure the PR description has `Fixes #21`.
- **The same response-error block and its comment are pasted into three clients** · attribution: model ·
  evidence: `shim/src/invoke.ts:28-33`, `shim/src/kv.ts:22-27`, `shim/src/blob.ts:24-29` carry the identical
  comment and an almost identical `res.on('error', …)` listener. `kv.ts` and `blob.ts` already had duplicate
  `request()` functions before this change, so a shared local-API request helper is a refactor outside this
  issue. Trivial; noted, not blocking. Fix (optional, later): one shared request helper for the three clients.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 875d48b`, then the new tests
  restored from `875d48b`: all three regression tests (`issue r21: … rejects the invoke promise`,
  `… rejects kv.get`, `… rejects blob.get`) end `cancelled` with *"Promise resolution is still pending but
  the event loop has already resolved"* — the never-settling promise that the issue describes
  (13 tests: 10 pass, 3 cancelled). Then `git reset --hard 875d48b`: 13/13 pass, 0 cancelled.
- **Mutants (each restored with `git checkout`), all killed:**
  1. `invoke.ts` — listener made a no-op (`err && 0 && reject(…)`): the invoke test is cancelled (pending promise).
  2. `kv.ts` — listener reduced to `res.on('error', reject)` (raw error, no call name): the kv test fails
     (`error: 'aborted'` does not match the expected message).
  3. `blob.ts` — listener removed: the blob test is cancelled.
- **Cause, not symptom.** The issue names the cause: only `data`/`end` listeners on the response, and Node
  reports a mid-body drop on the response, not the request. The fix adds the response `error` listener
  that rejects. No timeout, retry or swallowed error. The `cause` keeps the original Node error.
- **The user-visible behavior matches the issue's Expected text**: the rejection names the call, for example
  `context.invoke("callee") failed: connection closed before the reply ended`. The test fixture
  (`shim/test/reply.ts`, `cut: true`) declares one byte more than it sends and then destroys the socket, which
  is the same shape as the issue's probe (headers, a partial body, then a drop).
- **Scope.** Every hunk serves the issue: the three listeners, the rebuilt `shim.mjs`/`pool.mjs`, the three
  regression tests, and the shared `Reply`/`send` fixture. The existing test servers moved to `send()` with
  unchanged behavior for non-`cut` replies. No test was weakened or deleted. The added `server.unref()` in the kv
  and blob harnesses mirrors the existing invoke harness and turns a hang into a failure.
- **Reuse.** `reply.ts` replaces three inline reply writers with one shared fixture instead of adding a fourth.
  There is no other HTTP client in `shim/src` (only `invoke.ts`, `kv.ts` and `blob.ts` call `http.request`), so
  no client was missed.
- **ADR conformance.** ADR-0064 (fn-to-fn invoke), ADR-0069 (context.kv) and ADR-0127 (context.blob) are
  unchanged and not contradicted. The wire protocol, the `FUNCD_INVOKE_SOCKET` variable, the paths and the
  status handling are unchanged; only how the shim client settles its own promise changes. No funcd ↔ shim
  contract change, so no funcd ADR is needed.
- **Conventions.** Biome clean (`Checked 51 files … No fixes applied`). Imports are at module top level. Built
  `shim.mjs` and `pool.mjs` are committed and current (the `just ci` stale-output gate passed). No version,
  `CHANGELOG.md` or `version.txt` edit. The comments explain why. The subject is a Conventional Commit
  (`fix(shim): …`) with the attribution trailer, and the branch has one commit for one issue.
- **Checks.** `just ci` (install, lint, typecheck, test, build, go-check, stale-output gate) through the pinned
  dev shell: **exit 0**. The worktree was left at `875d48b` and clean.

### Definition of Done

10 / 11 items hold (fix checklist, adapted to TypeScript: node's test runner instead of `go test -race`, and
`issue r21: …` test names in place of `TestIssue<N>_…`). Miss: item 11, commit shape (`Refs #21` instead of
`Fixes #21`) — model.

### Model scorecard

claude-opus-5-5 on pyvvo/funcd-typescript#21 (fix) → pass, 0/0/2, 2 model-attributed, DoD 10/11.
Not recorded in the funcd ledger: this review was told not to edit funcd.

### Recommendation

Ship it. Before the PR, change the trailer to `Fixes #21` (or put `Fixes #21` in the PR body). A shared
local-API request helper for the three clients can be a later refactor.
