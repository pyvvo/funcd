## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0243 implementation, round 2, model: claude-opus-5-5)

Re-review of the fixes for [round 1](adr-0243-implementation-claude-opus-5-5.md): funcd `e220d7a1..2fffae60` on branch
`feat/adr-0243-http-functions`, and funcd-typescript `719e290..7e22d9a` on branch `feat/http-functions` (PR #71). Every
round-1 finding is fixed, each with a test that fails when its fix is reverted (by `-overlay`, against the round-1 file
or a mutant; the tree was not changed).

Round 1 missed a compile failure: under `-tags dev`, `cmd/funcdctl/dev.go` used `fs` without importing `io/fs`, and
`cmd/funcdctl/dev_reload.go` kept an unused `artifact` import (the round-1 files fail `go build -tags dev`). Its checks
left out the dev tag, which `just lint` and `just test` run on the `_dev-packages`. It would have been a Blocker
(`model`). The builder found and fixed it; this round runs the dev-tag build, vet, lint and test.

### ✅ Verified correct (keep it)

- **Major 1, `ctx.fetch` paths**: `/fetch/` requests go around ServeMux, and `serveFetch` splits the alias from the
  escaped path and forwards the path still escaped (`internal/workernode/local/local.go`). `TestFetchKeepsAnEscapedPath`
  (real handler, invoker and `net/http`) fails on the round-1 handler. Round-1 probe rerun: `%20`, `%25`, `%3F?x=1`
  and `caf%C3%A9` reach the data plane as sent; a `..` segment stays inside the target, since the data plane takes the
  name from the first segment and does no cleaning; `/fetch/devices` and `/fetch//x` answer 404; `%2F` becomes a
  separator, the risk the ADR accepts, as at the edge.
- **Major 2, `funcdctl push` of a bundle**: an `http` bundle removes a contract file left by an earlier push and goes
  through `artifact.PushBundleNoContract`, which pushes no contract layer and refuses a bundle that holds one.
  `TestScenarioPushHTTPFunction` fails on the round-1 `manifest.go`; `TestPushBundleNoContract` also checks that
  nothing is delivered at materialization. Probe rerun through the built CLI: exit 0 with and without a stale
  contract file, which is removed. `TestDevServesAnHTTPFunction` fails when dev drops `protocol`.
- **m1**: `HTTPEnvelope` also requires `utf8.Valid`; the Latin-1 case in `TestHTTPFunctionNonJSONBodyIsRaw` fails on
  the round-1 `normalize.go`. Probe rerun: the body reaches the app byte for byte, 15 bytes, `Content-Length: 15`.
- **m2**: `appRequest` sets `Content-Length` to the rebuilt body's length (funcd-typescript `shim/src/httpapp.ts`), and
  the tests assert the value for a re-encoded and a raw body. Probe rerun: 15, 18 and 21 for the three bodies.
- **m3**: `TestNestedCapCountsFetch` fails in 2.0 s with the cap skipped ("a second fetch to H reached H past its cap").
- **m4**: the `Pooling` doc comment is back on `type Pooling struct`.
- **Checks**, each run once through `scripts/agent/d`, all exit 0: `go build ./...`; `go build -tags dev` and
  `go vet -tags dev` of `./cmd/funcdctl/`; `go tool golangci-lint run ./...`, with `--build-tags e2e ./pkg/funcd/...`
  and with `--build-tags dev` on the `_dev-packages`, 0 issues each; `go test -race -count=1` on the touched package
  trees, `internal/artifact` included; `go test -tags dev -count=1 ./cmd/funcdctl/`;
  `go test -tags e2e -race -count=1 -run TestScenarioHTTP ./pkg/funcd/`, eight scenarios in parallel, the slowest
  1.11 s; the lane guard tests; `go mod verify`. funcd-typescript `just ci` exit 0 (172 shim tests), the tree clean
  after the build.
- **Lane**: `just lima-example http-api` passed on the round-1 code (Venom PASS). The `Content-Length` fix is on the
  path every request with a body takes; the e2e scenarios, which run the same shim build, pass with it.
- Everything round 1 verified still holds; the fixes changed nothing else.

### Definition of Done

19 / 19 hold (the ADR's Review checklist, 6; its done-when, 6; the generic items, 7).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0243 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 19/19. See
docs/reviews/model-scorecard.md.

### Recommendation

Stamped: ADR-0243 `Reviewing → Implemented`, F156 `reviewing → implemented`. Before the funcd PR: release the shim
(funcd-typescript PR #71), `go get` that tag here in place of the local `go.work`, and rerun the lane and the gate on
it. The PR closes #1045 (`Fixes #1045`).
