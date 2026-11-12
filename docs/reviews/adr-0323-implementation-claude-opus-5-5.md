## Verdict: pass — 0 blockers, 0 majors, 3 minors  (ADR-0323 implementation, model: claude-opus-5-5)

Branch `docs/adr-0323-api-reference`, three commits on `origin/main`: 193fc8f9 (the ADR), 8a6a65ac (the implementation),
2d64e0d5 (the KV Usage page, the Examples page and the Develop sidebar, shipped with this PR by the decider's choice and
reviewed for correctness and the guide's rules only). The pass depends on the repository-wide gate
(`scripts/agent/gate.sh`, which runs `just ci-full`). That gate ran in the same worktree at the same time and is
attached separately. If it fails because of this change, the verdict becomes changes-requested.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The constant pin is a substring match** · `model` · `api/types/v1alpha1/doc_test.go`:
  `require.Contains(t, got, c.want)` checks that the `doc` tag contains the constant's digits. The `maxKeyBytes` tag
  "0 means 1024 (1 KiB)" therefore still passes if `DefaultMaxKeyBytes` becomes 102, 10, 24 or 2, and the
  `maxValueBytes` tag "1048576" has the same weakness. A change to an unrelated number (1024 to 2048) is caught. Fix:
  match the tag's phrase (`"0 means " + want + " "`) or a whole number, not a substring.
- **The registry row leaves out one sentence of the ADR's Behavior rows** · `model` · `docs/behavior/limits.md`, row
  "A `spec.quota` refused at apply". The row records the 422 at `body.spec.quota.maxSize`, the three 400 cases and
  `differs`. An explicit empty `maxSize` is covered too, because the row says "outside the byte-size grammar". The row
  does not say that `funcdctl apply` runs `Validate` offline first, so a CLI user still sees `Validate`'s message and
  not the 422 that the answer column gives. Fix: add that clause to the row.
- **specgen's removal of stale pages has no test** · `model` · `internal/controlplane/cmd/specgen/main.go`
  (`writeGuide`). The `Marker` contract says specgen deletes a generated page that `Render` no longer returns, and no
  test pins this. This review ran it by hand and it works (see mutant E below). Fix: add a small test that runs
  `writeGuide` on a temporary guide folder holding one stale marked page and one hand-written page.

### Routed (not this work's)

- **The task issue for the order of the remaining descriptions** · home: elsewhere · destination: the PR step. The
  ADR's Open questions say it is "filed with the implementation PR" (Function, App and Workflow first).
- **The status stamp** · home: elsewhere · destination: the caller's Step 6. This run writes nothing in the
  worktree. If the gate is green, the ADR moves `Reviewing → Implemented` (with the date) and FEAT-0000 F02's
  "Spec tabs" goes from `reviewing` to `implemented`. No feature issue needs closing: the References cite none, and F02
  is the API contracts and codegen row.
- **The exit code of `just ci`** · home: elsewhere · destination: the gate's result, which the caller attaches. This
  review checked each part of it that writes nothing (below).

### ✅ Verified correct (keep it)

Commands were run through `scripts/agent/d`, and none of them wrote into the worktree (`git status --porcelain` stayed
empty).

- **Tests**:
  - `go test -count=1 -race ./internal/controlplane/kindref/ ./api/types/v1alpha1/` → `ok` for both, exit 0.
  - `go test -race ./scripts/ -run 'TestScenarioStaleGeneratedFileFailsCI|TestScenarioUndescribedListNeverGrows' -v`
    → both `--- PASS`.
  - `go test ./internal/controlplane/ -run 'TestScenarioByteSizeGrammarInSchema|TestSpecGeneratedFromGo|TestOpenAPIDocServed' -v`
    → all three `--- PASS`.
  - `go vet` on the five touched packages → exit 0.
  - `go tool golangci-lint run` on kindref, specgen, v1alpha1, scripts and controlplane → `0 issues.`, exit 0.
  - `go run ./scripts/behavior -check` → exit 0.
- **Every scenario has a named test that is not skipped and passes**:
  - spec-tab-generated: `TestScenarioSpecTabGenerated`
  - every-kind-in-one-feature: `TestScenarioEveryKindInOneFeature`
  - undescribed-field-refused: `TestScenarioUndescribedFieldRefused`
  - undescribed-list-only-shrinks: `TestScenarioUndescribedListOnlyShrinks`
  - undescribed-list-never-grows: `TestScenarioUndescribedListNeverGrows`
  - stale-generated-file-fails-ci: `TestScenarioStaleGeneratedFileFailsCI`
  - byte-size-grammar-in-schema: `TestScenarioByteSizeGrammarInSchema`, through the API: 422, which names the grammar
  - doc-names-the-constant: `TestScenarioDocNamesTheConstant`, with the `maximum` pin in `kvstore_test.go` kept
- **A second `just generate` changes nothing**: specgen ran twice into a scratch folder. Both runs were identical.
  The YAML was byte-identical to `api/openapi/funcd.v1alpha1.yaml`, and all 16 pages (15 `spec.md` and
  `common-definitions.md`) were identical to the committed pages.
- **Mutants** (`go test -overlay`, with the files outside the worktree):
  - A: a `doc` tag that names "ADR-0073" on `KVStoreStatus.tables` makes `TestEveryFieldIsDescribed` fail with
    "names no ADR".
  - B: removing that field's tag makes the test fail and name `KVStoreStatus.tables`.
  - C: a `ByteSize.Schema` that returns one shared schema makes both `TestSpecGeneratedFromGo` and
    `TestEveryFieldIsDescribed` fail.
  - E: specgen run on a scratch guide folder printed `specgen: removed develop/old/spec.md` for a stale marked page and
    kept the hand-written `index.md`.
  - F: `scripts/check-generated.sh`, run on a scratch repository with an untracked page in a new feature folder, exits 1
    and prints the page.
- **Links**: all 340 relative links and anchors in `docs/guide/develop/**/*.md` resolve (checked with
  github-slugger's heading rules), and no page has two headings with the same id. Each Spec page links an existing
  `docs/guide/platform/<path>.md`.
- **Contracts**:
  - `Feature`, `Features` (the 15 features of Decision 1, with the kinds in its order), `Omitted` (the same five kinds;
    the order differs, but it is a set), `Marker` (the same string), `Render`, `Undescribed`, `SpecAPI` and
    `ByteSize.Schema` match the Contracts.
  - `SpecAPI` replaces the two copies of the stub wiring in specgen and `api_test.go`.
- **Decision 1**: each kind has its apiVersion and kind, then its spec and status tables. Each nested object has its own
  table, linked from its parent's row. A schema already shown is linked, not repeated. The common definitions are linked
  to `common-definitions.md`, and fields are sorted by name.
- **Decision 4**: `ByteSize.Schema` returns a new schema on each call. Its pattern is the single `ByteSizePattern` that
  `byteSizeForm` compiles. The `pattern` tags are gone from `volume.go` and `namespace.go`, and no copy of the pattern
  is left in the code. No test or suite asserts the old pattern message.
- **Decision 5 (CI)**:
  - ci.yml has a `generated` output. It is ORed into the `ci` job's `if` and into the `just ci` step's, and the step for
    docs-only changes excludes it.
  - The list step runs on `pull_request` and `merge_group`, fetches `base.sha` or `merge_group.base_sha`, and runs
    `scripts/check-undescribed.sh`. The script fails on a base it cannot read and passes on a base that has no list.
  - The `ci` recipe runs `scripts/check-generated.sh` after `generate`.
- **Doc tags**:
  - They are written for users and name no package and no ADR. No `entrySchema` description names the App.
  - Their claims match the code: funcd never sets `deletionTimestamp`, and it never reads `finalizers` or
    `blockOwnerDeletion` (it only sets the latter). A Function's root KV tables default their owner to the Function
    (ADR-0248 Decision 4).
  - KVStore, its quota, ObjectMeta, OwnerReference, Condition and `Status` have no entry left in
    `testdata/undescribed.txt` (520 lines).
- **`.claude/rules/api-types.md`**: its `paths` list is in block style.
- **Tracking**: between 193fc8f9 and HEAD, the ADR changed only in its Status line, which now reads `Reviewing`. The
  F02 row reads "Spec tabs: reviewing". `go.mod` is unchanged, so no dependency was added.
- **The KV guide commit (2d64e0d5)**:
  - The pages use a neutral voice ("you", "your" and "we" do not appear).
  - Its facts were checked against the code and against funcd-typescript v0.25.0, the pinned version:
    - The method table and the rules for `ttl` and expiry match the shim's `kv.ts`.
    - The TypeScript snippet is kv-counter's `src/main.ts` without its header comment.
    - The claim snippet matches kv-claim's `src/main.ts`.
    - The Function manifest matches kv-counter's `counter.yaml`, except for a placeholder image, which `funcdctl dev`
      replaces with its build (`cmd/funcdctl/dev.go`).
    - `kvstore.engine: badger` and `funcdctl dev --persist` match `cmd/funcd/main.go` and `cmd/funcdctl/dev.go`.
    - The claims of the Examples page hold: funcd-python has Lake examples, and funcd-functions has the todo App.
  - The sidebar lists only the KV feature, the common definitions and Examples, as the decider chose. The other 14 Spec
    pages are built but are not in the sidebar yet.
  - This review did not run the examples with `funcdctl dev`.

### Definition of Done

23 of the 24 items were verified here: the 5 items of the ADR's Definition of done, the 11 items of its Review
checklist, and the 8 generic items that apply. The generic item "contract suites pass against every driver" does not
apply, because this ADR adds no port. The remaining item, `just ci` exits 0, is the gate's result, so the count is
24/24 if the gate is green. The Minors do not cause a miss: every pinned tag is pinned (only weakly), and the
`limits.md` row holds the ADR's facts except the CLI clause.

### Model scorecard

This run did not record the scorecard, because the caller records it: claude-opus-5-5 on ADR-0323 (implementation) →
pass, 0/0/3, 3 attributed to the model, DoD 23/24 (24/24 with a green gate).

### Recommendation

When the gate is green, stamp the ADR `Implemented` and set F02's "Spec tabs" to `implemented`, and file the task issue
for the order of the descriptions with the PR. The three Minors can go into this PR or a later one. None of them blocks
sign-off.

### Gate and follow-up

- `scripts/agent/gate.sh` on 2d64e0d5: PASS. The audit, `ci-full` (e2e included), the Linux build, vet and lint, the
  stress run and the unchanged tree all passed; the Linux-specific tests were skipped because their VM was not running,
  and the change touches no Linux-specific code. The verdict is therefore pass, and the definition of done holds 24 of 24.
- The three minors are fixed in 0815560f: the constant pins match whole numbers, `TestWriteGuideRemovesOnlyStaleGeneratedPages`
  covers specgen's removal of a stale page, and the `limits.md` row names `funcdctl apply`'s offline `Validate` message.
