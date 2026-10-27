## Verdict: changes-requested — 0 blockers, 1 major, 2 minors  (ADR-0290 amendment, implementation, model: claude-opus-5-5)

This review covers a7448ec2 on `feat/gateway-held-namespace` (on main a6da8bbe) against ADR-0290 as amended in place
on 2026-10-27: Decision 6, the five gateway scenarios, the `Deps.Held` contract, the new Review checklist item and the
done-when line. The decider asked for the in-place amendment; the ADR's header records it and its status stays
`Implemented`. The checks below ran in a worktree detached at a7448ec2. `scripts/agent/gate.sh` runs on the branch in
the builder's session and did not run here.

### Checks run
- `go build ./...`: ok. `gofmt -l` on both packages: nothing listed.
- `go test -race -count=1 ./internal/blob/s3gateway/`: ok. `go test -race -count=1 -run 'Hold|S3Gateway|OwnPrincipal'
  ./pkg/funcd/`: ok. The five `TestScenarioGateway*` tests pass, un-skipped.
- `go vet` on both packages, plain, `-tags dev`, `-tags e2e` (`pkg/funcd`) and `GOOS=linux`: ok.
- `golangci-lint run` on both packages: 0 issues. `just check-hygiene check-specs`: ok.
- Five mutants, run with `go test -overlay` against the gateway's scenario tests:
  - `UploadPart` on plain `authorize` fails `TestScenarioGatewayRestoreLoadRefused`;
  - the `restore` detail dropped fails `TestScenarioGatewayRestoreLoadRefused`;
  - `DeleteObjects` deleting before it asks the hold fails `TestScenarioGatewayWriteRefused`;
  - `DeleteObject` on plain `authorize` fails `TestScenarioGatewayWriteRefused`;
  - `CompleteMultipartUpload` on plain `authorize` **survives**, and survives the whole package too (the Major below).

  The builder's five mutants (the write check removed, the batch-delete check removed, Abort on `authorizeWrite`, the
  hold read before the PEP, the daemon's `Held:` line removed) each fail their test.

### 🟡 Major
- **No test sees `CompleteMultipartUpload` refused** (`model`). Of a multipart upload's three writes, only Complete
  changes the Bucket (`internal/blob/s3gateway/multipart.go:342-376`: create and part only buffer in memory). The
  scenario gateway-write-refused promises that an upload leaves the Bucket unchanged, but its test refuses only
  `CreateMultipartUpload` and then checks that `bronze/big.parquet` is absent, which holds whether or not the hold is
  asked, since Create never writes. A mutant that puts Complete back on plain `authorize` passes every test in the
  package, so the one multipart call that would land data in a held namespace rests on an untested line. **Direction**:
  in `TestScenarioGatewayRestoreLoadRefused` or `…WriteRefused`, upload a part before the hold, then assert that
  `CompleteMultipartUpload` gets the 503 and the key stays absent; the abort that follows already proves the upload is
  still there.

### Minor
- **Decision 2's detail is written twice** (`model`). `internal/dataplane/dataplane.go:313-319` (`heldError`) and
  `internal/blob/s3gateway/backend.go:117-128` (`refuseHeld`) each format the same two details and differ only in
  "calls" and "writes". The text is a contract, and two copies can drift. One formatter beside `Marker` in
  `internal/platform/hold`, taking the noun, would keep a single copy. Optional.
- **#1302 carries no record of the decider's choice** (`model`). The ADR's new header line says the amendment is "by
  the decider on #1302", and the References say #1302 is "answered by the amendment", but the issue has no comment.
  #1297 carries its decision comment, which the same References cite. Since this amendment overrides the freeze on an
  `Implemented` ADR, the decider's choice belongs on the issue before the PR that closes it.

### Routed (not this work's)
- **A running Lake engine may write its old catalog over an in-place Restore's** · home: elsewhere · inferred, not
  run. A Restore loads a Lake's files and then its `catalog.db` straight into the substrate
  (`internal/services/backup/restore.go:384-444`) and stops only volume workers (`:220`); nothing found in the
  Restore or the Lake reconciler restarts the engine afterwards. The engine's next save, a pending ADR-0289 retry or
  the next commit's, would then upload its own catalog over the restored one. The hazard exists without this
  amendment. The amendment closes it during the load, and an ADR-0289 retry can move it to the moment of the release.
  It needs a test before anyone files it.

### ✅ Verified correct (keep)
- **Where the hold is asked** (`authorizeWrite`, `backend.go:104-113`): after `authorize`, so after the PEP's allow and
  the Bucket's lookup. A denied caller gets the 403 it got before, and a missing Bucket still answers `NoSuchBucket`.
- **The six write paths**: `PutObject`, `DeleteObject`, `CreateMultipartUpload`, `UploadPart` and
  `CompleteMultipartUpload` call `authorizeWrite`. `DeleteObjects` asks the hold at each allowed key and returns the
  503 at the first one, so no key is deleted. `AbortMultipartUpload` and the reads (`GetObject`, `HeadObject`, the
  lists, `ListParts`) keep plain `authorize`. `be` embeds `backend.BackendUnsupported`, and `CreateBucket` and
  `DeleteBucket` stay `AccessDenied`, so no other operation writes.
- **The answer**: `s3err.APIError{Code: "ServiceUnavailable", HTTPStatusCode: 503}` with Decision 2's details, "writes
  resume after" for `data`. versitygw's only 503 entry is `SlowDown`, a rate limit, so building the error is right,
  and AWS SDKs retry an HTTP 503.
- **The wiring**: `Deps.Held` matches the amended Contracts, nil defaults to `hold.NeverCall` in `New`, and the daemon
  passes `c.callGate()` (`pkg/funcd/funcd.go:875`). `TestS3GatewayAsksTheNamespaceHold` runs a real gateway over a
  marker written by `hold.MarkNamespaces`, as `restore run` writes it, with a short `os.MkdirTemp` data dir. It sees
  the owner's put refused, then landing after `ReleaseNamespace`.
- **The tests**: one `TestScenarioGateway*` per gateway scenario, each asserting the status, the S3 code and the
  message. Refused writes are checked against the Bucket.
- **Docs**: `examples/workload-backup.md`, `examples/restore-runbook.md`, `blueprint.md`, F205 and the
  PROJECT-SUMMARY row say that writes through the S3 gateway get 503 while reads are served. The workload guide's old
  "not refused yet (#1302)" workaround is removed.

### Definition of done
8 of 9: the eight Review checklist items and the done-when line's per-change checks (the tests with `-race`, build,
vet plain, dev, e2e and Linux, lint, and `just check-hygiene check-specs`). The item "Every scenario has its named
test" falls short on gateway-write-refused's upload case (the Major). The line's `scripts/agent/gate.sh` runs on the
branch in the builder's session.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0290 (implementation, amendment) → changes-requested, 0/1/2, 3 model-attributed,
DoD 8/9. See docs/reviews/model-scorecard.md.

### Recommendation
Add the Complete case to a gateway scenario test, record the decider's choice on #1302, then re-review. The detail
helper is optional. ADR-0290 is already `Implemented`, so nothing is stamped.
