## Verdict: pass — 0 blockers, 1 major and 1 minor, both fixed before merge  (ADR-0249 implementation, model: claude-opus-5-5)

This reviews ADR-0249's implementation, built in Decision 6's three steps:

1. pyvvo/funcd#1095, merged as `f1adb743`: the `GET /secret` routes, the `Secrets` port and its facade, and
   `secrets.InjectorIdentity`, while funcd still injected the environment.
2. pyvvo/funcd-typescript#81 and pyvvo/funcd-python#95: both shims read `ctx.secret` from the routes, released in
   `v0.19.0` and `v0.14.0`.
3. pyvvo/funcd#1099, head `91fd2c3b`, merged as `cd914428`: `94e7fe7c` stops the injection, and `91e2827d` fixes a
   test; `cb8f9075` and `91fd2c3b` pins the shims.

The review found two defects, attributed to the model, both fixed before the merge:

- **Major**: on #1099, `TestScenarioProviderEnvUnchanged` failed 3 runs out of 3. Its CatalogService bound Secret
  `quack` without declaring it, which ADR-0251's `binding-declared`, on the same branch, refuses. `91e2827d` declares
  it.
- **Minor**: before #1095 merged, `GET /secret` answered `{"keys":null}` for a Function with no bound Secret. The
  Contract's list is an array, and both shims iterate it. `8f82f78c` returns `[]`, and
  `TestSecretListWithNoBindingIsEmpty` covers it.

Before the merge, the builder's lanes also found two handlers that read a bound secret from the environment, which
Decision 5's clean break leaves empty. funcd-typescript's env-echo example (`API_KEY`, and `SHARED`, which the Secret
shadows) and the todo App's token check now read through `ctx.secret`, in pyvvo/funcd-typescript#84 (`v0.19.1`) and
pyvvo/funcd-functions#19. The todo fix still refuses a missing or wrong token, and all ten `requireToken` calls are
awaited; an un-awaited async check would never refuse. A scan of the four repos found no other handler that reads
one.

The builder's full e2e run on the branch found two older tests that this step breaks, fixed in `4ca836e5`:
ADR-0250's split-by-secrets pool handlers now read `DB_PASS` through `ctx.secret`, and ADR-0248's pooled-config test
expects the shared env image's new `envStripe` field.

### ✅ Verified correct (keep it)

- **Checks**:
  - The builder's `scripts/agent/gate.sh` log shows every step passed on `91fd2c3b`: audit, `just ci-full` with
    e2e (123 packages `ok`, 0 lint issues with and without `-tags dev`), linux, linux-tests and tree-unchanged.
  - On #1099, `changes`, `pr-title`, `ci` and `e2e` passed on `91fd2c3b`.
  - With `-race`, through a `go.work` on the round-2 language-repo heads, the review ran the scenario tests:
    - `TestScenarioSecretReadFromFuncd`, `TestScenarioSecretDenied`, `TestScenarioSecretPooledMember` and
      `TestSecretListWithNoBindingIsEmpty` (`internal/workernode/local`);
    - `TestScenarioSecretShadowsConfig` (`internal/function`);
    - `TestScenarioSecretReadLive`, `TestScenarioSecretNotInEnv` and, after the fix, `TestScenarioProviderEnvUnchanged`
      (`pkg/funcd`, `-tags e2e`).
- **Decision 1**: the routes take the caller from the sandbox's fixed Ref, and a pool member from `X-Funcd-Member`, as
  KV and SQL do; the request never names the caller.
- **Decision 2**: each read resolves the caller's current `spec.bindings.secret` through the PDP-checked resolver,
  under `secrets.InjectorIdentity`, with no cache.
  - A later Secret's key wins, and `FUNCD_` keys drop.
  - A key no Secret holds is a 404, and a failed check is `fault.WriteProblem` (403 on a deny).
  - Values are never logged.
- **Decision 3**: `addBindingEnv` writes only the bound config keys and `FUNCD_CONFIG_KEYS`, on both worker paths and
  in a pool's shared environment.
  - No secret value and no `FUNCD_SECRET_KEYS` reach a worker.
  - `ConfigKeys` leaves out a key a Secret also holds, and is already guarded against `FUNCD_` keys.
  - `envresolve` is unchanged, so a CatalogService engine keeps `QUACK_TOKEN`.
  - The reconciler still resolves the Secrets before it starts a worker, and holds the Function with
    `SecretResolveFailed` when that fails.
- **Decision 4**: both shims keep ADR-0245's API.
  - `get` path-encodes the key and reads a 404 as `undefined` or `None`.
  - `list` returns the route's keys.
  - Any other status throws or raises with its status and body, as KV does.
  - The pool clients send their member's name. Python reuses the socket class of `invoke.py`.
- **Lanes**: every lane passes on the released pins, from the XML files the review read:
  - duckdb (11 cases), egress (5), env-echo (16), fn-to-fn (4), funclog (16), http-api (3), kv-counter (3),
    metastore (2), s3 (8), sql (9) and workflow (14), on `cb8f9075`;
  - sql-examples (4) and app-todo (50), on `91fd2c3b`;
  - none has a failure or an error. After `cb8f9075`, the PR changes only tests, the App part-root helper (which
    app-todo reruns) and the funcd-typescript pin (which sql-examples reruns).
- **Scenarios**: each of the eight has its test. `secret-unbound` is checked inside `TestScenarioSecretReadFromFuncd`
  on the funcd side, and has its named test in both shims.
- **Tracking**: ADR-0249 is at `Reviewing`, and only its status line changed; F161 is at `reviewing`. #1095 and the
  shim PRs say "Refs #1071", and #1099 carries `Fixes #1071`.

### Definition of Done

16 / 16 items hold:

- the ADR's Review checklist, 5 of 5;
- its done-when, 2 counted: the funcd checks (build, lint, `-tags e2e` and `-tags dev` tests, `go mod verify`), and
  both language repos' CI with every scenario named;
- the generic DoD, 9 applicable: suite green, scenarios, real behavior, Contracts, tree, conventions, dependencies,
  scope, tracking. The per-driver contract suites do not apply: the `Secrets` port has one driver, the facade.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0249 (implementation) → pass, 0/1/1, 2 model-attributed (fixed before merge),
DoD 16/16. See docs/reviews/model-scorecard.md.

### Recommendation

#1099 merged and closed #1071. This review stamps ADR-0249 `Implemented` and F161 `implemented`.
