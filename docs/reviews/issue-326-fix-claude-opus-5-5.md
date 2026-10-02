## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #326 fix, model: claude-opus-5-5)

Change: `b1423dc fix(config): reject out-of-range ports and negative sizes and counts` on `fix/i326`
(`internal/platform/config/config.go`, `internal/platform/config/config_test.go`).

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With `origin/main`'s `config.go` overlaid
  (`go test -overlay … -run TestIssue326`), every bad-value subtest fails on "is rejected": both ports at
  `-1` and `65536`, `maxAgeSeconds`, `chunkBytes`, `segmentMaxBytes`, `maxUploadBytes`, `defaultRetry`,
  `payloadLimit`, `deliveryAttempts`, `deadletter.maxEntries` at `-1`, and the env case
  (`FUNCD_NETWORK_DNS_FORWARDER_PORT=65536`). Package result `FAIL`. (A plain `git revert --no-commit`
  also removes the test, giving "no tests to run", so the overlay is the meaningful revert check. The
  worktree was reset to `b1423dc` and is clean.)
- **Passes with the fix under `-race`**: `go test -race -count=1 ./internal/platform/config/` → `ok`;
  28 `TestIssue326` subtests PASS, none skipped.
- **Mutants (3/3 killed)**:
  1. dnsForwarderPort `min=0,max=65535` → `min=0`: the `=65536` and `env` subtests fail.
  2. payloadLimit rule removed: the `workflow.payloadLimit=-1` subtest fails.
  3. deadletter.maxEntries `min=0` → `min=1`: the `=0` subtest fails, so the test also pins 0 as valid.
- **Cause, not symptom**: the cause named in the issue is the missing `validate` rules on the listed
  lines. The fix adds the rules to exactly those fields: maxAgeSeconds, both ports, segmentMaxBytes,
  maxUploadBytes, defaultRetry, payloadLimit, deliveryAttempts, deadletter.maxEntries. It also covers
  `kvstore.backup.chunkBytes`, the same class of field. After the change, the only plain numeric field
  in `Config` without a rule is `kvstore.maxStoresPerNamespace`, where a negative value means quota off.
  The test pins `-1` there as accepted. The `uint16()` conversions at `cmd/funcd/main.go:258/:275` are
  now safe, because Load bounds both ports to 0..65535.
- **Port 0 accepted at config level, consistent with the code.** `dnsForwarderPort: 0` means not wired
  (`main.go:274`). `egressGatewayPort: 0` with egress on is already rejected downstream by
  `network.Policy.Validate` ("gateway port is required"). So `min=0,max=65535` covers the
  issue's "0..65535" range, and the "1..65535 when used" condition holds through the existing check.
  The config layer does not duplicate that check.
- **Error shape**: rejections are `fault.Invalid`, and the dotted field key appears in the message
  (`require.ErrorContains(err, tc.key)`), through the existing `Validate` path that strips `Config.` from
  `fe.Namespace()`. This is the same mechanism as #164.
- **Scope**: two files. Every hunk is a `validate` tag, the corrected `PayloadLimit` comment (the issue
  calls it stale; it is now enforced, with 0 meaning unbounded), or the regression test. No test was
  weakened or deleted, and no ADR file was touched.
- **Reuse**: the fix uses the go-playground/validator tags and the `Validate` → field-error path that are
  already there. The test reuses `writeCfg`, `config.Load` and `fault.KindOf`, following the
  `TestIssue164_NegativeLimitsRejected` pattern. The small `yamlAt` closure builds nested keys that
  #164's inline strings could not; the package has no existing helper for this.
- **ADRs**: no Decision or Contract is contradicted. A negative value has no documented meaning for
  payloadLimit (ADR-0094), the forwarder and gateway ports (ADR-0117) or DLQ maxEntries (ADR-0118). In
  each case 0 keeps its documented default or off meaning. Negative-means-off on
  maxStoresPerNamespace (ADR-0072) is kept.
- **Conventions**: `strings` is a top-level import, comments are short and explain why, the test
  sits next to its #164 sibling and is named `TestIssue326_…`, and the code adds no YAML.
- **Checks (touched package)**: `go vet` OK, `golangci-lint run ./internal/platform/config/...` reports
  0 issues, and `go build ./...` OK. The repo-wide tests, Linux lint and e2e run in the group gate.
- **Shape**: `fix(config):` subject, `Fixes #326`, the attribution trailer, one commit for the one issue.

### Recommendation
Pass. Hand back to `/fix` Step 8, where the group PR carries `Fixes #326`.
