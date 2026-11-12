## Verdict: pass — 0 blockers, 0 majors, 3 minors  (ADR-0325 implementation, model: claude-opus-5-5)

The work spans three repositories:

- funcd, branch `docs/adr-0325-tested-examples`, two commits on `origin/main` (674e79b5): cfc1fd10 (the ADR) and
  62e446d6 (the implementation, which moves the ADR to `Reviewing`).
- pyvvo/funcd-typescript#103 (the regions) and its release #104 (tag v0.26.0, which funcd's `go.mod` now pins).
- pyvvo/funcd-frontend#39 (c8501a1: `remark-directive` in the preset, the mirror sync, `scripts/check-includes.ts`).

The pass depends on the repository-wide gate, which runs after this review. If that gate fails because of this change,
the verdict becomes changes-requested.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The include test does not check that a problem names the page** · `model` · `pkg/funcd/guide_examples_test.go`,
  `TestScenarioGuideIncludesResolve`. Scenario `include-resolves` says that the failing test names the page and the
  include. The assertions check only the include (`require.Contains(t, problems[0], "no-such-file.yaml")`). A mutant of
  `includeProblems` that drops `inc.page` from the message passes (`ok`). Today the message does name the page: a
  broken page under `docs/guide` was reported as `../../docs/guide/<page>.md: ../../_repos/funcd-typescript/…#no-such:
  the file marks no such region`. Fix: assert the page's name in both problems.
- **No assertion pins which files the apply test selects** · `model` · `guideManifests`. It keeps only `.yaml` files,
  but Decision 2 and the Contract say "every file under `api/types/v1alpha1/examples`", so a `.yml` or JSON example
  would be skipped without notice. The test asserts only `NotEmpty`, so a mutant that drops the included lane manifests
  from the list passes (`ok`). Today both files are applied (subtests `own-store.yaml` and `counter.yaml`). Fix: assert
  that every YAML file that `docs/guide` includes from a pinned repo is in the list, and apply or refuse every file of
  the folder.
- **The include pattern matches fewer directives than fumadocs embeds** · `model` · `guideIncludes`
  (`^::include\[([^\]]+)\]\s*$`). funcd-frontend's `scripts/includes.ts` uses the same pattern. fumadocs-core 16.18.4
  embeds every `include` container, leaf or text directive (`dist/mdx-plugins/remark-include.js` L9-15, L27-31), so
  `go test` does not check an include that is indented in a list item or that carries `{…}` attributes. Run: a page
  with `1. Apply it:` and an indented `::include[./no-such-file.yaml]` passed `TestScenarioGuideIncludesResolve` (`ok`),
  and the site build then exited 1 with `failed to read file …/no-such-file.yaml`. The failure is therefore found late,
  but it is not silent. Fix: allow leading whitespace and trailing attributes in the pattern, in both repositories.

### Routed (not this work's)

- **Decision 4 checks any funcd path, but Decision 5 mirrors only three** · home: elsewhere · destination: the decider
  (the ADR's text; the next ADR whose page includes from another funcd folder adds that folder to the mirror). As
  Decision 4 says, `TestScenarioGuideIncludesResolve` accepts any funcd file that exists. As Decision 5 says, the site
  mirrors only `docs/guide`, `api/types/v1alpha1/examples` and `go.mod`. A page that included, for example,
  `examples/funcdconfig.yaml` would pass `go test` and fail the site build. No page does that today. Nothing stays here.
- **The exit code of `just ci`** · home: elsewhere · destination: the repository-wide gate after this review. The run's
  rules exclude `scripts/agent/gate.sh`, `just ci-full` and the e2e suite here.

### ✅ Verified correct (keep it)

- **Tests**, in the funcd worktree through `scripts/agent/d`:
  - `go test -race -count=1 -run 'TestScenarioGuide|TestScenarioExampleManifestRefused' -v ./pkg/funcd/` → `ok`:
    `TestScenarioGuideExamplesApply` (subtests `own-store.yaml` and `counter.yaml`) 0.10 s,
    `TestScenarioExampleManifestRefused` 0.19 s, `TestScenarioGuideIncludesResolve` 0.10 s.
  - `go test -race -count=1 ./pkg/funcd/` → `ok` (50.0 s). `go test -race -count=1 ./internal/testkit/langmod/` → `ok`.
  - `go vet ./pkg/funcd/` exit 0. `go tool golangci-lint run ./pkg/funcd/...` → `0 issues`. `go build ./...` exit 0.
    `gofmt -l` lists nothing. `just check-hygiene` exit 0.
  - The two apply tests five times under `-race` → `ok`. No placeholder replica (`sleep 86400` whose parent is PID 1)
    was left over: there were 0 before the run and 0 after it. Decision 2's claim holds: the placeholders stop with
    the platform.
- **Every scenario has a named check that runs and passes**:
  - `examples-apply`: `TestScenarioGuideExamplesApply` applies the example file and the lane's `counter.yaml` that the
    KV page includes, each on its own `funcd.New(funcd.InMemory())`.
  - `example-manifest-refused`: `TestScenarioExampleManifestRefused`. As a data mutant, the testdata file was copied
    under `api/types/v1alpha1/examples/kv/`. `TestScenarioGuideExamplesApply` then failed with
    `guide example: ../../api/types/v1alpha1/examples/kv/zz-mutant.yaml: document 1 (Function "counter"): sdk:
    admission.binding-declared: …`, so the examples test itself fails with the file's path and the admission's
    refusal.
  - `include-resolves`: `TestScenarioGuideIncludesResolve`, with the fixture `pkg/funcd/testdata/guide-broken/page.md`.
    A data mutant with a missing `_repos` file and a missing region of a pinned file failed the test, and both problems
    named the page and the include.
  - `example-included`, `region-included` and `pinned-handler-code`: the site build. In a fresh worktree of
    funcd-frontend's `main` (c8501a1), `yarn install`, `yarn ui build`, then `FUNCD_DIR=<funcd worktree> yarn build` →
    exit 0, with `fetched 3 included file(s) of funcd-typescript at v0.26.0` and `includes: every page shows what it
    includes`. The KV page's `.md` copy holds five code blocks. Four equal the old inline blocks. The fifth is the
    lane's manifest, with its `oci-layout` image, which the page explains. Neither the page nor its copy keeps an
    `::include` line. Each region shows only its own lines: the files' header comments are left out, and the claim
    excerpt is dedented.
- **Mutants**:
  - Go, through `go test -overlay`: `marksRegion` always true → `TestScenarioGuideIncludesResolve` fails. The refusal
    without the file's path → `TestScenarioExampleManifestRefused` fails. The two mutants that survived are the first
    two Minors.
  - The site's check (`tsx scripts/check-includes.ts` on the built output): a changed claim block, `.tag` set to
    v0.25.0, and `::include[` appended to the page's HTML → exit 1 with three problems, one for each mutant.
  - The site built without `remark-directive` in the preset → exit 1 with seven problems: the page and its `.md` copy
    keep the `::include` lines, and the five code blocks are missing. The ADR's Context names this failure as silent;
    the build now reports it.
- **Contracts**: the two test functions are in `pkg/funcd` with the Contract's names and doc comments.
  `api/types/v1alpha1/examples/kv/own-store.yaml` holds a KVStore, then a Function that declares it by `ref`, with the
  regions `kvstore` and `binding`. Their rendered blocks equal the old page's two blocks. The KV page's five includes
  are exactly the Contract's list.
- **Decision 3**: kv-counter's manifest and handler come from the same lane example (`counter.yaml#manifest` and
  `src/main.ts#handler`), and kv-claim's excerpt is `src/main.ts#claim`. Lanes run both examples
  (`e2e/kv-counter.venom.yml`, `e2e/kv-claim.venom.yml`). The region markers have the forms that the Decision names.
  funcd-typescript#103 changed only those three files (+10/−4), with its CI green, and its comments inside the regions
  are written for readers. v0.26.0 changed nothing that funcd embeds: `shim.mjs` and `pool.mjs` are unchanged.
- **Decision 5** (funcd-frontend#39, CI green): `applyMdxPreset({ remarkPlugins: [remarkDirective] })` is in
  `app/lib/source.ts`, and `remark-directive` ^4.0.0 is MIT. The sync mirrors `docs/guide`,
  `api/types/v1alpha1/examples` and `go.mod` under `content/funcd`. It fetches each file that a page includes from a
  pinned repo at the tag in `go.mod`, and writes the tag to `_repos/<repo>/.tag`. `check-includes.ts` runs after
  `react-router build` and the pages-layout step, and it compares each block with fumadocs-core's own
  `extractCodeRegion`.
- **Review checklist item 1**: `docs/guide/develop/services/kv/index.md` is the guide's only Usage page, and no fenced
  code block is left on it.
- **Tree**: it holds the files of the Contract and of the Implementation plan. The one extra file,
  `pkg/funcd/testdata/guide-broken/page.md`, is the fixture of `include-resolves`. `go.mod` and `go.sum` change only the
  funcd-typescript pin, and no Go dependency is added.
- **Conventions**: imports are at the top level, the refusal is wrapped with `fault.Wrapf`, and each file gets its own
  platform under `t.Parallel()`, as `TestScenarioLibraryCredentialsReplaceDevToken` does. The YAML is in block style,
  and the comments say why.
- **Tracking**: between cfc1fd10 and 62e446d6, the ADR changed only in its Status line (`Accepted (2026-11-12)` →
  `Reviewing`). F02's entry read `tested examples: reviewing`.

### Definition of Done

15 of the 16 items were verified here: the 3 items of the ADR's Definition of done, the 5 items of its Review
checklist, and the 8 generic items that apply. "`just ci` passes" is counted once, although it is both an ADR item and a
generic item, and its result belongs to the gate. The generic item "contract suites pass against every driver" does not
apply, because this ADR adds no port. The count is 16/16 if the gate is green. The Minors cause no miss: today the
apply test applies every file, and the include test reports the page.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0325 (implementation) → pass, 0/0/3, 3 attributed to the model, DoD 15/16 (16/16 with
a green gate). See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. ADR-0325 is stamped `Implemented`, and F02's entry reads `tested examples: implemented`. The three Minors make
the tests stricter; the builder can fold them into this PR or into a later one, and none of them blocks. ADR-0325
realizes the infra row FEAT-0000/F02 and has no feature issue, so its PR closes no issue.
