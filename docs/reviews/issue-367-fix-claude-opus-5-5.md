# Fix review — issue #367 (egress DNS forwarder re-packs answers uncompressed), model: claude-opus-5-5

Change: branch `fix/i367`, commit 7460563 `fix(egress): fit the relayed DNS reply to the client's UDP size`.
Files: `internal/network/egress/forwarder.go`, `internal/network/egress/forwarder_test.go`.

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #367 fix, model: claude-opus-5-5)

### 🟡 Minor 1 — the EDNS0 limit branch has no test  ·  attribution: model

`Serve` sets `limit = int(opt.UDPSize())` when the query carries an OPT record. Mutant m4 (replace it with
`dns.MinMsgSize`) passes the whole package: no test sends an EDNS0 query. The branch is correct by reading
(`Msg.Truncate` clamps sizes below 512, as RFC 6891 requires), and the harness upstream always truncates
UDP answers to 512, so a test would need an upstream that honors the client's OPT size. Not a blocker: the
issue's path (no EDNS0, 512 bytes) and the TCP path are both covered.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 7460563` with the HEAD test
  file restored: `TestIssue367_ForwarderFitsReplyToClientUDPSize` fails with `dns: buffer size too small`
  (the default non-EDNS0 client cannot read the uncompressed > 512-byte datagram). `TestIssue137_…` fails
  the same way once its client loses the 4096-byte workaround buffer — the exact symptom the issue reports.
- **Passes with the fix under `-race`**: both tests PASS; the full `internal/network/egress` package is
  green with `-race`. Worktree reset to 7460563 and left clean.
- **Root cause, not symptom.** The fix calls `resp.Truncate(limit)` before `WriteMsg`: miekg/dns v1.1.72
  `Truncate` sets `Compress = true` when the uncompressed length exceeds the limit and sets TC on what
  still does not fit — the remedy the issue names. The limit is derived per transport: 512 (`dns.MinMsgSize`)
  over UDP without EDNS0, the OPT UDP size with it, 65535 (`dns.MaxMsgSize`) over TCP. No retry, timeout
  or swallowed error was added.
- **Mutants** (overlay, `-run` on the forwarder tests):
  - m1 drop `resp.Truncate(limit)` → both issue tests FAIL.
  - m2 UDP non-EDNS0 limit `MinMsgSize → MaxMsgSize` → both FAIL.
  - m3 TCP limit `MaxMsgSize → MinMsgSize` → `TestIssue137_…` FAILS (full answer no longer attested).
  - m4 EDNS0 limit → survives (Minor 1).
- **Scope.** Every hunk serves the issue. The test refactor extracts the existing #137 upstream+forwarder
  setup into `startForwarder` and reuses it, and removes the 4096-byte client buffer that masked this
  defect — the #137 test is strengthened, not weakened.
- **Reuse.** Uses the library's own `Msg.Truncate`, `Msg.IsEdns0`, `dns.MinMsgSize`/`dns.MaxMsgSize`
  instead of hand-rolled size logic; no new dependency; the test harness is shared, not duplicated
  (`testIPs` is a four-line helper with no existing equivalent in the package).
- **Conventions.** ctx-first and error handling unchanged; doc comment on `handle` updated to say why
  (Compress false after unpack), without narration; no YAML, no in-function imports.
- **ADRs.** ADR-0117 (egress policy) says nothing about relaying verbatim or reply size; no ADR file and
  no living doc is touched or made untrue.
- **Checks (touched package).** `go test -race` ok, `go vet` clean, `golangci-lint` 0 issues, `gofmt` clean.
  Repo-wide, Linux lint and e2e are left to the group gate.
- **Shape.** Subject `fix(egress): …`, body names cause/fix/test, `Fixes #367`, attribution trailer, one
  issue in one commit.

### Definition of Done

11 / 11 apply and hold (item 8 verified for the touched package on the host; the group gate covers the rest;
item 4 holds on the fix's key lines, with the EDNS0 branch gap recorded as Minor 1).

### Model scorecard

claude-opus-5-5 · fix phase · pass · 0 blockers · 0 majors · 1 minor (model) · DoD 11/11.

### Recommendation

Merge as is. Optionally, a follow-up test with an upstream that honors the OPT size would cover the
EDNS0 branch.
