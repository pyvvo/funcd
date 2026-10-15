## Verdict: pass — 0 blockers, 1 major (resolved), 5 minors  (ADR-0239 implementation, Decision 10, model: claude-opus-5-5)

Reviewed `git diff bb9aa110..aee4f1a4` on branch `docs/adr-site-runtime-config`: the amendment commit 95b3456b
(Decision 10, an opt-in `<base href>` for path mounts, and its implementation) and the lane fix aee4f1a4. Decisions
1–9 passed review earlier ([adr-0239-implementation-claude-opus-5-5.md](adr-0239-implementation-claude-opus-5-5.md)).
The bar is the ADR text in 95b3456b. The Go checks ran on 95b3456b. aee4f1a4 changes only `e2e/app-todo.venom.yml`,
so the lane guard tests ran again on it. The lane evidence is the builder's two app-todo runs on real containerd, read
from their logs and not rerun here.

### 🟡 Major M1 — the lane failed at C6 on the amendment commit (resolved in aee4f1a4)  ·  attribution: model

Lane run 5, on 95b3456b, ended `EXIT=2` with `final status: FAIL`. Its only failed step was C6 step 2: the assertion
`result.systemout ShouldContainSubstring BASE-OK` failed after 16 attempts, while APP-OK, CONFIG-OK and ONE-DIGEST were
printed. C1–C5 and every earlier case passed. The step grepped for `<head><base href="/preview/">`
(`e2e/app-todo.venom.yml:2593` in 95b3456b). The example's index (funcd-functions `apps/site-config/web/index.html`)
opens its `<head>` with `<meta charset="utf-8">`, and Decision 10 puts the base after that element: `withBase` on that
file returns `<meta charset="utf-8"><base href="/preview/">` (checked with a scratch test). The product was right. The
assertion contradicted the rule that it checks, a rule that the unit case "after a meta charset that opens head"
already shows.

aee4f1a4 checks that the page holds exactly one `<base ` and that it is `<base href="/preview/">`
(`e2e/app-todo.venom.yml:2593-2594`). The lane guard tests pass on aee4f1a4, and lane run 6 passed (see below).

### Minor

- **m1 · `model` · no test covers a copy that the Bucket refuses.** Decision 10 says "A copy the Bucket refuses fails
  the deploy like a bundle object". `writeIndexCopy` maps `PayloadTooLarge` and `Invalid` to `MaterializeFailed`
  (`internal/site/basehref.go:56-58`), and no test reaches that branch. A scratch test, run with `go test -overlay` so
  that no file of the branch changed, set the Bucket's `maxObjectBytes` to 34. The 29-byte index fits under that cap
  and the 47-byte copy does not. After `baseHref` was turned on, the Site was `MaterializeFailed` with a message that
  named the copy's key and the cap, the Route kept the index `index.html` at `/`, and the digest and the unobserved
  generation stayed as they were. The behavior holds. Add that test beside
  `TestSite_ObjectOverBucketCapIsNotReadyNotRetried`.
- **m2 · `model` · `withBase` starts above the bloat audit's complexity limits.** With the audit's linter settings
  (`scripts/agent/audit.golangci.yml`), `withBase` (`internal/site/basehref.go:70`) has 56 statements (limit 50), a
  cognitive complexity of 32 (limit 30) and a cyclomatic complexity of 23 (limit 20). Against bb9aa110, `Reconcile`
  grows from 71 to 80 statements, from 35 to 40 cognitive and from 29 to 32 cyclomatic, and `unpack` grows from 31 to
  32 cognitive and from 21 to 22 cyclomatic. The audit reports these deltas without failing on them, and `dupl` finds
  no duplicate in `internal/site` or `internal/edge/static`. Splitting the scan (the `<base>` spans, the target, the
  insertion point) from the splice would bring `withBase` under the limits.
- **m3 · `model` · `scripts/lanes.yaml` does not mention C6.** The app-todo `description` (`scripts/lanes.yaml:609`)
  and the comment on the staged example (`scripts/lanes.yaml:674`) still describe the site-config use case as staging
  and prod only. The suite's header describes C6 (`e2e/app-todo.venom.yml:51-53`).
- **m4 · `adr` · a request for the index by its own name gets the bundle's index.** Decision 10 serves the copy "at
  `/` and as its SPA fallback". `GET <mount>/index.html` resolves to the bundle's own `index.html`: a scratch test of
  the static handler answered `/` and `/devices/42` with the copy and `/index.html` with the bundle's index. A client
  that loads the index by its name therefore gets no mount base. A service worker that precaches `index.html` and
  answers navigations with it (Workbox `navigateFallback`, Angular's `ngsw`) breaks deep links again once it is
  installed, and an Angular build's own `<base href="/">` applies at `<mount>/index.html`. The Consequences do not
  name this limitation. The decider can name it there while the amendment is open, or leave it to a follow-up.
- **m5 · `adr` · the kept `target` comes from the first `<base>`, not from the first `<base>` that has a target.** A
  browser takes the default target from the first `<base>` that carries a `target` attribute. For
  `<base href="/"><base target="_top">`, the copy keeps no target (scratch test). HTML allows one `<base>` per
  document, so only a non-conforming index meets this case.

### ✅ Verified correct (keep it)

- **Checks**, each run once from the worktree:
  - `scripts/agent/d go build ./...` → exit 0.
  - `scripts/agent/d go vet ./api/types/v1alpha1/ ./internal/site/ ./internal/edge/static/ ./pkg/funcd/` → exit 0.
  - `scripts/agent/d go tool golangci-lint run` on the same packages → exit 0, 0 issues; with
    `--build-tags e2e ./pkg/funcd/` → exit 0, 0 issues.
  - `scripts/agent/d go test -count=1 ./api/types/v1alpha1/ ./internal/site/ ./internal/edge/static/ ./internal/controlplane/... ./internal/app/ ./internal/gc/ ./pkg/funcd/`
    → exit 0. This includes the staleness test of the committed OpenAPI file in `internal/controlplane`; a scratch
    regeneration with `internal/controlplane/cmd/specgen` also equals `api/openapi/funcd.v1alpha1.yaml`.
  - `scripts/agent/d go test -count=1 -tags e2e -run 'TestScenarioE2ESiteConfig' -v ./pkg/funcd/` → exit 0 (package
    5.8 s).
  - `scripts/agent/d go test -count=1 -run Lane ./cmd/funcd/ ./internal/testkit/langmod/` → exit 0 on 95b3456b and
    again on aee4f1a4 (with the local `go.work`).
  - `scripts/agent/d go mod verify` → exit 0. `git status --short` is empty.
- **Scenarios.** All 16 scenarios of the amended ADR have a passing e2e test. Each test calls `t.Parallel()` and names
  its scenarios in its doc comment, and the "within 5 s" of `site-config-base-href-paths` is `configWithin`. The
  reconciler halves of the new scenarios have unit tests in `internal/site/basehref_test.go`.

| Test | Scenarios | Time |
|---|---|---|
| `TestScenarioE2ESiteConfigBaseHref` | base-href, one-digest-two-mounts | 0.11 s |
| `TestScenarioE2ESiteConfigBaseHrefPaths` | base-href-paths | 0.14 s |
| `TestScenarioE2ESiteConfigRuntimeReserved` | runtime-reserved | 0.14 s |
| `TestScenarioE2ESiteConfigTemplate` | two-environments, revalidates, ahead-of-spa, app-configmap | 5.07 s |
| `TestScenarioE2ESiteConfigConfigMap` | configmap-missing (missing and deleted), configmap-change | 0.17 s |
| `TestScenarioE2ESiteConfigPathMounted` | path-mounted | 0.10 s |
| `TestScenarioE2ESiteConfigShadowsThenRemoved` | shadows-bundle-file, removed | 0.13 s |
| `TestScenarioE2ESiteConfigRouteConflict` | route-conflict | 0.10 s |
| `TestScenarioE2ESiteConfigSharedPrefix` | shared-prefix | 0.13 s |
| `TestScenarioE2ESiteConfigRefused` | refused | 0.06 s |

- **The copy follows Decision 10** (`internal/site/basehref.go:27-39`, `70-147`). `withBase` removes every `<base>`
  element and inserts one `<base>`, with the first one's `target`, after `<head>` or after a `<meta charset>` that
  opens the head, else after `<html>`, else before the first element. It copies every other byte from the input, so
  upper case and attributes stay (`TestWithBase`, 11 cases). The tokenizer ignores a `<head>` in a comment and a
  `<base>` in a script. A scratch run on more inputs (a BOM, CRLF, a `<base>` in the body, a truncated tag at the end,
  a self-closing `<meta charset/>`, a `<template>`) gave exactly one parsed `<base>` with the mount each time. The base
  is the mount ending with `/`, percent-encoded by `url.URL.EscapedPath` and then HTML-escaped. The key is
  `_runtime/<site>/<12 hex digits of the mount's SHA-256>/<index>` in the digest folder.
- **The reconciler order** (`internal/site/reconcile.go:162-175`, `199`). The copy is written after the bundle is
  complete and before `ensureRoute`, only when it differs from the stored copy, and the bundle rule's `index` points
  at it. The first Route already serves the copy; a new mount writes a new copy and moves the index; a new digest
  moves the prefix and the index in one write; turning `baseHref` off restores `index.html`
  (`TestScenarioSiteConfigBaseHrefCopy`, `TestSiteConfigBaseHrefRootAndNewDigest`). `baseHref` alone writes no
  `config.json`.
- **The reserved entry.** `unpack` refuses a bundle with a root `_runtime` entry for every Site, before it writes
  anything, and names the entry (`internal/site/reconcile.go:329-331`); the previous digest keeps serving.
  `status.objects` and `status.bytes` skip `_runtime/` (`internal/site/reconcile.go:402-408`; 2 objects with the copy
  present).
- **The edge change is the one that Decision 10 names** (`internal/edge/static/static.go:166-171`, `202`).
  `resolveKey` appends the index's file name to a directory, and `cacheControl` matches the index by its file name. For
  an index without a directory, both behave as before. `TestNestedIndex` covers `/`, a deep link and `/docs/`, all
  `no-cache`. No Route field was added. The only API change is `SiteIngress.BaseHref` (`json:"baseHref,omitempty"`), as
  the Contracts give it, with a `doc` tag for its OpenAPI description (`api/types/v1alpha1/site.go:91-94`).
- **The lane on real containerd** (app-todo). Run 6, on aee4f1a4, ended `EXIT=0` with `final status: PASS` and no
  failed step. C1–C5 passed with the example App's Sites on `baseHref` at the host root. C6 deployed the same digest
  as a Site at `/preview`, and a direct deep link returned one `<base href="/preview/">`; `app.js` answered
  `200 text/javascript`, `config.json` held the Site's `API_URL`, and both Sites reported one digest.
- **Documents.** The back-links in ADR-0120 (§2–§3), ADR-0139 (Decisions 2 and 8) and ADR-0140 (the rejected
  `<base href>`, the Scope Out item, the constraint and its consequence) match the text they point at. The blueprint's
  Site sentence, the F104 and F152 rows and the OpenAPI description are updated, and F152 stays `implemented`. The
  amendment is the decider's and is declared in the ADR's header, after the precedent of ADR-0238. Outside Decision 10
  and the two clauses that point at it (Decision 2's fetch rule and the edge constraint), the edits to Decisions 1–9
  change wording only, and `site-config-configmap-deleted` is merged into `site-config-configmap-missing` with the same
  Given/When/Then, so the earlier review stands. The example's README shows relative URLs and `document.baseURI`, as
  Decision 10 asks.
- **Dependencies.** `golang.org/x/net` was already a direct requirement; `go.mod` and `go.sum` are unchanged.

### Definition of Done

11 / 13 hold: the Review checklist 6/6 and the ADR's "Done when" 5/5 (build, lint, tests with `-tags e2e`,
`go mod verify`, the lane on real containerd with aee4f1a4). Two items are pending, not failed (process): the
funcd-functions v0.16.0 release with its `go get`, and the repo-wide gate on the PR.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0239 (implementation) → pass, 0/1/5, 4 model-attributed, DoD 11/13. See
docs/reviews/model-scorecard.md.

### Recommendation

Pass. The ADR's "Amended before merge" line now records this review; no status and no feat row changed. Before the
merge, release funcd-functions v0.16.0 (pyvvo/funcd-functions#17) and take it with `go get`: v0.15.0 has neither
`sites/preview.yaml` nor `baseHref` on the App's Site, so C6 needs the bump. Then run the gate, and put `Fixes #1031`
(the F152 issue) in the PR description. The model minors can land in the same PR. m4 and m5 are the decider's
decisions; m4 deserves a line in the Consequences before the ADR freezes at merge.
