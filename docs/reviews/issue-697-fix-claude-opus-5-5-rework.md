# Fix review: issue #697, rework 1 (stalled registry response body)

- **Change:** commit 52a728d5 `fix(artifact): bound a stalled registry response body`, on top of 4b510d9a on
  `fix/w15c-733-function-lifecycle` (PR #754)
- **Model:** claude-opus-5-5
- **Reviewer:** claude-opus-5-5, independent `/fix-review` run
- **Verdict:** **pass**. There are no Blockers and no Majors, and there is one Minor (a test gap). The fix
  checklist holds 11 of 11.

## What the rework had to fix

The branch already bounds the wait for response headers (`ResponseHeaderTimeout = registryResponseTimeout`,
10 s). A registry that sends the headers and then stops sending the body still blocks a pull, and with it the
controller's only worker. The rework must bound every registry body read with an idle-read timeout, not with a
total `Client.Timeout`.

## Verification (run, not read)

| Check | Result |
|---|---|
| `TestIssue697_StalledRegistryBodyEnds` without the fix (overlay of `internal/artifact/artifact.go` from `HEAD~1`, with the test and `stallregistry` from HEAD) | **FAIL**, for the reported reason. All three subtests (`platforms`, `contract`, `blob`) print "the call still reads the stalled response body after 30s". |
| The same test with the fix, `-race -v` | **PASS**. Each subtest ends after 10.01 to 10.02 s. `TestIssue697_StalledRegistryCallEnds` (the header stall) still passes in about 10 s per subtest. |
| `internal/artifact` and `internal/testkit/stallregistry`, `-race -count=1` | ok (41.8 s); stallregistry has no test files |
| `go vet`: artifact, stallregistry, `tests/chaos` (the other `stallregistry.Start` user) | clean on the host and with `GOOS=linux` |
| golangci-lint, the same packages | 0 issues on the host and with `GOOS=linux` |
| Mutant m1: the timer branch returns the underlying `err` instead of `errRegistryStalled` | **killed**. The calls end with "use of closed network connection", which fails `ErrorContains`. |
| Mutant m2: the `Reset` on every later Read is removed, so the timer is armed only for the first Read | **killed**. All three subtests still block after 30 s. |
| Mutant m3: a total body deadline, where the timer is armed once at the first Read, never stopped or reset, and every non-EOF read error maps to `errRegistryStalled` | **survives**. See Minor 1. |

The test reproduces the integrator's probe exactly: the body has a Content-Length of 500 (`stallregistry.BodySize`)
and the registry sends one byte, then nothing. The first Read returns `{`, and the second Read blocks. This is why
m2 dies: the test exercises the timer being re-armed for each Read, not only the first wait.

## Findings

### Blocker

None.

### Major

None.

### Minor

1. **The idle-not-total property has no test** (`model`). The rework was asked for an idle-read timeout and
   explicitly not a total cap, because a total cap would cut legitimate large downloads. Mutant m3 turns the bound
   into a 10 s total deadline from the first body Read, and every test in `internal/artifact` still passes. The
   code is correct by inspection: `Stop` runs after each Read and `Reset` runs before the next one, so time
   between reads does not count. However, a later change could cap every blob that takes longer than 10 s to
   download, and no test would catch it. Suggested fix: a fourth `StartBody`-style subtest that sends one byte
   about every 2 s for more than 10 s must succeed. It runs in parallel with the other 10 s subtests, so it adds
   only a few seconds of wall time.

### Observations (recorded, not scored)

- **A blob stall is reported as an invalid bundle.** This classification existed before the commit. Pull streams
  the blob into `untarBundle` (`internal/artifact/bundle.go`), which wraps any read error from
  `gzip.NewReader`/`tar.Next` as `fault.Invalidf("bundle layer is not valid gzip: …")`. So a stalled blob now ends
  as `fault.Invalid` "bundle layer is not valid gzip: the registry sent no response bytes for 10s". A
  connection reset in the middle of a blob is classified the same way. The message carries the true cause, but the
  kind says "bad artifact" for a transient registry fault. This is worth a follow-up issue: classify read errors
  from the source separately from decode errors.
- **Possible sibling outside the artifact client (not verified).** On Linux, the containerd runtime-image pull
  (`internal/runtime/containerd/containerd_linux.go`, `d.client.Pull` in `resolveImage`, used only for
  `imageOverride` refs that `Pullable` allows) uses containerd's own resolver, not `registryClient`. This review did
  not trace whether its `Create` context carries a deadline. The issue names the `internal/artifact` client, and
  that client is fully covered (see below), so this is not counted against the fix. It is worth one check when the
  group is integrated.

## Verified correct (keep)

- **The root cause is fixed, not masked.** `idleBodyTransport` sits between `retry.Transport` and the httpx
  transport. Every response body becomes an `idleBody`. A Read that waits `registryResponseTimeout` for bytes has
  its body closed by a `time.AfterFunc`, and that Read ends with `errRegistryStalled`. There is no
  `http.Client.Timeout`, and no timeout was lengthened. The retry policy is unchanged. A body-read error is never
  retried, because `retry.Transport` acts only on RoundTrip results. The timer runs only while a Read is blocked:
  it is created lazily, so a body that is never read starts no timer, and it is stopped after every Read, so no
  timer leaks after EOF.
- **Every registry body read is covered.** `registryClient` is the only registry HTTP client in
  `internal/artifact`: `resolveTarget` (every remote Pull, Resolve, Inspect, Platforms, site and contract path) and
  `Login` both build it, and oras-go's auth token and error bodies go through the same `auth.Client`. The test
  covers the manifest fetch (`Platforms`, `InspectContract`) and the blob read (`Materialize` → `Pull`). `Resolve`
  reads no body, because it uses a HEAD of the manifest, and the header-stall test already covers it. Platforms
  and Inspect fetch the manifest and the index through the same `fetchManifest`/FetchBytes path, so both errors end
  at the same wrap site with the same fault kind.
- **The timeout value is justified.** The rework reuses `registryResponseTimeout` (10 s), the header bound that the
  branch already sets. Both values bound how long a registry may stay silent, and 10 s also matches the
  transport's `TLSHandshakeTimeout`. The constant's comment states both uses and the restart rule, without padding.
- **Reuse.** This review confirmed that no existing helper does the job. `internal/platform/httpx` offers only
  transports and clients. `activator.DeadlineTransport` (`internal/activator/deadline.go`) applies one absolute
  deadline to the RoundTrip, and so to the headers, not an idle bound on the body. oras-go has no body timeout. The
  wrapper is about 25 lines and is the smallest form. `stallregistry.Start` and `StartBody` now share one `serve`,
  so the code that mutates the TLS trust exists once, and `Start`'s signature and behavior are unchanged
  (`tests/chaos` still compiles and vets).
- **Race safety.** `b.timer` is touched only by the reading goroutine. The AfterFunc callback calls only the
  embedded `Close`, which net/http allows concurrently with a Read and which unblocks that Read. `-race` is clean.
- **Conventions.** The imports are at the top of the file, the comments state why and not what, and there is no
  YAML. The error is a package sentinel that callers wrap with `fault.Wrapf`, the same way as the transport errors
  around it. The subject is `fix(artifact):`, the commit has one issue, and it carries the attribution trailer. The
  body says `Refs #697` because the branch's first #697 commit carries `Fixes #697`. No ADR file was touched, and
  no Accepted ADR is contradicted.

## Recommendation

Pass. Before merge, optionally add the drip-feed subtest from Minor 1, so that the idle-not-total requirement is
locked by a test and not only by code review. File the `untarBundle` classification observation as a separate
issue.

## Ledger row

```json
{
  "issue": 697,
  "phase": "fix",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 11,
  "dod_total": 11,
  "report": "docs/reviews/issue-697-fix-claude-opus-5-5-rework.md",
  "notes": "rework 1 (body stall): regression fails without the fix (3/3 subtests block 30s) and passes under -race in ~10s; mutants: error-mapping and per-Read Reset killed, total-deadline mutant survives (Minor, model: idle-not-total untested); obs (unscored): blob stall classified Invalid by untarBundle (pre-existing); containerd runtime-image pull not traced"
}
```
