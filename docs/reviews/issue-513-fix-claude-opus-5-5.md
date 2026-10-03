## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #513 fix, model: claude-opus-5-5)

Change: branch `fix/i513`, commit f606da1 `fix(funcd): log egress and containerd warnings through the configured logger`.

The issue names two causes: the daemon never installs its configured logger as the slog default, and the
egress gateway and the containerd driver log through package-level `slog`. The fix removes the second
cause by injection, as ADR-0002 §6 requires, instead of masking it with `slog.SetDefault`. `egress.Deps`
and `containerd.Config` gain a `Logger` field, `network.New` takes a logger, nil falls back to
`slog.Default()`, and the daemon passes its configured logger at every call site
(`cmd/funcd/main.go:285`, `cmd/funcd/main.go:664`, `pkg/funcd/funcd.go:560`).

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **The Linux-only lines that the issue names have no test** · attribution: model ·
  `internal/network/egress/gateway_linux.go:80,91,104,109` (the gateway's `handle` warnings) and
  `internal/runtime/containerd/containerd_linux.go:161` (the `ip_forward` warning). The regression test
  is `//go:build !linux` and proves the same root cause through the non-Linux F80/F81 "unavailable"
  warnings on the same `network.New`/`egress.New` paths. A mutant that puts `slog.Warn` back into
  `gateway_linux.go` survives on every host. A cheap Linux unit test would close the gap: build the
  gateway with a buffer logger, hand `handle` a plain loopback TCP connection that has no
  `SO_ORIGINAL_DST`, and assert that "egress: cannot recover original destination" lands in the buffer.
  This is not a blocker, because the fix is mechanical and both GOOS builds pass `go vet`.

### ✅ Verified correct (keep it)

- **The test fails without the fix, for the reported reason.** After `git revert --no-commit f606da1`
  with the test file restored, `TestIssue513_EgressWarningsUseConfiguredLogger` fails with
  `a warning bypassed the configured logger`. The leaked buffer holds the two F80/F81 warnings as
  text-handler lines (`level=WARN msg="network isolation (F80) …"`, `msg="egress gateway (F81) …"`).
  After `git reset --hard f606da1`, the test passes under `-race`. The worktree is clean at f606da1.
- **Mutants: 3 of 3 killed** (go test overlays). The killing test is the same in all three, with
  `a warning bypassed the configured logger`:
  1. `network.New(true, root)` changed to `network.New(true, nil)` in `cmd/funcd/main.go`. Killed.
  2. The `Logger: p.logger.With("component", "egress.gateway")` line removed from `pkg/funcd/funcd.go`. Killed.
  3. `d.Logger.Warn` changed to `slog.Default().Warn` in `internal/network/egress/gateway_other.go`. Killed.
- **The test reproduces the user-visible behavior.** It runs the real daemon command (`newRootCmd`)
  with `log.format: json` and `log.level: warn`. It requires every stdout line to parse as JSON, and it
  requires the stand-in global default to receive nothing. It uses `shortDataDir`. It restores the slog
  default in `t.Cleanup`. The fixed egress ports never bind: the gateway's `Serve` runs only in `Start`
  (`pkg/funcd/funcd.go:1158`), and the test stops at the data-plane bind (`pkg/funcd/funcd.go:985`).
- **Cause, not symptom.** No package-level `slog` call is left in the touched daemon packages. The only
  one in `cmd/funcd/main.go:57` runs before the logger exists, as the issue notes.
- **Scope.** Every hunk serves the issue. The F80 `nftables_other.go` warning is the same defect on the
  same path. The `containerd.Config` comment column moved only because gofmt realigned it. The existing
  tests were adapted to the new `network.New` signature and were not weakened.
- **Reuse.** The fix adds no new helper. The `Logger *slog.Logger // nil ⇒ slog.Default()` field and the
  nil fallback copy the existing precedent (`internal/auth/cedar/cedar.go:24`,
  `internal/blob/s3gateway/s3gateway.go:53`). The per-component `p.logger.With("component", …)` copies
  the `egressAuditSink` wiring next to it.
- **Conventions.** The change follows ADR-0002 §6 (inject the logger, slog only) and uses
  `WarnContext`/`DebugContext` where a ctx is in scope. Imports are at the top level. No comment bloat
  was added. No ADR file was touched, and ADR-0117 (F81) and ADR-0115 (F80) are still followed.
- **Checks on the touched packages.** `go build ./...` passes. `go test -race -count=1` passes for
  `./cmd/funcd/`, `./internal/network/...`, `./internal/runtime/containerd/...` and `./pkg/funcd/`.
  `go vet` passes with GOOS set to linux and to darwin. Host `golangci-lint` reports `0 issues.` Linux
  lint, e2e and the lanes are left to the group gate.
- **Shape.** One commit with a `fix(funcd):` subject, `Fixes #513` and the attribution trailer.

### Recommendation

Pass. A Linux unit test for the gateway's `handle` warning path is optional follow-up work for the
fixer. Hand back to `/fix` Step 8.
