# ADR-0148: Size caps answer 413 wherever funcd enforces one; malformed input stays 400

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: errors, rfc9457, size-cap, local-api, kv, blob, s3, workflow, edge, control-plane
- **Realizes**: [FEAT-0001/F38](../feat/0001-feat-v1.1.md) (KV data-plane); relates to FEAT-0001/F32 (admission), F42 (KV
  bindings), F92 (context.blob), FEAT-0003/F47 (S3 frontend), FEAT-0005/F64 (workflow engine), FEAT-0006/F75 (ingress
  limits), FEAT-0008/F102 (catalog edge), FEAT-0000/F02 (control-plane API)
- **Supersedes (in part, scoped)** — only these lines (Decision §1-§3 replace them); nothing else changes:
  - [ADR-0063](0063-admission-framework.md) Contracts (`Admission` doc comment): "a fault error (Invalid/Forbidden/Conflict)
    on deny" — the list gains `PayloadTooLarge`.
  - [ADR-0069](0069-kv-data-plane.md) Decision §1: the error map's "`Invalid→422`".
  - [ADR-0073](0073-kv-bindings-and-subdomains.md) Decision §4: "Per-op caps (`maxValueBytes`/`maxKeyBytes`) → `Invalid`";
    scenario `value-over-cap-rejected`: its Then-clause "(`Invalid`)".
  - [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) Contracts (`BucketSpec` comment): "a write past it ⇒ Forbidden".
  - [ADR-0112](0112-ingress-protection-limits.md) Decision §3: "V1 does **not** promise a clean 413 (nor no-wake) on the
    chunked branch … Converting `http.MaxBytesError`→413 at the proxy is a V2 follow-up", and the same clause in scenario
    `over-size-413` and the Acceptance note — the clean-413 half only; no-wake stays unpromised.
  - [ADR-0127](0127-context-blob-data-plane.md): scenario `blob-size-cap` ("413/422"); Decision §1, the put bullet
    ("over-cap ⇒ 413/422") and the error map ("`Invalid→422`"); the Review checklist's "over-cap ⇒ 413/422".
  - [ADR-0138](0138-external-catalog-ingress-and-route-aggregation.md) Contracts (data-plane comment): "ErrorHandler ⇒
    fault.Unavailable (5xx)", for a request body over the edge cap only.

  Each keeps status `Implemented` and gets a one-line `Superseded in part by: ADR-0148` back-link at acceptance (as
  ADR-0058 carries for ADR-0060). Not superseded: [ADR-0094](0094-workflow-engine-core.md) (states the cap, not its status) and ADR-0018 C3 ("admission
  validates (400)" fixes stage order; ADR-0063 already widened deny kinds).
- **Relates to**: ADR-0002 (`api/fault`, the single map) · ADR-0003 · ADR-0005 (§4 huma error bridge; §6 huma's 422, out of scope) ·
  ADR-0018 (Invalid → 400) · ADR-0064 (local API) · ADR-0134 (413 at the data plane) · drafts on the same code (the second
  to land rebases): ADR-0159 (`cappedBucket.Put`, `mapBlobErr`) · ADR-0164 (rewrites `limit.go` `Config`/`Chain` next to
  the `:46`/`:77` comments; adds a check in `serveFunction`) · ADR-0151, ADR-0171 (`serveFunction`)

## Context & Need

`api/fault/problem.go` is the one place funcd decides HTTP statuses (ADR-0002): `Invalid` → 400, `PayloadTooLarge` →
413. Some size caps raise `Invalid`, `Forbidden` or `Unavailable`, so a caller cannot tell "too big" from "malformed",
"denied" or "down"; ADR-0069 and ADR-0127 say the local API answers `Invalid` with 422, which the code never did
(#171). Verified on main 1193be6:

- Already 413: `readBody` (`internal/workernode/local/local.go:180`), the data plane's invoke read
  (`internal/dataplane/dataplane.go:167`), the edge's `Content-Length` check (`internal/edge/limit/limit.go:73`).
- 400: KV value/key over the store cap (`internal/services/kv/kv.go:124`, `:127`; only `Put` checks the key); WorkflowRun
  input over `workflow.payloadLimit` (`admission/workflowrun.go:41`). A 70000-byte key delete → 500 (Badger's 65000-byte
  limit, badger v4.9.2 `txn.go:352`).
- 403: `context.blob.put` over `maxObjectBytes` (`internal/blob/capped.go:31`); `s3BucketFor` (`pkg/funcd/funcd.go:1705`)
  wraps every Bucket view in it, so S3 (`AccessDenied`) and the site reconciler share it.
- 503: the edge cap on a chunked body (`limit.go:78`) on an edge-upstream route (`dataplane.go:248-251`); a function invoke
  gets 413 naming `maxNormalizeBytes`, not the cap that tripped (`dataplane.go:171`).
- Control plane (huma v2.38.0 `MaxBodyBytes`, 1 MiB, `huma.go:1379-1382`): 413 typed `about:blank` with huma's title
  (`newFaultError`, `internal/controlplane/controlplane.go:317`). `s3gateway.maxUploadBytes`: S3 `EntityTooLarge`, HTTP 400
  (`internal/blob/s3gateway/backend.go:617-618`, `multipart.go:76-77`), as AWS S3.
- Shims raise on any non-2xx except a get's 404 (`null`/`None`); `pkg/sdk` maps 413 to `PayloadTooLarge`
  (`pkg/sdk/sdk.go:540`); nothing in `pkg/sdk` or `funcdctl` branches on `Invalid`.
- Purpose: the status alone tells the caller its input was too big (413), not malformed (400); the documents match.

## Scenarios

"413" below means status 413 with problem type `urn:funcd:problem:payload-too-large`.

- `scenario: kv-value-over-store-cap` — Given a KVStore with `maxValueBytes: 100` bound to a Function, When it calls
  `context.kv.put` with a 101-byte value, Then 413, the detail naming the cap.
- `scenario: kv-key-over-store-cap` — Given the store with `maxKeyBytes: 1024`, When it puts a 1025-byte key, Then 413.
- `scenario: kv-key-over-hard-limit` — When it gets, puts or deletes a key one byte over `v1.MaxKeyBytesLimit` (64000), Then
  each call answers 413, never 404, 400 or 500.
- `scenario: kv-lowered-key-cap-keeps-keys` — Given a 2000-byte key stored under `maxKeyBytes: 4096`, When the cap is
  lowered to 1024, Then the KV facade's `Get` returns the value and its `Delete` removes the key.
- `scenario: blob-over-bucket-object-cap` — Given a Bucket with `maxObjectBytes: 16` whose prefix the Function is bound to
  and owns, When it calls `context.blob.put` with 17 bytes, Then 413 and nothing is stored.
- `scenario: s3-over-cap-is-entity-too-large` — Given that Bucket, When an S3 `PutObject` sends 17 bytes, Then S3
  `EntityTooLarge` with HTTP 400, not `AccessDenied`, and nothing is stored.
- `scenario: local-api-body-over-limit` — When an invoke, `kv.put` or `blob.put` body exceeds the local API's limit, Then
  413.
- `scenario: local-api-invalid-stays-400` — When a `context.kv.put` body breaks off mid-read for a non-size reason (e.g.
  malformed chunked encoding), Then 400 of type `urn:funcd:problem:invalid`.
- `scenario: workflowrun-payload-over-cap` — When a WorkflowRun with input over the payload cap is applied, Then admission
  denies it with `PayloadTooLarge`, rendered 413.
- `scenario: edge-chunked-body-over-cap` — Given `limit.Config.MaxBodyBytes` 1024, When a 64 KiB chunked body hits an edge
  Route to an upstream, Then 413, not 503; and a 2 KiB chunked body to `/function/<name>` gets 413 naming 1024.
- `scenario: control-plane-body-over-cap` — When a control-plane body exceeds huma's 1 MiB `MaxBodyBytes`, Then 413 with the
  title "Content Too Large".

## Scope

A *size cap* is a byte bound funcd declares on a payload, a key or an object; a field's validation length (huma's 422, a
`Validate` 400) is not one. In: every size cap that answers an HTTP request — the local API's KV and blob routes (KVStore key/value caps, Bucket
`maxObjectBytes`), the WorkflowRun payload cap at admission, the edge cap on a chunked body, the control plane's body cap; the S3 frontend's mapping of
the Bucket cap's new kind (forced by the shared `blob.Capped`); `Invalid` → 400 on the local API; the comments and ADR lines
stating these.

Out: the invoke pass-through (a target's own status, e.g. the shim's 422 — ADR-0058, ADR-0123 — reaches the caller
unchanged, ADR-0064); huma's 422 (ADR-0005 §6) and its rejection of a body of exactly `MaxBodyBytes` (`huma.go:2128`);
fileblob's `ENAMETOOLONG` → `Invalid` (`internal/blob/gocloud/gocloud.go:240-242`; S3 PUT `InvalidRequest`,
`backend.go:150-151`; GET/HEAD `NoSuchKey`), a backend naming rule; header caps (net/http's 431, `MaxHeaderBytes` about
1 MiB, RFC 6585 §5 — a KV key over it in the URL keeps 431; versitygw's 8 KiB `RequestHeaderSectionTooLarge`); versitygw's
own caps (§5); caps answering no HTTP request (the engine's run-start and step-output checks,
`internal/workflow/engine.go:260`, `:831`; `RunRecordTooLarge`, `internal/workflow/runstate/badger/badger.go:96`; the
512-byte `maxStatusError`, `engine.go:62`).

## Constraints & Decision drivers

- ADR-0002's single status site (the `maxInvokeBytes` response cap keeps 413, though sending less cannot fix it). The S3
  frontend speaks S3 (ADR-0080): versitygw `s3err` codes, not RFC 9457; clients key on the code.
- Lowering a store's cap must not make data unreachable; no status change beyond Decision §2-§5.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **One rule: a size cap is `PayloadTooLarge` (413), `Invalid` is 400; S3 keeps S3's statuses** | One status per meaning, through the single map | Seven Implemented ADRs superseded in part; several cap errors change status | **chosen** |
| A local-API override `Invalid` → 422 (#171 a) | Matches ADR-0069 and ADR-0127 as written | A second status map, against ADR-0002 | rejected |
| `Invalid` → 422 in the `api/fault` map (#171 b) | One map, matches ADR-0069/0127 | Breaks the 400 of ADR-0002, ADR-0003, ADR-0018 | rejected |
| A new fault Kind for caps (#171 c) | Explicit cap kind | Grows `api/fault`; `PayloadTooLarge` already says it | rejected |
| Keep 400 / 403 / 503 | No status change | A cap error looks like malformed input, a denial or an outage | rejected |
| 414 for an over-long KV key | RFC 9110 names URI length | Two statuses for "too big" | rejected |
| `Get`/`Delete` check the store's `maxKeyBytes` | Same check on every verb | Lowering the cap strands stored keys | rejected |
| S3 frontend answers 413 with `EntityTooLarge` | Same status as the local API | AWS S3 answers 400; leaves the protocol | rejected |

## Decision

1. **`Invalid` is 400** through the single map (ADR-0002), the local API included (no `Invalid→422`; supersedes it in ADR-0069 and
   ADR-0127).
2. **A size cap answers 413 wherever funcd enforces one**: the enforcing site raises `fault.PayloadTooLarge`, and the single
   map turns it into 413 (the Context's already-413 sites keep it). This ADR changes:
   - **KV**: a value over `maxValueBytes` and a key over `maxKeyBytes` on `Put`. `Get` and `Delete` do not check the store
     cap, so lowering `maxKeyBytes` never strands a key. A key over `v1.MaxKeyBytesLimit`
     (`api/types/v1alpha1/kvstore.go:19`) is `PayloadTooLarge` on every key-taking verb: `checkKeyLimit` on `Get` and
     `Delete`; on `Put` the store cap is at most that limit (`EffectiveMaxKeyBytes`). Badger's key limit never surfaces as
     500. `List` (a prefix) is unchanged. Supersedes ADR-0073's "→ `Invalid`";
   - **Bucket**: `blob.Capped.Put` over `maxObjectBytes` raises `PayloadTooLarge` (supersedes ADR-0080's "⇒ Forbidden"
     and ADR-0127's "413/422"). `Capped.SignedURL` still refuses a PUT
     URL with `Forbidden` (no size is exceeded);
   - **WorkflowRun**: the `workflowrun-payload` admission raises `PayloadTooLarge` for an input over
     `workflow.payloadLimit`; ADR-0063's deny kinds gain it.
3. **The edge cap on a chunked body answers 413.** `serveUpstream`'s `proxy.ErrorHandler` maps a `*http.MaxBytesError` to
   `PayloadTooLarge` before its `Unavailable` fallback; on a function invoke the detail names `MaxBytesError.Limit`, not
   always `maxNormalizeBytes`. Supersedes ADR-0112 §3's clean-413 clause and ADR-0138's "ErrorHandler ⇒
   fault.Unavailable" for that error.
4. **The control plane's body cap answers funcd's problem.** `newFaultError` maps huma's 413 to
   `fault.ToProblem(fault.PayloadTooLarge…)`: type `urn:funcd:problem:payload-too-large`, funcd's title.
5. **The S3 frontend speaks S3 — the one exception to §2.** The Bucket cap and `s3gateway.maxUploadBytes` answer S3
   `EntityTooLarge` with HTTP 400, as AWS S3 does. `mapBlobErr` maps `PayloadTooLarge` to `s3err.ErrEntityTooLarge` (what
   `readCapped` already returns; otherwise `InternalError`, 500). versitygw's own caps stay as versitygw answers.
6. Comments corrected: `internal/workernode/local/kv.go`, `blob.go` ("over a cap → 413, bad input → 400");
   `internal/blob/capped.go`, `api/types/v1alpha1/bucket.go` ("⇒ PayloadTooLarge"); `newFaultError`'s ("type about:blank");
   `api/fault/fault.go:32` (a payload, key, object or record over a size cap funcd enforces — 413 when it answers HTTP);
   `internal/edge/limit/limit.go:46`, `:77` (413; a chunked body too, unless the upstream already answered);
   `internal/site/reconcile.go` (unpack's put: "an object over the Bucket's maxObjectBytes (blob.Capped: PayloadTooLarge),
   or a key the substrate cannot store (Invalid)").

## Temporary workarounds

None.

## Contracts

No exported signature changes. The statuses after this ADR, all through `api/fault/problem.go` except the S3 row:

| Outcome | `fault` kind | Status | Problem type |
|---|---|---|---|
| Malformed or unreadable input | `Invalid` | 400 | `urn:funcd:problem:invalid` |
| Over a size cap: a local API body; an invoke response over `maxInvokeBytes`; an external invoke body over `maxNormalizeBytes`; a KV value, or a key on put, over its store cap; a KV key over `v1.MaxKeyBytesLimit` on any verb; an object over its Bucket's `maxObjectBytes` (local API); a WorkflowRun input over `workflow.payloadLimit`; a body over the edge cap (declared or chunked); a control-plane body at or over huma's `MaxBodyBytes` | `PayloadTooLarge` | 413 | `urn:funcd:problem:payload-too-large` |
| S3 frontend: a write over the Bucket cap or `s3gateway.maxUploadBytes` | `PayloadTooLarge` / — | 400 | S3 XML `EntityTooLarge` |
| Binding or owner denial; a PUT URL on a capped Bucket | `Forbidden` | 403 | `urn:funcd:problem:forbidden` |
| Missing key or object | `NotFound` | 404 | `urn:funcd:problem:not-found` |
| Every other kind (`Internal` 500, `Unavailable` 503, …) | per `api/fault/problem.go` | | |

The changed error sites keep their messages and `op`; `checkKeyLimit`, its two `op`s and the control-plane `op` are new:

```go
// internal/services/kv/kv.go — Get ("services.kv.get") and Delete ("services.kv.delete"), after resolveAuth
func checkKeyLimit(op, key string) error {
	if len(key) > v1.MaxKeyBytesLimit {
		return fault.PayloadTooLargef(op, "key (%d bytes) exceeds the largest storable key (%d bytes)", len(key), v1.MaxKeyBytesLimit)
	}
	return nil
}
// internal/services/kv/kv.go (Put): both caps raise PayloadTooLargef instead of Invalidf
return fault.PayloadTooLargef("services.kv.put", "value (%d bytes) exceeds the store cap (%d bytes)", len(value), b.MaxValueBytes)
return fault.PayloadTooLargef("services.kv.put", "key (%d bytes) exceeds the store cap (%d bytes)", len(key), b.MaxKeyBytes)
// internal/blob/capped.go (cappedBucket.Put)
return fault.PayloadTooLargef("blob.Capped.Put", "object %q is %d bytes, over the bucket's maxObjectBytes (%d)", key, len(data), c.max)
// internal/blob/s3gateway/backend.go (mapBlobErr)
case fault.PayloadTooLarge:
	return s3err.GetAPIError(s3err.ErrEntityTooLarge)
// internal/controlplane/admission/workflowrun.go (workflowRunPayload.Admit, op "admission.workflowrun-payload")
return nil, fault.PayloadTooLargef(op, "run input %d bytes exceeds the payload limit %d — pass large data by reference on the blob substrate", n, a.limit)
// internal/dataplane/dataplane.go (serveUpstream's proxy.ErrorHandler, first)
var tooLarge *http.MaxBytesError
if errors.As(perr, &tooLarge) {
	fault.WriteProblem(w, fault.PayloadTooLargef(op, "request body exceeds %d bytes", tooLarge.Limit))
	return
}
// internal/dataplane/dataplane.go (serveFunction): the detail names tooLarge.Limit instead of maxNormalizeBytes
fault.WriteProblem(w, fault.PayloadTooLargef(op, "request body exceeds %d bytes", tooLarge.Limit))
// internal/controlplane/controlplane.go (newFaultError, first)
if status == http.StatusRequestEntityTooLarge {
	return &faultError{Problem: fault.ToProblem(fault.PayloadTooLargef("controlplane.body", "%s", msg))}
}
```

Consumes `api/fault` (`PayloadTooLargef`, `ToProblem`), `v1.MaxKeyBytesLimit`, versitygw `s3err.ErrEntityTooLarge`; exposes
the table's statuses on the local API, data plane and control plane, and S3 `EntityTooLarge` (400) for a Bucket cap.

## Implementation plan

1. Code: the Contracts snippets at their sites; `internal/site/reconcile.go` unpack's put drops its `k == fault.Forbidden`
   arm (keeps `PayloadTooLarge`, `Invalid`); then the comments of Decision §6.
2. Tests, `TestScenario<Name>` with a `// scenario: <name>` comment; existing tests are upgraded, not duplicated:
   - `internal/services/kv/kv_test.go`: `TestScenarioValueOverCapRejected` (ADR-0073),
     `TestIssue377_DeclaredKeyCapIsStorable` and `TestIssue461_StoredOverLimitCapsAreClamped` assert `PayloadTooLarge` where
     they assert `Invalid` (TestIssue461 also gets/deletes the 70000-byte key through Badger); new
     `TestScenarioKVLoweredKeyCapKeepsKeys` (two facades over one engine, cap 4096 then 1024).
   - `internal/workernode/local` via `NewHandler` with real facades (status, type, cap in detail): `kv_test.go`
     `TestScenarioKVValueOverStoreCap`, `TestScenarioKVKeyOverStoreCap`, `TestScenarioKVKeyOverHardLimit` (the
     `fakeKVResolver`'s `MaxKeyBytes: 1024` trips the store cap on put; `TestIssue461` covers the clamp); `blob_test.go`
     `TestScenarioBlobOverBucketObjectCap` (`iblob.Capped(newBlobMapBucket(), 16)`); `TestScenarioLocalAPIInvalidStays400`
     (non-size read error);
     `TestIssue172_OverCapBodyIs413` gains `// scenario: local-api-body-over-limit`; `TestScenarioBlobSizeCap`
     (`blob_internal_test.go`) tightens `>= 400` to 413.
   - `pkg/funcd/s3gateway_internal_test.go`: `TestIssue109_BucketMaxObjectBytesForbidsOversizeWrite` →
     `…RejectsOversizeWrite`, tagged `s3-over-cap-is-entity-too-large`, asserts `EntityTooLarge`/400 (as
     `TestIssue30_MultipartTotalCappedAtUploadPart`) and the view's `fault.PayloadTooLarge`.
   - `internal/site/reconcile_test.go`: `TestSite_ObjectOverBucketCapIsNotReadyNotRetried` becomes one `blob.Capped` case
     expecting `PayloadTooLarge`/`MaterializeFailed`; the `tooLarge` fake is deleted.
   - `internal/services/blob/blob_test.go`: `TestIssue374_PresignedPutRefusedOnCappedBucket` keeps `Forbidden`.
   - `internal/controlplane/admission/workflowrun_test.go`: `TestWorkflowRunPayloadAdmission`, tagged
     `workflowrun-payload-over-cap`, asserts `PayloadTooLarge` and `fault.ToProblem(err)` 413 of that type.
   - `internal/dataplane/upstream_test.go`: `TestScenarioEdgeChunkedBodyOverCap` (`dataplane.Handler` behind
     `limit.Chain(limit.Config{MaxBodyBytes: 1024})`, a body-reading upstream, a function route);
     `internal/controlplane/api_test.go`: `TestScenarioControlPlaneBodyOverCap`.
3. Documents: at Draft, F38 (`docs/feat/0001-feat-v1.1.md`) adds `(+ ADR-0148 size-cap statuses)` to its ADR cell, status
   `data plane: implemented · size-cap statuses: adr` (as F13). At acceptance, the seven back-links; F32, F42, F92, F47,
   F75, F102 add ADR-0148 to their ADR cell, status unchanged. No blueprint change.
4. Done when: `just ci` green; no `422` in `internal/workernode/local/kv.go`, `blob.go`, `internal/services/kv/kv.go`;
   `Forbiddenf` in `capped.go` only in `SignedURL`; no payload-cap `Invalidf` in `workflowrun.go`; no `maxNormalizeBytes` in
   `serveFunction`'s 413 detail; no `Forbidden` arm in `internal/site/reconcile.go`.

## Review checklist

- [ ] Every site of Decision §2-§5 answers as the Contracts table says; no key-taking KV verb reaches Badger with a key over
      `v1.MaxKeyBytesLimit`; `Get`/`Delete` never check the store's `maxKeyBytes`.
- [ ] Unchanged: `Capped.SignedURL`'s `Forbidden`, the engine's payload checks, `readBody`'s 413/400, huma's other errors
      (`about:blank`), the invoke pass-through, `ErrorHandler`'s `Unavailable` for every non-`*http.MaxBytesError`; no
      second status map.
- [ ] No §6 comment claims 422, `Invalid`, `Forbidden` or `about:blank` for a size cap.
- [ ] Each scenario has one named, passing test; the seven back-links and the feat rows of step 3 are in place.

## Consequences

- Positive: one rule for size caps; Badger's key limit never surfaces as 500; lowering `maxKeyBytes` never strands a key.
- Negative: cap failures once 400/403/503 now answer 413 (no shipped client compares); S3 sees `EntityTooLarge`, not
  `AccessDenied`; the control plane's 413 changes its problem type from `about:blank` to
  `urn:funcd:problem:payload-too-large`. The Site reconciler's `MaterializeFailed` check narrows from PR #596's `Forbidden`/`PayloadTooLarge` to
  `PayloadTooLarge`. A get of a key over `v1.MaxKeyBytesLimit` answered 404 (shims return `null`/`None`: funcd-typescript
  v0.4.4 `shim/src/kv.ts:71`, funcd-python v0.3.5 `shim/src/funcd_shim/kv.py:51`), now 413, which throws; its delete moves
  from 204 (memory) or 500 (Badger) to 413.
- Risks accepted: an edge-upstream proxy streams, so the upstream sees the body up to the cap, then a cut-off. A KV key over
  the store cap but within the limit can be read and deleted, not rewritten.

## Open questions

None. The sweep's two other defects (a Site retrying forever over the Bucket cap; an S3 listing over versitygw's 4 MiB XML
cap answering 500) are fixed in PR #596.

## References

Issue [#171](https://github.com/pyvvo/funcd/issues/171); `api/fault/problem.go`, `api/fault/fault.go`; versitygw v1.6.0
`s3err`; huma v2.38.0 `huma.go` (`ensureMaxBodyBytes`, `readBody`); badger v4.9.2 `txn.go` (`maxKeySize`); RFC 9457; RFC
9110 §15.5.14 (413), §15.5.15 (414); RFC 6585 §5 (431).
