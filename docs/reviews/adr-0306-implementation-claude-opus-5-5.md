## Verdict: pass — 0 blockers, 0 majors  (ADR-0306 implementation, model: claude-opus-5-5)

Reviewed at `a35b1083`: the implementation commit `75bdb89c` and the `Reviewing` stamp, on main `3ab55014` (#1372).
The reviewer ran the checks below and changed none of the work. As the parent session asked, it did not run
`scripts/agent/gate.sh` or the Lima lanes again. They ran on `3e951404`, which differs from `75bdb89c` only in the
documents that main brought in (#1369, #1372); no Go file differs.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **m1 · `adr` — Consequences' "33 fewer production lines" does not hold against main.** `git diff --numstat
  3ab55014 75bdb89c` over the non-test files of `internal/function` gives +380 −297: 83 more lines, 57 more without
  comments and blank lines. The ADR's number is the experiment's second commit, `4e71a184`, measured against its
  first, `0e43b602` (net −33); the two together add 80 lines to their base. The code follows the Implementation plan,
  so this is not the builder's error.
  The number sits in Consequences, not in a Decision or a Contract, so the frozen ADR needs no successor for it. The
  later ADRs of this series should measure against main.
- **m2 · `model` — an assertion's message names an order that the assertion cannot see.**
  `internal/function/lake_internal_test.go:133` asserts `require.Empty(t, be.worker.own, "the Lake hook holds the
  consumer before its token is derived")`. `bindings` returns a zero `worker` with every gate
  (`internal/function/workerenv.go:70-72`), so the assertion holds in any order. Mutant 13 below derives the token
  before every check and passes the test. The hand-over says the test "now asserts that the Lake hook holds the
  consumer before the token is derived"; it does not. The order has no observable effect, because the derivation
  cannot fail (gap 2 below). Fix: drop the assertion, or word its message as what it checks (a held result carries no
  own env). The `LakeNotReady` assertion above it is sound.
- **m3 · `model` — the Lake hook's comment names a guard that `addBindingEnv` does not have.**
  `internal/function/workerenv.go:220-221` says the Lake keys go "never through addBindingEnv, whose FUNCD_ guard would
  drop them". `addBindingEnv` writes only the config keys of an `envresolve.Resolved` (`internal/function/secrets.go:45-52`),
  and the guard is `secrets.MergeEnvGuarded` inside `envresolve.Resolve` (`internal/envresolve/envresolve.go:93`,
  `:107`). With the hooks, no path can send the Lake keys through config. Fix: drop the sentence.

### The hand-over's gaps, judged

1. **"33 fewer production lines"**: m1 above.
2. **The Lake token is derived after every check**: not a defect. Decisions 2 and 7 put `own` after every check, and
   Decision 2 keeps `LakeResolveFailed` as the reason when `own` fails. The case cannot occur:
   `lakegw.DeriveLakeToken` hashes the master into a 32-byte HS256 key (`internal/lake/gateway/token.go:35-38`) and
   signs two string claims (`:88-99`), and neither step can fail. For the same reason mutant 11, which ignores a gate
   from `own`, survives: no input reaches that path. No score.
3. **A ConfigMap's `AWS_ACCESS_KEY_ID` in a pool's shared env**: not a defect, and not new. On main, `poolSharedEnv`
   also wrote the config keys into the shared env. The value there is the ConfigMap's, not an issued key, so the
   checklist line holds; mutant 6, which puts the issued pair into the shared env, fails. Both pinned pool hosts merge
   a member's row last: funcd-typescript v0.24.0 starts a member with `env: { ...process.env, ...this.spec.env }`
   (`shim/pool.mjs`), and funcd-python v0.19.0 runs `os.environ.update(env or {})` in each member's process
   (`shim/src/funcd_shim/_poolworker.py`). So each member's handler gets its own issued pair.
4. **`mustWorkerSpec` lost its Lake env argument**: not a defect. Decision 7 takes the token out of
   `resolveLakeEnv`, and the helper now runs the real S3 and Lake hooks and `ownWorkerEnv`
   (`internal/function/secrets_internal_test.go:63-74`), which is closer to a reconcile pass than the old hand-fed map.
   Each changed call site drops one argument, and no assertion was weakened. The Lake tests got stricter:
   `resolveLakeEnv` must return exactly the URL, `lakeOwn` requires a nil `issue` for the token, and a Function that
   binds no Lake gets no token. The audit's soft `discard-test` note (`internal/function/volumes_internal_test.go:332`)
   is on an existing `_ = mustWorkerSpec(...)` line.

### Routed (not this work's)

- **`ctx.config` still lists a key that an issued key replaced.** Under Decision 3 the issued `AWS_ACCESS_KEY_ID`
  replaces a ConfigMap key of that name, but `FUNCD_CONFIG_KEYS` still lists the key, and both shims read a listed key
  from the env (`listedValue` in funcd-typescript, `listed_value` in funcd-python). So `ctx.config.get` returns the
  issued value for that key. A pool member already did this on main, and a solo worker now does the same. Nothing
  leaks: the handler can read its own pair from the env. Home: the ADR's Negative consequence. Destination: the
  decider, if `ctx.config` should drop such a key, in a later ADR. Nothing for the builder.

### ✅ Verified correct (keep it)

- **Contracts.** `workerEnv`, `boundEnv`, `workloadHook`, `ownWorkloadHook`, `workloadHooks`, `bindings`,
  `ownWorkerEnv` and `into` match the ADR's Contracts block exactly (`internal/function/workerenv.go`). The extra
  helpers are unexported: `checkAll`, `shimWorkerEnv`, `lakeResolveFailed` and `s3Hook.binds`.
- **One list in Decision 2's order** (`workerenv.go:62-64`), the two own parts asserted at compile time (`:56-59`), no
  hook wrapped. `checkAll` stops at the first hook that holds (`:86-88`).
- **Every gate keeps its reason, phase and requeue.** The config, data-reference and Lake gates build the same
  `gateFailure` values that `bindings` built on main (reason, message, ready message, phase, requeue). The root-entry
  and volume hooks call `ensureRootEntries` and `ensureVolumes`, as before.
- **Which key wins (Decision 3).** `into` writes the shared env, then the own env, then a freshly issued part
  (`workerenv.go:27-33`). In the shared env an earlier hook's key wins, so a ConfigMap still moves `AWS_REGION`,
  `AWS_ENDPOINT_URL_S3` and `DUCKDB_EXTENSION_DIRECTORY`, as on main. A ConfigMap cannot set a `FUNCD_` key, so writing
  `FUNCD_INVOKE_SOCKET` and `FUNCD_PORT` before the hooks' env changes no value.
- **Where the env goes (Decision 4).** The solo order is the Executor's own env, the hooks' env, then the volumes'
  (`internal/function/function.go:3314-3324`). The pool's shared env is the hooks' shared env and the record bound
  (`internal/function/pool.go:793`). Each row carries `memberEnv(path, own)` (`pool.go:775`). `createPool` adds the
  issued part through `withPairs` (`pool.go:808`), after `manifestSignature` has signed the rows (`pool.go:415`), whose
  `issue` field JSON does not encode.
- **Config joins the shared env (Decision 5).** `envresolve.Resolved` appears only in `resolveBindingEnv`,
  `addBindingEnv` and a local of the config hook. The seven functions take a `workerEnv` instead.
- **One env builder (Decision 6).** `workerSpec` builds both modes with `shimWorkerEnv`; a container worker's socket
  is the in-container path, mounted (`function.go:3302-3305`).
- **The Lake token in one place (Decision 7).** `lakegw.DeriveLakeToken` has one non-test caller,
  `lakeHook.own` (`workerenv.go:247`), and `resolveLakeEnv` returns the URLs only.
- **The rest of the behavior is unchanged.** `memberEnv`'s delete loop is gone with no row change. `poolRenewal`
  checks `m.issue != nil` (`internal/function/renew.go:205`), which holds for the same members as the old `s3`
  marker. A serving or held revision's worker gets the Function's hook env, as before: `revisionTemplate` replaces only
  the runtime, handler and image (`function.go:1811-1812`).
- **Scenarios, each with its named passing test** (`-v`, none skipped, each 0.02 s or less, each new test parallel):
  - `workload-hooks-hold-in-order` → `TestWorkloadHooks_HoldInListOrder`: `ConfigResolveFailed` with the poll requeue,
    then `LakeNotReady` Pending, and no worker created.
  - `a-worker-gets-each-providers-env` → `TestWorkloadHooks_ASoloWorkerGetsEachProvidersEnv`: all eight keys.
  - `a-pool-shares-only-what-is-shared` → `TestScenarioCtxPooledMember` and `TestPoolManifestCarriesMemberEnv`, unchanged
    apart from their `// scenario:` lines.
  - `issued-keys-win` → `TestWorkloadHooks_IssuedKeysWinOverConfig`, solo and pooled.
  - The plan's fourth test, `TestWorkloadHooks_AContainerWorkerDialsItsSocketInside`, passes. One regression test was
    renamed with the function it guards (`TestIssue450_InvokeSocketDocDescribesOnlyInvokeSocket`); none was removed.
- **Mutants, each run through `go test -overlay` on a copy:**

  | # | Change | Result |
  |---|---|---|
  | 1 | `into` writes the shared env last (main's solo order) | killed: `IssuedKeysWinOverConfig/solo`; `/pooled` passes, as it should |
  | 2 | the Lake hook before the config hook | killed: `HoldInListOrder` |
  | 3 | the config gate without its requeue | killed: `HoldInListOrder` |
  | 4 | the issued pair in a row before signing | killed: `PoolKeepsItsWorkerAcrossAWindow`, `PoolWorkerDrainsForRenewal` |
  | 5 | the S3 pair as stable own env, not issued | killed: `RenewalStatusPool`, `PoolKeepsItsWorkerAcrossAWindow`, `PoolWorkerDrainsForRenewal` |
  | 6 | the issued pair in the S3 hook's shared env | killed: `PoolManifestCarriesMemberEnv` |
  | 7 | `createPool` skips `withPairs` | killed: `PoolManifestCarriesMemberEnv`, `IssuedKeysWinOverConfig/pooled` |
  | 8 | the pool's shared env left empty | killed: `CtxPooledMember`, `PoolManifestCarriesMemberEnv` |
  | 9 | a container worker gets the host socket path | killed: `AContainerWorkerDialsItsSocketInside` |
  | 10 | the Lake hook without the DuckDB extension dir | killed: `DevLakeExtensionDirToConsumer` |
  | 11 | `bindings` ignores a gate from `own` | survives: no input makes the derivation fail (gap 2) |
  | 12 | `checkAll` runs the later checks after a hold, still reporting the first | survives (below) |
  | 13 | the token derived before every check | survives (m2) |

  Mutants 1 and 2 repeat the hand-over's first two, with the same result. Mutant 12: no test pins that a hold stops
  the later checks; with it, a Function held at its root entries would still reserve its volumes. Main's early
  returns were not pinned either, and the checklist asks only that the first hold wins, which mutants 2 and 3 show is
  pinned. A test of it is optional hardening.
- **Checks**, through `scripts/agent/d`: `go build ./...` exit 0; `go vet ./internal/function/...` exit 0; `go test
  -race -count=1 ./internal/function/...` exit 0 (`ok`, 12.5 s); `go tool golangci-lint run ./internal/function/...`
  exit 0, `0 issues.`; `gofmt -l internal/function/` prints nothing; `just check-hygiene` clean after the stamp. From
  the hand-over, not run again: `GATE PASS` in 1207 s (ci-full 903 s, the stress run of every changed test 20 times
  with no failure), `just lima-example-all` 13/13 and `just lima-example app-todo` PASS.
- **Conventions.** `internal/function` only; no exported API, `any`, `panic` or new dependency (`go.mod` untouched);
  the hooks are unexported value types; `check` takes the context first.
- **Tracking.** ADR-0306 went `Accepted → Reviewing` with its substance unchanged (`a35b1083` changes only the Status
  item), and F220 went `accepted → reviewing`.

### Definition of Done

17 / 17 items hold: the ADR's seven Review-checklist items, its "Done when" (the gate, the 13 lanes and the app-todo
lane, from the hand-over), and the nine generic items that apply (the contract-suite item does not: this ADR adds no
port). Misses: none; m1 sits in Consequences, outside the bar.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0306 (implementation) → pass, 0/0/3, 2 model-attributed, DoD 17/17. See
docs/reviews/model-scorecard.md.

### Recommendation

Stamp ADR-0306 `Implemented` and F220 `implemented`. The builder can fold m2 and m3, a test message and a comment,
into a later change. m1 needs no superseding ADR.
