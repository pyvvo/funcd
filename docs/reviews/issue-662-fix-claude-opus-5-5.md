# Fix review — issue #662 (a catalog consumer starts on the pre-restart proxy URL)

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #662 fix, model: claude-opus-5-5)

Change: branch `fix/662-catalog-live-proxy`, commit a4dbd3b2 `fix(catalog): start a catalog consumer only on its live proxy URL`, five files (+95/-0): `gateway.Manager.URL`, a `function.CatalogProxies` interface and `Deps.CatalogProxies` field, the gate in `resolveCatalogEnv` (`internal/function/catalog.go`), the wiring in `pkg/funcd/funcd.go`, and the regression test `TestIssue662_ConsumerWaitsForTheLiveCatalogProxy`.

### 🟡 Minor 1 — the production wiring is not covered by a test  ·  attribution: model
Mutant m3 removes `CatalogProxies: catalogMgr` from `buildControlPlane` (`pkg/funcd/funcd.go`). The whole `pkg/funcd` package, unit and `-tags e2e`, still passes (270 s). Because `nil` means "no liveness check", the daemon can silently lose the fix and no test notices. The regression test proves the gate with the field set by hand; nothing asserts that the daemon sets it. A small assertion in `pkg/funcd` (the Function reconciler receives the same Manager the CatalogService reconciler uses) would close the gap.

### 🟡 Minor 2 — the wait is not named in the condition message or the function's doc comment  ·  attribution: model
When the gate fires, the Function shows `CatalogNotReady` with the message "a bound CatalogService is not Ready yet (no endpoint or token); waiting" (`internal/function/function.go`), while the CatalogService itself reads Ready. The `resolveCatalogEnv` doc comment still describes readiness as the catalog's Phase only. The inline comment at the new check explains the case, and the window is short (until the catalog's first pass after a restart), so this is cosmetic.

### ✅ Verified correct (keep it)
- **Revert check**: on plain `origin/main` the test does not compile, because it sets the new `Deps.CatalogProxies` field and calls `Manager.URL`. Overlaying only `internal/function/catalog.go` (the `origin/main` version, without the gate) keeps the new API, so the test compiles and fails at its first assertion: `Should be zero, but was 1` — "no worker starts with the URL of a proxy this daemon does not run". The gate is the whole behavior change; the field and `URL` only feed it. So this overlay reproduces the pre-fix behavior for the reported reason, and it is an adequate revert check.
- **Passes with the fix** under `-race`, un-skipped, and the whole `internal/function` package passes under `-race`.
- **Cause, not symptom**: the issue's cause is that the consumer trusts a persisted Ready `status.endpoint` that can name a proxy the restarted daemon does not run (the proxy binds port 0). The gate checks the stored endpoint against the URL of the proxy that is running now, so a stale URL is never injected. There is no added timeout, retry or swallowed error.
- **A Ready catalog cannot hold a consumer forever**: a provider reports Ready only with a non-empty Address (`internal/provider/runtime.go` requires the instance IP before `Ready = true`; the dev engine always sets its address). With a proxy wired, the CatalogService reconciler calls `Ensure` before it writes Ready, and publishes the proxy URL as `status.endpoint`. `pkg/funcd` passes the same Manager to both reconcilers. If `Ensure` fails, the pass returns before the status write, and the consumer waits for the retry, which is the fail-closed behavior.
- **No race between the two reads**: the consumer reads the store, then calls `URL`. In every interleaving with the catalog's `Ensure` and status write, it either sees an equal live URL or requeues (2 s).
- **Suspend and retarget keep working (#59, #372)**: `Suspend` keeps the listener, so `URL` still reports the same address; `holdNotReady` clears `status.endpoint`, so the existing Phase/endpoint gate already stops consumers. On recovery, `Ensure` retargets the same listener and the URL is unchanged, so the stored endpoint equals the live URL again. Delete calls `Remove`, and the existing NotFound gate holds the consumer.
- **Mutants** (overlay, restored): m1 (the URL comparison dropped, only liveness kept) is killed at "the stored endpoint is not the live proxy's URL yet"; m2 (the `catalogProxies` assignment dropped in `NewReconciler`) is killed at the first assertion; m3 survives (Minor 1).
- **Scope**: every hunk serves the issue; no test was changed or deleted.
- **Reuse**: `URL` reuses `managerKey` and `publishURL` under the existing mutex; the test reuses `newShimHarness` (its `Deps` option), `h.create`, `h.reconcile`, `h.condition` and `h.rt.specOf`, and a real `cataloggw.Manager`. No new dependency.
- **Conventions**: a consumer-side interface with typed `v1.NamespaceName`/`v1.ObjectName`, no `any`, short comments that cite ADR-0137 and #662, top-level imports.
- **ADRs**: ADR-0091 ("fail-closed on readiness — inject only a real endpoint", reason `CatalogNotReady`) and ADR-0137 (the consumer injects the proxy URL) both hold; the fix makes the readiness check stricter. No ADR file is touched.
- **Checks**: `go test -race` on `internal/function`, `internal/catalog/...`, `internal/services/catalog`, `pkg/funcd` all pass; `-tags e2e -run Catalog ./pkg/funcd/` passes (2 scenarios); `go vet` clean on the host and with `GOOS=linux`; `golangci-lint` 0 issues on the host and with `GOOS=linux` (as the gate runs it); `GOOS=linux go build ./...` passes.
- **Commit shape**: `fix(catalog):`, `Fixes #662`, attribution trailer, one issue per commit.

Not run: the issue's own reproduction, the containerd duckdb lane testcase `owner-kind-across-daemon-restart`. It needs a Lima VM, and it failed in only 1 of 7 runs, so one green run would prove little.

### Definition of Done
11 items apply; 10 hold. Item 4 is partial: reverting or mutating the gate fails the test, but removing the daemon wiring does not (Minor 1).

### Model scorecard
claude-opus-5-5: a small, correct root-cause fix with a precise regression test that walks both stale states (no proxy, then a proxy on a new port) before the endpoint matches. It left the daemon wiring untested and did not update the user-visible wait message.

### Recommendation
Pass. Merge as is. Optionally, add a `pkg/funcd` assertion for the wiring and name the proxy wait in the `CatalogNotReady` message.
