## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #705 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i705`, commit 2243ca59 `fix(funclog): keep a log or trace segment whose Put fails for a retry`,
based on the current `origin/main` (a394c6f1). Touched: `internal/funclog/sink.go`, `internal/funclog/tracesink.go`,
`internal/funclog/shutdown_test.go`.

### 🟡 Minor
- **Append still reports "append failed" for a record it now keeps** · attribution: model · evidence: `sink.go`
  `Append` returns the wrapped Put error after `requeue` has kept the record; the callers (`internal/funclog/route.go:200,215`,
  `internal/funclog/pump.go:38`, `internal/workflow/reconcile_run.go:86`) log a Warn that says the append or emit failed.
  No caller retries the call on an error, so nothing is duplicated; only the log text is now misleading. Fix (optional):
  word the Warn as "segment Put failed, kept for retry", or leave it as is.

### ✅ Verified correct (keep it)
- **Proof first, on the current main**: an overlay of the `origin/main` `sink.go` and `tracesink.go` (the test file kept)
  fails all six subtests for the issue's reason: `logs/flush` and `traces/flush` persist only `[second]` (the first
  segment is lost), `logs/append` and `traces/append` persist nothing, and both `cap` cases persist nothing. Each case
  of the issue (log Flush, trace Flush, Append-triggered flush) has its own subtest, for both sinks.
- **Passes with the fix**: `go test -race -count=3 -run TestIssue705` — all six subtests pass, three times.
- **Cause, not symptom**: both `flush` paths now call `requeue` on a Put error, which puts the detached records back ahead of
  any segment opened since, so the age flusher, `Flush` and `Close` retry them. No timeout, retry loop or swallowed error;
  the error still reaches the caller.
- **Bounded memory**: `keepNewest` caps what is kept at `2*maxBytes` and drops the oldest records with a Warn, as the issue
  proposed. `retained` excludes the kept bytes from the size trigger and `opened` restarts, so a failing bucket is retried
  after another `maxBytes` or `maxAge`, not on every Append; the `cap` subtests assert both (exactly 2 Puts, `[r2 r3]` kept).
- **Concurrency**: `requeue` reads the detached segment only after `flush` locked and unlocked it, and no `Append` can reach
  a detached segment (`lockedSegment` finds segments under `s.mu`), so the reads are ordered by the mutex; `-race` is clean.
  Two concurrent flushes of one Resource only change the order of the objects, and records carry their own timestamps.
- **Mutants** (overlay, `-run TestIssue705`), each killed:
  1. drop `s.requeue(res, seg)` in `BlobSink.flush` → `logs/flush`, `logs/append`, `logs/cap` fail;
  2. disable the drop loop in `keepNewest` (`bytes > limit` → `false`) → `logs/cap`, `traces/cap` fail;
  3. size trigger ignores `retained` (`seg.bytes-seg.retained` → `seg.bytes`) → `logs/cap` fails (the Put count).
- **Scope**: every hunk serves the issue. Extracting `lockedSegment` from `Append`/`AppendSpan` is the shared get-or-create
  that `requeue` needs; the existing #33/#152 comment ("lock seg before releasing the map") is kept in its doc comment.
- **Reuse**: `keepNewest` is one generic helper for both sinks (`T Entry | Span`) using `slices.Clone`; the test reuses the
  package's `memBucket`, `newSink`, `newTraceSink`, `defaultRes`, `serverSpan` and `logBodies` helpers. The two `requeue`
  methods mirror the existing per-sink duplication of `flush`/`Close`, which this fix does not widen beyond five lines.
- **Siblings**: `delete(s.segments` appears only in `sink.go` and `tracesink.go`; both are fixed. The `memberSink` and
  `logobserver` wrappers in `pkg/funcd` delegate to `BlobSink` and inherit the fix.
- **ADRs**: matches ADR-0081 (no loss while the process lives; a hard crash may still lose a segment) and ADR-0101 for
  spans; no ADR file edited.
- **Conventions**: `fault.Wrapf` kept, slog only, no `any` in signatures, top-level imports, no comment narration.
- **Checks (touched packages)**: `go test -race ./internal/funclog/...` ok (funclog, compact, logread); `go vet` clean;
  golangci-lint `0 issues`. Linux lint and e2e are left to the group gate.
- **Shape**: `fix(funclog):` subject, `Fixes #705`, the attribution trailer, one issue in one commit; the worktree is clean.

### Definition of Done
12 / 12 items hold (item 8 at the host scope; the Linux lint and e2e run in the group gate). Misses: none.

### Model scorecard
Not recorded here (the batch records it): claude-opus-5-5 on issue #705 (fix) → pass, 0/0/1, 1 model-attributed, DoD 12/12.

### Recommendation
Ready to integrate. The Minor (log wording at the callers) is optional and does not block.
