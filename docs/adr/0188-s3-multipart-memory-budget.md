# ADR-0188: S3 multipart memory budget — one daemon-wide bound on buffered parts and assembled copies

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05
- **Deciders**: green-0-rabbit
- **Tags**: s3, blob, multipart, memory, capacity
- **Realizes**: [FEAT-0003/F47](../feat/0003-feat-data-platform.md) (S3-protocol frontend on the blob substrate)
- **Supersedes in part**: None. The per-object limit (`min(maxObjectBytes>0, maxUploadBytes)`, ADR-0080 line 241) and
  its 400 `EntityTooLarge` stay; this ADR adds a retryable capacity limit beside them. No back-link is owed.
- **Relates to**: refines [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) (Implemented) lines 140, 185,
  239-241 and 356 · relies on [ADR-0159](0159-blob-content-digest-and-metadata.md) (Implemented) line 42 · stays
  outside [ADR-0148](0148-size-caps-answer-413.md) (Implemented) Scope line 84 (see Constraints) ·
  [ADR-0085](0085-s3-in-platform-identity-funcd-keypair.md) (the in-flight cap `maxInFlightRequests`, unchanged).

## Context & Need

Issue [#731](https://github.com/pyvvo/funcd/issues/731), reproduced on a394c6f1 (0.6.0) and confirmed by an
independent refuter; code lines below are at origin/main 6b06320c. Every multipart upload is buffered in one
process-wide map (`internal/blob/s3gateway/multipart.go:33-37`). `putPart` (`:94-109`) compares only that upload's size
with `maxUpload`; nothing sums bytes across uploads. Measured with `maxUploadBytes` = 4 MiB: one principal opened 20
uploads and sent a full-cap part to each, all 20 answered 200, and the heap grew 80 MiB after a forced GC. At the 1 GiB
default about 15 uploads fill a 15 GiB host (extrapolated, not run). The parts live until Abort, Complete or the 1-hour
idle expiry (`:28`), and the idle sweep runs only in `create` (`:65-69`).

`assemble` (`:115-143`) builds a second full copy and neither drops nor marks the upload, so each concurrent Complete
of one id holds its own copy. Measured: 16 concurrent Completes of one 4 MiB upload added 61.6 MiB of heap, and all 16
answered 200. The only other limit, `maxInFlightRequests` = 4096 (`s3gateway.go:29`, `:137`), counts requests, and
buffered parts outlive their requests.

**Purpose.** The S3 gateway's multipart state holds at most 3 × `maxUploadBytes` of daemon RAM, and one principal holds
at most `maxUploadBytes` of buffered parts, so no writer with `s3::write` can exhaust the host or the budget through
buffered parts or assembled copies. Request bodies still being read stay outside it (Risks accepted).

## Scenarios

M below is `s3gateway.maxUploadBytes`.

- **scenario: multipart-share-per-principal** (the #731 reproduction) — Given M = 64 KiB and `etl-svc` owning
  `lakehouse/bronze`, When `etl-svc` opens 20 uploads and sends a 64 KiB part to each, Then the first part answers 200
  and the other 19 answer 503 `SlowDown` with nothing buffered (ListParts lists no part); after `etl-svc` aborts the
  first upload, a retried part of another upload answers 200.
- **scenario: multipart-budget-daemon-wide** — Given two principals each holding an upload of M (the parts limit, 2M),
  When a third principal sends a part, Then 503 `SlowDown`; after one of the two aborts, the retried part answers 200.
- **scenario: complete-copy-counts-and-always-fits** — Given two principals each holding an upload of M and a bucket
  whose Put blocks, When both Complete at once, Then one Complete proceeds and the other answers 503 `SlowDown` with its
  parts kept; once the first answers 200, the retried Complete answers 200.
- **scenario: concurrent-complete-single-flight** — Given one upload of M and a bucket whose Put blocks, When its owner
  sends two Completes for that upload id at once, Then the second answers 503 `SlowDown` while the first holds its copy,
  and the first answers 200 once the Put returns.
- **scenario: expired-upload-frees-budget-on-part** — Given two uploads holding the parts limit, last touched at 0:00,
  and a third principal whose upload is created at 0:30 and whose part is refused then, When the third principal
  retries the part at 1:01 with no CreateMultipartUpload in between, Then the idle uploads are dropped and the part
  answers 200.
- **scenario: non-growing-part-does-not-hold-budget** — Given two principals whose uploads of M (the parts limit) last
  grew at 0:00, When each owner re-sends part 1 at the same size at 0:20 and 0:50 (200 each) and a third principal,
  whose upload is created at 0:30, has its part refused then, Then its retry at 1:01 sweeps both and answers 200.
- **scenario: size-cap-stays-entity-too-large** — Given an upload holding M − 1 bytes, When its owner sends a 2-byte
  part, Then 400 `EntityTooLarge` (ADR-0148 §5), not 503, although the share would also be passed.

## Scope

- **In**: byte accounting in `multipartStore` (buffered parts, assembled copies, per-principal shares); a single-flight
  Complete per upload id; a sweep of expired uploads when a part or a Complete is refused; the key's doc comments.
- **Out**: request bodies still being read by `readCapped` (`backend.go:688-702`) and PutObject's buffer (`:458`),
  bounded by the in-flight cap as today; early rejection of parts above a Bucket's `maxObjectBytes` (the contract
  already holds at Complete through `blob.Capped`, `pkg/funcd/funcd.go:1917`, and the waste is now bounded by the
  share); a streaming blob seam (ADR-0080's exit); a new config key; metrics; any status of ADR-0148.

## Constraints & Decision drivers

- ADR-0080 lines 140 ("never OOMs the daemon") and 356 ("multipart RAM bound") hold today for one upload only; here
  they hold for all buffered multipart state, bounded by 3 × M. `maxUploadBytes` (lines 185, 239-241) also sizes the
  budget and the share.
- ADR-0159 line 42: a refused Complete leaves the upload's parts in place for a retry, so the copy cannot be built by
  consuming the parts, and no sweep may drop them under a Complete.
- ADR-0148 Scope line 84 defines a size cap as a byte bound on a payload, a key or an object; the budget bounds the
  daemon's capacity, so §5 (line 140, `EntityTooLarge`) and the rejected "Keep 400 / 403 / 503" (line 113) do not
  apply. Size caps keep 400 `EntityTooLarge`; only this capacity refusal answers 503.
- No port, shim or wire contract change; every part and every Complete already passes `m.mu`.
- versitygw v1.6.0 has `s3err.ErrSlowDown` (`SlowDown`, HTTP 503, `s3err/s3err.go:700-704`), the same answer as the
  in-flight cap (`s3gateway.go:137`).

## Alternatives considered

| Option | Pros | Cons — why it lost |
|---|---|---|
| **A, refined: 3 × M budget, parts ≤ 2M, copies counted, single-flight Complete, share M per principal** (chosen) | Bounds RAM for any number of uploads, Completes and principals; one file; no new key | A principal writing several files in parallel gets 503 past M; principals filling the parts limit stall others until they finish or stop growing past their peak for `multipartIdleExpiry` (Risks accepted) |
| A as first proposed: 2 × M, counted in `putPart` only | Fewer lines | Concurrent Completes add uncounted copies (measured 61.6 MiB from one 4 MiB upload); two full-cap uploads fill it and neither can Complete; one principal can hold all of it for 1 h |
| B: per-principal share only | Isolates tenants | RAM still grows with the number of principals × M |
| C: streaming blob seam (parts to temp files, streamed Put) | Lifts the RAM bound | New method on the blob port and every driver (ADR-0007), crash cleanup of temp files, a disk budget; far larger than a priority/low, opt-in bug |
| New key `s3gateway.maxBufferedBytes` | Operator sets the budget directly | One more knob; an operator who lowers M to fit RAM already gets a matching budget |
| 400 `EntityTooLarge` when full | No 503 | Not a size cap (ADR-0148 Scope); clients treat it as final, not retryable |
| Reserve the declared Content-Length of bodies being read | Covers parallel bodies | versitygw streams bodies; chunked or aws-chunked bodies have no usable length, so it falls back to M per request; widens the budget to PutObject — deferred (Open questions) |
| Drop the parts when the copy is built | No double copy | Breaks ADR-0159's retry after a refused Complete |

## Decision

1. **Budget.** `budget = 3 × maxUpload` (`multipartBudgetFactor`, chosen, not measured; 3 GiB at the default).
   When `3 × maxUpload` would overflow int64 (the key is validated only `min=0`, `config.go:229`), the budget is off:
   every budget check passes, since `maxUpload` is then unbounded in practice. Every check is written as a
   subtraction so it cannot overflow. Under `m.mu` the store keeps `buffered` (the sum of every upload's size),
   `assembling` (the copies held by in-flight Completes) and each owner's buffered bytes.
2. **Owner.** An upload's owner is the principal that created it (`principal.ref`, `auth.go:18`), recorded at
   `create`; every part of the upload counts against that owner, whoever sends it.
3. **Parts.** A part that grows the upload is admitted only when afterwards `buffered ≤ budget − maxUpload`,
   `buffered + assembling ≤ budget` and the owner's bytes `≤ maxUpload`. A part that does not grow the upload is always
   admitted. The per-upload cap is checked first and keeps 400 `EntityTooLarge`. An admitted part refreshes `touched`
   only when it takes the upload past `peak`, the largest size it has held; a same-size, smaller or regrown part does
   not, so re-sending parts cannot keep an upload from the sweep.
4. **Refusal.** When a check fails, the store sweeps uploads idle past `multipartIdleExpiry` and checks again; when it
   still fails it answers 503 `SlowDown` and changes nothing about the refused upload (no part, no `touched` refresh).
   The sweep (here and in `create`) skips an upload marked completing.
5. **Complete.** `assemble` answers, in order: `NoSuchUpload`; the part-list errors as today (`MalformedXML`,
   `InvalidPartOrder`, `InvalidPart`); 503 `SlowDown` when a Complete of that id is in flight. It then sums the listed
   parts (n); when `n > budget − buffered − assembling` it sweeps and checks again, answering `NoSuchUpload` when the
   sweep dropped this upload and 503 `SlowDown` when the copy still does not fit. Otherwise it marks the upload
   completing, adds n to `assembling` and builds the copy. Because parts never pass `budget − maxUpload`, one
   Complete always fits when no other is in flight.
6. **Release.** Every exit of `CompleteMultipartUpload` after an admitted `assemble` calls `release`: it returns n and
   clears completing; on success and on the cap refusal it also drops the upload and returns its parts. A 503, any
   `writeConditions` refusal (412, 404 or 501, ADR-0159 Decision 5; or a Stat error, as on main) or a Put error leaves the parts in
   place, and a `release` that keeps the upload leaves `touched` as it was.
7. **Returning bytes.** Abort, the successful Complete and the idle sweep return the upload's bytes to `buffered` and
   to its owner; an owner at 0 leaves the map. `release` on an upload aborted meanwhile returns only n.

## Temporary workarounds

- The budget belongs to ADR-0080's buffered-multipart workaround and shares its exit: a streaming blob seam, at which
  point the buffers, the budget and the share go away.

## Contracts

```go
// internal/blob/s3gateway/multipart.go

// multipartBudgetFactor sizes the daemon-wide budget on buffered multipart bytes (ADR-0188): budget =
// multipartBudgetFactor × maxUpload, and parts may use budget − maxUpload so one full-cap Complete always fits.
const multipartBudgetFactor = 3

type multipartStore struct {
	mu         sync.Mutex
	uploads    map[string]*upload
	clock      clock.Clock
	maxUpload  int64                     // s3gateway.maxUploadBytes: the per-upload cap and the per-principal share
	budget     int64                     // multipartBudgetFactor × maxUpload; 0 = off when that overflows
	buffered   int64                     // the sum of every upload's size
	assembling int64                     // the copies held by in-flight Completes
	owned      map[authz.EntityRef]int64 // each owner's buffered bytes; no entry at 0
}

type upload struct {
	target     uploadTarget
	opts       blob.PutOptions
	parts      map[int32][]byte
	size       int64
	touched    time.Time       // the last Create or part that took size past peak
	peak       int64           // the largest size the upload has held
	owner      authz.EntityRef // the principal that created the upload
	completing bool            // a Complete holds this upload's assembled copy; the sweep skips it
}

func newMultipartStore(maxUpload int64) *multipartStore
func (m *multipartStore) create(t uploadTarget, owner authz.EntityRef, opts blob.PutOptions) string
func (m *multipartStore) putPart(id string, t uploadTarget, num int32, data []byte) error
func (m *multipartStore) assemble(id string, t uploadTarget, mpu *awstypes.CompletedMultipartUpload) ([]byte, blob.PutOptions, error)
func (m *multipartStore) release(id string, t uploadTarget, n int64, drop bool)
func (m *multipartStore) sweep(now time.Time) // the caller holds m.mu
```

`abort` and `parts` keep their signatures; `abort` also returns the upload's bytes.

| Site (origin/main) | Change |
|---|---|
| `s3gateway.go:118` `newMultipartStore()` | `newMultipartStore(maxUpload)` |
| `multipart.go:65-69` | the loop becomes `m.sweep(now)` |
| `multipart.go:189` `b.mp.create(…)` | passes `pr.ref` |
| `multipart.go:212` `b.mp.putPart(…, b.maxUpload)` | drops the last argument |
| `multipart.go:237-247` | `:238` and `:247` call `release(id, target, n, true)` instead of `abort`; the `writeConditions` exit (`:241-243`) and the Put error (`:244-246`) call `release(id, target, n, false)` |

| Condition | S3 error | HTTP |
|---|---|---|
| A part takes its upload past M | `EntityTooLarge` | 400 (unchanged) |
| A part passes the share, the parts limit or the budget after a sweep | `SlowDown` | 503 |
| A Complete while another Complete of the same id is in flight | `SlowDown` | 503 |
| A Complete whose copy does not fit after a sweep | `SlowDown` | 503 |

| Consumes | Exposes |
|---|---|
| `s3gateway.maxUploadBytes` (`internal/platform/config/config.go:229`, default 1 GiB at `s3gateway.go:24`), `principal.ref` (`auth.go:18`) | the 503 answers above; no new config key, port method, metric or dependency |

## Implementation plan

1. **Prove first (fails on current main).** Add `TestScenarioMultipartSharePerPrincipal` to a new
   `internal/blob/s3gateway/budget_test.go` (package `s3gateway_test`, harness `newGateway`, `lakehouseMeta`,
   `memBucket`, `MaxUploadBytes` = 64 KiB, per call `o.RetryMaxAttempts = 1`). Run
   `scripts/agent/d go test -race -run TestScenarioMultipartSharePerPrincipal ./internal/blob/s3gateway/`: on main all
   20 parts answer 200 and the test fails. Paste that output in the PR.
2. Implement the Contracts and Decision 1-7 in `multipart.go` and `s3gateway.go:118`.
3. One test per scenario:
   - `TestScenarioMultipartSharePerPrincipal` — step 1; all assertions pass.
   - `TestScenarioMultipartBudgetDaemonWide` — unit, `multipart_unit_test.go`, three owner `EntityRef`s, M = 1024.
   - `TestScenarioCompleteCopyCountsAndAlwaysFits` — unit: two full uploads, the first `assemble` admitted, the second
     `SlowDown`; after `release(…, true)` the second is admitted.
   - `TestScenarioConcurrentCompleteSingleFlight` — `budget_test.go`, a `makeBucket` returning a wrapper that embeds
     `blob.Bucket` (so it compiles after ADR-0184 adds `ListAfter`) and whose Put blocks on a channel; the second
     Complete gets `SlowDown`, the first 200 after the Put is released.
   - `TestScenarioExpiredUploadFreesBudgetOnPart` — unit, `settableClock`, no `create` between the refusal and the retry.
   - `TestScenarioNonGrowingPartDoesNotHoldBudget` — unit, `settableClock`, three owners, M = 1024.
   - `TestScenarioSizeCapStaysEntityTooLarge` — unit: `putPart` returns `ErrEntityTooLarge`.
   - Unit `TestMultipartBytesReturned` — abort, a successful or refused `release` and the sweep leave `buffered`,
     `assembling` and `owned` consistent (no entry at 0); a smaller replacement part is admitted at the limit; a
     smaller then regrown part leaves `touched` as it was; the sweep skips a completing upload;
     `newMultipartStore(math.MaxInt64)` admits a part.
   `TestIssue30_AbandonedMultipartUploadExpires` takes the new signatures only; the other existing tests stay as they are.
4. Update the key's doc comments: `examples/funcdconfig.yaml:181` and `Deps.MaxUploadBytes` (`s3gateway.go:55-56`) name
   the derived 3 × M budget and the M share.
5. Run `scripts/agent/d go test -race ./internal/blob/s3gateway/`, `go vet` and `golangci-lint` on that package, and
   `go build ./...`.

**Definition of done**: the step-1 failure is in the PR; every test above passes with `-race`; the PR states whether
DuckDB httpfs (the version in `images/runtime/duckdb/Dockerfile`) retries a 503 `SlowDown`, citing its source or a
`just lima-example duckdb` run, and files an issue when it does not; the repo-wide checks run once per PR in
`scripts/agent/gate.sh`.

## Review checklist

- [ ] `putPart` checks the per-upload cap (400) before the share and the budget (503).
- [ ] Parts never pass `budget − maxUpload`; `buffered + assembling` never passes `budget`; the budget is off when
  `3 × maxUpload` overflows and no check can overflow.
- [ ] A refused part or Complete sweeps once, then answers `SlowDown` and changes nothing about the refused upload.
- [ ] Only a part that takes the upload past `peak` refreshes `touched`; a kept `release` does not; the sweep skips a
  completing upload.
- [ ] `assemble` refuses a second Complete of the same id; every exit after an admitted `assemble` calls `release`; a
  503, a `writeConditions` refusal or a Put error keeps the parts.
- [ ] Abort, the successful Complete and the sweep return bytes to `buffered` and the owner; `owned` has no zero entry.
- [ ] One test per scenario, named as above; the step-1 failure is shown.
- [ ] No new config key, port method, metric or dependency; no absolute path or username in the diff.

## Consequences

- **Positive**: buffered parts plus assembled copies stay at or below 3 × M; one principal holds at most M of parts.
- **Negative**: a principal writing several files in parallel gets 503 once its parts pass M; two principals at full
  share stall other tenants' multipart writes until they complete, abort or let every upload go `multipartIdleExpiry`
  without growing past its peak; a client that does
  not retry `SlowDown` fails the write; a client that pauses longer than `multipartIdleExpiry` between growing parts
  loses the upload, as today, and so can one whose Complete is refused (412, a Put error) near that bound, since a
  refused Complete does not refresh `touched` (nor does any Complete on main today).
- **Risks accepted**: request bodies being read and PutObject's buffer stay outside the budget, bounded only by
  4096 × M; the factor 3 is not measured; the budget is a shared capacity limit, like ADR-0112's in-flight ceiling: principals
  that keep their uploads growing (1 byte per `multipartIdleExpiry` is enough below the share) or re-create them can
  hold the parts limit, two at full share, with no time bound; per-namespace fairness is out of scope (an upload
  lifetime would not help, since an upload can be re-created).

## Open questions

- Count request bodies being read (a reservation that falls back to M for a chunked body) — a follow-up ADR if a burst
  of parallel bodies is reported.

## References

- Issue [#731](https://github.com/pyvvo/funcd/issues/731).
- versitygw v1.6.0 `s3err/s3err.go:700-704` (`ErrSlowDown`).
- ADR-0007, ADR-0080, ADR-0085, ADR-0148, ADR-0159.
