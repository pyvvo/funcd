# ADR-0134 Implementation Review — Gateway-normalized invoke body

**Verdict**: **pass** — the data-plane edge builds the CloudEvent for external invokes exactly as the
Contracts specify; all five scenarios are named, un-skipped, passing tests; the build/lint/test/mod checks
are green; no new dependency.
**Reviewed against**: ADR-0134 Contracts / Scenarios / Review checklist · blueprint (data-plane, single
binary, library-first) · ADR-0002 conventions.
**Producing model**: claude-opus-4-8.

## Verification run (evidence)

- `go build ./...` → exit 0.
- `go tool golangci-lint run ./internal/dataplane/...` → **0 issues** (the initial `any`/forbidigo hits were
  fixed by switching to `json.Valid` + `map[string]json.RawMessage`).
- `go test ./internal/dataplane/...` → `ok` (all scenario + regression tests pass).
- `go vet ./internal/dataplane/...` → clean.
- `go mod verify` → all modules verified.
- Identity/path grep over all changed files → clean.

## Conformance to the Contracts

- `normalizeInvokeBody(ns, name, body, newID) ([]byte, bool)` implements the four-branch rule verbatim:
  empty → `data:null`; object with `specversion`/`data` → passthrough; any other valid JSON → wrapped;
  invalid JSON → `ok=false` (forward raw). The envelope is the specified v1.0 shape
  (`specversion/type/source/id/data`).
- `serveFunction` normalizes **only** `!internal` invokes; the read is bounded by
  `http.MaxBytesReader(w, r.Body, maxNormalizeBytes)` (1 MiB) → `413` via `fault.PayloadTooLargef`, so the
  buffering is independent of the optional edge cap (the folded Major).
- `internal` traffic is forwarded byte-for-byte — asserted end-to-end (`TestScenarioInternalInvokeUntouched`
  sends a plain `{"x":1}` that wraps when external and is forwarded verbatim when internal).

## Scenario → test map (all passing)

| Scenario | Test |
|---|---|
| no-input-empty-body-invoke | `TestScenarioNoInputEmptyBodyInvoke` + `TestScenarioEmptyBodyForwardsNullData` (e2e) |
| plain-json-body-wrapped | `TestScenarioPlainJSONBodyWrapped` (incl. scalar/array/null) |
| cloudevent-passthrough | `TestScenarioCloudEventPassthrough` |
| sensor-and-gateway-agree | `TestScenarioSensorAndGatewayAgree` |
| internal-invoke-untouched | `TestScenarioInternalInvokeUntouched` (e2e, fake echo upstream) |
| (invalid JSON not normalized) | `TestNonJSONBodyNotNormalized` |

## ✅ Verified correct — keep

- The `map[string]json.RawMessage` + `obj != nil` decode cleanly distinguishes object-envelopes from
  `null`/array/scalar without `any` (satisfies forbidigo while staying precise).
- External-only gating via the spoof-proof `internal` ctx marker keeps fn-to-fn/workflow/sensor untouched —
  the safest scope.
- The e2e internal test uses a body that *would* wrap externally, so it genuinely distinguishes
  skip-normalization from passthrough (a weaker test with an envelope body would not).

## Findings

### Blockers
None.

### Major
None. (The pre-accept Major — unbounded buffering when the edge cap is off — was folded into the ADR and
implemented as `maxNormalizeBytes` + `413`.)

### Minor
- The documented wrap-vs-passthrough ambiguity (a raw object whose own top-level key is `data`) is inherent
  to back-compat and is called out in the ADR + example note; no code change warranted.

## Recommendation

Stamp **Implemented**. Follow-on (already noted in the ADR Open questions): binary / non-JSON
`datacontenttype` invoke payloads.
