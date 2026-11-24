## Verdict: pass — 0 blockers, 0 majors, 3 minors (ADR-0351 implementation, model: claude-opus-5-5)

This report reviews ADR-0351's part of the branch `feat/jobs` at 4c46aeaa: the 18 commits after the docs commit
afe2468a, which main holds as the squash db7cb569 (#1551). ADR-0347's part has its own report,
[adr-0347-implementation-claude-opus-5-5.md](adr-0347-implementation-claude-opus-5-5.md). The reviewer did not write
the work, and every check below was run, not read.

The stamp rests on the decider's ruling of 2026-11-23 on #1018 (option A): the `GET /job/run` of
`scenario: job-runs-have-their-own-sockets` is ADR-0348's facet, so this PR's test checks that two `Running` runs hold
two sockets and that each run's `ctx.run` names that run, and the `GET /job/run` assertion joins that test with
ADR-0348's implementation.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

- **A run re-created under the name of a run that is still being removed can lose its local API socket** · model ·
  `internal/function/jobexec.go:56` and `:151`. Decision 2 says the executor creates a run's worker only once no
  instance of that name remains, "so a reused run name never shares a socket or a worker". `nameFree` waits for the
  instance alone, while the old executor removes the socket last: its deferred `removeSocket` (`:56`) runs after the
  deferred worker removal and the egress index's removal (`:68-74`). The socket's key is the ref (namespace, Job, run),
  and `SocketFor` returns an existing listener for the same ref (`TestEveryRunHasItsOwnSocket`), so a new run that
  passes `nameFree` before the old socket goes binds the old listener, which the old executor then closes. An overlay
  probe on the executor rig, with the old run's index removal held, recorded `bind default/hello/hello-a`,
  `bind default/hello/hello-a`, `remove default/hello/hello-a` while the second run was in its handler. Without an
  injected delay the window is the old executor's emptyDir release and index removal; it was not observed there. The
  emptyDirs, keyed by the same instance ID, sit in the same window (not checked).
- **A KV table's owner kind is not narrowed** · model · `api/types/v1alpha1/kvstore.go:120`. `validateTables` checks
  no owner, so a KVStore whose table is owned by `{kind: Lake, name: x}` passes `Validate` (an overlay probe), and the
  API's schema enum lists `Lake` too. The Contract says each field's admission narrows `WorkloadRef`'s kinds, a
  Workflow's tables refuse a Lake owner ("a Lake, which owns no table", `api/types/v1alpha1/workflow.go`), and the
  behavior row in `security.md` gives a Lake only a database or a prefix. Such an owner writes nothing, so the mistake
  surfaces only as a 403 at the write.
- **The doc comment of `JobSpec.FunctionSpec` is split** · model · `api/types/v1alpha1/job.go:91-96`: commit 27e21884
  put `isPrincipalObject` between its two halves, so the method's comment begins mid-sentence ("run over them
  unchanged") and the marker's begins with the method's first line.

### Routed (not this work's)

- **`GET /job/run`** of `scenario: job-runs-have-their-own-sockets` · elsewhere: ADR-0348's implementation, by the
  decider's ruling on #1018.
- **ADR-0346's per-worker listener**, which Decision 2 says takes the whole ref · elsewhere: ADR-0346 is Accepted and
  not built, so its listener takes the ref when it is built.
- **The pinned example modules and the merge with main** · integration: as in ADR-0347's report, the go.mod bump to the
  releases of funcd-functions#41, funcd-typescript#117 and funcd-python#137 makes `TestExampleResourcesDecode` pass on
  the pinned tags (it fails there now and passes with the three modules in an uncommitted go.work), and a rebase onto
  origin/main that drops afe2468a resolves the add/add conflicts in the ADR and feat docs.

### ✅ Verified correct (keep it)

- **The checks**: the same runs as ADR-0347's report, on the 43 touched packages through `scripts/agent/d`: build,
  vet and `go tool golangci-lint run` (0 issues, also for linux and with `--build-tags dev`), and `go test`, exit 0.
- **The scenarios**: `go test -race -tags e2e -count=1 -run 'TestScenarioJob' ./pkg/funcd/ -v` exits 0. Seven of the
  eight run on the process driver and the real Node shim in `pkg/funcd/job_principal_e2e_internal_test.go`, each with
  `t.Parallel()` and at most 1.6 s: a write of the Job's prefix as itself, its Secret, a forbid that names
  `Job::"default/tofu"` while a same-named Function still reads, the Lake and add-on proxy tokens, the S3 key pair, the
  writer role, and the two sockets with `ctx.run`. `job-egress-named` has `TestScenarioJobEgressNamed` on the egress
  index and the PDP (`pkg/funcd/own_principal_internal_test.go:113`), and its case on the egress lane, which passed with
  the rest of that lane at 2f78059b (`.cache/lanes/feat_jobs-egress.log` of the main checkout: the run under the policy
  that names the Job reaches the host, the Function of its name does not, and a run under an empty `appliesTo` does
  not). The commits after 2f78059b change docs and the job lane only.
- **The checklist**:
  - no path authorizes a Job's worker as a Function, nor the reverse: the local API's principal is the ref's kind
    (`internal/workernode/local/local.go`), the facets read bindings through `services.CallerBindings`, which reads the
    ref's kind (`internal/services/bindcache.go:89-105`), and the S3 key code `J`, the Lake token's `kind` claim and the
    add-on token's `<kind>/<ns>/<name>` read back by kind (`TestDeriveKeypairKindsDiffer`,
    `TestWorkloadLakeTokenNamesItsKind`, `TestJobCallsAsItself`, `TestJobIsItsOwnPrincipal`);
  - every owner and `appliesTo` item is a `WorkloadRef`, and each write check compares kind and name through
    `workloadUID` (`internal/auth/cedar/capabilities.go`); the pool access index counts Function owners and items only
    (`internal/function/poolaccess.go`);
  - every run has a socket of its own and a ref of any other kind, the zero value included, gets none and a 403
    (`TestEveryRunHasItsOwnSocket`, `TestARefOfAnotherKindIsRefused`); an empty `appliesTo` reaches no Job
    (`EgressDomains`, the compiled namespace scope `principal is Function`, `TestEgressAppliesToKinds`).
- **The contracts**: `WorkloadRef` with `FunctionOwner`, `JobOwner` and `PrincipalKindJob`; `local.Ref` with `Kind`
  and `Run` and `Manager.SocketFor(ref)`; `DeriveLakeToken(master, ns, kind, name)`; `addon.Token(master, ns, kind,
  name)`; the entity type `Job` as a principal; the S3 code `J`; and the three field changes of the Contracts table.
- **The egress index of a run's worker**: the executor indexes it as its Job before the call and removes it after the
  worker (`jobexec.go:93`, `:104-114`), and the worker sync reads its run's `spec.job` by the worker's name
  (`pkg/funcd/funcd.go`, `egressPrincipal`; `TestJobRunWorkerIsInTheEgressIndexForItsCall`).
- **The behavior rows**: `security.md` (the owners, the principal, the local API, the keys and tokens) and `egress.md`
  (`appliesTo` items, a run's worker), each citing a test that exists; the guide's Secrets, KV, SQL, Bucket and
  networking pages name the `{kind, name}` form.
- **Tracking**: the ADR's substance is unchanged (`git diff` of the ADR shows the status line only), and F140 and F151
  were `reviewing`.

### Definition of Done

14 of 14 items hold: the 4 Review-checklist items (the sockets one under the ruling above); `just ci-full` and
`just test-linux` through the touched packages' checks and the egress lane, which the gate runs in full next; the
plan's behavior rows and guide pages; and the 7 generic items (behavior with no stub, contracts, the plan's surface,
ADR-0002 conventions, dependencies, scope, tracking).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0351 (implementation) → pass, 0/0/3, 3 model-attributed, DoD 14/14. See
`docs/reviews/model-scorecard.md`.

### Recommendation

ADR-0351 is stamped `Implemented`, and F140 and F151 turn `implemented`. The three Minors go back to the builder with
ADR-0347's round; none of them changes the decision. Feature issues #1018 and #1027 are the ones that the implementing
PR closes, once ADR-0347 passes too.
