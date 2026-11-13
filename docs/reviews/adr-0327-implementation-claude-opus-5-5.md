## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0327 implementation, model: claude-opus-5-5)

One independent subagent reviewed the work in two rounds.

- **Round 1** reviewed the branch at `e83dec7c`, which holds the implementation `675d3de6` on `33104f35`. It used the
  unreleased shims (pyvvo/funcd-typescript `205ef11`, pyvvo/funcd-python `18ab28a`) through a local go.work that git
  ignores.
- **Round 2** reviewed the branch rebased on `3f179b55`:
  - the same four patches (`git range-diff` marks each `=`);
  - `47aa6219`, which pins funcd-typescript v0.28.0 and funcd-python v0.23.0 (`go.mod` and `go.sum` only; the go.work is
    gone);
  - `ef4e1d5e`, which fixes the audit's flag.

[ADR-0328](adr-0328-implementation-claude-opus-5-5.md) renames the TypeScript delete. It has its own report.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **A behavior row changed during the move** · `model` · `docs/behavior/secrets.md`, the row "A bound Secret is missing".
  - The ADR's *Behavior rows* say that security.md's Secret rows move "unchanged".
  - The row used to say `Failed`, `SecretResolveFailed` (ADR-0221 Decision 3, `TestIssue77_MissingBindingRecoversWhenApplied`).
    It now says `Pending`, `SecretNotFound` (ADR-0248 Decision 6, `TestScenarioSecretMissingUnchanged`).
  - The new text matches the Function type's doc (`api/types/v1alpha1/function.go:96`) and the e2e test, so the
    correction is right. It was still outside this ADR, and nothing named it.
  - Round 2: closed. The PR body names the correction and says why the old text was wrong.
- **`SecretWriters` copied `SQLWriters`** · `model` · `internal/services/roles/writers.go`.
  - At `47aa6219`, the gate's bloat audit failed on one hard flag, `dupl`. The two filters differed only in the action
    and the scope kind.
  - Round 2: closed in `ef4e1d5e`. Both filters now call `writersOf`.
  - At `ef4e1d5e`, `python3 scripts/agent/audit.py --base origin/main` reports no hard flag and PASS.
    `TestSQLWriters` and `TestSecretWriters` pass under `-race`. The SQL e2e tests `TestScenarioOwnerWrites`,
    `TestScenarioNonWriterWriteRefused` and `TestScenarioWriterRoleGrantsWrite` pass.

### Routed (not this work's)

- **F240's issue** · home: elsewhere · destination: tracker #1430.
  - In round 1, the row F240 that ADR-0328 realizes had no feature issue.
  - Resolved: the tracker's table records F240 under #1432 ("#1432, with F236"), so the PR's `Fixes #1432` covers both
    rows.

### ✅ Verified correct (keep it)

- **Checks at `ef4e1d5e`.** Each ran through `scripts/agent/d` and exited 0:
  - `go build ./...`;
  - `go vet ./...`, with `-tags e2e ./pkg/funcd`, and with `-tags dev ./cmd/funcdctl`;
  - `go mod verify`, and `go mod tidy -diff` with no diff;
  - `go test -race -count=1` on `api/types/v1alpha1`, `internal/auth/cedar`, `internal/function`, `internal/secrets`,
    `internal/services/roles`, `internal/services/secret` and `internal/workernode/local`;
  - `go test -tags e2e -count=1 -run Secret ./pkg/funcd/`: 25 tests pass on the pinned modules, `ok` in 13.2 s;
  - `just lint` (0 issues), `just check-hygiene` and the audit.

  `specgen`, run into a scratch directory, regenerates the API schema and every guide `spec.md` exactly as committed.
- **Checks in round 1**:
  - `just check-specs`: 19 specs, 0 failing;
  - `just check-behavior`;
  - `-race` on `internal/auth/...` and `scripts/behavior`;
  - `go test -race ./pkg/funcd/` without the e2e tag: `ok` in 56 s.
- **The gate at `47aa6219`**: ci-full PASS, linux PASS, stress PASS and tree-unchanged PASS. linux-tests was SKIP because
  its VM was not running; CI runs that suite.
- **The shims as released.**
  - funcd-typescript v0.28.0's `shim/` (from the module cache) differs from `205ef11` only in two things: the version, and
    `put`'s doc comment, which no longer names an ADR (`src/secret.ts`, `api.json`).
  - funcd-python v0.23.0 differs from `18ab28a` only in that doc line (`secret.py`, `api.json`).
  - Round 1 ran each repo's checks on those commits. TypeScript: `just test`, `just typecheck`, `biome ci` and
    `just go-check`. Python: `ruff format --check`, `ruff check`, mypy (41 files), pytest (481 passed) and `just go-check`.
- **The Contracts.** Every row is present, with its exact signature:
  - `ActionSecretWrite`, and `secret-write-base` / `secret-single-writer` in `builtin_secret.cedar`.
  - `isWriteAction`; `builtinRoles`, with `Reader`, `Contributor` and `Owner` untouched; `scopeHead`'s `Secret`.
  - `WriterLister.SecretWriters` and `(*Lister).SecretWriters`, which take the Namespace and Secret scopes and the write
    action only.
  - `SecretCapabilityWithWriters`, and `secretResource`, which gives a bare entity for a missing Secret.
  - `CheckWrite`.
  - The Facade's `Put` and `Delete`, with `writeAttempts = 5`.
  - The local `Put` and `Delete`, the two routes and `maxSecretBytes`.
  - `accessIn` and `poolKeyFor`, and the PDP registration in `pkg/funcd`.
- **Decision 4, in the design model's order.**
  - The order: the binding check (403), the Get (404), `CheckWrite` after the read (403), the change, then
    `store.Update` on the read's resourceVersion. A Conflict starts the attempt over; after 5 updates the write answers
    409.
  - The PDP reads the store itself (`cedarMetaReader{c.store}`), so the decision sees the stored owner after the write's
    read, as the spec's *Environment* assumes. Get decodes a fresh object, so the change in place is safe under `-race`.
  - A put checks its value as a reader does (`EnvDataProblem`), and the whole result as the API does (`Validate` in
    `store.Update`).
  - A body over 1 MiB gets 413.
  - No value reaches a log: the logged errors carry only the binding, the key and the problem.
- **The design model.**
  - `key-write.fizz` passes at 463 states, and each of its 5 mutants fails the assertion it names. `key-write.md` is
    current.
  - The four assertions carry oracle markers on passing tests in `internal/services/secret/secret_test.go`. Those tests
    force each interleaving with a resolver hook, and they count the 5 updates against a store that always conflicts.
- **The scenarios.** Each has one named test:
  - In e2e on real Node workers, each in parallel and taking 0.27 to 0.89 s: `TestScenarioSecretOwnerWrites`,
    `TestScenarioSecretWriteRefused`, `TestScenarioSecretWriterRole`, `TestScenarioSecretApplyReplacesWrittenKeys` and
    `TestScenarioSecretOwnerSplitsThePool`. The pool-split test now requires node, as its neighbours do.
  - On the routes: `TestScenarioSecretWriteChecksTheValue`.
  - On the facade: `TestScenarioSecretConcurrentWrites`.
- **The pool key.** An owned and bound Secret joins the key as `secret/<name>`. A Secret change re-keys the namespace's
  pooled Functions through `function.AccessKinds()`. A test pins that list, which now also covers SQLDatabase's failed
  List.
- **The behavior registry.**
  - The Secrets chapter is in `chapters()` and in the README.
  - The chapter holds the three new rows.
  - `writeAttempts` appears in secrets.md, and `maxSecretBytes` in workers.md.
  - security.md's pool row cites Decision 6.
- **The cost.**
  - The PR body shows main against the branch: `ResolveEnv` goes from about 27 to about 59 µs/op, and `CheckRead` from
    about 5.9 to about 16.5 µs/op.
  - This review measured 67 to 72 µs/op and 19 to 21 µs/op on the branch.
  - Both are far under the 50 ms `DependencyCheckBudget`.
- **Tracking.**
  - After acceptance, the ADR's only edits are its status line and ADR-0328's `Superseded in part by` line.
  - ADR-0321 and ADR-0322 gained only their header lines.
  - The blueprint names the write, and F236 was at `reviewing`.

### Definition of Done

19 of 19 items hold:

- the 6 Review-checklist items;
- the 7 Done-when items: `just ci` and the e2e suite (both through the gate's ci-full), the scenarios, `just check-specs`,
  `just check-behavior`, both shims released and pinned, and the PR's benchmarks before and after;
- 6 generic items: no stub, the Contracts, the tree, the conventions, only the sanctioned module bumps, and tracking.

The gate ran at `47aa6219`. The later `ef4e1d5e` refactors two filters, and the checks above cover it. CI runs the whole
suite again on the PR.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0327 (implementation) → pass, 0/0/2, 2 model-attributed, DoD 19/19. See
docs/reviews/model-scorecard.md.

### Recommendation

Both ADRs are stamped `Implemented` (2026-11-13): ADR-0327 with F236 `implemented`, and ADR-0328 with F240. The PR
closes #1432.
