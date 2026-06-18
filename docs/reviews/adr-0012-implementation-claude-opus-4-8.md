# ADR-0012 Implementation Review — Gateway / ingress port (embedded + Lura) (model: claude-opus-4-8)

**Verdict**: **pass** — DoD met, zero Blockers/Majors. The port and both drivers (embedded
reverse-proxy + Lura) realize the Contracts; both pass the identical 5-scenario contract against a real
`httptest` upstream; port stays lura-free. One non-blocking Minor — and it's `adr`-attributed (the ADR
had a latent §3 tension the impl resolved correctly), so it doesn't count against the model.
**Reviewed against**: ADR-0012 Contracts/Scenarios/Review-checklist/DoD · ADR-0002 (§1/§2/§5/§6) ·
blueprint "Ingress / API Gateway" · FEAT-0000/F10.
**Date**: 2026-06-14

## Verification run (evidence)

| Check | Command | Result |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./internal/gateway/...` | exit 0 |
| Lint (full) | `go tool golangci-lint run ./...` | **0 issues** |
| Tests (no cache) | `go test -count=1 -v ./internal/gateway/...` | **both** drivers: 4 contract subtests PASS each |
| Tree vs ADR | `git status --porcelain internal/gateway/` | `gateway.go` + `embedded/` + `lura/` + `gatewaycontract/` + tests — exactly the plan |
| Stubs/skips/panic | `grep "not implemented\|t.Skip\|panic("` | none |
| `any` in sigs | `grep "\bany\b\|interface{}"` | none (only comments; Lura's `proxy.Response.Data` is its own type, not exposed) |
| Port lura-free | `sed import gateway.go` | imports only `context` + `net/http` |
| Identity | identity grep (username / `/Users/` paths / email) | clean |
| Dep | `grep go.mod` | `luraproject/lura/v2 v2.14.1` (Apache-2.0), direct |

## 🔴 Blockers
None.

## 🟡 Major
None.

## Minor
- **The Lura driver realizes the proxy pipeline via `proxy.NewDefaultFactory`/`proxy.Proxy` + an outer
  router, not the literal `mux.DefaultEngine` the Decision §3 named.** Evidence: `lura.go:48-49`
  (`CustomHTTPProxyFactory` + `NewDefaultFactory`), `lura.go:121` (`proxy.Request` invoked directly);
  no `mux.Engine`/`mux.EndpointHandler`. → ***Attribution: `adr`*** — Decision §3 carried a latent
  tension introduced at judge time: it asked for **both** an outer prefix-strip wrapper **and** a
  `mux.Engine` that routes by path, which conflict (the wrapper strips the path *before* an engine
  could route on it). The implementer resolved it the clean way — outer wrapper does routing+strip,
  Lura's **proxy pipeline** (where auth/rate-limit/lb middleware attaches) does the backend call with
  no-op encoding for transparent passthrough. Observable parity with the embedded driver is exact.
  → **Direction (future, not rework)**: when **router-level** Lura middleware is wired, revisit whether
  the mux engine is needed; the proxy-level plugin seam suffices for V1. Recorded; **not** model-scored.

## ✅ Verified correct — keep it
- **Both drivers pass the identical contract** (`gatewaycontract.RunContract`): `route-proxies-to-upstream`,
  `route-strips-prefix`, `unprogrammed-path-404`, `reprogram-replaces-routes` — against a real
  `httptest` upstream that echoes its path, so prefix-stripping is *observably* verified, not asserted.
  This is the substrate-port parity discipline done right.
- **Declarative `ProgramRoutes` replace** is real: `reprogram-replaces-routes` proves a dropped route
  returns 404 and `Routes()` reflects the last desired set; both drivers rebuild + atomically swap the
  table under a mutex, and `Handler()` returns a **stable** `http.HandlerFunc` across re-programs.
- **The Lura `{{.path}}` passthrough** is correct and non-obvious: the impl found that Lura's
  `GeneratePath` uses `{{.key}}` template syntax (not `{key}`) and supplies the stripped remainder as
  `Params["path"]` with no-op encoding — a genuine, working Lura embedding, not a fake.
- **Port stays framework-free** (`gateway.go` imports only `context`+`net/http`) — ADR-0002 §2; Lura
  confined to the `lura` subpackage. Longest-prefix matching (`sort` by prefix length desc) prevents
  `/function/echo` vs `/function/echo-2` collisions.
- Invalid upstream URLs → `fault.Invalid` from `ProgramRoutes`; `Close` leaks no goroutine (no
  background servers — funcd owns the `http.Server`).

## Conventions spot-check
ports/drivers (flat subpackages, one file each) ✓ · `api/fault` ✓ · ctx-first on `ProgramRoutes`/`Routes` ✓ ·
no `any` in signatures ✓ · no globals/`init` ✓ · slog-only (no logger needed; Lura's bridged to discard) ✓ ·
no cgo ✓ · driver constructors return the port interface (ADR-0002 §1 sanctioned exception) ✓.

## DoD
ADR Review-checklist: **7/7** items hold (item 3's literal `mux.Engine` wording is superseded by the
correct resolution of the ADR's own §3 tension — the Lura **proxy pipeline** is genuinely used). All 5
scenarios have named, un-skipped, passing tests against **both** drivers.

## Recommendation
**pass** → stamp ADR-0012 `Reviewing → Implemented`, feat F10 → `implemented`. The lone Minor is an
ADR-side tension the implementation resolved correctly, not a model defect — no rework. A future
superseding note can record the mux-engine-vs-proxy-pipeline choice when router-level Lura middleware
is wired.
