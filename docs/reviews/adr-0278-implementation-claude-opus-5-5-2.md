## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0278 implementation, round 2, model: claude-opus-5-5)

This re-review checks e3ba99ea on `feat/adr-0278-durable-file-blob`, the fixes for the round-1 report
([adr-0278-implementation-claude-opus-5-5.md](adr-0278-implementation-claude-opus-5-5.md), cherry-picked as
ff0a690b). Since 8d22d07e, the code changes are the UTF-8 refusal in `checkKey`, the shorter `newFileAttrs`
signature, the tests and one comment. The docs gain three ADR back-links.

### Round-1 findings
- **The Major (a key that is not valid UTF-8) is fixed.** `checkKey` now refuses such a key with `fault.Invalid`
  (`internal/blob/gocloud/gocloud.go:282-284`), and it runs before every file:// call. The round-1 probes now match
  base. `Get`, `GetRange`, `Attributes`, a Put and a create-if-absent Put of `x\xff` are each `fault.Invalid`, and
  the gateway answers NoSuchKey for `GET bronze/x%FF`. Both new tests fail when the check is disabled:
  `TestFileNonUTF8KeyRefused` and `TestFileGetOfNonUTF8KeyIsNoSuchKey`.
- **The missing tests are added, with one gap left.** `TestFileWriteCancelledPublishesNothing` fails without the
  context check (`internal/blob/gocloud/file.go:171-173`). The pass-set subtest now fails with a FIFO set. One part
  of least-recently-used eviction is still untested; see the Minor below.
- **The slow tests are fixed.** The gocloud listing tests seed through `blobseed` and now take 0.3 s each. The
  package runs in 16.4 s under `-race` (17.9 s at base, 65.9 s in round 1).
- **The stale comment is fixed.** The GetObject comment (`internal/blob/s3gateway/backend.go:156-158`) now names
  ADR-0278's check of the whole file.
- **The routed back-links are added.** ADR-0159, ADR-0203 and ADR-0208 each gain one `Superseded in part by:
  ADR-0278` line below their Status line. Each line names the parts that ADR-0278's Supersedes header lists.

### Minor
- **A full read's refresh of a file already in the pass set has no test** (`model`). A full read passes the file
  again (`file.go:321`), and `add` moves an entry already in the set to the front (`file.go:455-458`). A mutant
  without that move passes every gocloud test. The cost is an extra hash on a later ranged read, never a wrong
  read. This does not block the stamp.

### Routed (not this work's; unchanged from round 1)
- **Whole-second mtimes** (the decider). The Design model leaves file systems that keep whole-second mtimes to
  tests, but no test covers them.
- **An edit after acceptance** (the decider, for information). Commit 059935eb changed the Accepted ADR's
  Constraints.

### Verified correct (keep)
- Everything the round-1 report lists under *Verified correct* still holds at e3ba99ea. That covers the write order,
  parity with fileblob, the pairing rule, the pass set, the mirror, the compactor, log reads, `blobseed` and its
  depguard rule, and the Contracts.
- The UTF-8 refusal sits in `checkKey`, which returns at once for mem:// and s3://, so only the file backend changes.
  `newFileAttrs` keeps its own UTF-8 check on metadata keys and values.

### Checks
- `go build ./...` on the host and with `GOOS=linux`: exit 0 for both.
- `go vet` and `golangci-lint` on the touched packages, on the host and with `GOOS=linux`: exit 0, 0 issues.
- `go test -race -count=1 ./internal/blob/... ./internal/backup/blobmirror/ ./internal/funclog/...
  ./internal/platform/durable/ ./internal/testkit/blobseed/`: all ok (gocloud 16.4 s, s3gateway 31.3 s). The
  scenario tests and the new tests are named and pass.
- `just check-specs`: 7 specs, 0 failing. `file-put.fizz` passes with 6,098 states.
- 27 mutants were run through `go test -overlay`; 24 were killed. The survivors are the mtime set after the data's
  sync (a test cannot observe it without a crash), a walk that refuses undecodable attributes (unchanged from base),
  and `add`'s refresh (the Minor).
- Reported by the builder and not rerun here: all 13 Lima lanes, the duckdb, s3 and funclog lanes among them, pass
  on 8d22d07e. The fix commit's only behaviour change refuses keys that no lane uses.
- Pending after this verdict: the gate, experiment 0009's s3 power-loss case, and the app-todo lane.

### Definition of Done
12 of 15 items hold. The other three wait on runs after this verdict; that is sequencing, not the model. They are
step 5 in checklist item 4, plan step 5 (`just ci-full`, `just test-linux` and experiment 0009), and the generic
"full suite green".

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0278 (implementation, round 2) → pass, 0/0/1, 1 model-attributed, DoD 12/15. See
docs/reviews/model-scorecard.md.

### Recommendation
Pass. This branch stamps ADR-0278 `Implemented` (2026-10-24) and F191 `implemented`. There is no board card, because
ADR-0278 is a needs-adr bug fix. The stamp stands only if the gate and experiment 0009's s3 case pass before the PR
merges. If either fails, the branch goes back to the builder before it merges. The PR closes #1247 and #1251.
