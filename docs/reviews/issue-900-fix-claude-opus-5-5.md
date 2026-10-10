# Fix review — issue #900 (claude-opus-5-5)

- **Issue**: #900 — a PUT without a resourceVersion answers 409 when a controller writes the object's status between
  the server's read and its write (ADR-0210 Decision 3 makes such a write unconditional).
- **Change**: branch `fix/900-unversioned-replace-retries`, one commit `c9fc6525`
  `fix(controlplane): a write without a resourceVersion no longer fails with 409 on a concurrent status write`.
- **Files**: `internal/controlplane/handlers.go`, `internal/controlplane/kvhandover.go`,
  `internal/controlplane/concurrency_test.go`, `internal/controlplane/kvhandover_test.go`.
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass** — Blockers 0 · Majors 0 · Minors 0 · DoD 12/12

## What the fix does

`replaceObjIf` and `HandoverKVStore` now run their read → guard/precondition → admission → conditional
`store.Update` inside one closure passed to a new helper, `retryOwnRead`. The closure reports `lost` only when the
store refused the server's own read version (`fault.Conflict` from `store.Update`) and, for `replaceObjIf`, only when
the client sent no version (`want == ""`). `retryOwnRead` reruns the closure from a fresh read, at most
`ownReadAttempts = 5` times (the same bound as `storescaler.maxAttempts`), then returns the last 409.

## Verification (run, not read)

| Check | Result |
|---|---|
| Regression test fails without the fix (overlay of `origin/main` `handlers.go` + `kvhandover.go`) | **FAIL as required**: `replace` → `expected: 200 actual: 409`, detail `store.Update: Function "f" resourceVersion mismatch`; `kvstore handover` → `expected: 200 actual: 409`. That is the issue's reported cause. |
| Regression test passes with the fix, `-race -count=3` | PASS 3/3 (both subtests) |
| Mutant M3: drop the attempt bound (`if !lost {`) | killed — `replace`: expected 409 (status changes on every read), got 200 |
| Mutant M4: handover never reports `lost` | killed — `kvstore handover`: expected 200, got 409 |
| Mutant M1: drop `want == "" &&` (versioned writes also retry) | survives — **equivalent**: the retried attempt rereads and `staleVersion` answers 409 for the client's version, so the status code is unchanged; only the 409 detail differs (precondition text instead of the store's mismatch text). Not a test gap. |
| `go test -race -count=1 ./internal/controlplane/...` | ok (controlplane 9.1 s, admission 1.2 s) |
| `go build ./...` darwin + linux | ok |
| `go vet ./internal/controlplane/...` darwin + linux | ok |
| `golangci-lint run ./internal/controlplane/...` darwin + linux | 0 issues / 0 issues |
| `gofmt -l internal/controlplane` | clean |

User-visible behavior: the regression test drives the real HTTP handler (`send` → PUT on the route, and the handover
endpoint) over a store wrapper that lands a status write right after each server read, which is the issue's race
exactly. A real daemon race was not reproduced (timing-dependent and expensive); the handler-level test is sufficient
evidence. No e2e, no repo-wide test, no Lima lane run, per the per-review rule; the PR gate runs those once.

## Fix checklist (Definition of Done)

1. ✅ `TestIssue900_UnversionedWriteSurvivesStatusWrite` reproduces the race (`statusRaceStore` writes the status
   between the handler's `Get` and its `Update`).
2. ✅ Fails on pre-fix code for the reported reason (store's `resourceVersion mismatch` 409).
3. ✅ Passes with the fix, un-skipped, under `-race`.
4. ✅ Reverting the fix and two of three mutants fail a test; the survivor is equivalent (above).
5. ✅ Cause, not symptom: the server's own read is no longer a client-visible precondition. This is not a masking
   retry: every attempt re-reads and re-judges (guard, `staleVersion`, admission, handover's live-marker and spec.kv
   checks) the fresh stored object and still writes conditional on that read, so no verdict is bypassed. Only
   `store.Update`'s RV mismatch (its sole `Conflict` source, `internal/store/store.go`) is retried; guard and
   admission conflicts return `lost=false`. A client version keeps its 409, and the bound keeps a hot object at 409.
6. ✅ Scope: every hunk serves #900. The handover change is the same-cause sibling (server-own read version, no client
   version). The two test constructors were split (`concurrencyServerOn`, `kvServerOn`) without changing their callers'
   behavior; no test was weakened or deleted.
7. ✅ ADRs: conforms to ADR-0210 Decision 3 ("a PUT is read-then-update", unconditional) and its scenario
   `unversioned-writes-stay-unconditional`; ADR-0210's table row for `HandoverKVStore` ("own read version") still holds —
   the handover still writes conditional on its own read. The ADR-0063 admission pipeline is re-run per attempt; its
   admissions are idempotent validations. No ADR file edited.
8. ✅ Build, vet, lint (darwin + linux) and the touched package's race tests green; e2e deferred to the PR gate.
9. ✅ Conventions: `api/fault` kinds, ctx-first, no `any`, top-level imports; the doc comments state the why
   (ADR-0210, the bound's precedent). No YAML touched; pitfall 5 (e2e speed) not applicable — no e2e changed.
10. ✅ Reuse: no shared conflict-retry helper exists (`storescaler` inlines its loop; `funcdctl` retries are
    client-side); the bound mirrors `storescaler.maxAttempts`. The tests reuse `twoVersions`, `send`, `fnBody`,
    `requireConflict`, `kvWorkflow`, `keptStore`, `handover`, by extracting the existing server constructors rather
    than copying them.
11. ✅ Shape: one commit, `fix(controlplane):`, `Fixes #900`, attribution trailer.
12. ✅ Every case covered: unversioned replace (fixed + tested), versioned replace keeps 409 (tested), bounded
    give-up (tested), handover sibling (fixed + tested). Unversioned DELETE passes no precondition to the store
    (`deleteObjIf` → `store.Delete(..., rv)`), so it has no sibling defect. The other `store.Update` read-modify-write
    sites (`internal/function`, `internal/workflow`) are controllers, not API writes.

## Findings

None.

## ✅ Verified correct — keep

- The `lost` signal is decided inside the closure, next to the store write, so only the server's own read version can
  trigger a retry; the precondition, guard and admission outcomes can never loop.
- The test proves both directions: the unversioned write survives one race with the controller's status kept, and the
  versioned write plus a status that changes on every read still end in 409.

## Recommendation

Pass. Hand back to `/fix` Step 8 to open the PR (`Fixes #900`).
