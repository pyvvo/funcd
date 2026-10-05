# ADR-0148 implementation review — claude-opus-5-5 (loop 1)

- **ADR**: [ADR-0148](../adr/0148-size-caps-answer-413.md) — size caps answer 413; malformed input stays 400
- **Work**: branch `feat/adr-0148-size-caps-413`, one commit `4220076` on `origin/main` `464f08a` (23 files, +290/−95)
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass** — 0 Blocker, 0 Major, 2 Minor (1 model, 1 adr/process)

## Verification run (in the branch worktree, through `scripts/agent/d`)

| Check | Command | Result |
|---|---|---|
| Build (darwin) | `go build ./...` | exit 0 |
| Build (linux) | `GOOS=linux go build ./...` | exit 0 |
| Vet (darwin, linux) | `go vet` on the 11 touched package trees | exit 0, exit 0 |
| Tests | `go test -race -count=1` on `api/fault`, `api/types/v1alpha1`, `internal/blob/...`, `internal/controlplane/...`, `internal/dataplane`, `internal/edge/limit`, `internal/services/{kv,blob}`, `internal/site`, `internal/workernode/local`, `pkg/funcd` | 13 packages `ok`, exit 0 |
| Flake check | the 7 new/retagged scenario tests, `-race -count=25` | 4 packages `ok` |
| Lint (darwin) | `golangci-lint run` on the touched trees | `0 issues.`, exit 0 |
| Lint (linux) | host-built `golangci-lint` (`go tool -n`), `GOOS=linux … run` | `0 issues.`, exit 0 |
| Done-when greps (plan step 4) | no `422` in `local/kv.go`, `local/blob.go`, `services/kv/kv.go`; `Forbidden` in `capped.go` only in `SignedURL` (`:39`) and its doc; no payload-cap `Invalidf` in `workflowrun.go` (only the schema-mismatch one, `:87`); `maxNormalizeBytes` in `dataplane.go` only at the read bound (`:165`, `:167`), not in the 413 detail; no `Forbidden` in `internal/site/reconcile.go` | all hold |

No e2e, no `go test ./...`, no Lima (per the task); the repo-wide gate runs once per PR.

### Mutants (`go test -overlay`, one changed line each) — 5/5 killed

| # | Mutant | Killed by |
|---|---|---|
| 1 | `services/kv/kv.go` Get: `checkKeyLimit(…, key)` → `checkKeyLimit(…, "")` | `TestIssue461_StoredOverLimitCapsAreClamped` (Badger), `TestScenarioKVKeyOverHardLimit` |
| 2 | `dataplane.go` `serveUpstream` ErrorHandler: `if errors.As(perr, &tooLarge)` → `if false && …` | `TestScenarioEdgeChunkedBodyOverCap/upstream` |
| 3 | `dataplane.go` `serveFunction` 413 detail: `tooLarge.Limit` → `maxNormalizeBytes` | `TestScenarioEdgeChunkedBodyOverCap/function` |
| 4 | `controlplane.go` `newFaultError`: the 413 branch disabled | `TestScenarioControlPlaneBodyOverCap` |
| 5 | `s3gateway/backend.go` `mapBlobErr`: the `PayloadTooLarge` case removed | `TestIssue109_BucketMaxObjectBytesRejectsOversizeWrite` (got 500 `InternalError`) |

## Contracts and Decision, site by site

| ADR item | Code | Holds |
|---|---|---|
| §1 `Invalid` → 400 on the local API | `TestScenarioLocalAPIInvalidStays400`: a body that breaks off with `io.ErrUnexpectedEOF` → 400 `urn:funcd:problem:invalid`; the local kv/blob doc comments now say "bad input → 400" | yes |
| §2 KV `Put` value / key over the store cap → `PayloadTooLargef("services.kv.put", …)`, messages kept | `internal/services/kv/kv.go:127`, `:130` | yes |
| §2 `checkKeyLimit` on `Get`/`Delete`, after `resolveAuth`, ops `services.kv.get` / `services.kv.delete`; store cap not checked there | `kv.go:113`, `:141`, `:147-154`; matches the Contracts snippet verbatim | yes |
| §2 Badger's key limit never surfaces as 500 | `TestIssue461` gets and deletes a 70000-byte key through Badger → `PayloadTooLarge` | yes |
| §2 `blob.Capped.Put` → `PayloadTooLarge`; `SignedURL` keeps `Forbidden` | `internal/blob/capped.go:32`, `:39`; `TestIssue374_PresignedPutRefusedOnCappedBucket` unchanged and passing | yes |
| §2 WorkflowRun admission → `PayloadTooLargef(op, …)`; ADR-0063 deny kinds gain it | `admission/workflowrun.go:41`; `admission.go` doc comment | yes |
| §3 `serveUpstream` ErrorHandler maps `*http.MaxBytesError` first, `Unavailable` fallback kept; `serveFunction` names `tooLarge.Limit` | `internal/dataplane/dataplane.go:171`, `:249-253` | yes |
| §4 `newFaultError` maps huma's 413 to `fault.ToProblem(PayloadTooLargef("controlplane.body", "%s", msg))`; other huma errors stay `about:blank` | `internal/controlplane/controlplane.go:319-321`; the rest of the function untouched | yes |
| §5 `mapBlobErr`: `PayloadTooLarge` → `s3err.ErrEntityTooLarge` (HTTP 400) | `internal/blob/s3gateway/backend.go:152-153` | yes |
| §6 comments | `fault.go`, `bucket.go`, `capped.go`, `newFaultError`, `limit.go:46`, `:77`, `local/kv.go`, `local/blob.go`, `site/reconcile.go` — wording matches §6; a repo grep finds no comment left claiming 422, `Invalid`, `Forbidden` or `about:blank` for a size cap | yes |
| Plan step 1: `reconcile.go` drops the `Forbidden` arm, keeps `PayloadTooLarge`, `Invalid` | `internal/site/reconcile.go:288` | yes |

No exported signature changed; no second status map; the invoke pass-through and `readBody`'s 413/400 are untouched.

## Scenarios — each has one named, passing test

| Scenario | Test |
|---|---|
| `kv-value-over-store-cap` | `internal/workernode/local/kv_test.go` `TestScenarioKVValueOverStoreCap` (real facade via `NewHandler`; status, type, cap in detail) |
| `kv-key-over-store-cap` | `TestScenarioKVKeyOverStoreCap` |
| `kv-key-over-hard-limit` | `TestScenarioKVKeyOverHardLimit` (GET/PUT/DELETE subtests; the put trips the 1024 store cap, as plan step 2 prescribes) + `TestIssue461` for the Badger clamp |
| `kv-lowered-key-cap-keeps-keys` | `internal/services/kv/kv_test.go` `TestScenarioKVLoweredKeyCapKeepsKeys` (two facades over one engine, 4096 then 1024) |
| `blob-over-bucket-object-cap` | `internal/workernode/local/blob_test.go` `TestScenarioBlobOverBucketObjectCap` (`iblob.Capped(…, 16)`; asserts nothing stored, 16 bytes lands) |
| `s3-over-cap-is-entity-too-large` | `pkg/funcd/s3gateway_internal_test.go` `TestIssue109_BucketMaxObjectBytesRejectsOversizeWrite` (renamed, tagged; `EntityTooLarge`/400 and the view's `PayloadTooLarge`) |
| `local-api-body-over-limit` | `local_internal_test.go` `TestIssue172_OverCapBodyIs413` (tagged; invoke, kv put, blob put) |
| `local-api-invalid-stays-400` | `TestScenarioLocalAPIInvalidStays400` |
| `workflowrun-payload-over-cap` | `admission/workflowrun_test.go` `TestWorkflowRunPayloadAdmission` (tagged; kind + `ToProblem` 413 + type) |
| `edge-chunked-body-over-cap` | `internal/dataplane/upstream_test.go` `TestScenarioEdgeChunkedBodyOverCap` (behind `limit.Chain{MaxBodyBytes: 1024}`, a body-reading upstream, a function route; asserts `ContentLength == -1`) |
| `control-plane-body-over-cap` | `internal/controlplane/api_test.go` `TestScenarioControlPlaneBodyOverCap` (413, `application/problem+json`, type, title "Content Too Large") |

Existing tests were upgraded rather than duplicated, as plan step 2 asks: `TestScenarioValueOverCapRejected`, `TestIssue377`, `TestIssue461`, `TestScenarioBlobSizeCap` (`>= 400` tightened to 413), and the Site test collapsed to one `blob.Capped` case with the `tooLarge` fake deleted.

## Findings

### Minor

1. **Feat cells of plan step 3 are missing ADR-0148** — *attribution: adr/process (acceptance step), not model.* The
   seven back-links are in place, and F38 cites ADR-0148 with `size caps: accepted`, but F32, F42, F92
   (`docs/feat/0001-feat-v1.1.md`), F47, F75 and F102 do not cite ADR-0148 on `origin/main` `464f08a` (a grep of
   `docs/feat/` finds only the F38 row). Step 3 assigns this to acceptance, and this task forbids doc edits, so
   the last Review-checklist item fails only on its document half. The wave's docs PR should add the six
   cells while it moves the ADR to `Reviewing`/`Implemented` and F38's size-cap status forward.
2. **Reflowed comment overruns its neighbours** — *attribution: model, cosmetic.*
   `internal/controlplane/admission/admission.go:49` is a 132-character comment line under a 71-character one.
   The linter passes, and the line does not change behaviour.

### ✅ Verified correct — keep

- Every Contracts snippet lands at its site with the message and `op` kept, and the new `op`s are exactly the
  ones the ADR names.
- `Get`/`Delete` check only `v1.MaxKeyBytesLimit` after authorization. The lowered-cap test proves that a stored key stays
  readable and deletable while a rewrite is still bounded.
- The edge test forces a truly chunked request (`ContentLength == -1`) and covers both branches of §3. It passed
  25 times under `-race`.
- The S3 test asserts both the HTTP 400 and the `EntityTooLarge` code. A missing `mapBlobErr` case would fall
  through to 500, and mutant 5 confirms that the test catches it.
- The Site test no longer fakes the error kind. It probes the real `blob.Capped` view, so it now pins the
  ADR's single kind.
- The diff is small and stays in scope: no exported change, no new dependency, no stray file.

## Recommendation

Pass. No rework is needed. The wave's docs PR should stamp ADR-0148 and F38 and add ADR-0148 to the F32, F42, F92,
F47, F75 and F102 cells (Minor 1).

```json
{
  "date": "2026-10-05",
  "adr": "0148",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 1,
  "dod_passed": 9,
  "dod_total": 10,
  "report": "docs/reviews/adr-0148-implementation-claude-opus-5-5.md",
  "notes": "all Contracts snippets at their sites, 11/11 scenarios have a named passing test, build/vet/lint green on darwin+linux, touched packages green under -race, edge/KV/control-plane scenarios stable at -count=25, 5/5 overlay mutants killed, all plan step-4 greps hold; admission.go:49 comment reflowed to 132 chars (model, cosmetic); F32/F42/F92/F47/F75/F102 cells lack ADR-0148 from the acceptance step (adr/process)"
}
```
