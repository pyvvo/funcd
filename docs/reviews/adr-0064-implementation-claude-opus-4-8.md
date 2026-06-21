# ADR-0064 Implementation Review — Declarative synchronous fn-to-fn RPC (links)

**Verdict**: **pass** — `spec.links` + the two link admissions (consuming ADR-0063) + the worker-node local API (HTTP-over-UDS) + the in-process invoker + `context.invoke` in both shims are all implemented per Contracts, with matrix/table tests where the contract is a closed set and the security model (connection-scoped identity, link-as-grant default-deny, status propagation) proven by unit/contract tests. The four sub-checks are green module-wide. The two **cross-process e2e** scenarios and the **live per-sandbox UDS provisioning** are deferred to the Linux integration lane per the ADR's test-sequencing (recorded below). No Blockers, no Majors, no Minors.
**Reviewed**: ADR-0064 (status `Reviewing`) · FEAT-0001/F33 · ADR-0063 (consumed) · ADR-0033/0016/0058/0018/0011/0046 · ADR-0002 conventions
**Producing model**: claude-opus-4-8

## Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...` | **exit 0** |
| tests (full) | `go test ./...` | **ALL GREEN** (incl. `TestSpecGeneratedFromGo` — OpenAPI matches the Go types) |
| race | `go test -race ./internal/controlplane/admission/ ./internal/workernode/...` | **ok** |
| lint | `go tool golangci-lint run ./...` | **0 issues** |
| mod | `go mod verify` | **all modules verified** |
| OpenAPI | `just generate` → diff | only the `FunctionLink`/`links` schema (26 lines), regenerated |
| Node shim | `just build-shim` (typecheck + self-test + esbuild) | **pass** — shim.mjs/pool.mjs rebuilt with `invoke` |
| Python shim | `just check-shim-python` (ruff + mypy + pytest) | **pass** — 39 passed, 0 type issues |

## Scenario → test traceability

| ADR Scenario | Test | Form |
|---|---|---|
| unlinked-alias-denied | `TestResolver` (no link → `fault.Forbidden`) | scenario |
| link-target-must-exist | `TestLinkValidityAdmission` (target missing) | **table** |
| link-cycle-forbidden | `TestLinkValidityAdmission` (self-link + two-node cycle) | **table** |
| linked-input-contract-validated | `TestInvokerPropagates` + `TestScenarioInvokeErrorTaxonomy` (422 propagated) | **matrix** |
| delete-protected-while-linked | `TestLinkDeletionProtectionAdmission` (`fault.Conflict`, reads Old) | scenario |
| caller-identity-from-connection | `TestScenarioCallerIdentityFromConnection` (body cannot name caller) | scenario |
| (structural) link alias/uniqueness/target | `TestFunctionLinkValidateMatrix` | **matrix** |
| (taxonomy) 403/404/422/500/503 | `TestScenarioInvokeErrorTaxonomy` | **matrix** |
| handler-invokes-linked-function | **deferred** (Linux integration lane) | e2e |
| cold-target-woken | **deferred** (Linux integration lane) | e2e |

## Review checklist (ADR-0064) — all hold

- ✅ `FunctionSpec.Links`/`FunctionLink` added with the documented tags; OpenAPI regenerated (`TestSpecGeneratedFromGo` green); `Function.Validate()` enforces alias format + uniqueness + target format (`TestFunctionLinkValidateMatrix`, 9 cases).
- ✅ link-validity rejects a missing target and any cycle incl. self-link (coloured DFS); deletion-protection rejects deleting a linked-to Function (`fault.Conflict`, reads `Old`). Both registered on the ADR-0063 pipeline via `controlplane.Deps.Admissions` in `pkg/funcd` (a `storeReader` adapter keeps the admission package leaf).
- ✅ The local API attributes the caller from the fixed sandbox `Ref` — a request body cannot name a different caller (`TestScenarioCallerIdentityFromConnection`).
- ✅ `invoke` is default-deny: no matching link ⇒ `fault.Forbidden` (`TestResolver`).
- ✅ Input/output 422/500 are the target shim's, **propagated** verbatim by the in-process `Invoke` (`*UpstreamError`); daemon faults are 403/404/503 (`TestScenarioInvokeErrorTaxonomy` matrix). The daemon does **not** re-validate (the judge's Major shape).
- ✅ `context.invoke` exists in both shims (Node `src/invoke.ts` wired into shim.ts + pool.ts contexts; Python `funcd_shim/invoke.py` wired into `_Context` + `_Ctx`; embedded via `embed.go`), dials the UDS at `FUNCD_INVOKE_SOCKET`. Same-namespace only.
- ✅ Zero new deps (stdlib `net`/`net/http` UDS; Python stdlib `http.client`); `just ci`-equivalent checks green.

## ✅ Verified correct — keep it

- **The judge's Major shape is faithfully implemented** — `proxyInvoker` forwards a synthesized `POST /function/<target>` through the **in-process** data-plane `Handler.ServeHTTP` and returns an `*UpstreamError{Status,Body}` for any non-2xx, which the local API writes **verbatim** (422/500 from the target's shim). The daemon holds no per-function validator; keep this — a future edit must not "add validation" in the daemon.
- **Security spine is real, not aspirational** — `NewHandler(caller Ref, …)` bakes the caller at construction; the handler never reads a caller from the request, proven by `TestScenarioCallerIdentityFromConnection`. `invoke` takes an alias only (no SSRF). The link is the grant (`TestResolver` default-deny). Keep.
- **Matrix discipline** — the error taxonomy (`{403,404,422,500,503}`) and the link-structural rules are parametrized tables; the graph/security proofs stay discrete. Right call.
- **Leaf discipline preserved** — the link admissions live in `internal/controlplane/admission` and use a local `StoreReader`; the `store.Store` adaptation is in the wiring layer (`pkg/funcd`), so the admission package still imports neither controlplane nor store.

## Findings

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
None blocking.

## Deferred (recorded, env/e2e-attributed — not model defects)

Per the ADR's test plan ("the cross-process e2e ones DEFER to the Linux integration lane"), two pieces are deferred — both are inherently cross-process (a real sandbox + a bind-mounted UDS) and cannot be unit-tested:

1. **Live per-sandbox UDS provisioning** — the reconciler calling `local.Serve(ctx, socketPath, NewHandler(ref, resolver, invoker))` per sandbox and the process/containerd drivers bind-mounting + exporting `FUNCD_INVOKE_SOCKET`. All the *components* it wires (`Serve`, `NewResolver`, `NewInvoker`, `NewHandler`) are implemented and unit-tested; the per-sandbox lifecycle wiring belongs to the Linux integration lane.
2. **The cross-process e2e scenarios** `handler-invokes-linked-function` and `cold-target-woken` — the full brokered round-trip + activator wake, which need a live shim + sandbox + UDS.

This matches how the project sequences Linux-only/e2e work; the unit/contract-testable surface (admissions, local API, resolver, invoker, both shim SDKs, spec.links) is complete and green.

## Recommendation

**pass** → stamp ADR-0064 `Reviewing → Implemented` and feat row F33 → `implemented`; move the Project #4 RPC item `In Progress → Done`. The deferred live-provisioning + e2e is the ADR-sanctioned integration-lane follow-through, not an implementation gap.
