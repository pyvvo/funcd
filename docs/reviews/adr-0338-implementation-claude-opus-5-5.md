## Verdict: pass — 0 blockers, 0 majors, 0 minors (ADR-0338 implementation, model: claude-opus-5-5)

This report reviews the commit `feat: implement ADR-0338` on the branch that ships FEAT-0019's five ADRs in one PR,
after ADR-0339's commit. By the decider's standing rule of no review agents, the session that wrote the code ran this
review, so it is not independent. Every check below was run, not read. The `Implemented` stamp waits for the lane case
(`addon-rauthy-signin`: a Function sends a header to an engine through `ctx.addon.fetch` and through the proxy, gets
404 for a private path and fails to dial the engine) and for the Functions guide's section, whose handler code comes
from that lane's example.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

None.

### Routed (not this work's)

- **The lane case** waits for its Function in pyvvo/funcd-functions, released and pinned, with ADR-0341's.
- **The Functions guide's section and the add-ons Spec page**: the guide takes its handler code from that lane
  example (the develop-page rule), and `ctx.addon` gets its Handler API rows once the add-ons group has a Spec page,
  which the docs pass that the decider asked for adds. kindref's table already maps `addon.fetch` in both languages.
- **ADR-0339's AddonRevision operations had no answer rows**: the full `pkg/funcd` run's TestMain named five answers
  that no test got. The commit before this one adds `TestAddonRevisionAnswersMatrix`.

### ✅ Verified correct (keep it)

- **The checks**, through `scripts/agent/d`, on the tree of this commit: `go test` on the touched packages
  (`api/types/v1alpha1`, `internal/auth/...`, `internal/controlplane/...`, `internal/dataplane/...`,
  `internal/function/...`, `internal/gc`, `internal/health`, `internal/network/egress`, `internal/platform/...`,
  `internal/pooling`, `internal/workernode/local`, `internal/services/addon`, `internal/app/...`, `cmd/funcd`,
  `scripts`, and all of `pkg/funcd`, whose TestMain checks every declared answer); `go vet`; `go tool golangci-lint
  run`, 0 issues; `just check-behavior`; `just check-specs`, 23 specs, 0 failing; `just generate`. In the language
  repos, `just ci` passed on each shim PR, and both PRs and their releases merged through their queues.
- **The scenarios**: all 13 have a test. `addon-call`, `-sdk`, `-refused`, `-held`, `-timeout`, `-too-large` and
  `-headers` run against an engine in `internal/workernode/local`, `addon-call-path` and `-no-engine` in
  `internal/services/addon` (readiness's report in `TestCheckerAddon`), `addon-binding-missing` in
  `internal/function`, `addon-binding-undeclared` and `-standalone` in `pkg/funcd`, and `addon-fence-stays` in
  `internal/network/egress`; the shims test `addon-call` from their side (`addon.test.ts`, `test_addon.py`).
- **The causes**: `TestAddonCauseMatrix` provokes each of the ten causes on both patterns through a running platform's
  local API, 20 rows; `TestEveryDeclaredCauseHasARow` holds the facet to it.
- **The design model**: `restore-start.fizz` keeps the mutant `skip-hold` that the ADR added; the facet's held test
  carries the `NoAckedWriteRolledBack` marker, since each path is one more writer under the hold.
- **The mutants**, each run against the test that should catch it:

  | Mutant | Caught by |
  |---|---|
  | an `X-Funcd-*` header reaches the engine | `TestScenarioAddonCallHeaders` |
  | the client follows a redirect | `TestScenarioAddonCallHeaders` |
  | a held refusal without `Retry-After` | `TestScenarioAddonCallHeld` |
  | no hold check | `TestCallsRefuseAHeldNamespace` |
  | no path check | `TestScenarioAddonCallPath` |
  | any proxy token names its Function | `TestProxyToken` |
  | no Cedar check | `TestCallsRefuse` |
  | an App's Function binds an object its App does not declare | `TestScenarioAddonBindingUndeclared` |
  | Python: no refusal check, no span error, no header dedupe | `test_addon.py` |

- **What the review found and the builder fixed before this commit**: the hold was checked after the object's
  readiness, but a held object has no engine, so a held call answered `local.addon-engine-unavailable` without
  `Retry-After`; the hold is now checked first, just before the dial, and `TestCallsRefuseAHeldNamespace` covers an
  object that is not Ready. The proxy bound the bridge address in `funcd.New`, which fails while the bridge is not up
  (`TestScenarioBridgeKeeps7075` failed); it now binds at its first use, as a Lake's proxy does, and a failed bind is
  retried by the Function's next pass. Tests were added for the engine's address in no answer or log, for the two
  causes no test reached (`local.addon-reply-cut-off`, `local.body-unreadable`), for the pool's access document, for
  readiness's report and for the fence.
- **The contracts**: `FunctionAddon` and `bindings.addon`; the Cedar action `addon::call` and the built-in
  `addon-call-binding`; the facet `/addon/{alias}/{path...}` and its root; the proxy with its HMAC token, under a key
  of its own derived from the node master; `FUNCD_ADDON_<ALIAS>_URL` and `_TOKEN`; `X-Funcd-Addon-Reply`; the causes
  `local.addon-*`; `addons.callTimeout` (10 s) and `addons.callPayloadLimit` (1 MiB); the edge's `keptPath`, moved to
  `pathx.Kept` for both sides. funcd pins funcd-typescript v0.31.0 and funcd-python v0.26.0, whose `ctx.addon.fetch`
  follow Decision 8.
- **Three choices of the implementation, for the decider to confirm**: the proxy binds at its first use rather than
  at funcd's start; an engine's 5xx sets the shims' CLIENT span to ERROR; and Python raises `ValueError` for a path
  that does not start with `/`, as an argument error, where a refusal of funcd raises `RuntimeError`.
- **The behavior rows** the ADR lists are in `calls.md`, `addons.md`, `workers.md`, `limits.md`, `security.md` and
  `disaster-recovery.md`; the config reference has both keys.

### Definition of Done

17 of 19 items hold: the 6 Review-checklist items, the scenarios, the design model and its oracle, the touched
packages' checks (the gate runs `just ci-full`), the behavior rows and the config reference, and 7 generic items. Open:
the lane case, and the Functions guide's section with the add-ons Spec page.

### Model scorecard

To record: claude-opus-5-5 on ADR-0338 (implementation) → pass, 0 blockers, 0 majors and 0 minors. DoD 17/19. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Nothing goes back to the builder. The lane run before the PR, with the guide's section, stamps ADR-0338
`Implemented`. The decider confirms the three choices. The PR that ships the batch names issue #1505.
