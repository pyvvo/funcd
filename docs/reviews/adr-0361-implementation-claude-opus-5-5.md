## Verdict: pass — 0 blockers, 0 majors, 0 minors (ADR-0361 implementation, model: claude-opus-5-5)

This report reviews ADR-0361's implementation on branch `feat/dev-lan`, commit d8721b3d (code in that commit; the ADR's
own commits since origin/main 68dde8e3 precede it). The reviewer did not write the work, and every check below was run
on a host with a LAN address, so the reachability scenarios ran rather than skipped. The repo-wide checks (`just ci`,
the e2e suite, the Linux lanes) were left to the PR's gate, which runs them once.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

None.

### Routed (not this work's)

- **The repo-wide `just ci` and `scripts/agent/gate.sh`** · elsewhere: the PR's gate. The ADR's *Done when* names
  them; this review ran the package's checks only, as the gate runs the rest once. The `Implemented` stamp below rests
  on that gate passing.
- **Feature issue**: #1597 (tracker #1028). The PR that ships this work carries `Fixes #1597`.

### ✅ Verified correct (keep it)

- **Build, vet, lint**: `go build` and `go vet` of `./cmd/funcdctl/` with and without `-tags dev`, exit 0;
  `go tool golangci-lint run --build-tags dev ./cmd/funcdctl/`, `0 issues.`
- **Tests**: every ADR-0361 test by name under `-race`, exit 0, none skipped: `TestScenarioDevLANOff`,
  `TestScenarioDevLANEveryAddress` (the pinned http-api example, a call and a two-socket WebSocket relay on the host's
  LAN address), `TestScenarioDevLANLoopbackKept`, `TestScenarioDevLANOneAddress`, `TestScenarioDevLANBanner`,
  `TestScenarioDevLANRefused`, and the unit tests `TestCheckLAN`, `TestGatewayListen`, `TestGatewayURLs`,
  `TestLANAddrs`, `TestDecodeDevLAN`, `TestDevLANFlagWinsOverTheKey`, `TestDevLANChangeNeedsARestart`,
  `TestLANArgsLeavesADirectoryNamedLikeAnAddress`. The package's tests pass in both builds: `go test ./cmd/funcdctl/` exit 0 (5 s) and `go test -tags dev ./cmd/funcdctl/` exit 0 (211 s).
- **Mutant killed**: making a bare `--lan` bind `127.0.0.1` (`cmd/funcdctl/devlan.go`, `gatewayListen`) through
  `go test -overlay` failed `TestGatewayListen` and `TestScenarioDevLANEveryAddress`, so the scenario proves the bind,
  not only the banner.
- **Contracts**: `cmd/funcdctl/devlan.go` holds every declared function and type with the declared signatures
  (`checkLAN`, `gatewayListen`, `gatewayURLs`, `lanAddrs`, `devLANKey.UnmarshalYAML`, `lanArgs`, `devLANAll`);
  `devConfig` gains `lan` and the `findLAN` seam, `devFileKeys` the `LAN devLANKey` field, `devInstance`
  `networkURLs` and `offLoopback`; the flag is a string flag with `NoOptDefVal` `devLANAll`.
- **Decision 2**: only `funcd.WithDataPlaneAddr` changes (`cmd/funcdctl/dev.go`, `devPlatformOptions`); the control
  plane, S3, reload stream and container workers keep their binds, and dev adds no authenticator.
  `net.JoinHostPort` brackets an IPv6 address (unit-tested with `::1` and `2001:db8::5`).
- **Decision 1**: the flag is checked in `resolveDevRun` before the file is read; `lanArgs` refuses a bare `--lan`
  before an address that is no directory, with or without a second argument, and leaves a directory named like an
  address alone; `dev.lan: 3` is refused naming it; the flag wins over the key; a changed `dev.lan` prints the restart
  line through `changedDevKeys`.
- **Decision 3**: network lines beneath the gateway, each `POST` line and each host-less Route and Site line
  (`appEndpoints` in `cmd/funcdctl/dev_app.go`), none beneath a line with a host; title and warning follow
  `platformconfig.IsLoopbackAddr` on the bound address; the warning precedes the egress warning; the URL column widens
  so a long address does not wrap.
- **Docs and rows**: the Spec page is current (`TestScenarioCommandSpecGenerated` passes); the guide's funcdctl, App and
  networking pages name the opt-in and what it exposes; `docs/behavior/security.md` gains the `accepted` row and
  `docs/behavior/funcdctl-dev.md` its opening; `just check-behavior` and `just check-hygiene` exit 0. ADR-0125 carries
  the `Superseded in part by` line, and the blueprint names `--lan`.
- **Tracking**: the implementation commit changes the ADR's status line only (`Accepted → Reviewing`) and F278 only
  `reviewing`; the ADR's substance is unchanged.
