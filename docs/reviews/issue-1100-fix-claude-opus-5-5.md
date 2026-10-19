## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1100 fix, model: claude-opus-5-5)

This reviews the fix of #1100, tests that reuse a closed server's port as an unreachable address: pyvvo/funcd#1101,
one commit `1d1fa1d9` on main `05a0bee7`, test code only.

### ✅ Verified correct (keep it)

- **The cause**: `TestIssue379_ProxyLogsThroughManagerLogger` got 403 where it expected 503, in about 1% of Linux runs.
  - Its "dead" engine was an `httptest` server it closed at once, freeing its port.
  - The Manager then bound its own proxy listener on `127.0.0.1:0`.
  - When the kernel handed that listener the freed port, the proxy forwarded to itself with the engine token swapped
    in, and the inner pass refused that token.
  - The builder's raw logs show 17 such failures in 1,500 runs at `--cpus=2`, and 17 at `--cpus=1`. Each one is
    `expected: 503 / actual: 403` at `manager_test.go:203`, and nothing else fails. The binary's sha256 matches the
    one reported.
- **Reproduction and revert check**: the review built the package's linux/arm64 test binary from `1d1fa1d9` twice,
  once with main's `manager_test.go` and `proxy_test.go` overlaid and once without. It ran each in `alpine:3.20` as
  uid 1000: the whole package skipping `TestSwapHandshakeToken`, `-test.count=1500 -test.parallel=64`.
  - Reverted, `TestIssue379` failed 13 runs out of 1,500 at `--cpus=2` and 25 out of 1,500 at `--cpus=1`, every
    one `actual: 403`, with no other failure.
  - With the fix, it failed none of the 3,000.
  The overlay must revert both files: with `proxy_test.go`'s closed servers fixed, the package churns fewer ports, and
  1,500 runs of `TestIssue379` alone or 500 of the package showed no failure on either binary.
- **The fix removes the cause**: the eight tests point at `http://127.0.0.1:1`. Port 1 is privileged, and a `:0` bind
  draws from the ephemeral range, so no listener in the test process can take it. There is no retry, no longer
  timeout and no skip, and no assertion changed. The literal is the repository's existing idiom (`TestIssue454`,
  `upstream_test.go:77`, `cmd/funcdctl/bench_test.go`, `internal/testkit/loadgen`), so a new helper would duplicate
  nothing.
- **Siblings**: the eight sites are every test in the repository that closed a new `httptest` server to get an
  unreachable URL. The review's searches find none left, neither a server closed right after it starts nor a
  server's URL used after a non-deferred `Close`.
- **Checks**:
  - `go test -race`, `go vet` and `golangci-lint` on the four touched packages (`internal/activator`,
    `internal/catalog/gateway`, `internal/dataplane`, `internal/services/sql`) pass, 0 lint issues.
  - The builder's `scripts/agent/gate.sh` passed every step.
  - On #1101, `changes`, `pr-title`, `ci` and `e2e` passed on `1d1fa1d9`.
- **Shape**: `fix(test): …`, `Fixes #1100`, the attribution trailer, one commit.

### Fix checklist

12 / 12 items hold. Item 1 holds in its flake form: the reproduction is the existing `TestIssue379` under `-count`
stress, as `/fix` allows for a flaky test, rather than a new deterministic test.

### Model scorecard

Recorded: claude-opus-5-5 on #1100 (fix) → pass, 0/0/0, DoD 12/12. See docs/reviews/model-scorecard.md.

### Recommendation

Merge #1101; it closes #1100. #1098, which the investigation started from, stays parked: its mechanism did not
reproduce.
