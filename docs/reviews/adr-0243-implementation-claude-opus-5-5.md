## Verdict: changes requested — 0 blockers, 2 majors, 4 minors  (ADR-0243 implementation, model: claude-opus-5-5)

> Erratum (2026-10-18): this round also missed a model-attributed Blocker. `funcdctl` did not build under `-tags dev`
> (`cmd/funcdctl/dev.go` used `fs` without importing it; `dev_reload.go` kept an unused import), as these checks left
> the dev tag out. The builder found and fixed it; the ledger row counts it.

Reviewed funcd branch `feat/adr-0243-http-functions` (b8fa3d04 on `origin/main` 604223f1) and funcd-typescript branch
`feat/http-functions` (719e290 on v0.13.0, d08c245), with ADR-0243 as of 604223f1 as the bar. funcd's tests ran on the
unreleased shim through a local, gitignored `go.work`; `go.mod` keeps funcd-typescript v0.13.0 until the shim is
released, as the implementation plan intends. The probes below ran as overlay tests or on scratch files; the tree was
not changed.

### 🟡 Major 1 — `ctx.fetch` breaks on a path that needs percent-encoding  ·  attribution: model

`serveFetch` hands the invoker the decoded wildcard (`internal/workernode/local/local.go:281`), and
`proxyInvoker.Fetch` splices it into a request line with `httptest.NewRequest`
(`internal/workernode/local/invoker.go:76`). A probe through the real handler and invoker, served by `net/http` as the
local API is:

| request to the local API | result |
|---|---|
| `/fetch/devices/devices/truck-7` | 200; the target sees `/devices/truck-7` |
| `/fetch/devices/devices/My%20Truck` | the handler panics (`invalid NewRequest arguments; malformed HTTP version`); `net/http` logs the stack and drops the connection, so the caller gets EOF |
| `/fetch/devices/files/100%25` | the same panic (`invalid URL escape "%"`) |
| `/fetch/devices/files/a%3Fb?x=1` | 200, but the target sees `/files/a` with query `x=1`: the encoded `?` cut the path |

The shim sends a space as `%20` (`new URL`), so `ctx.fetch('devices', '/devices/My Truck')` fails, and a `DELETE` of a
name that holds an encoded `?` reaches another resource. The edge serves the same paths correctly. Decision 6 says the
request carries the caller's path. Fix (builder): carry the path in its escaped form into a `url.URL` rather than
through a request line, and test these paths through the real invoker.

### 🟡 Major 2 — `funcdctl push` cannot push an `http` Function as a bundle directory  ·  attribution: model

`pushFromManifest` skips the bundle contract for an `http` Function (`cmd/funcdctl/manifest.go:83`), but
`artifact.PushBundle` still runs `VerifyBundleContract` (`internal/artifact/bundle.go:225`), which refuses a bundle
without `__funcd_contract.json`. A scratch bundle (`funcdctl.yaml` with `protocol: http` and a `handler.mjs`) gives
exit 1, `bundle is missing __funcd_contract.json (every function must declare an I/O contract, ADR-0090)`; the same
handler pushed as a single file gives exit 0. A bundle that still holds the contract of an earlier `cloudevent` push
would be pushed with it, and the Function would stop at boot as `ShapeInvalid` (the `with-contract` case of
`TestScenarioHTTPMismatch`); `funcdctl dev` removes such a file (`cmd/funcdctl/dev.go:1615`), push does not. Decision 7
says push generates no contract for an `http` Function. No Go test covers push or dev with an `http` manifest; the lane
covers only the single-file push. Fix (builder): push an `http` bundle without a contract layer, refuse or ignore a
stale contract file, and add the push and dev cases to the existing `cmd/funcdctl` tests.

### Minor

- **m1 · `model` · a JSON body that is not UTF-8 reaches the app as the envelope.** The edge wraps any body that Go's
  `json.Valid` accepts (`internal/dataplane/normalize.go:85`), and `json.Valid` does not check UTF-8; the shim decodes
  with `TextDecoder('utf-8', { fatal: true })` (funcd-typescript `shim/src/httpapp.ts:29`) and passes the bytes on as
  raw when that throws. Probe: `{"name":"Caf\xe9"}` (Latin-1) through `HTTPEnvelope` and the real `appRequest` gives
  the app the whole envelope. Review-checklist item 2 asks for a body rebuilt without ambiguity. Fix: the edge treats a
  body as JSON only when it is also valid UTF-8, so such a body passes raw.
- **m2 · `model` · `Content-Length` is dropped, not recomputed.** `appRequest` removes it and sets none
  (funcd-typescript `shim/src/httpapp.ts:14`), for a re-encoded body and for a raw one (probe: null in both cases).
  Decision 4 says recomputed, and Node's `Request` keeps the header when it is set. The test asserts only that the
  envelope's length is not kept (`shim/test/http.test.ts:81`).
- **m3 · `model` · no test pins the nested call cap on `ctx.fetch`.** `nestedCapInvoker.Fetch` takes the cap
  (`internal/workernode/local/nestedcap.go:52`) and production wraps the invoker with it (`pkg/funcd/funcd.go:775`),
  but with `Fetch` changed to skip the cap, every test of `internal/workernode/local` still passes. Review-checklist
  item 5 names the check.
- **m4 · `model` · the `Pooling` doc comment moved onto `FunctionProtocol`.** The new type sits between the comment and
  `type Pooling struct` (`api/types/v1alpha1/function.go:180-206`): `go doc` shows `Pooling` with no comment and
  `FunctionProtocol` documented as "Pooling is the per-function pooling opt-in …".

### ✅ Verified correct (keep it)

- **Checks**, each run once through `scripts/agent/d`: `go build ./...` exit 0; `go tool golangci-lint run ./...` and
  `--build-tags e2e ./pkg/funcd/...` 0 issues; `go test -race -count=1` on the ten touched package trees exit 0;
  `go test -tags e2e -race -count=1 -run TestScenarioHTTP ./pkg/funcd/` exit 0; the lane guard tests exit 0;
  `go mod verify` all modules verified. funcd-typescript `just ci` exit 0, the tree clean after the build.
- **Lane**: `just lima-example http-api` on containerd, run by the coordinating session: Venom PASS; the export
  streamed (two lines over 157 ms after the first).
- **Scenarios**: one e2e test per scenario, named after it (`TestScenarioHTTPRest` … `TestScenarioHTTPLimits`), each
  with `t.Parallel()`, each under 1.1 s. Revert check: with the edge encoding an `http` Function's body as ADR-0134
  does, `TestScenarioHTTPFunctionWrapsEveryJSONBody` and `TestScenarioHTTPRest` fail (the app created `ghost`).
- **Decision 1**: `validateProtocol` refuses an unknown protocol, pooling and a non-Node runtime
  (`TestFunctionProtocolValidateMatrix`); an App's inline Function gets the same rules, since `App.Validate` validates
  it as a `Function`; `isNodeFamily` became `RuntimeName.IsNode`; `FUNCD_PROTOCOL` sits beside `FUNCD_HANDLER`.
- **Decisions 2 and 4**: each mismatch and a delivered contract exit 3 before listening (shim test and e2e); the shim's
  `/health/*` shadows the app; the app's body streams through a pull stream and the span ends with it.
- **Decision 3**: the edge names the prefix it stripped in `X-Forwarded-Prefix`, replacing the client's, and wraps
  every JSON body of an `http` Function, `data` and `specversion` keys included; an event Function keeps ADR-0134.
- **Decision 5, builder choices 2 and 3**: the Sensor and hook invoker and the dispatcher refuse before any request
  (unit tests count no request and no wake). The Sensor keeps its subscriptions, so each matched event fails and is
  dead-lettered as the scenario requires; `MapFunction` re-runs it when its Function changes. A step ref to an `http`
  Function is untyped, so the run reaches the dispatcher instead of waiting for a contract, and its refusal is
  `Permanent`. Sound; keep.
- **Decision 6, builder choice 1**: link, PDP, nested-cap and protocol checks; `X-Funcd-Member` stays on the caller's
  side; the reply returns whole, at most 1 MiB. Resolving with any reply and rejecting on a refusal follows the Web
  `fetch` model; the local API sets the marker only on a forwarded reply and the shim strips it, so a target can
  neither forge a refusal nor hide a reply. Sound; keep.
- **Builder choices 4 and 5**: each addition to the example backs a scenario (purge for http-fetch, firmware for
  http-limits, `?name=` and the request-id middleware for http-route); running the e2e tests in `default` changes
  nothing the ADR decides, since no code path here depends on the namespace. Acceptable.
- **Tracking**: the ADR's diff is its status line only (`Accepted → Reviewing`); F156 is at `reviewing`; the
  back-links and the blueprint (from #1046) and the shim README are in place.

### Definition of Done

17 / 19 hold (the ADR's Review checklist, 6; its done-when, 6; the generic items, 7). Misses: checklist item 2 (m1)
and the generic "real behaviour" item (Majors 1 and 2), both `model`.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0243 (implementation) → changes-requested, 0/2/4, 6 model-attributed, DoD 17/19. See
docs/reviews/model-scorecard.md.

### Recommendation

Back to `adr-impl` for Majors 1 and 2, each with a test; m1–m4 are cheap to take in the same pass. Nothing advances:
ADR-0243 stays `Reviewing` and F156 `reviewing`. The PR closes #1045 once a re-review passes.
