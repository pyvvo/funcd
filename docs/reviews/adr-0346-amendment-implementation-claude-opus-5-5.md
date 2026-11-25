## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0346 amendment, implementation, model: claude-opus-5-5)

This independent review covers the fix of how a container worker reaches funcd under `funcdctl dev` in a VM (ADR-0346
Decision 5, amended in place on 2026-11-25), on `origin/main` 9cf2cb77, before the commit was rewritten with the fixes
below.

Read-only review. Nothing was written in the worktree; the reverted run used `go test -overlay`. Nothing ran on Docker,
colima or a real nerdctl here: the real runs are the author's.

### Blocker

None.

### Major

None.

### Minor

1. **Three texts keep the former rule or miss the new test** · here.
   - `internal/runtime/pair.go:18-19`: `EndpointHost` is still "a name that resolves to the host's gateway on an engine in a
     VM". It now resolves to the address of Decision 5, and to the gateway only without one.
   - `docs/adr/0346-…:66-67` and `:77-79`: "Measured: colima's host gateway" and "`funcd.host:host-gateway` resolved to
     `192.168.5.2`" on colima. By the commit's own account that holds for colima's Docker and not for its containerd, and
     neither line names the engine.
   - `docs/behavior/funcdctl-dev.md`, "Such a worker's network": its VM half cites no test. `TestWorkersReachTheHostOfTheirVM`
     holds it now.
2. **The helper's failure has no test** · here · `internal/provider/docker/workers.go:390-393`. By reading it is right: the
   error returns before the output pipe and the container, as the host-network helper's does, the instance keeps its state,
   and `hostAddr` stays empty, so the next Start asks again. A fake `Exec` that fails once, then answers, would hold that.
   With it, "reads once" (`:439-440`) is loose: two first Starts at once each read it, harmlessly, with the same result.

### What is right and must stay

- **Check 1, the address.**
  - No such name, as on Docker Desktop or OrbStack by the brief: `limaAddress` gives "" and the engine's `host-gateway`
    stays, which the test's last three cases hold (a comment, a name that only ends in it, an address that does not parse,
    an empty file).
  - The host's network: `Start` asks only in its `else`, so no engine that shares the host's network is asked. A rootless
    engine fails at `Create` before any Start.
  - Concurrent Starts: `hostAddr` is read and written under `mu`, the helper's name carries random bytes, and the value is
    the same each time. By reading, with `-race` on the sequential test.
  - The helper is the one `exec` uses elsewhere, bounded by `helperTimeout`.
- **Check 2, `runSpec`'s callers.** Two: `Start` (`:382`), which adds the extra host for an engine in a VM, and `Exec`
  (`:732`), which takes its `Mounts` only. The host's network keeps an empty `ExtraHosts` (`workers_test.go:395`).
- **Check 3, the texts.** ADR-0346 Decision 5's amended sentence, its Contracts line, the header's entry, the constants'
  comment and `Start`'s comment match the code, Minor 1 aside. The ADR says that nerdctl's gateway is the VM itself, which
  is the case it leaves for a VM that gives no such name.
- **Check 4, the reruns.**

  | Run | With the fix | With the old line back (overlay) |
  |---|---|---|
  | `TestWorkersReachTheHostOfTheirVM`, `-race` | pass | fails: expected `funcd.host:192.168.5.2`, got `funcd.host:host-gateway` |
  | `TestWorkersThroughNerdctl`, `-race` | pass | fails: no `run -d` call holds `--add-host funcd.host:192.168.5.2` |

**Not checked**: Docker Desktop, OrbStack and real engines, by the brief; whether the helper's image can be missing at a first Start.

### After the review

Both Minors are fixed in the same commit: the three texts, the row's test, a test of the helper's failure, and the comment.
By the author: seven mutants of the change, each caught by a test, and runs of the InfraStack add-on under the fixed
`funcdctl dev` on a Mac, where its random-stack turned `Ready` on colima's containerd (nerdctl 2.3.1) and on its Docker.
