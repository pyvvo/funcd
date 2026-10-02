## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #125 fix, model: claude-opus-5-5)

Fix under review: commit `0841d02` `fix(workflow): reject a second run under an existing WorkflowRun name`
on branch `fix/198-workflow` (group HEAD `fbcb9ff`). Only this commit was reviewed; the other commits on
the branch belong to other issues of the workflow group.

The issue: `funcdctl workflow run <wf> <existing-run>` went through `sdk.Client.Apply` (PUT, then POST on
NotFound), and the server admitted a WorkflowRun Update that replaced `spec.input` or `spec.workflow`. The
CLI printed `started`, nothing ran, and the stored run recorded an input that never ran. This contradicts
ADR-0094 scenario `duplicate-run-name-rejected`.

The fix closes both halves of the cause the issue names:

- `cmd/funcdctl/workflow.go`: the `run` and `replay` verbs now call a new `sdk.Client.Create` (POST only),
  so a taken name returns the store's `Conflict` ("already exists"), which is this codebase's AlreadyExists
  (`internal/store/store.go` `store.Create`; `TestDuplicateRunNameRejected` asserts the same kind).
- `internal/controlplane/admission/workflowrun.go`: a new Validating admission `workflowrun-spec-immutable`
  (Update on WorkflowRun) rejects a change to `spec.workflow`, `spec.input` (whitespace-insensitive) or
  `spec.replay`; `spec.paused` and `spec.cancel` stay mutable. It is wired in `pkg/funcd/funcd.go` next to
  the other WorkflowRun admissions.
- `pkg/sdk/sdk.go`: `Apply`'s two duplicated POST blocks are folded into a private `create`, which the new
  public `Create` reuses.

### 🟡 Minor 1 — the `replay` verb's switch to `Create` is not covered by a test  ·  attribution: model
Evidence: overlay mutant M6 (the `replay` call site back to `c.Apply`) leaves the whole
`cmd/funcdctl` package green (`go test -overlay … ./cmd/funcdctl/` → `ok`). The admission backstops a replay
whose seed differs, but a `workflow replay` re-issued with the same seed under an existing name is an
identical-spec Update, which the admission admits by design. Only the `Create` call makes that case a
Conflict, and no test pins it.
Fix (builder): add a `workflow replay … --name <taken>` case to `TestIssue125_RerunNameIsRejected`.

### 🟡 Minor 2 — `sameJSON` normalises whitespace but not key order  ·  attribution: model
Evidence: `internal/controlplane/admission/workflowrun.go` `sameJSON` compares `json.Compact` output, so
`{"a":1,"b":2}` and `{"b":2,"a":1}` count as different inputs. A no-op re-apply from a client that emits
keys in another order than the stored bytes is rejected as "already exists". The failure is in the safe
direction (no spec is replaced) and the error text is clear, and `funcdctl` itself emits keys in a stable
order, so the practical impact is low.
Fix (builder, optional): compare the decoded values instead of the compacted bytes, or leave as is and
note the behaviour in the doc comment.

### ✅ Verified correct (keep it)
- **Regression tests fail without the fix, for the issue's reason.** `git revert --no-commit 0841d02` with
  the three test files restored from HEAD (the revert applied cleanly; no overlay needed):
  - `TestIssue125_RerunNameIsRejected` (`cmd/funcdctl`) → FAIL: expected `conflict`, got no error, output
    `started WorkflowRun/dur-4` — exactly the issue's symptom.
  - `TestIssue125_ApplyCannotReplaceRunSpec` (`pkg/funcd`, e2e tag) → FAIL: the second `Apply` with
    `{"n":42}` returns `<nil>`.
  - `TestIssue125_RunSpecIsImmutableOnUpdate` (`internal/controlplane/admission`) → does not compile
    (`undefined: admission.NewWorkflowRunSpecImmutableAdmission`), as expected for a new symbol.
- **They pass with the fix** after `git reset --hard fbcb9ff`, under `-race`, un-skipped (all three PASS).
- **Each layer is tested independently.** The `cmd/funcdctl` harness (`newClient`) builds a control plane
  with no admissions, so the CLI test proves the `Create` change alone; the e2e test drives `Apply` (PUT)
  against the real platform, so it proves the admission alone.
- **Mutants**: M1 `run` verb back to `Apply` → CLI test FAIL; M2 drop the `spec.workflow` comparison →
  admission test FAIL ("a changed spec.workflow is a Conflict"); M3 `sameJSON` → `bytes.Equal` → admission
  test FAIL (whitespace case); M4 drop the `spec.replay` comparison → admission test FAIL; M5 unwire the
  admission in `pkg/funcd/funcd.go` → e2e test FAIL. Only M6 survives (Minor 1).
- **User-visible behaviour on real binaries** (funcd and funcdctl built from the group HEAD, memory mode,
  port 31250, builtin step): `workflow run dur dur-4 --input '{"n":1}'` → `started`, rc 0; the re-run with
  `{"n":42}` → `store.Create: WorkflowRun "dur-4" already exists`, rc 1; `describe` still shows `"n": 1`;
  `workflow pause dur-4` → rc 0 and `"paused": true`; `funcdctl apply -f` of the same run with `n: 42` →
  rejected by `admission.workflowrun-spec-immutable`, rc 1. The daemon was stopped by PID.
- **Root cause, not symptom**: both named causes are removed — the CLI's upsert path for a run and the
  mutable run spec on Update. No retry, timeout or swallowed error.
- **Scope**: every hunk serves the issue. The `Apply` refactor in `pkg/sdk/sdk.go` is the minimum needed to
  share the POST path with `Create` and removes a duplicated block; `Apply`'s behaviour is unchanged
  (`pkg/sdk` tests green). No test was weakened or deleted.
- **Reuse**: the admission mirrors the existing `site-prefix-immutable` shape (`internal/controlplane/
  admission/site.go`: Validating, Update-only, reads `req.Old`). No existing JSON-equivalence helper exists
  in the module (searched), so `sameJSON` duplicates nothing. `sdk.Client.Create` reuses `toWireBody`,
  `collectionURL` and `do`.
- **Conventions**: `api/fault` errors (`fault.Conflictf` with an `op`), ctx-first, no `any` in signatures,
  imports at top level, terse why-comments that name ADR-0094, the wiring comment matches its neighbours.
  `Conflict` (not the Site precedent's `Invalid`) is the right kind here, because ADR-0094's scenario asks
  for AlreadyExists, which `store.Create` reports as `Conflict`.
- **ADRs**: no ADR file was edited. The fix realises ADR-0094 `duplicate-run-name-rejected` and keeps
  ADR-0094's declarative pause/cancel (`spec.paused`, `spec.cancel` stay mutable; both verbs still work).
  ADR-0107 replay is respected (a replay's `spec.replay` is fixed at creation, as its input is).
- **Checks** (through `nix develop -c`): gofmt clean on the touched packages; `go build ./...` OK;
  `go vet ./...` OK on host and with `GOOS=linux`; golangci-lint `0 issues` on the touched packages on host
  and for Linux; `go test -race` green for `cmd/funcdctl`, `internal/controlplane/...`, `pkg/sdk`,
  `pkg/funcd`, `internal/workflow/...`; the e2e suite `go test -tags e2e ./pkg/funcd/...` → `ok`;
  `just check-hygiene` → clean. Under `-race` the e2e suite reports a data race in
  `TestScenarioE2ETLSSelfSignedServesHTTPS` (net/http `onceSetNextProtoDefaults` across the platform's two
  TLS servers). It reproduces identically on the base commit `b5f28fc` (origin/main), and this fix does not
  touch TLS serving, so it is pre-existing and not attributed (env).
- **Shape**: `fix(workflow):` subject, `Fixes #125`, the attribution trailer, one issue in the commit.

### Definition of Done
11 / 11 items hold (fix checklist). Item 4 holds on the fix's key lines (5 of 6 mutants killed); the one
survivor is the secondary `replay` call site, recorded as Minor 1.

### Model scorecard
To record: claude-opus-5-5 on issue #125 (fix) → pass, 0/0/2, 2 model-attributed, DoD 11/11.

### Recommendation
Pass. The two Minors are optional follow-ups for `/fix` (a `replay` case in the CLI regression test, and
key-order-insensitive input comparison); neither blocks the PR. The TLS e2e race under `-race` is
pre-existing on main and deserves its own issue.
