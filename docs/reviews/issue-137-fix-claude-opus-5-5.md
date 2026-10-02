## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #137 fix, model: claude-opus-5-5)

Change: `fix/i137`, commit deb317a `fix(egress): serve the DNS forwarder over TCP as well as UDP`
(`internal/network/egress/forwarder.go`, new `internal/network/egress/forwarder_test.go`).

### 🟡 Major
None.

### Minor 1 — the error-branch shutdown is untested  ·  attribution: model
Mutant 3 removed the new `shutdown()` call in `Serve`'s `case err := <-errCh:` branch (in
`internal/network/egress/forwarder.go`). The full package suite under the mutant still passed (`ok`, 0.295s).
So nothing checks that, when one listener fails (for example, the TCP bind on a port that only UDP has
free), the other server is stopped and does not keep running. The behavior is correct as written. The
gap is in the test only. Fix (optional): a test that holds the TCP port and asserts that `Serve` returns
an `Internal` fault and that the UDP port is free afterwards.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** A `git revert --no-commit` of deb317a also removes the
  new test file (`[no tests to run]`), so I used the overlay method of fix-review Step 2.1:
  `origin/main:internal/network/egress/forwarder.go` overlaid →
  `TestIssue137_ForwarderServesDNSOverTCP` FAIL: `dial tcp 127.0.0.1:<port>: connect: connection refused`.
  This is the issue's ECONNREFUSED on TCP to the forwarder port. The revert was then reset to deb317a, and
  the worktree is clean.
- **Passes with the fix**: `go test -race -count=3 -run TestIssue137 ./internal/network/egress/` → `ok`.
  The full package also passes with `-race -count=1` → `ok`.
- **The user-visible behavior is covered by the test.** It runs the real `NewForwarder(...).Serve` against
  a miekg upstream that serves UDP and TCP. A 40-record answer comes back with TC=1 over UDP. The TCP retry
  returns all 40 records with no truncation. `DomainsFor(127.0.0.1, 203.0.113.40)` returns
  `big.example.com`, so an IP that only the TCP answer carries is now attested. This reproduces the issue's
  own probe without a VM.
- **The root cause is fixed, not masked.** `Serve` now runs a UDP server and a TCP server on the forwarder
  port. Both use the same mux, which matches `dnsForwarderProtos` (UDP+TCP redirect and input accept). Each
  query is forwarded upstream over the transport it arrived on (the client is chosen by the
  `*net.TCPAddr` type), so a TCP retry gets the whole upstream answer and not a second truncated UDP one.
  There are no timeouts, retries or swallowed errors.
- **Mutants**: M1 changes the upstream TCP client to `Net: "udp"` → FAIL (`Should be false`, the answer is
  truncated). M2 drops the TCP `dns.Server` → FAIL (connection refused). M3 is the survivor noted under
  Minor 1.
- **Scope**: both hunks serve the issue. No test was weakened or deleted. No ADR or doc was touched.
- **ADRs**: this conforms to ADR-0117. The forwarder is the only reachable resolver, and the redirect is
  now served on every protocol the substrate redirects. No Accepted or Implemented ADR is contradicted.
- **Reuse**: the fix uses miekg/dns's own `Server` for both nets (it is already a dependency). There is no
  new helper in production code. The test helpers (`bindUDPTCP`, `exchangeUntilServed`) are local and
  small. Nothing equivalent exists in `internal/testkit` or the egress package: the repository has no other
  `dns.Server` or dual UDP/TCP bind helper.
- **Conventions**: errors still go through `fault.Wrapf(..., fault.Internal, op, ...)`. Imports are at
  the top level. There is no `any` in any signature. The comments are short and explain why (F80 redirect,
  RFC 7766). The test name follows `TestIssue<N>_…`. The test waits by retrying instead of sleeping.
- **Checks (touched package)**: gofmt clean, `go build ./...` OK, `go vet` OK,
  `golangci-lint run ./internal/network/egress/...` → `0 issues.`, and package tests pass with `-race`.
  The Linux lint, the e2e suite and the egress lane are left to the group gate, as instructed.
- **Shape**: the subject is `fix(egress): …`, the body has `Fixes #137`, the commit has the
  `Co-Authored-By` trailer, and the commit covers one issue.

### Note (not scored)
The issue also mentions that the upstream UDP client sends no EDNS. With TCP served, a truncated answer
is now retried correctly, so EDNS is only an optimisation and is not needed for correctness. This is
out of scope for this fix.

### Recommendation
Pass. Hand back to `/fix` Step 8. Adding a test for the error-branch shutdown is optional.
