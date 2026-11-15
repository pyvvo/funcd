## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0331 implementation, round 2, model: claude-opus-5-5)

Round 2 reviews the fixes for [round 1](adr-0331-implementation-claude-opus-5-5.md)'s three Minors:

- funcd `e218a7b1`, on round 1's `303f7c83`: `TestReferencePaths` spells the order of a kind's operations and holds
  each kind folder's title equal to the tag of its operations;
- funcd-frontend `959f977`, on round 1's `26c61861`: a reference page indexes each case's when text and its what to do
  as two search entries;
- funcd-frontend `dd7c1d1`: `search-finds-a-case` runs fumadocs-core's `staticClient` on the built index, the checks
  take their expected answers from the mirrored spec and compare `/openapi.json` with it, `agent-reads-an-operation`
  joins a cell's lines as the copy does, and the script sets its exit code instead of calling `process.exit`.

Round 1's three Minors are closed. One Minor is open, and it is what remains of round 1's Minor 2: no check fails when
the spec that the checks read yields no case.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

1. **No check fails when the spec that the checks read yields no case** · attribution: model
   - The checks now read the mirrored spec, but through fumadocs-openapi's loader (`createOpenAPI(…).getSchema`), the
     loader that `app/lib/openapi.ts` also uses for the pages.
   - A probe that drops `x-funcd-cases` inside that loader, in a scratch install of the dependency, empties both sides.
     Replace a KVStore shows 9 rows, one per status, instead of 18. The build exits 0 and all five checks pass:
     `search-finds-a-case` on 0 case texts, and `function-call-in-reference` on the call's 11 status rows.
   - Round 1 showed the same pass on 0 case texts.
   - Fix: fail when the spec yields no case, or when it yields fewer cases than the mirrored file lists, counted
     without the loader.

### Routed (not this work's)

- Round 1's two routed findings, the summary shown twice in search and the copies' full body schemas, wait for the
  decider, as do the answers to the two open questions, the samples' `example.com` host and the playground left off.
  The code keeps Decisions 3.3 and 3.5 as written, and this round rules on none of them.

### ✅ Verified correct (keep it)

- **Round 1's Minor 1 is closed: a search for each case's text shows it on a page that answers it.**
  - The site's own search code (`zbsearch`'s `create` and `load`, then `searchAdvanced` with its defaults), with the
    expected cases read by a plain YAML parse of the mirrored spec, finds each of the 343 case texts on a page that
    answers it. "The name in the path is not a DNS-1123 label." now returns the three Namespace pages first. The search
    dialog in a headless Chromium shows Read a Namespace first, with that text as its first hit.
  - The check now searches. With the single joined entry restored, the build exits 1 with one problem: the Namespace
    text, which the search then shows on AppRevision and App pages. With the when entries dropped, it fails all 343
    case texts. A case text blanked in the built index fails it too.
  - Decision 3.3 still holds with two entries per case. Each of the 143 pages has a search entry equal to its summary,
    and each of the 1,205 cases has one entry with its when text and one with its what to do, under the Answers
    heading. No joined entry is left. The index grew from 9,823,420 to 10,671,044 bytes.
- **Round 1's Minor 2 is closed: the checks no longer take their answers from the site's output.**
  - The expected answers come from `content/funcd/api/openapi/funcd.v1alpha1.yaml`, read apart from
    `app/lib/openapi.ts`, and `reference-shows-an-operation` fails when `/openapi.json` holds other cases.
  - Round 1's mutant, which drops `x-funcd-cases` in `app/lib/openapi.ts`, now fails four checks:
    `reference-shows-an-operation` with 286 problems, `function-call-in-reference` with 19, `search-finds-a-case`
    with 343 and `agent-reads-an-operation` with 1,205. `reference-has-its-own-sidebar` reads no case. A case dropped
    from the built `/openapi.json` fails `reference-shows-an-operation`.
  - With `process.exitCode`, that build exits 1, and through a pipe the report keeps its header and all 1,853
    problems, 1,854 lines.
- **Round 1's Minor 3 is closed: `TestReferencePaths` has its own oracle for the order and the title.**
  - `get` before `create` in `verbs` fails "workloads/function: get before create", and `delete` before `replace`
    fails as well.
  - A lower-cased title and a title taken from the section's name each fail "the folder is titled with its
    operations' tag".
  - Round 1's 8 other layout mutants, such as the sections sorted by name, are still killed.
- **Nothing that the fixes touch regressed.** Each of the 22 built-site mutants is killed by the check named for it:
  round 1's 21, with the dropped index entries replaced by a blanked case text, and a case dropped from the served
  `/openapi.json`.

### Checks run

funcd, at `e218a7b1`:

| Check | Result |
| --- | --- |
| `go test -race -count=1 ./internal/controlplane/kindref/...` on the batch | exit 0 |
| `go vet` and `golangci-lint run` on `kindref` | exit 0; 0 issues |
| The four order and title mutants, and round 1's 8 other layout mutants, in a scratch worktree | each killed |

funcd-frontend, at `dd7c1d15`:

| Check | Result |
| --- | --- |
| `yarn install`; `yarn lib build`; `yarn ui build` | exit 0 |
| `yarn build` in `packages/docs`, with `FUNCD_DIR` set to the batch: the sync, the build and every check | exit 0; "a search for each of 343 case texts shows it on a page that answers it", and the other four checks pass |
| `yarn react-router typegen`, then `yarn tsc -p tsconfig.json`, which includes `scripts/` | exit 0; exit 0 |
| `just ci` | exit 0 |
| `yarn vp lint` on `app/lib/source.ts` and `scripts/check-api-reference.ts` | exit 0 |

The source mutants and the probe ran on a scratch worktree of funcd-frontend, each built against the batch:

| Mutant | Result |
| --- | --- |
| The single joined entry restored in `withCases` | build exit 1: `search-finds-a-case`, the Namespace text only |
| The when entries dropped from `withCases` | build exit 1: `search-finds-a-case`, 343 problems |
| `x-funcd-cases` dropped in `app/lib/openapi.ts` (round 1's Minor 2) | build exit 1: four checks, 1,853 problems |
| `x-funcd-cases` dropped in fumadocs-openapi's loader (probe) | build exit 0: all five checks pass (the open Minor) |

### Definition of Done

13 of the 13 items still hold.

### Model scorecard

To be recorded with the batch's ledger rows: claude-opus-5-5 on ADR-0331 (implementation, round 2) → pass, 0/0/1, 1
model-attributed, DoD 13/13. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. ADR-0331 has been Implemented since round 1, so this round advances no status.

- The open Minor goes back to the builder: one guard in `check-api-reference.ts`.
- The routed items wait for the decider's ruling.
