## Verdict: pass — 0 blockers, 0 majors, 3 minors  (ADR-0240 implementation, model: claude-opus-5-5)

Reviewed the working tree of branch `feat/adr-0240-namespace-hosts` against `origin/main` 4f6f0358, with the ADR text
of that commit as the bar. The review was run by the session that built the work, so it is not independent; the
evidence below is from commands run on this tree.

### Minor

- **m1 · `model` · `router.IsFunctionPath` is exported beyond the Contracts.** The data plane needs the
  `/function` test that the aggregator applies (`internal/edge/router/hosts.go:48`), so it is exported rather than
  copied. The Contracts list only `NamespaceHost` and `NamespaceOfHost`.
- **m2 · `model` · no test accepts a base domain of exactly 189 characters.** The refusals are each covered
  (`TestScenario_namespace_host_base_domain`); the boundary that must still start is not.
- **m3 · `model` · the e2e `namespace-host-explicit-mode` checks the 404, not the absence of a wake.** The unit test
  `TestNamespaceHost_ExplicitAndInternal` checks with a spy scaler that the refusal wakes nothing.

### ✅ Verified correct (keep it)

- **Checks**: `scripts/agent/d go build ./...` exit 0; `go vet ./...` exit 0; `go tool golangci-lint run` on the
  touched packages, and with `--build-tags e2e` on `pkg/funcd`, 0 issues; `go test ./...` passes (the one failure of
  the first run, `TestSpecGeneratedFromGo`, preceded the OpenAPI regeneration and passes after it);
  `go test -tags e2e ./pkg/funcd/` passes after the four Site tests moved to their namespace host; `go mod verify`
  needs nothing new (no dependency added).
- **Lanes on containerd**: `just lima-example env-echo` and `just lima-example app-todo` both end `final status:
  PASS`; the moved steps ran (`anonymous /guarded → 401`, admin `→ 200`, C6 `BASE-OK`).
- **Decision 3**: the host step runs once, in `Aggregator.Set`, before arbitration (`aggregator.go`); no source
  changed what it sends.
- **Decisions 4 and 5**: the reservation is computed from stored entries per arbitration (`reserving`), so it starts
  and ends with the namespace's entries (`TestAggregator_ReservationStartsAndEnds`, across sources in
  `TestAggregator_ReservationAcrossSources`); `HostReserved` sits after `HostRequired` and before `EdgeStarting`, and
  `ReservedPath`/`HostRequired` reach only entries still host-less (`TestAggregator_ExplicitNamespaceHostlessEntry`).
  The router skips host-less rows on a reserved host and on names under it.
- **Decision 6**: the data plane ignores another namespace's match under `/function` on a namespace host and serves
  by name there, ignoring the header; internal calls keep the header
  (`internal/dataplane/namespacehost_test.go`). The exposure gate and the rate key use that namespace.
- **Decision 7**: `RouteStatus.Host` is written from every verdict and left empty for a Route whose backend is
  missing; a Site's own Route carries it (`TestScenario_namespace_host_site`).
- **Decision 1**: `validBaseDomain` runs at the start of `Run` and refuses every case of the scenario with
  `fault.Invalid`.
- **Scenarios**: one e2e test per scenario, named after it, each with `t.Parallel()`, each under 0.5 s.
- **Tracking**: the ADR's only edit is the status bump; the F153 row moves with it.

### Recommendation

Pass. Stamp ADR-0240 `Implemented` and the F153 row `implemented`; the implementing PR carries `Fixes #1038`.
