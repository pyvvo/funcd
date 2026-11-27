## Verdict: pass — 0 blockers, 0 majors, 1 minor (ADR-0360 implementation, model: claude-opus-5-5)

This report reviews ADR-0360's implementation in two repositories: pyvvo/funcd-typescript branch `feat/websocket`
(PR pyvvo/funcd-typescript#126; commits 949d407, 9e59507 and db7a3b9 on its main) and pyvvo/funcd commit 17acce66 on
`feat/http-streams`. The reviewer did not write the work, and every check below was run, not read, except the http-api
lane case, which was read only (a Lima VM was already running on the host). pkg/funcd's tests ran against the local
funcd-typescript checkout through an uncommitted, ignored `go.work`, since go.mod still pins the shim's v0.36.0 until
the shim's release.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

- **The hook's shape differs from the Contracts in two small ways** · attribution: model.
  - The ADR's Contracts declare `TakeWebSocket` in `shim/src/types.ts`, beside `WS_UPGRADE`; the work declares it in
    `shim/src/ws.ts:10`, and `shim/src/shim.ts` imports it from there.
  - The Contracts say the hook "rejects otherwise", and Decision 1 says the helper then fails and "the app answers its
    error". `takeWebSocket` (`shim/src/shim.ts`, the function under `shimCall`) instead resolves a bare 500: its own
    branch with no shim context, and `@hono/node-server`'s `upgradeWebSocket`, which answers
    `new Response(null, { status: 500 })` when the call did not come through the server's upgrade listener
    (`node_modules/@hono/node-server/dist/index.mjs:1282`). So for that case the app's `onError` does not run and the
    client gets an empty 500. The case needs `Upgrade: websocket` without `Connection: Upgrade`, which Node then serves
    as a plain request; no test covers it.
  - Fix: move the type to `types.ts`, and have the hook reject (throw) when the answer is not a taken upgrade, or
    record in the ADR's supersession path that a 500 is the answer. Neither changes a scenario.

### Routed (not this work's)

- **The shim's release, then `go get github.com/pyvvo/funcd-typescript@<tag>`** · elsewhere: the integration step of
  the batch PR. go.mod pins v0.36.0, while the commit message of 17acce66 says "this commit pins its release". Until
  the bump lands, CI's pkg/funcd e2e tests and the http-api lane run the old shim and fail the five scenarios and the
  `101` case.
- **The http-api lane's `101` case on containerd** · elsewhere: the integration step, after the bump. It was read,
  not run: `e2e/http-api.venom.yml` sends the handshake headers through the Route's `/api/relay` with
  `curl -sS -i -N --max-time 2 … || true` and asserts `101 Switching Protocols` in its output, as the Implementation
  plan says. The `Implemented` stamp below rests on that lane passing in the PR's gate or CI.

### ✅ Verified correct (keep it)

- **funcd-typescript `just ci`** exit 0: Biome, typecheck, the shim's 205 `node --test` cases with the eight new ones
  in `shim/test/ws.test.ts` (ws-relay, ws-not-taken, ws-message-cap with code 1009, onOpen before onMessage with the
  upgrade's principal and `remainingMs()` null, the span at the handshake and at a refusal, the helper's next-handler
  and no-hook paths, the entrypoint under `FUNCD_PROTOCOL=http`, and the built `shim.mjs`), the example's new test, and
  the final check that the build outputs match the committed ones.
- **funcd e2e**: `go test -tags e2e -count=1 -race -run 'TestScenarioWS|TestHTTPFunctionStream|TestScenarioHTTP'
  ./pkg/funcd/` exit 0; one test per scenario, named after it, each with `t.Parallel()` and the ADR's short pacing
  (`spec.timeout` 1 s, `idleTimeout` 1 s with `ReclaimInterval` 200 ms, `switchGrace` 2 s). `TestScenarioWSAtSwitch`
  bounds the old socket's end between `drainGrace` and `drainGrace` + 3 s from `status.drainingSince`.
- **Mutants, both killed**: raising `maxMessageBytes` to 4 MiB in the bundled `shim.mjs` failed
  `TestScenarioWSMessageCap` (no EOF), which also shows the e2e tests run the local shim; dropping the
  `invStore.run` wrapping in `inUpgradeCall` failed the shim's context test. Both files were restored.
- **Reuse**: the shim writes no handshake. `serve()` takes a `ws` `WebSocketServer` with `noServer: true` and the named
  `maxPayload`, only under `protocol: http`; the hook calls `@hono/node-server`'s `upgradeWebSocket` with the shim's
  own context, and a non-websocket upgrade gets that package's `400`. `isWebSocketUpgrade` only picks the span's path.
  The e2e change extracts `waitSwitch` from the SSE test instead of copying it.
- **Package**: `./ws` is a new entry built by `tsconfig.lib.json`; `hono` is an optional peer, with `@types/ws` (the
  guide says so); `types.ts`, the main entry, imports no `hono`; `bufferutil` and `utf-8-validate` stay external, and
  the banner's `createRequire` lets the bundled CommonJS `ws` load (tested on the built file).
- **No funcd Go code outside tests changed.** The guide's "Take WebSocket connections" names the handshake-only
  timeout, the 1 MiB cap, the drain's cut, the reconnect and the 5-minute idle close, and includes the example's
  `#region relay`. The Calls row and the widened Functions row cite ADR-0360 and the tests.
- **Other checks**: `go vet -tags e2e ./pkg/funcd/` exit 0; `go run ./scripts/behavior -check` exit 0;
  `go test -count=1 -run 'TestDevelopPagesFollowTheTemplate|Lane' ./pkg/funcd/ ./scripts/` exit 0.
- **Tracking**: ADR-0360 was at `Reviewing` with only its status line changed by 17acce66, and F277 at `reviewing`.

### Definition of Done

5 / 6 Review-checklist items hold. The fifth holds for the e2e tests; its lane half was not run here (env, routed
above). The Implementation plan's "Done when" holds except that lane run.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0360 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 5/6. See
docs/reviews/model-scorecard.md.

### Recommendation

Stamped `Implemented`, with F277 `implemented`. The batch PR carries `Fixes #1589`. Before it merges: release the
shim, bump funcd-typescript in go.mod, and let the gate or CI run the pkg/funcd e2e tests and the http-api lane on the
released shim. The Minor can follow in the shim's next change.
