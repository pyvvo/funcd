## Verdict: pass — 0 blockers, 0 majors, 3 minors  (ADR-0145 amendment, implementation, model: claude-opus-5-5)

This independent review covers the fix of `funcdctl dev`'s platform for container workers (ADR-0145 Decisions 3 to 6,
ADR-0346 and ADR-0355 amended in place on 2026-11-25) and one sentence of ADR-0358, on `origin/main` 1d190ebc, before
the commits were rewritten with the fixes below.

Read-only. The reverted runs used `go test -overlay`.

### Blocker

None.

### Major

None.

### Minor

1. **ADR-0358's new sentence says more than the code does** · here (ecdc9e3c) · Decision 5, "may name another field, which
   finds it from then on".
   - A target's call lists its objects by the kinds of the revision that the target pins: `internal/services/addon/settings.go:305`
     takes `rev`'s settings and `:321` lists with them. So until the target moves, the former field still says what it is sent.
   - Admission uses the Addon's own kind (`internal/controlplane/addon.go:705`).
   - Read now; run in round 1 of ADR-0358's review at ce68555f: both replaces answered 200, the old objects read
     `TargetNotFound` while their Function still applied them, and a new one got 422.

   Fix: say that the new field finds the target for a write and an object's status from then on, and that a target which
   pins the former revision is sent the objects that the former field names until it moves.
2. **Three lines keep the former rule** · here.
   - `docs/adr/0355-…:97-98`: the rejected "one bundle with both platforms' providers" still says that `funcdctl dev` on a Mac
     "could pull it (Consequences)". The Consequences now say that dev pulls the engine's bundle, so that benefit is gone.
   - `internal/function/function.go:713-715`: the gate's comment says "built for no platform the node runs", where
     `placeable`'s own comment now names the container engine's.
   - `internal/function/pool.go:129`: "whose artifact no node can run", the same. Under dev no such runtime pools, so it is
     only wording.
3. **The no-engine case of a Job has no test** · here · `TestDevAddonWorkerPlatformWithNoEngine` runs a Function only. By
   reading a Job is the same: `job.go:99` calls the same `placeable`, and its runtime gate follows. A rootless engine is
   tested at the worker driver (`TestWorkersRefuseAnEngineWithoutTheHostsNetwork`), not at a gate.

### What is right and must stay

- **Check 1, every path.** One function decides, `workerPlatform` (`internal/function/addonruntime.go:126`), on the predicate
  that `addonImage` uses: an `ImageRunner` and a runtime that is none of funcd's own.
  - Placement: both gates through `placeable` (`function.go:716`, `job.go:99`), and each replica's `Schedule` (`:1983`).
  - Fetch: `fetch` is the one caller of `Materialize` in the reconciler, found by grep. Through it go the run's executor
    (`jobexec.go:60`), both contract reads (`contracts.go:168`, `:208`), held revisions and pools (`pool.go:733`, `:736`).
  - No missed caller. No wrapper hides `MaterializeFor`: `pkg/funcd` passes the materializer as it is, and dev's is one too.
  - No process worker takes the engine's platform: dev registers shims for node and python only (`dev.go:1728`, `:1743`), so
    any other runtime has none and no pool host.
- **Check 2, no engine.** The skip leaves the runtime gate, which fails an Addon's runtime with the engine search's reason,
  and any other with no shim. Past the gate, a replica's `Schedule` gets `WorkerPlatform` "", so the node's platform judges
  it and the node's bundle is fetched: a bundle that does not fit is refused, by reading. A failed `Platform` call at
  `NewWorkers` becomes `NoImages` with its reason (`dev_addon.go:96-98`).
- **Check 3, the cache.** The cache dir and the fill are keyed by digest and platform, and `Materialize` is
  `MaterializeFor(m.node)`, so a server's dir names are as before. The `.platforms.json` file holds the list, which is the
  same for every platform.
- **Check 4, the texts.** ADR-0145's, ADR-0346's and ADR-0355's amended lines, the two behavior rows and the guide
  paragraph match the code, Minor 2 aside. ADR-0145 states the emulated-image cost.
- **Check 5, the reruns.**

  | Run | With the fix | `function.go` and `addonruntime.go` from origin/main, which compile |
  |---|---|---|
  | `internal/function`, `-race` | ok, 3.7 s | `TestDevAddonWorkerPlatform` fails ("node local runs darwin/arm64") and `TestDevAddonWorkerPlatformWithNoEngine` fails; the rest pass |
  | scheduler, runtime, provider | ok | ok: the two files are not in these packages |
  | `TestScenarioDevAddonWorkerPlatform` on Docker | pass, 1.5 s | fails after 180 s: the Function never turns Ready |

  One run beyond the list, for check 3: `internal/artifact`, `-run 'Materialize|Platform' -race`, ok.

**Not checked**: dev's App-template mode and a real nerdctl, only by reading and by the fake; the held-revision path with no engine, only by reading; issue and board searches for the three Minors.

### After the review

The three Minors are fixed in the same commits: ADR-0358's sentence says what the code does, ADR-0355's alternative and the
gate's comment follow the rule, and `TestDevAddonWorkerPlatformWithNoEngine` covers a Job. `pool.go`'s comment stays: a
pool's members run on the node. By the author: 16 mutants of the change, each caught by a test, and a run of the InfraStack
add-on under the fixed `funcdctl dev` on a Mac's Docker, where a module pushed with `funcdctl infra push` applied
(`Succeeded`) once the add-on's OpenTofu image carried funcd-python's shim v0.31.0 (pyvvo/funcd-addons#32).
