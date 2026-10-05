# ADR-0134: Gateway-normalized invoke body — the edge builds the CloudEvent

- **Status**: Implemented
- **Superseded in part by**: [ADR-0181](0181-edge-forwards-bodiless-upgrades.md) (2026-10-05) — Decision lines 113-114, Contracts Wiring lines 176-177, Definition of done lines 211-212 and checklist line 220: a bodiless upgrade request is forwarded without the envelope.
- **Date**: 2026-07-13 (**Implemented 2026-07-13** — review pass (claude-opus-4-8): `normalizeInvokeBody`
  (`internal/dataplane/normalize.go`, four-branch rule) + the `serveFunction` external-only normalization
  land; the five scenarios are named passing tests (`normalize_test.go` + `normalize_e2e_test.go`);
  `internal` traffic is forwarded byte-for-byte (asserted); the read is bounded by `maxNormalizeBytes`;
  example READMEs use the plain-body form with a back-compat note. Green: `go build ./...` · `go tool
  golangci-lint run ./internal/dataplane/...` (0 issues) · `go test ./internal/dataplane/...` · `go mod
  verify`. No new dependency. **Accepted 2026-07-13** via /adr-batch self-accept — judge pass, no open
  Blockers.
  Folded **1 Major**: the normalization step *buffers* the body (today it is streamed), and the optional edge
  body cap ([internal/edge/limit](../../internal/edge/limit/limit.go) `MaxBodyBytes`) may be off, so the read
  is now independently bounded by a fixed `maxNormalizeBytes` (1 MiB) ceiling → `413`, and internal traffic
  stays streamed. Verified `internal` is a spoof-proof ctx marker, so `!internal` gating genuinely leaves
  fn-to-fn / workflow / sensor untouched.)
- **Deciders**: green-0-rabbit
- **Tags**: data-plane, gateway, invocation, cloudevent, dx, F99
- **Realizes**: [FEAT-0001/F99](../feat/0001-feat-v1.1.md) (gateway-normalized invoke body — a caller POSTs
  plain data, or nothing, and the edge constructs the CloudEvent)
- **Relates to / refines**:
  [ADR-0033](0033-data-plane-serving-and-trigger-wake.md) — the data-plane `/function/<name>` handler this
  refines (adds a body-normalization step before the activator hop);
  [ADR-0038](0038-event-data-contract-jtd.md) / [ADR-0123](0123-runtime-compiled-io-validators.md) — the
  shim validates `event.data`; this makes the wire form the shim receives always a well-formed envelope;
  [ADR-0094](0094-workflow-engine-core.md) / [ADR-0109](0109-sensor-event-action-binder.md) — internal
  producers (workflow dispatch, sensor/eventing) already build a v1.0 envelope and are **unchanged**.

## Context & Need

Invoking a function over the data plane is `POST /function/<name>`. The handler
([internal/dataplane](../../internal/dataplane/dataplane.go) `serveFunction`) forwards the raw HTTP body to
the shim unchanged, and the shim decodes that body **as a CloudEvent envelope**
([shim/python `_poolworker.py`](../../shim/python/src/funcd_shim/_poolworker.py): `event = json.loads(body)`;
`event.get("data")`). Two consequences push platform plumbing onto every caller:

1. **A no-input function still needs a body.** To invoke a function whose contract is `type: null`, the
   caller must send `{"data":null}` (or the shim reads `data` off whatever it got).
2. **The caller must hand-write the CloudEvent envelope.** Sending the input directly — `-d '{"x":1}'` —
   makes the shim look for `.data` on `{"x":1}`, so the contract fails (`data must be object`). The caller
   must instead send `{"data":{"x":1}}`.

This is inconsistent with the **Sensor** and **workflow** paths, which construct the CloudEvent *for* the
function (`{"specversion":"1.0",…,"data":<payload>}` — [dispatch.go](../../internal/workflow/dispatch.go),
[eventing/cloudevent.go](../../internal/eventing/cloudevent.go)) and POST it directly to the shim upstream,
**bypassing** the `/function/` edge. Envelope construction is the platform's job at every entry, not the
external caller's. This ADR moves that job to the data-plane edge for direct HTTP invokes, so a caller
sends plain data (or nothing) and the edge builds the CloudEvent — the same normalization the sensor
already does. It caused a real papercut (a mystery 502 while building the releve-lakehouse
`query-transactions` function; the malformed-body crash itself is fixed directly as a shim bug fix,
committed alongside this — a crash guard is not a design decision and carries no ADR).

## Scenarios

- **scenario: no-input-empty-body-invoke** — *Given* a Ready function whose input contract is `type: null`,
  *when* a client `POST`s `/function/<name>` with **no body**, *then* the function runs with `event.data ==
  null` and returns `200` — no `{"data":null}` required.
- **scenario: plain-json-body-wrapped** — *Given* a function whose input is an object, *when* a client POSTs
  the input **directly** (`-d '{"x":1}'`), *then* the handler sees `event.data == {"x":1}` and returns `200`.
- **scenario: cloudevent-passthrough** — *Given* a caller that sends a full envelope (`-d '{"data":{…}}'` or
  a body carrying `specversion`), *when* it POSTs `/function/<name>`, *then* the envelope is forwarded
  **unchanged** (`event.data` is its `data`) — back-compat with the pre-0134 convention and with advanced
  callers.
- **scenario: sensor-and-gateway-agree** — *Given* the same payload delivered once by a Sensor and once by a
  direct HTTP invoke, *then* the handler sees an equivalent CloudEvent (`data` equal; both carry
  `specversion` `1.0`) — the two entry paths normalize to the same envelope shape.
- **scenario: internal-invoke-untouched** — *Given* an internal fn-to-fn / workflow / sensor dispatch (which
  already builds a v1.0 envelope), *when* it reaches the function, *then* the edge does **not** re-wrap it
  (its `data` is unchanged) — normalization is external-caller-only.

## Scope

- **In**: a body-normalization step in the data-plane `serveFunction` for **external** invokes
  (`!internal`), covering both the `/function/<name>` path form and a Route hit; the wrap/passthrough rule;
  updating the example READMEs to the plain-body form.
- **Out**: the malformed-body **crash → clean 4xx** shim guard (a separate direct bug fix committed
  alongside this — a crash guard is not a design decision); changing the **shim** decode contract (the shim
  keeps reading an envelope — the edge just guarantees it receives one for external JSON invokes); non-JSON
  body handling (forwarded as-is; the shim bug fix makes it reject cleanly); response/output shaping;
  auth/rate-limit (unchanged);
  binary / non-JSON `datacontenttype` payloads (a documented follow-on — V1 invoke data is JSON).

## Constraints & Decision drivers

- **Consistency with the sensor/workflow entry paths** — the platform builds the CloudEvent; the caller
  supplies data. This is the whole motivation.
- **Back-compat** — existing callers/docs/tests that send `{"data":…}` must keep working unchanged.
- **One normalization locus, no ambiguity** — the wrap-vs-passthrough decision must be a single robust rule,
  not a guess that silently corrupts a payload.
- **Internal producers untouched** — workflow/sensor/fn-to-fn already emit a v1.0 envelope and bypass this
  edge; the change must not double-wrap them.
- **No new dependency** (blueprint: library-first, single binary); pure stdlib `encoding/json`.

## Alternatives considered

- **Shim-side normalization (treat the raw body as `data`).** Move the "raw body = data" rule into every
  shim (`data = json.loads(body)`), dropping the envelope. *Rejected*: it would break the sensor/workflow
  producers that send a full v1.0 envelope (their `{"specversion",…,"data"}` would become the `data`), and
  it duplicates the rule across the Python + Node shims instead of one Go locus. The edge is the single
  normalization point that already distinguishes external from internal traffic.
- **Always wrap, no passthrough.** Simpler rule (`data = body` always). *Rejected*: breaks back-compat —
  every existing `{"data":…}` caller would double-wrap to `data = {"data":…}`. The passthrough branch is
  required.
- **`specversion`-only discriminator.** Passthrough iff the body carries `specversion`. *Rejected as the
  sole rule*: the current convention is `{"data":…}` with **no** `specversion` (the shim never required it),
  so specversion-only would wrap today's envelopes and break them. The rule must also recognize a top-level
  `data` key. (Both are accepted — see Decision.)
- **A new query param / header to opt into wrapping.** *Rejected*: pushes a second knob onto the caller; the
  whole point is zero ceremony.

## Decision

The data-plane `serveFunction` **normalizes the request body into a CloudEvents v1.0 envelope before the
activator hop, for external invokes only** (`!internal`). Given the read body `b`:

1. **Empty body** (`len == 0`) → envelope with `data: null`.
2. **`b` parses as a JSON *object* that already looks like an envelope** — it has a top-level `"specversion"`
   **or** a top-level `"data"` key → **passthrough unchanged** (back-compat + advanced callers).
3. **`b` parses as any other JSON value** (object without `data`/`specversion`, array, string, number,
   boolean, or `null`) → **wrap**: `data` = that value.
4. **`b` is not valid JSON** → **forward unchanged** (the shim rejects it; the shim bug fix makes that a
   clean `400`). The edge normalizes *valid JSON*; it does not become a second JSON validator.

The constructed envelope is:

```json
{
  "specversion": "1.0",
  "type": "io.funcd.invoke",
  "source": "funcd://<namespace>/function/<name>",
  "id": "<16 hex, crypto/rand>",
  "data": <the value per the rule above>
}
```

Internal traffic (`internal == true`: fn-to-fn over the worker-node local API, and the workflow/sensor
dispatchers that POST directly to the shim upstream) is **never** normalized — those producers already emit
a full v1.0 envelope. The shim's decode contract is unchanged: it still reads `event.data`; the edge just
guarantees external JSON invokes arrive as an envelope.

**Ambiguity note (documented):** a function whose input is itself an object with a top-level `data` key
would, sent raw, be read as an envelope (rule 2) and unwrapped. Such a caller sends a full envelope
explicitly (`{"specversion":"1.0","data":{"data":…}}`, rule 2 passthrough). This is the accepted cost of
back-compat with the `{"data":…}` convention; it is called out in the function-authoring docs.

## Temporary workarounds

None. (The malformed-body crash is fixed directly as a shim bug fix, committed alongside this — not an ADR.)

## Contracts

**Normalization helper** (new, `internal/dataplane`):

```go
// normalizeInvokeBody returns the CloudEvents v1.0 envelope bytes to forward to the shim for an
// EXTERNAL invoke of ns/name, given the raw request body. It wraps plain data, passes an existing
// envelope through, and returns raw (ok=false) when body is not valid JSON so the caller forwards it
// unchanged for the shim to reject. newID returns the event id (crypto/rand hex); injected for tests.
func normalizeInvokeBody(ns v1.NamespaceName, name v1.ObjectName, body []byte, newID func() string) (envelope []byte, ok bool)
```

- `len(body)==0` → `ok=true`, envelope with `data:null`.
- `body` is valid JSON that is an object containing `"specversion"` or `"data"` → `ok=true`, envelope
  = `body` unchanged.
- `body` is any other valid JSON value → `ok=true`, envelope wrapping it as `data`.
- `body` is not valid JSON → `ok=false` (forward `body` unchanged).

**Wiring** (`serveFunction`, external branch only): normalization **buffers** the body (today it is
streamed), so the read must be **independently bounded** — the optional edge body cap
([internal/edge/limit](../../internal/edge/limit/limit.go) `MaxBodyBytes`) may be `0` (off). Read via
`http.MaxBytesReader(w, r.Body, maxNormalizeBytes)` with a fixed ceiling `maxNormalizeBytes` (const, `1 <<
20` = 1 MiB — invoke `data` is small JSON); a body over it → the `MaxBytesReader` error → `413
fault.WriteProblem` (no wake, no partial forward). On a successful read, call `normalizeInvokeBody`; if
`ok`, replace `out.Body`/`out.ContentLength`/`Content-Length` with the envelope; else forward the original
(read) bytes. `internal` requests skip normalization entirely (streamed as today — the buffer bound applies
only to external invokes). The `GET`/no-body and other methods keep current behavior (an empty body
normalizes to `data:null`).

**Dependencies & I/O**

| Consumes | Produces |
|---|---|
| the external HTTP request body at `/function/<name>` (path form or Route hit) | a v1.0 CloudEvent envelope forwarded to the shim upstream via the activator |
| `crypto/rand` (event id), `encoding/json` (stdlib) | unchanged response passthrough |

No CRD, config key, or new dependency.

## Implementation plan

1. `internal/dataplane/normalize.go` — `normalizeInvokeBody` (+ a small `looksLikeEnvelope(map)` helper and
   `newInvokeID` using `crypto/rand`). Imports at file top.
2. `internal/dataplane/dataplane.go` — in `serveFunction`, for `!internal`: read the body via
   `http.MaxBytesReader(w, r.Body, maxNormalizeBytes)` (const `1<<20`), normalize, set the cloned request's
   body + `ContentLength` + `Content-Length` header. A read error from the cap → `413`. Internal branch
   unchanged (streamed).
3. **Test plan** (`internal/dataplane/normalize_test.go`, hermetic — no node/docker):
   - `TestScenarioNoInputEmptyBodyInvoke` — empty body → envelope `data:null` (unmarshal, assert).
   - `TestScenarioPlainJSONBodyWrapped` — `{"x":1}` → `data == {"x":1}`.
   - `TestScenarioCloudEventPassthrough` — `{"data":{"a":1}}` and a `specversion`-carrying body → unchanged.
   - `TestScenarioSensorAndGatewayAgree` — a wrapped invoke and an `eventing.NewNamedEvent`-style envelope
     both yield `specversion=="1.0"` and equal `data` for the same payload.
   - `TestScenarioInternalInvokeUntouched` — an end-to-end `serveFunction` (httptest, fake activator
     capturing the forwarded body) with `internal==true` forwards the body byte-for-byte; `internal==false`
     wraps. Non-object JSON (`42`, `[1]`, `null`, `"s"`) each wrap. Non-JSON (`abc`) forwards unchanged.
4. Update `examples/python/{hello-world,kv-counter,catalog-quack,releve-lakehouse}` READMEs (and any
   `.md`/script invoke snippets) to the plain-body / empty-body form; keep one note that `{"data":…}` still
   works. Keep block-style YAML; no absolute paths / username.
5. Verify: `go build ./...` · `go tool golangci-lint run ./...` · `go test ./internal/dataplane/...` ·
   `go mod verify`. (`just ci` git-diff gate fails on uncommitted tracked files — expected until commit.)

**Definition of done**: the five scenarios have named passing tests; `serveFunction` normalizes external
invokes and leaves internal ones byte-for-byte; back-compat (`{"data":…}`) preserved; example READMEs updated;
four sub-checks green.

## Review checklist

- [ ] `normalizeInvokeBody` implements exactly the four-branch rule; `ok=false` only for invalid JSON.
- [ ] Passthrough triggers on a top-level `specversion` **or** `data` key; wrap otherwise.
- [ ] Only `!internal` invokes are normalized; internal traffic is forwarded byte-for-byte (asserted).
- [ ] Empty/absent body → `data:null` (no crash, no `{}`).
- [ ] The normalization read is bounded (`maxNormalizeBytes`, independent of the optional edge cap); an
      over-limit external body → `413`, no wake. Response passthrough is unchanged.
- [ ] Every scenario has a named, un-skipped, passing test; hermetic (no node/docker).
- [ ] Example READMEs use the plain-body form; `{"data":…}` back-compat noted.
- [ ] No new dependency; imports at file top; no identity/path leak; block-style YAML.

## Consequences

- **Positive**: direct HTTP invoke is zero-ceremony — plain data or an empty body; consistent with the
  sensor/workflow entry paths; the shim always receives a well-formed envelope for external JSON invokes;
  no new dependency.
- **Negative / risks**: the wrap-vs-passthrough rule has one documented ambiguity (a raw object whose own
  top-level key is `data`); mitigated by the explicit-envelope escape hatch and an authoring-docs note.
  Reading the body at the edge adds a bounded buffer on the external invoke path (already size-limited).
- **Accepted**: passthrough keeps `{"data":…}` working, so no caller breaks; internal producers are out of
  scope and untouched.

## Open questions

- **Binary / non-JSON `datacontenttype`** invoke payloads (e.g. a raw `application/octet-stream` body) — a
  follow-on ADR; V1 invoke `data` is JSON. Noted, not decided here.

## References

- [ADR-0033](0033-data-plane-serving-and-trigger-wake.md), [ADR-0038](0038-event-data-contract-jtd.md),
  [ADR-0094](0094-workflow-engine-core.md), [ADR-0109](0109-sensor-event-action-binder.md),
  [ADR-0123](0123-runtime-compiled-io-validators.md)
- Project #4 card: gateway builds the CloudEvent from the invoke body (PVTI_lAHOBMTWh84BbERrzgysne0)
- CloudEvents v1.0 JSON event format (specversion/type/source/id/data)
