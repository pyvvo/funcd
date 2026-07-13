# ADR-0127 implementation review — context.blob data-plane

- **ADR**: [ADR-0127](../adr/0127-context-blob-data-plane.md) — context.blob data-plane (native blob binding accessor)
- **Realizes**: [FEAT-0001/F92](../feat/0001-feat-v1.1.md)
- **Producing model**: claude-opus-4-8
- **Gate**: ADR-0000 gate #5 (implementation review) — this reviews the *code*, and records; it does not fix.
- **Verdict**: **pass**
- **Date**: 2026-07-12

## Verdict: pass

The implementation realizes ADR-0127 faithfully: the function-facing blob path is a binding-gated
facade that mirrors `services/kv.Facade`, authorizes the already-Function-aware `S3Capability`, and keys
objects on the same `s3BucketFor` substrate under `blobKey` as the ADR-0080 S3 frontend. The whole
build is green on both tags; every ADR Scenario maps to a named, un-skipped, passing test; every ADR
Review-checklist item holds with evidence. No Blockers, no Majors.

## Verification (captured exit codes, all via `nix develop -c` at repo root)

| Check | Result | Exit |
|---|---|---|
| `go build ./...` | clean | `0` |
| `go build -tags dev ./...` | clean | `0` |
| `go tool golangci-lint run ./...` | `0 issues` (only environmental `ld:` macOS-version warnings — ignored per instructions) | `0` |
| `go test ./internal/services/blob/... ./internal/workernode/local/... ./pkg/funcd/...` | all `ok` | `0` |
| `go test -tags e2e -run TestScenarioE2EBlobObjectViaContextBlob ./pkg/funcd/...` | `ok 1.474s` — **ran** (shim `node_modules` present; not skipped) | `0` |
| `go mod verify` | `all modules verified` | `0` |
| Node shim `npm test` | `tests 50 · pass 50 · fail 0 · skipped 0` (blob scenarios present) | `0` |
| Python shim `ruff check .` | `All checks passed!` | `0` |
| Python shim `mypy` | `no issues found in 25 source files` | `0` |
| Python shim `pytest -q` | `79 passed` | `0` |

(`just ci`'s git-diff gate was not run as a pass/fail signal — the tracked files are intentionally
uncommitted pending the per-phase batch commit; expected, not a defect.)

## Scenario → test mapping (each ADR Scenario has a named, un-skipped, passing test)

| ADR Scenario | Test(s) | Level |
|---|---|---|
| blob-read-write | `TestScenarioBlobReadWrite` (`internal/services/blob/blob_test.go:92`, `internal/workernode/local/blob_test.go:93`) + e2e | hermetic + e2e |
| blob-list | `TestScenarioBlobList` (`blob_test.go:118`, `local/blob_test.go:108`) | hermetic |
| blob-signed-url | `TestScenarioBlobSignedURL` (`blob_test.go:151`, `local/blob_test.go:117`) | hermetic |
| blob-unbound-forbidden | `TestScenarioBlobUnboundForbidden` (`blob_test.go:136`, `local/blob_test.go:131`) + e2e | hermetic + e2e |
| blob-size-cap | `TestScenarioBlobSizeCap` (`internal/workernode/local/blob_internal_test.go:51`) | hermetic |
| blob-parity | Node `blob.test.ts` (5 wire scenarios) + Python `test_blob.py` (5 wire scenarios), both green; plus the Node `TestScenarioE2EBlobObjectViaContextBlob` live e2e | cross-shim wire + Node live e2e |

**Recorded deferral (not a defect)**: the parity *live* leg is proven on the Node shim end-to-end
(`pkg/funcd/blob_e2e_test.go`) plus the identical Node/Python wire suites; a Python **live** e2e is not
run, consistent with the ADR's Node-gated-e2e test plan. Sequencing, not a model gap.

## Critical correctness property — VERIFIED in code

This ADR's entire point is that the function-facing facade authorizes the per-object `S3Capability` with a
**Function** principal (bind-as-grant), not the legacy `Kind:Service`/no-`Action` path. Confirmed:

- `internal/services/blob/blob.go:89-104` (`Facade.authorize`) builds
  `auth.Request{Identity{Principal: &EntityRef{Type: v1.KindFunction, Namespace: ns, Name: fn}}, Action:
  ActionS3Read|ActionS3Write, Resource: &EntityRef{Type: v1.KindBucket, Namespace: ns, Name: bucket,
  Path: prefix}}` — **byte-for-byte the same shape** as `internal/blob/s3gateway/backend.go:70-79`
  (`be.authorize`), only with the Function principal. A deny maps to `fault.Forbidden`.
- Objects keyed via `blobKey(prefix, key)` (`blob.go:77-82`, `132/149/158/170/189`) on the
  `s3BucketFor` substrate view — the same keyspace the S3 frontend serves (coexistence).
- The legacy no-`Action` `Kind:Service` facade is **gone**: the only `NewFacade` in the package is the
  new binding-gated one (`blob.go:56`), and no `KindService`/`Action==""` blob authz path exists in code
  (grep found the term only in a doc-comment describing what was superseded). The live Service-dispatcher
  `TypeHandler` is untouched (`blob.go:202-218`).
- The local `Blob` port's `SignedURL` takes `blob.SignOptions` (`internal/workernode/local/blob.go:30`),
  and the facade satisfies `local.Blob` **directly** — `pkg/funcd/funcd.go:455-467` assigns the
  `*Facade` into `var blobPort local.Blob` with no `pkg/funcd` adapter. The wiring also correctly avoids
  the typed-nil-interface trap (interface stays nil unless `c.blob != nil`), so nil ⇒ no `/blob` routes.

## ADR Review-checklist — all 10 items hold

1. Routes `GET/PUT/DELETE /blob/{binding}/{key...}`, `GET /blob/{binding}` list, `?sign=1` presign, RFC
   9457 errors — `local/blob.go:37-107` (`fault.WriteProblem`), mirrors `registerKV`. ✅
2. Identity is the fixed caller `Ref` (`ns, fn`), never read from the request — `local/blob.go:38-39`
   captures `caller.Namespace/Function` once; handlers pass them, reading only `{binding}`/`{key}`/query. ✅
3. Bind-as-grant: unbound alias ⇒ 403 — resolver default-denies (`resolver.go:99-101` Forbidden on no
   `spec.blob` entry) and the PDP denies; e2e asserts the 403. ✅
4. Facade authorizes `s3::read`/`s3::write` on `KindBucket`+`Path:prefix` with a Function principal, keys
   via `blobKey` — verified above. ✅
5. `maxBlobBytes` (64 MiB) enforced via `http.MaxBytesReader` on put; over-cap ⇒ 413/422 —
   `local/blob.go:19,72-75`; `TestScenarioBlobSizeCap`. ✅
6. `pkg/funcd` builds the facade from `s3BucketFor` + the `spec.blob` resolver + the cedar PDP, satisfies
   `local.Blob` directly (no adapter), nil ⇒ no `/blob` routes — `funcd.go:453-468`. ✅
7. Socket serves `/invoke`, `/kv`, `/blob` on one UDS — `local/local.go:79-84` registers all three; the
   sandbox socket is provisioned for every function (`addInvokeSocket`), a superset of links-or-kv-or-blob. ✅
8. `context.blob` in Node + Python shims, typed on `FunctionContext`, regenerated bundles, stdlib/`node:http`
   only (no S3 SDK) — `shim/nodejs/src/blob.ts` (`node:http`), `shim/python/src/funcd_shim/blob.py`
   (`http.client`); `types.ts`/`types.py` expose `blob`; `shim.ts`/`pool.ts`/`shim.py`/`_poolworker.py`
   construct it; `shim.mjs`/`pool.mjs` regenerated. ✅
9. `examples/js/blob-object` exercised by the e2e; parity round-trips Node↔Python — the e2e drives the
   real handler; the two wire suites round-trip identical bytes. ✅
10. `S3Capability` PDP + s3gateway unchanged; legacy Service `TypeHandler` unchanged; no new dep
    (`go mod verify` clean, no `go.mod` change); ctx-first; `api/fault`. ✅

## ✅ Verified correct — what is strong and should be kept

- **Authz shape parity with the S3 frontend.** `Facade.authorize` is a deliberate mirror of
  `s3gateway.authorize`; the two paths genuinely share the PDP, substrate, and keyspace, so
  `context.blob` objects are the same objects `aws s3` sees. This is exactly the ADR-0069 pattern applied
  to blob, and it is implemented without touching the PDP or the gateway.
- **The typed-nil-interface trap is explicitly handled** in the wiring (`funcd.go` comment + `var blobPort
  local.Blob`), a subtle Go footgun avoided.
- **Symmetry with the kv twin** across the whole surface (port shape, error mapping, `?sign=1` route,
  shim client structure, lazy client construction) keeps the three accessors legible.
- **Connection-scoped identity** held throughout: no handler reads a caller from the request.

## Minor (non-blocking, not model-attributed)

- **Lazy `from .blob import BlobClient` inside the Python `blob` property** (`shim.py:64`,
  `_poolworker.py:79`) is a function-level import, which the house rule generally discourages — but it
  mirrors the existing `kv`/`invoke` twins **byte-for-byte** (top-level import kept under `TYPE_CHECKING`),
  is `ruff`-clean, and is the codebase's established shim pattern for lazy client construction. Holding blob
  to a stricter bar than its already-merged twin would be inconsistent; noted for awareness only.

## Recommendation

**Advance.** DoD met (10/10 ADR checklist items, plus the generic DoD), all Scenarios covered by
named passing tests, no Blockers or Majors. Stamp ADR-0127 `Reviewing → Implemented`, feat F92
`reviewing → implemented`, and move the Project #4 card to Done.
