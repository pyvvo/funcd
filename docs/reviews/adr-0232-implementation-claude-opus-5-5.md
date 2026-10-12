## Verdict: changes-requested — 0 blockers, 1 major, 3 minors  (ADR-0232 implementation, model: claude-opus-5-5)

Scope: the uncommitted working tree of `feat/sql-provider` against `43ae961e` (ADR-0231, Implemented). The new code is
`api/types/v1alpha1/sqldatabase.go`, `internal/services/sql/` (`engine.go`, `reconcile.go`, `client.go`,
`engineargs/`), `internal/controlplane/admission/sqldatabase.go`, `cmd/funcd-rqlite/main.go` and
`images/runtime/rqlite/`. The kind is wired through `metadata.go`, the control-plane handlers, routes and stubs, the
hold, `pkg/sdk/kinds.go`, `pkg/funcd` (runtime, reconciler, admissions, catalog row, options), the config, the
embedded image and the justfile. The scenario tests are in `pkg/funcd/sql_internal_test.go`.

**Not stamped.** The ADR stays `Reviewing` and the FEAT-0011 F128 row stays `reviewing` until Major 1 is fixed.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...`; `GOOS=linux go build ./...` | exit 0 × 2 |
| modules | `go mod verify` | `all modules verified` |
| touched packages, race | `go test -race -count=1` on `internal/services/sql/...`, `internal/controlplane/...`, `api/...`, `cmd/funcd-rqlite`, `cmd/funcdctl`, `internal/platform/...`, `internal/runtime/embedimg`, `pkg/sdk`, `internal/provider` | all `ok`, exit 0 |
| pkg/funcd, cmd/funcd | `go test -race -count=1 ./pkg/funcd/ ./cmd/funcd/` (e2e tagged out) | `ok` × 2, exit 0 |
| scenario tests | the 8 `TestScenario…` tests in `pkg/funcd`, `-v` | 8 × PASS, 0.18 s to 0.50 s each, all `t.Parallel()` |
| vet | `go vet` on the same packages plus `pkg/funcd`, `cmd/funcd` | exit 0 |
| lint | `go tool golangci-lint run` on the same packages | `0 issues.`, exit 0 |
| gofmt | `gofmt -l` on the changed `.go` files | **`api/types/v1alpha1/metadata.go`** (Major 1) |
| OpenAPI | `specgen` into a scratch file, compared with `api/openapi/funcd.v1alpha1.yaml` | identical |
| catalog tests unchanged | `git diff 43ae961e --stat -- internal/services/catalog` | empty |
| real image | `just smoke-rqlite-image` (colima) | `smoke-rqlite-image: ok` (readiness without a credential, 401 without and with a wrong password, accepted with it, uid `65532:65532`, a table that survives `docker kill` and a new container on the same volume) |
| no shell in the image | `docker run --entrypoint /bin/sh …`; `docker export … \| tar -t` | `stat /bin/sh: no such file or directory`; the only binaries are `usr/local/bin/rqlited` and `usr/local/bin/funcd-rqlite` |

### 🔴 Blocker
None.

### 🟡 Major

1. **`api/types/v1alpha1/metadata.go` is not gofmt-clean** (`model`). Inserting the commented `KindSQLDatabase`
   (`metadata.go:39-40`) splits the const alignment block; `gofmt -d` re-aligns `KindPolicy`, `KindWorkflow`,
   `KindWorkflowRun` and `KindSensor` (`metadata.go:41-44`). golangci-lint does not catch it, but `just ci` runs
   `go fmt ./...` and then fails on any changed `.go` file (`justfile:502-503`), so the ADR's "the gate passes" does
   not hold once the work is committed. This is the same defect as Major 1 of the ADR-0231 review. Fix:
   `gofmt -w api/types/v1alpha1/metadata.go`.

### Minor

1. **Two doc comments are misplaced** (`model`). In `internal/platform/config/config.go:384-391` the `SQL` struct was
   inserted between the `Site` doc comment and the `Site` field, so the `SQL` field carries both comments and `Site`
   has none. In `internal/platform/hold/hold.go:64-66` the reflowed `Evidence` comment leaves the fragment
   `// no object names,` on a line of its own. Move the `SQL` block above the `Site` comment and reflow the
   paragraph.
2. **`NewSQLDatabaseDeletionProtectionAdmission` drops a Contract parameter** (`model`). The ADR's Contract is
   `NewSQLDatabaseDeletionProtectionAdmission(r StoreReader, p SQLProber) Admission`; the code takes only the prober
   (`admission/sqldatabase.go:217`). The reader is unused, so behaviour is right, but the shipped signature differs
   from the Contract and from its Bucket counterpart. Either take `r` as the Contract says or note the deviation in
   the review handoff; a follow-up ADR that relies on the Contract would otherwise not compile.
3. **The containerd shim's memory per engine was not measured** (`env`). The ADR's Open question asks for the
   measurement on the target box during implementation. Nothing in this repo runs the engine on containerd yet (the
   SQL lane is F129's, per the ADR's Temporary workarounds), so it needs a manual deployment to the target box. Not
   scored; carry it to the F129 lane.

### ✅ Verified correct (keep)

- Contracts match except Minor 2: `KindSQLDatabase`, `SQLDatabase{Spec,Status}` with the inline `Status` and
  `Bindings`, `EnginePassword` (HMAC-SHA256 over `funcd-sql-engine/<ns>/<name>`, unpadded base64url), `Engines` with
  `Set`/`Delete`/`Address` under an `RWMutex`, `ReconcilerDeps` field for field, `Reconcile`, `ReclaimOrphans`,
  `Orphans`, `SQLProber`, `NewSQLDatabaseQuotaAdmission`. Both status reasons exist (`EngineConvergeFailed` from the
  reconciler, `EngineNotReady` from the provider runtime, `internal/provider/runtime.go:165,175`).
- The kind is threaded through every site: `Kind.Validate`, `NewObject`, `AllKinds` (count test 31 → 32),
  `stampTypeMeta` (`handlers.go:351`), the handlers interface, store handlers, stubs, REST routes, the SDK plural
  `sqldatabases`, and the regenerated OpenAPI. The scenario tests create databases through `sdk.Client.Apply`, the
  path `funcdctl apply` takes.
- The engine spec (`reconcile.go` `engineSpec`): `ImageFor("rqlite")`, port 4001, `GET /readyz` → 200, one replica,
  storage `/data` as 65532:65532, the default `256Mi` when memory is empty without rewriting the spec — all asserted
  in `TestScenarioDatabaseStartsItsEngine`.
- Re-convergence: a Ready pass requeues after the supervision period, a starting one after 2 s; status is written only
  when it changes (`TestReconcileStartingThenReady` checks the resource version stays). A generation change tears the
  engine down once and records `observedGeneration` and the condition's `ObservedGeneration` in the same write,
  whatever the engine's state (`TestGenerationChangeTearsDownOnce`, `TestScenarioResourcesChangeRestartsEngine`).
- `NewReconciler` fails `Invalid` on a runtime without `provider.Volumes` (`TestNewReconcilerNeedsVolumes`).
- Delete and hold: teardown always, reclaim only when not held (`TestDeleteReclaimsUnlessHeld`); boot
  `ReclaimOrphans` runs only in the not-held branch of `Run` and before the controller; `hold status` lists
  `sqlOrphans` as `<ns>/<name>` (`hold_test.go`, `TestScenarioDeleteReclaimsEngineAndDataAfterHold`).
- The deletion probe fails closed: no engine and a directory ⇒ `Unavailable` ⇒ 409; no directory ⇒ the delete passes
  (the process-runtime case, `TestScenarioDeleteOfNeverStartedDatabase`); a refused credential is `Internal` and its
  error does not contain the password (`TestProberHasTables`). The query is the ADR's `sqlite_master` count, and the
  response read is bounded.
- The quota: 0 ⇒ 20 in `pkg/funcd`, negative disables, `Invalid` (400) at the cap, config key and env name as the ADR
  says, documented in `examples/funcdconfig.yaml`.
- The entrypoint writes `/tmp/auth.json` at 0600 on every start, outside `/data`, with `funcd`/`all` and `*`/`ready`,
  passes `-http-adv-addr 127.0.0.1:4001` and Raft on `127.0.0.1:4002`, and execs `rqlited` with an empty
  environment, so the password does not reach rqlited's environment. The arguments live in `engineargs`, which imports
  only the standard library.
- The Dockerfile pins both bases by digest, checks out the rqlite tag and verifies its commit against `rqlite.pin`,
  builds statically (the static build worked; no glibc fallback was needed) and fails the build if the binary
  requests an interpreter. The final image is distroless static with `USER 65532:65532`.
- The catalog row `sql` is an add-on with no binding yet; the bindings, `status.bindings` and the delete-while-bound
  rule are left to ADR-0233, as the ADR's Scope says. No other scope creep.

### Definition of Done

13 / 14 items hold (the ADR's 9 Review-checklist items and 5 Definition-of-done items). Miss: "the gate passes" —
`just ci` would fail on the gofmt drift in `metadata.go` once committed (Major 1, `model`).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0232 (implementation) → changes-requested, 0/1/3, 3 model-attributed, DoD 13/14.
See `docs/reviews/model-scorecard.md`.

### Recommendation

Back to `adr-impl` for Major 1 (one `gofmt -w`) and Minors 1 and 2 in the same pass. The re-review is a gofmt check,
the touched packages' tests and a look at the admission signature; then stamp ADR-0232 `Implemented`, F128
`implemented`, and move the feature's board card.
