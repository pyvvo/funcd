## Verdict: pass — 0 blockers, 0 majors, 3 minors  (ADR-0238 implementation, model: claude-opus-5-5)

Reviewed against the ADR as amended before merge (`catalog.proxyHost` removed, no alias), on branch
`feat/adr-0238-bridge-address` (implement commit, then the alias removal and the pin of funcd-functions v0.13.0 and
funcd-python v0.10.1).

Two Definition-of-done items are outside this review: the `sql` and `sql-examples` lanes were rerun without the alias
while this review ran (their earlier runs passed and logged `bridge address address=10.63.0.1`), and the PR gate runs
once per PR. The merge needs both green.

### Minor

- **m1 · `model` · a stale comment names the removed field.** `internal/platform/config/config.go:391-392` says the
  change feed port is "on the catalog proxies' host (catalog.proxyHost, else loopback)". It should name the bridge
  address. The example config at `examples/funcdconfig.yaml` was updated correctly.
- **m2 · `model` · the daemon half of `bridge-address-from-subnet` is not named for the scenario and leaves out the
  allow list.** `TestScenarioBridgeAddressFromSubnet` (config) covers the derivation. `cmd/funcd/bridge_test.go`
  `TestDaemonBridgeAddress` checks the value handed to the library, but no test checks the scenario's
  `10.63.0.1:<sql.changeFeedPort>` egress allow-list entry, which is built inline in `buildOptions`
  (`cmd/funcd/main.go:433-436`). The duckdb lane covers the real bind.
- **m3 · `adr` · an Accepted ADR's Decision was edited.** Decision 2, a scenario, the checklist and the Consequences
  changed after acceptance, which the freeze rule forbids. The decider asked for the change before merge and the ADR
  records it under "Amended before merge". This finding is recorded for the process and is not scored.

### ✅ Verified correct (keep it)

- **Checks.** `go test -race -count=1` passed on all six touched packages (config, cmd/funcd, pkg/funcd non-e2e,
  services/catalog, catalog/gateway, services/sql). `go vet` exited 0, `golangci-lint` reported 0 issues on them, and
  `gofmt -l` printed nothing. `just check-hygiene` exited 0 and `GOOS=linux go build ./...` exited 0.
- **Scenarios.** All 7 scenarios have passing tests: `TestScenarioBridgeAddressFromSubnet`, `…FollowsSubnet`,
  `…Explicit`, `…OutsideSubnetRefused` (out of subnet and not an IP), `TestScenarioProxyHostRefused`,
  `TestScenarioProcessRuntimeLoopback` and `TestScenarioS3EndpointFromBridge`. The S3 test covers `0.0.0.0`, `[::]`, an
  empty host, the bridge itself, the loopback case that keeps `http://<listenAddr>`, an explicit endpoint and the
  process runtime.
- **The removal is complete.** No code, test fixture or example reads or sets `catalog.proxyHost`,
  `FUNCD_CATALOG_PROXY_HOST` or `WithCatalogProxyHost`. The only exceptions are the m1 comment, the refusal test and
  frozen review docs. The pinned funcd-functions v0.13.0 and funcd-python v0.10.1 modules mention it only in a
  changelog. Strict parsing refuses the key and names it.
- **One source.** Every reader goes through `Config.BridgeAddress()`, through `cmd/funcd`'s `bridgeAddress`: the allow
  list, `WithBridgeAddress` and `s3Endpoint`. Validation (`CheckBridge`) runs in `Validate`, so the error that
  `bridgeAddress` discards cannot happen for a loaded config.
- **The proxies stay node-private.** They bind and publish the bridge address and no longer bind `0.0.0.0`
  (`pkg/funcd/funcd.go`, `catHost`). The process runtime keeps loopback.
- **The bind-before-bridge fix is sound.** `Manager.Listen` keeps the recorded port on `EADDRNOTAVAIL` and wraps the
  cause. The reconciler converges the engine and binds on a later pass, and
  `TestReconcileConvergesBeforeTheBridgeAddressExists` covers it. The change feed's `Serve` already retries each second.
  The duckdb lane passed with neither the proxy host nor `s3gateway.endpoint` set.
- **Tracking.** ADR-0085 has its `Superseded in part by: ADR-0238` back-link. The F136 row and the ADR are both at
  `reviewing`. The `sql` lane step reads the new `bridge address` log line.

Recommendation: pass, with stamps applied. Fix m1 and m2 in the same PR if convenient. Merge only after the `sql` and
`sql-examples` reruns and the gate are green.
