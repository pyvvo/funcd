## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0331 implementation, round 3, model: claude-opus-5-5)

Round 3 checks only the fix for [round 2](adr-0331-implementation-claude-opus-5-5-2.md)'s Minor, and records the
decider's ruling on the routed findings. The fix is funcd-frontend `05affd01`, one commit on round 2's `dd7c1d15`. It
changes only `packages/docs/scripts/check-api-reference.ts`, 23 lines added and 1 changed, and funcd's batch has not
changed since round 2's report (`c14c5ea9`).

The new `spec` check runs before the five scenario checks. It fails when fumadocs-openapi's loader yields no case, and
when the number of cases that it yields differs from the number of the mirrored YAML file's lines that match
`^ *(- )?when: \S`.

Round 2's Minor is closed, and no finding is open.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### Routed findings, ruled

The decider ruled on 2026-11-15, and nothing changes:

- **A reference page's summary shows twice in the search results.** It stays, because it keeps ADR-0331's behavior.
- **Each Markdown copy writes its body's schema in full.** It stays, because linking a kind's Spec tab instead would
  change what an agent reads in one copy.
- **The two open questions.** The decider confirmed the `example.com` samples and the playground left off.

### ✅ Verified correct (keep it)

- **Round 2's Minor is closed: a loader that loses cases now fails the build.**
  - Round 2's probe, which drops `x-funcd-cases` inside fumadocs-openapi's loader, makes the build exit 1 with one
    problem: "fumadocs-openapi yields 0 cases from content/funcd/api/openapi/funcd.v1alpha1.yaml, whose lines list
    1205".
  - The five scenario checks pass in each probe, because the pages and the expected answers lose the same cases; the
    `spec` check alone catches the fault.
- **The line count is independent of the loader, and sound for funcd's spec.**
  - It reads the mirrored file as text, with no parser and no fumadocs code.
  - On the batch's spec, it counts 1,205 lines, the 1,205 cases that the loader yields, and the 1,205 that a plain
    YAML parse finds across 143 operations. Every when text is one line.
  - The file has 1,206 lines with a `when:` key. The extra one is the property `WorkflowStep.when`, whose `$ref` sits
    on the lines below the key, so `\S` leaves it out.
  - A change of funcd's format fails the check instead of passing it silently. With each when text moved to the next
    line, the file lists 0 cases against the loader's 1,205. With `x-funcd-cases` and `when:` both renamed, the loader
    and the file both give 0, and the check fails on the empty spec.
- **The probes that the count catches and the one that it does not.** Each ran in a scratch install of the dependency:

  | The loader | Build | Problem |
  | --- | --- | --- |
  | drops every case | exit 1 | `spec`: 0 against 1205 |
  | drops one case, the first 409 case of Replace a KVStore | exit 1 | `spec`: 1204 against 1205 |
  | drops the call of a Function's 19 cases | exit 1 | `spec`: 1186 against 1205 |
  | adds a copy of one case | exit 1 | `spec`: 1206 against 1205 |
  | changes the text of one case | exit 0 | none |

  - The last probe shows the limit: the check compares numbers, not texts. A loader has no reason to rewrite a
    string, unlike dropping an extension that it does not know, so this limit is acceptable. Two faults that cancel
    out, one case lost and one extra line matched, would pass too.
- **Nothing else changed.** The diff from `dd7c1d15` to `05affd01` holds only the new check and the script's header
  comment, which now names it.

### Checks run

| Check | Result |
| --- | --- |
| `yarn build` in `packages/docs` at `05affd01`, with `FUNCD_DIR` set to the batch | exit 0; "fumadocs-openapi yields 1205 cases from the mirrored spec, as many as its lines list", and the five scenario checks pass |
| `yarn react-router typegen`, then `yarn tsc -p tsconfig.json`, which includes `scripts/` | exit 0; exit 0 |
| `just ci` | exit 0 |
| `yarn vp lint` on `scripts/check-api-reference.ts` | exit 0 |
| The five loader probes, and the two probes of the mirrored file, on a scratch worktree of funcd-frontend | as listed above |

Every probe ran on the scratch worktree's own install, and the frontend worktree's tracked files did not change.

### Definition of Done

13 of the 13 items still hold.

### Model scorecard

To be recorded with the batch's ledger rows: claude-opus-5-5 on ADR-0331 (implementation, round 3) → pass, 0/0/0, 0
model-attributed, DoD 13/13. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass, with no finding open. ADR-0331 has been Implemented since round 1, so this round advances no status, and the
decider's ruling changes no code.
