## Verdict: pass — 0 blockers, 0 majors (ADR-0332 implementation, round 2, model: claude-opus-5-5)

Round 2 reviews `feat/addons` at 7d7ddeaa. That commit includes the round-1 fixes (d0ebb6ae, d4f07fc9, ab67c895) and the
merges of ADR-0333 (fa7a6e2b) and ADR-0334 (fe1845cb), which changed `internal/services/addon` and `pkg/funcd`.

Every finding of round 1 is closed, and I rechecked ADR-0332's clauses on the merged code. What is left is one registry
citation (Minor, model) and the 400-against-422 difference routed in round 1 (Minor, adr).

### Round 1's findings

- **Blocker 1, `pkg/funcd`'s answers check: closed.** d0ebb6ae adds answers matrices for the Addon, add-on object and
  OIDCProvider operations (`pkg/funcd/addons_answers_test.go`). `TestMain` holds every listed answer to a test only on a
  full run that passes:
  - `go test -count=1 -parallel 4 ./pkg/funcd/`, with no `-run` and no `-skip`, passes (82 s, exit 0), so the check ran
    and held.
  - `go test -race ./pkg/funcd/` at default parallelism fails one test only:
    `TestBackup_GoesOnWhenItsListingFails/pages_with_no_key`, whose wait ran out after 17 s. It failed the same way in a
    run without `-race`, and passes alone under `-race` in 3.9 s. It is main's own test (#1390, before this epoch) and
    times out under the host's load. It is not counted.
- **Major 1, the guide's command and example: closed** (ab67c895). The release manifest is piped to
  `funcdctl apply -f -`. The example IdentityProvider has `metadata.resourceGroup` and `spec.bindings.secret`. Offline,
  funcdctl now decodes and validates both the example as written and the published rauthy Addon piped through stdin, and
  stops only at the PUT to the absent server.
- **Minor 1, rules with no Go test: closed** (d4f07fc9). `TestScenarioAddonPrivatePath` now sends `/auth/v1/x/../ping`,
  so mutant M1 is killed. `TestReconcileHoldsAnObjectWhoseEnvFails` reaches the `EnvInvalid` hold.
- **Minor 2, how an object finds its Addon: closed** (d4f07fc9). `addonOf` and `MapAddon` match by plural, as the API
  does. `TestReconcileFindsTheAddonByPlural` covers a kind registered again in another case, and mutant M4 is killed.
- **Minor 3, wording: closed** (ab67c895). The `plugin list` example has funcdctl's spacing. The sign-in text names
  `client.registration` for the registration and `addon` for the address. The fence is stated for a server, and dev runs
  on the local engine's network. The rows "An Addon's kind" and "An Addon's image" mark their 400s `differs` and cite the
  scenario that says 422.
- **The 400-against-422 difference (adr): unchanged.** The rows mark it `differs`. ADR-0291 and ADR-0292 were stamped
  `Implemented` with the same kind of row (`docs/behavior/admission.md`, `docs/behavior/limits.md`), so that precedent
  covers it, and it no longer holds the stamp. It is recorded as one Minor (adr).
- **The two questions I left to the decider were settled under the standing go-ahead.** A failed converge now reads
  `EngineConvergeFailed`, as for a Lake and a SQLDatabase (`internal/services/addon/reconcile.go:96-98`). The token rule
  is written in `build.md`, and it matches `descriptorFor` (`pkg/sdk/kinds.go`).

### 🟡 Major

None.

### Minor

- **Minor 1: a registry row cites the wrong ADR for a reason · model.** In `docs/behavior/addons.md`, the row "An object's
  engine" now lists `EngineConvergeFailed` and cites ADR-0332 Decision 3 and `TestScenarioAddonObjectRuns`. ADR-0332
  names no such reason: its Contracts list `EngineNotReady` as the only reason for the engine. The reason comes from
  ADR-0232's reason table ("the runtime could not create or start the engine"), which ADR-0281 also follows, and the test
  that pins it is `TestReconcileFindsTheAddonByPlural`. Fix: cite those, so that the row does not misstate ADR-0332.

### Routed (not this work's)

- **400 where ADR-0332 says 422 · adr · the decider.** This is round 1's routed finding. It is carried as one Minor
  (adr), and the ADR-0291 and ADR-0292 precedent lets the stamp go ahead.
- **Pending the module pin and the final gate, not findings.** `TestScenarioLaneStagesResolve` still fails with
  `go mod download github.com/pyvvo/funcd-addons: … not a known dependency`. `TestScenarioExamplesBuildWithTheTools` now
  passes, since it skips a lane that pushes nothing. The `addon-rauthy` lanes and the Linux-only tests run in the gate.

### ✅ Verified correct (keep it)

- **Checks on 7d7ddeaa**:
  - `go build ./...` exits 0. `go vet` exits 0 on darwin, and on linux for `internal/network`, `internal/runtime/containerd`,
    `internal/provider`, `cmd/funcd` and `pkg/funcd`.
  - `go tool golangci-lint run` reports 0 issues on darwin and on linux. `gofmt -l` lists nothing, and `go mod verify`
    passes.
  - `go test -race` passes on `api/types/v1alpha1`, `internal/auth/cedar`, `internal/controlplane/...`,
    `internal/dataplane`, `internal/edge/router`, `internal/network/...`, `internal/provider/...`,
    `internal/services/addon`, `pkg/sdk`, `scripts/behavior`, `cmd/funcd` and `cmd/funcdctl`.
  - With `-race -run Addon`, the 34 tests of `pkg/funcd` pass in 4 s or less each. They include the 13
    ADR-0332 scenarios and ADR-0333's add-on scenarios.
  - `just check-behavior` exits 0, `just check-specs` reports 21 specs with 0 failing, and `just check-hygiene` reports
    clean.
- **ADR-0332 on the merged reconciler**: the holds keep their reasons and their order: `AddonNotFound`,
  `RuntimeUnavailable`, `ConfigInvalid`, `EnvInvalid`, then `SecretNotFound` and `BindingResolveFailed`, each with no
  engine and no entry. ADR-0333's restore step, before the Secrets, and ADR-0334's `RuntimeUnavailable` from a dev
  runtime with no container engine, after the converge, add to these and change none of them. `engineSpec` still sets
  one replica, `EgressTo("")` and `HostOnly`.
  `secretEnv` still keeps only `secretKeys`. The edge entry, its stance and the private paths are unchanged, and
  funcd's sign-in cookies no longer reach the engine (`oidc.DropCookies` in `serveUpstream`, ADR-0335 Decision 8).
- **Decision 6 on the merged control plane**: `AnyRegistry` lifts the registry list only when `WithAnyAddonRegistry` is
  set, and only `funcdctl dev` sets it (`cmd/funcdctl/dev_addon.go`, ADR-0334 Decision 5). A server keeps
  `addons.registries`, and every image is still pinned by digest.
- **Contracts**: with ADR-0333's merge, `AddonSpec.Snapshot`, `AddonSpec.Restore` and `AddonObjectSpec.Deletion` are
  present, so the Contracts block matches in full.
- **API texts** (d0ebb6ae): an add-on object's cases say "an object". `replaceAddon` lists 409 only for a stale version,
  and a missing object's 404 says "not found".
- **Guide pages**: the ADR-0333 and ADR-0334 statements that round 1 routed to integration now exist in the code: a
  BackupSchedule's `addons`, `snapshot`, `restore` with `${{ restore.file }}`, and `funcd.DevAdminToken`. The
  `#sign-users-in` anchor exists.
- **Tracking**: ADR-0332 has not changed since round 1 (`Reviewing`), and the F258 row reads `reviewing`.

### Mutants (`go test -overlay`, nothing written to the tree)

- **M1** removes the `..` refusal in `keptPath`. Killed by `TestScenarioAddonPrivatePath`; round 1's survivor is closed.
- **M4** makes `addonOf` compare the kind exactly. Killed by `TestReconcileFindsTheAddonByPlural`.

### Definition of Done

18 of 20 items hold: the 8 Review-checklist items, `just check-behavior`, the guide pages, and 8 generic items (real
behavior, contracts, the provider runtime's tests, tree, conventions, dependencies, scope, tracking). Two items are
pending, and neither is a miss of the model:
- "`just ci` is green": every package I ran passes, and `TestScenarioLaneStagesResolve` waits for the funcd-addons pin.
- The `addon-rauthy` lane case waits for the final gate.

### Model scorecard

To record: claude-opus-5-5 on ADR-0332 (implementation, round 2) → pass, with 0 blockers, 0 majors and 2 minors. One
Minor is the model's, and the other is the adr-attributed 400-against-422 difference. DoD 18/20.

### Recommendation

Stamp ADR-0332 `Implemented` and the F258 row `implemented` once the final gate passes. That gate covers the module
pin, `TestScenarioLaneStagesResolve`, the `addon-rauthy` lanes and `just test-linux`. The Minor is a one-line fix to
the registry row, which can go in the stamp commit.
