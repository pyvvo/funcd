# ADR-0314 implementation review — claude-opus-5-5

## Verdict: pass — 0 blockers, 0 majors, 3 minors (ADR-0314 implementation, model: claude-opus-5-5)

Reviewed four commits on top of the acceptance commit `213641ee`, built by a session other than this one: `55d85554` (the default and
the helper), `1c73c882` (the rule, the warning, the library and the docs), `f8ec619c` (the unit's runtime, by `/fix-review`'s checks
below) and `d50e359b` (the rows and the status). Implementation plan step 7 and *Done when* were read in the form they land in:
`scripts/lima.yaml` runs `funcd install` and disables the unit at once, `TestInstallUnitRunsContainerd` pins the unit's environment,
and the release wave's one lane pass runs that `funcd install`. That lane pass and the wave's gate run once on the wave's tip and are
not part of this report; *Done when* names them.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Six rows of the ADR's *Behavior rows* were reworded after acceptance** · attribution: model · Besides the status line, `d50e359b`
  rewords six rows of the Accepted ADR: for example `` `off loopback` `` becomes `` `is off loopback` `` and "this rule's message"
  becomes "This refusal". No Time, Status or Source cell moved, and each new wording says what the ADR already says elsewhere, so no
  Context, Scenario, Decision, Contract or Alternative changed. It is wording, not substance, so this review places it here and not as
  the Blocker that the generic Definition of Done's "any other diff" would read. The rule still allows only the
  status bump, and the edit was not needed: ADR-0313 Decision 10 asks that the chapters' rows match the section, and the chapters
  already differ from it in form (`ADR-0314 Decisions 2 and 3` against `Decisions 2 and 3`). Fix: none for the platform. Acceptance,
  this wording and the stamp land in one PR. If the decider wants the accepted wording back, it is six rows in the ADR.
- **`TestScenarioRealCredentialBindsAnywhere/empty-address` does less than its row says** · attribution: model · The Calls row *An
  explicit empty `server.listenAddr` with a real credential* cites this test for "reaches the `Production()` default". The subtest
  asserts only that `checkBuiltInTokenBind` passes, which a real credential does on any address. A mutant that makes `New` bind
  `127.0.0.1:0` for an empty address (`empty-bind`, `pkg/funcd/funcd.go`, `c.listenAddr = defaultProdAddr`) survives this test and
  `TestScenarioLibraryFollowsTheRule`. The behavior is what the row says: on the real host that config logs `"addr":"[::]:8080"`, and
  the VM's other address answers its token with 200 and the built-in token with 401. Fix: assert in the subtest that the loaded address
  is empty. A test of the bind itself would hold a fixed port, which this accepted cost does not justify.
- **`feat(config)` carries no `!`** · attribution: model · `55d85554` changes a default: a node with no `server.listenAddr` moves to
  loopback (`docs/install.md`, Upgrade; ADR-0314 Consequences). `1c73c882` marks its change `feat(auth)!` and this one does not, so the
  changelog lists it as a plain feature. Fix: `feat(config)!:` in its subject or in the PR's commit override.

### Routed (not this work's)

- **Implementation plan step 7 and *Done when*** · home: elsewhere · destination: the decider, who ruled on 2026-11-05 that no lane
  case runs the installed unit. Both passages moved after acceptance by that ruling, and this review read them as stated above. Nothing
  stays here.
- **The installed unit serving on containerd** · home: elsewhere · destination: ADR-0054's Linux integration lane, which already holds
  `runs-out-of-the-box` (lines 184-186). `TestInstallUnitRunsContainerd` pins the environment, the real host shows that environment
  taking the containerd path, and lanes start the private containerd from their own configs. No test or lane case here watches the
  installed unit itself serve on containerd.

### ✅ Verified correct (keep it)

- **Checks, run by me through `scripts/agent/d`.**
  - `go test -race` on `internal/platform/config`, `cmd/funcd` and `pkg/funcd`: `ok` in 1.6 s, 75 s and 51 s. The new tests under
    `-race -count=3`: exit 0, and none skipped.
  - `go build ./docs/demo/... ./internal/testkit/...`, `go vet` with no tag and with `e2e`, `GOOS=linux go vet`, `golangci-lint` (0
    issues), `just check-behavior` and `just check-hygiene`: each exit 0, on the touched packages where a command takes packages.
  - `go test -run Lane ./cmd/funcd/ ./internal/testkit/langmod/ ./scripts/`: `ok`. `TestScenarioDevRunsResources` with `-tags dev` in
    `cmd/funcdctl`, under `-race`: pass.
  - Not e2e tests, and fast: the slowest new daemon test takes 1.9 s.
- **Each Scenario, and its rule reverted.** Every revert ran with `go test -overlay` on a scratch copy of the changed file.

  | Scenario | Test | Passes under `-race` | With its rule reverted |
  |---|---|---|---|
  | `fresh-config-binds-loopback` | `TestScenarioFreshConfigBindsLoopback` | yes | the old default: fails, expected `127.0.0.1:8080`, actual `0.0.0.0:8080` |
  | `built-in-token-off-loopback-refused` | `TestScenarioBuiltInTokenOffLoopbackRefused` | yes | `serve` without the call: all 22 subtests fail, and `does-not-count` no longer gets `fault.Invalid`. The rule gone from `serve` and `validate`: the `192.0.2.1` subtest fails. The rule after `MkdirAll`: 21 fail on "refused before the data directory exists" |
  | `built-in-token-on-loopback-warns` | `TestScenarioBuiltInTokenOnLoopbackWarns` | yes | the old warning condition: the 3 forms that did not warn fail. The rule refusing loopback too: all 22 fail. No warning, or a second one: the 4 starts fail |
  | `real-credential-binds-anywhere` | `TestScenarioRealCredentialBindsAnywhere` | yes | `auth.credentials` not counted, any `auth.token` counted as the built-in one, a warning with a real token, or the built-in token still accepted beside a real one: each fails, the last with expected `unauthorized`, actual `""` |
  | `library-follows-the-rule` | `TestScenarioLibraryFollowsTheRule` | yes | `validate` without the call: the 8 refused cases fail. An empty address not resolved: `empty-resolves-to-default` fails. Every store refused: the 4 own-credential starts fail |
  | `edge-auth-follows-the-rule` | `TestScenarioEdgeAuthFollowsTheRule` and the edge rows of `TestScenarioLibraryFollowsTheRule` | yes | `serve` not reading the data plane: the 4 forms fail. The rule gone from `serve` and `validate`: `serve` keeps running until the test's timeout. The rule applied with edge off: `edge-off-starts` and `edge-off-every-address` fail. `validate` not reading the data plane: its 2 edge cases fail |

- **Mutants.** 31 in all, the reverts above included; 30 are caught, and the survivor is the second Minor. The others also cover the
  helper (no `localhost`, only `127.0.0.1`, an empty host), the trim of `auth.token`, and a token value in the refusal.
- **The real host, in `funcd-linux-tests` as root, on `GOOS=linux GOARCH=arm64 CGO_ENABLED=0` builds of this code.**
  - `funcd install --print` prints `Environment=FUNCD_DATA_DIR=/var/lib/funcd` and `Environment=FUNCD_RUNTIME=containerd`. With that
    environment the binary takes the containerd path with no process-mode warning, and stops at `crun not found in funcd's bin dir`,
    since no runtime is laid down in this VM.
  - With no config it logs `"addr":"127.0.0.1:8080"`, and `/proc/net/tcp` holds `127.0.0.1:8080`. Loopback answers the built-in token
    with 200 and no token with 401, and the VM's other address makes no connection. Step 4's `funcdctl` command exits 0. There is one
    warning, and no log line holds the token. `auth.token` and `FUNCD_TOKEN` with the built-in value give the same, and so does
    `--memory` for the bind.
  - An address off loopback with no credential exits 1 with the Contracts' message and leaves the data directory absent. So do
    `FUNCD_TOKEN` trimmed and untrimmed, an empty address, an address with no port, and edge on with a data plane off loopback. Eight
    refused starts on a data directory that exists leave it empty.
  - With an address and a token of its own, and with step 5's `auth.credentials`, also from a token file that ends in a newline, the
    VM's other address answers that token with 200 and the built-in token with 401. There is no warning, and step 5's `funcdctl`
    command exits 0. Edge off with a data plane off loopback starts.
  - A listener on `0.0.0.0` shows as `[::]` in the log and not in `/proc/net/tcp`: Go opens one dual-stack socket. The answers at the
    other address are the evidence for it.
- **Decisions 1 to 4, read against the code.** `defaults()` holds the new value and `server.dataPlaneAddr` keeps `127.0.0.1:0`.
  `builtInTokenInEffect` follows Decision 2, trim included, through `parseToken`. `serve` calls the check right after `config.Load` and
  before `os.MkdirAll`. The refusals and the warning match the Contracts word for word, and `FUNCD_LISTEN_ADDR` and
  `FUNCD_DATA_PLANE_ADDR` are the keys' real variables.
- **Decisions 5 and 6, read against the code.** `checkDevTokenBind` runs in `validate` after the credentials case, asks
  `c.credentials.Lookup` and resolves an empty address with `cmp.Or` to what `New` binds. The daemon and `validate` read the one
  `IsLoopbackAddr`, and no other loopback helper exists to reuse. `c.credentials` has two readers: the control plane and the edge PEP,
  built only under `edgeAuthEnabled`.
- **What the ADR keeps.** `presets.go` and `options.go` change only in comments. `defaultProdAddr` and `defaultDataPlaneAddr` do not
  move. No file changes in `configs/systemd`, `cmd/funcdctl`, `docs/demo` or `internal/testkit/bench`, and `go.mod` and `go.sum` do not
  change.
- **Callers.** The other non-test `funcd.New` callers are `funcdctl dev`, `docs/demo/server` on `127.0.0.1:8080` and the bench's two, on
  `InMemory()` with no address option. In the tree and the three pinned modules, the only off-loopback `listenAddr` in a config is
  `s3gateway.listenAddr: 0.0.0.0:9000`, another key that reads no `c.credentials`. No test sets an off-loopback address apart from this
  ADR's. The e2e-tagged tests compile and were scanned, not run: the gate runs them.
- **Docs.** `docs/install.md` steps 2, 4 and 5 and the upgrade note hold against the code. `/etc/funcd/funcdconfig.yaml` is on
  `config.Locate`'s list, an environment variable wins over the file (`config.go`, the table in `config_test.go`), and
  `internal/upgrade` starts the unit, prints its line, waits for no answer and writes no unit. `examples/funcdconfig.yaml` shows the
  default and the rule.
- **Rows.** The section's eight rows, its changed machine edge and its changed row are in the chapters with the same facts, and the
  `server.listenAddr` value is regenerated. A grep of `docs/behavior/` for the token's names finds no row left behind, and the old
  warning's text remains in no code or doc outside the ADRs and reviews.
- **Tests kept.** `TestScenarioLegacyTokenIsDeveloper` keeps every assertion and runs them per form. `TestScenarioFileSetsAddresses`
  still tells the file's address from the default. `TestScenarioZeroConfigDefaults` follows the new default, as step 1 says. Only the
  `[::1]` start skips, on a host with no IPv6 loopback, and the rule's `[::1]` cases always run.
- **Conventions (ADR-0002).** No `any` in the new signatures, `api/fault` errors, `log/slog` only, no new dependency, and the import
  graph holds: `internal/platform/config` gains only `net`, and `pkg/funcd` already imported it under the same alias.
- **Tracking.** The ADR was at Reviewing and F228 at reviewing. The back-links on ADR-0028, ADR-0061, ADR-0062, ADR-0113 and ADR-0171 are
  one header line each, and the blueprint's API access line follows the ADR. F228 takes no feature issue (the ADR's References), so
  the PR closes none.

### The unit's runtime (`f8ec619c`), by `/fix-review`'s checks

Pass, with no finding.

- **Fails without it, passes with it.** With the `Environment=` line removed, `TestInstallUnitRunsContainerd` fails with expected
  `containerd`, actual `process`. It passes with the line, under `-race`. With `FUNCD_RUNTIME=process` in the unit it fails too.
- **Design check.** It breaks a clause. ADR-0054 scenario `runs-out-of-the-box` has `sudo funcd install` start the private containerd,
  and `docs/install.md` step 2 and the comment in `configs/systemd/funcd.service` say the same. `install.go` on `origin/main` has no such
  line, and `defaults()` gives `process`. Nothing accepts the old unit: ADR-0036's `process` stays the config default, ADR-0062 and
  ADR-0052 speak only of the variable, two issue searches and the board list hold nothing on it, and no test pinned it.
- **Cause, not symptom.** The unit set no runtime. It now sets the existing key, and the config default and the process runtime's unit
  stay as they are.
- **Scope and reuse.** One line of code, one test and one sentence in `docs/install.md`. The test reuses `renderUnit` and `config.Load`,
  and reads every `Environment=` line, so it follows the unit as it changes.
- **Siblings.** `renderUnit` is the one writer of this unit. `funcd upgrade` does not rewrite it, which the upgrade note says. Case D2
  of `e2e/app-todo.venom.yml` runs `funcd install` on a host with nothing else running, so its start touches no running daemon.
- **Shape.** `fix(install):` with its trailer, and no `Fixes #N`, since it has no issue by the decider's choice.

### Definition of Done

15 of 16 hold: the ADR's 7 checklist lines and 9 applicable generic items (no port has drivers here, so the contract suite item does not
apply). The miss is the generic Tracking item's "only the status bump" (the first Minor, attribution: model). The full suite here is the
touched packages; the gate runs on the wave's tip.

### Model scorecard

To record with the wave's last commit: claude-opus-5-5 on ADR-0314 (implementation): pass, 0 blockers, 0 majors, 3 minors, 3
model-attributed, DoD 15/16. See docs/reviews/model-scorecard.md.

### Recommendation

Merge after the wave's gate and its one lane pass are green on the tip. The second and third Minors are one line each and worth making
in the wave. The first needs nothing unless the decider wants the accepted wording back.

### After this review

The session that wrote the work rebuilt it before any push, as one commit per item on `c8791a05`, the main that holds ADR-0313. So the
hashes above name the reviewed tree, and its code stands but for one assertion. The first Minor no longer applies: the ADR reaches
Accepted in one commit, with the rows' final wording. The second is folded in: the subtest asserts that the loaded address is empty.
The third no longer applies: the default and the rule share one `feat(server)!:` commit.
