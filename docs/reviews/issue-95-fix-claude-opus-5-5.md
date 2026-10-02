## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #95 fix, model: claude-opus-5-5)

Commit under review: `c4cba86` `fix(artifact): start a cached OCI Function after a restart while its source is down`
(branch `fix/206-restart-recovery`). The earlier commit on the branch (`b799e07`, issue #40) was not reviewed.

The change makes `OrasMaterializer.Platforms` (`internal/artifact/artifact.go`) keep the platforms of a pinned
digest in a `<digest>.platforms.json` file in the artifact cache dir as well as in memory, and read that file before
it asks the source. A restarted daemon therefore passes the ADR-0145 step-2b gate for a digest it already resolved,
and `Materialize` then serves the bundle from the existing `<digest>-<os>-<arch>` cache dir. The regression test is
`TestIssue95_CachedPlatformsNeedNoSourceAfterRestart` in `internal/artifact/platform_test.go`.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minors
- **The stale `Ready` status from the issue's expected behavior is not addressed** · attribution: issue · evidence:
  the issue's expected behavior says "The status must not report Ready/replicas=1 while no worker runs". When the
  step-2b gate returns a resolver error, the reconcile still returns before any status write
  (`internal/function/function.go:387`, `artifactPlatforms` at `:1527`), so a Function whose digest was never
  resolved on this node still keeps its pre-restart `Ready`/`replicas=1` status while its source is down. That is a
  separate defect with its own cause (ADR-0145 Decision 5 routes a resolver error to a requeue, not to a status).
  The fix removes the cause the issue names for a cached digest, which is the reported scenario. Route: a follow-up
  issue or a note on tracker #206; it is not a reason to hold this fix.
- **The e2e suite has a pre-existing data race under `-race`** · attribution: env · evidence:
  `go test -race -tags e2e ./pkg/funcd/...` fails `TestScenarioE2ETLSSelfSignedServesHTTPS` with "race detected
  during execution of test": the two servers share one `tls.Config` and both `ServeTLS` calls write it through
  `http2ConfigureServer` (`pkg/funcd/funcd.go:1137` and `:1141`). The same failure reproduces with the pre-fix
  `internal/artifact/artifact.go` overlaid, and the commit does not touch `pkg/funcd`. The repo's own recipe
  (`just test-e2e`, which is `go test -tags e2e ./pkg/funcd/...` without `-race`) is green. Route: a separate
  `kind/bug` issue for the shared TLS config.

### Observations (not scored)
- A cache written by 0.2.0 has bundle dirs but no `.platforms.json`. The first restart after an upgrade to this fix,
  with the source down, still needs the source once. After one successful lookup the file exists. The issue already
  notes that an upgrade from 0.1.4 must re-pull, so this window is narrow.
- The issue's side observation that `resolveTarget` creates a missing oci-layout dir is unchanged on the miss path.
  On the cached path the source is no longer opened, so the probe below shows no recreated layout dir with the fix.
- One run of `go test -race ./cmd/funcd/` failed with output that was not captured; the next eight runs (`-count=1`
  four times, `-count=4` once) all passed. The commit does not touch `cmd/funcd`. Treated as host load from
  parallel reviews, not as a finding.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** In the review worktree, `git revert
  --no-commit c4cba86`, then the test file restored from `c4cba86`: `go test -race -run TestIssue95_
  ./internal/artifact/` fails at `platform_test.go:227` with `artifact.Platforms: fetch artifact sha256:…: not
  found` and the message "unannotated manifest: the platforms of a cached digest need no source". That is the same
  error the issue's daemon log shows. The revert applied cleanly; no overlay was needed.
- **It passes with the fix**, un-skipped, under `-race` (`--- PASS: TestIssue95_CachedPlatformsNeedNoSourceAfterRestart`),
  and `go test -race` of `./internal/artifact/...`, `./internal/function/...`, `./pkg/funcd/`, `./cmd/funcdctl`
  and `./internal/workflow/...` is green.
- **The user-visible behavior is fixed (real daemon).** A probe ran the issue's steps with the process runtime,
  file storage and ports 30950-30951: push the hello-world handler to an oci-layout, apply a Function by tag,
  invoke, stop the daemon with SIGTERM, move the layout away, restart on the same data dir, invoke.
  - Pre-fix daemon (`internal/artifact/artifact.go` from `c4cba86~1` overlaid): `503 … did not become ready within
    30s`, 51 `artifactPlatforms` reconcile errors since restart, and the layout dir was recreated.
  - Fixed daemon, 2/2 runs: `{"greeting":"Hello, probe."}` after the restart, 0 gate errors, one
    `.platforms.json` (content `null` for the unannotated manifest), and no recreated layout dir.
  All probe processes were stopped by PID; nothing listens on the probe ports afterwards.
- **Cause, not symptom.** The gate's source round-trip for an already-resolved digest is removed at its source.
  There is no retry, no longer timeout and no skipped gate. A resolver error on a real miss still returns as a
  reconcile error, as ADR-0145 Decision 5 requires. The cached value is safe to trust: the file is written only
  after `Platforms` has checked that the fetched descriptor matches the digest (`internal/artifact/platform.go`),
  the digest is validated by the `^sha256:[a-f0-9]{64}$` pattern on `Function.spec.imageDigest`, and the
  platforms of a digest cannot change.
- **Failure modes are benign.** A missing, empty or truncated file fails `json.Unmarshal` and falls back to the
  source, which rewrites the file. A failed write is ignored and the next miss asks the source again. An
  unannotated manifest round-trips as `null` → `nil`, which the gate treats as "runs anywhere". The empty-digest
  path keeps the old behavior (no caching).
- **Mutants (overlay, each killed):** removing the `os.WriteFile` fails the unannotated case; replacing the
  `os.ReadFile` with a miss fails the unannotated case; persisting `nil` instead of the list fails the index case
  with "Not equal". The test covers both an unannotated manifest and an index, and it also re-materializes from the
  cache with a fresh materializer, which is the full restart path.
- **Scope:** two files, every hunk serves the issue. No test was weakened or deleted.
- **Reuse:** the file sits beside the existing cache dirs and reuses `sanitizeDigest` and the existing test helpers
  (`writeBundle`, `multiArch`, `mkFunction`). The repo has no atomic-write helper to reuse, and none is needed given
  the fallback above.
- **Conventions:** ctx-first signature unchanged, no new exported API, no `any`, imports unchanged, a short "why"
  comment on the best-effort write, the doc comment updated, and the `//nolint:gosec` annotation matches the
  surrounding code. The test follows the loop idiom of `TestScenarioPullSelectsNodePlatform`.
- **ADRs:** ADR-0145 (Implemented) says the materializer implements `PlatformResolver` "caching per digest"; a disk
  cache is consistent with that, and Decision 5's requeue on a resolver error still holds. ADR-0035's pull-by-digest
  rule is untouched. No ADR file was edited.
- **Checks:** `gofmt -l internal/artifact` empty; `go build ./...` and `GOOS=linux go build ./...` exit 0;
  `go vet` host and Linux clean; `golangci-lint run ./internal/artifact/...` "0 issues." on host and with
  `GOOS=linux`; `just check-hygiene` "hygiene: clean"; `go test -tags e2e ./pkg/funcd/...` ok (111 s). Lima lanes
  were not run (a later stage owns them); the process-runtime path is covered by the daemon probe above.
- **Shape:** `fix(artifact):` subject, `Fixes #95`, the Co-Authored-By trailer, one issue in the commit, and the
  commit body names the regression test.

### Definition of Done
11 / 11 items hold. Item 8 holds with the canonical e2e recipe; the `-race` e2e failure is pre-existing and
`env`-attributed.

### Model scorecard
To record: claude-opus-5-5 on issue #95 (fix) → pass, 0/0/2, 0 model-attributed, DoD 11/11
(`docs/reviews/issue-95-fix-claude-opus-5-5.md`). Not recorded by this stage.

### Recommendation
Pass. File two follow-ups outside this fix: the stale `Ready` status on a reconcile that errors before its status
write (or note it on #206), and the shared `tls.Config` race in `pkg/funcd/funcd.go`.
