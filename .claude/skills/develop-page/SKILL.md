---
name: develop-page
description: Write or update one page of the Developer chapter of funcd's user guide, under docs/guide/develop, to the shared template — the hand-written Usage tab (index.md) with tested, included examples, beside the generated Spec tab (spec.md). Use for "write the Function page", "bring the Bucket page to the template", "develop page for SQL", or the rollout of the Develop chapter. It writes prose and examples, checks every fact in the code, and stops on any conflict between sources.
---

# develop-page

One feature page of the Developer chapter, written to one shape so a person or an agent finds the same things in the
same place. KV (`docs/guide/develop/services/kv/index.md`) is the reference page: read it before writing another.

## What is generated, what is written

| Part | Source | Written by |
| --- | --- | --- |
| Spec tab (`spec.md`): each kind's tested example, its spec and status fields, the Handler API in each language | the Go types' `doc` tags, `api/types/v1alpha1/examples/`, the shims' `api.json` | `just generate` (kindref); never edit it |
| Usage tab (`index.md`): the sections of `template.yaml`, prose and `::include` lines | this skill | this skill |
| Code on a page | a tested file: a manifest under `api/types/v1alpha1/examples/<feature>/`, or a region of a lane example in a language repo | this skill adds it when missing |

kindref (`internal/controlplane/kindref/kindref.go`) holds two tables: `Features`, each feature's path and kinds, and
`clients`, the ctx clients whose methods a feature's Spec tab lists under Handler API (`"services/kv": {"kv"}`; `""`
is the context's own methods, shown as `ctx`).

`<feature>` is the last part of the feature's path (`services/kv` → `kv`). A kind's example on the Spec tab is
`<feature>/<kind>.yaml`, the kind's name in lower case: the file holds every object the example needs, and its region
named after the kind marks the kind's own object (`kv/kvstore.yaml#kvstore`). A kind that funcd creates rather than
a user (Revision, AppRevision, Invocation) has no example file.

A region opens and closes with a comment: `// #region <name>` and `// #endregion <name>` in TypeScript, `# region
<name>` and `# endregion <name>` in YAML and Python.

## Two routes

Every page shares the reader contract of `template.yaml`'s first section; the work differs by route.

- **A feature of kindref's `Features` table**, with kinds and maybe a handler: steps 5 to 9 apply as written, and
  `TestDevelopPagesFollowTheTemplate` checks its Usage page.
- **A tool or configuration page** (funcd, funcdctl): its Spec page comes from `internal/reference`, which `just
  generate` writes from the config struct and the command trees, as `.claude/rules/config-and-cli.md` says. Its first
  example is the tool's own command, run as the page documents it; it needs no example manifest and no push. Its page
  tests (`cmd/funcd`, and `cmd/funcdctl` with `-tags dev`) check it, not the template test.

## Steps

1. **Read** `template.yaml` (beside this file), the rules in `.claude/rules/` whose `paths` cover `docs/guide/`, the
   KV page and the pages of the feature's group, so the new page reads like its siblings.
2. **Gather**, and rank the sources when they disagree: the code on origin/main (the code path, not its comments),
   its tests, the behavior registry's rows (`docs/behavior/`), Implemented ADRs, then the Platform page. The feature's
   kinds and fields are on its Spec page; its handler API is in the shims' `api.json`; `scripts/lanes.yaml` names the
   lane that runs each example, and the lane's fixtures (`e2e/fixtures/`) hold what it applies besides the example,
   such as a RolesAssignment.
3. **Stop on a conflict** between sources: apply the two checks of CLAUDE.md ("Is it a bug?") before writing. A
   behavior they leave `uncovered` is not written as fact: list it in the PR as a question for the decider.
4. **Keep a fact ledger** for the page in the session's scratchpad: each claim with the file and line, test, row or
   ADR that supports it. The PR description carries it in short.
5. **Prepare the Spec tab.** Every field needs a user-facing `doc` tag, and `TestEveryFieldIsDescribed` refuses a
   field without one: add the missing tag as `.claude/rules/api-types.md` says, run `just generate`, then the test.
   To list a client under Handler API, add it to `clients`; first check that each of its methods has
   a doc in both shims' `api.json` that names no ADR, or `TestEveryHandlerMethodIsDescribed` fails. A missing or
   internal doc is fixed in the language repo: one PR and one release per repo, then the `go get` bump in funcd.
6. **Plan the sections** from the template: which optional ones apply, which free topics the feature needs. The
   first section links the Platform page instead of repeating it. Describe a field or a method on the Spec tab, not
   in a hand-written table.
7. **Add missing examples.** A manifest a page shows without a handler goes under
   `api/types/v1alpha1/examples/<feature>/`, one scenario per file with every object it needs;
   `TestScenarioGuideExamplesApply` applies it. Handler code comes from a lane example, marked by a region, each
   language from its own example; the regions of a batch go in one PR and one release per language repo, then the
   `go get` bump in funcd.
8. **Write** in the guide's voice, around the reader's task: the first section's example is the one that
   `template.yaml` names for the feature's type (service, workload, tool or cross-cutting), a free section is one task
   under a heading that names it, and an optional section appears only when it explains a real decision or behavior.
   Run the first example before keeping it, as its route runs it: a tool's command with the tool itself; a resource
   manifest, such as a namespace or a grant, by applying it to a running platform with no push; the server's
   configuration file by starting funcd with `--config`; and a service or workload example as follows: `scripts/example-copy.sh js/<example>` (or
   `python/<example>`) prints a writable copy of a lane example, and `scripts/agent/d go run -tags dev ./cmd/funcdctl
   dev <that folder>` runs it as it stands. dev runs each Function from its local build, which it finds by the
   Function's name (`<name>.mjs`, or the Python entry), in place of its `image` (`cmd/funcdctl/dev_folder.go`). Call
   it on the gateway address that dev prints, then stop dev with SIGINT (Ctrl-C).
9. **Check**: `scripts/agent/d just generate`; `go test` for `TestDevelopPagesFollowTheTemplate`,
   `TestScenarioGuideIncludesResolve` and `TestScenarioGuideExamplesApply` (`pkg/funcd`), and for kindref when its
   tables changed; then the site's build, in funcd-frontend's `packages/docs`, with `FUNCD_DIR` set to the funcd
   worktree (`yarn build`, whose last step, `check-includes.ts`, fails on an include that did not render). On the
   tool route: `just generate`, then the page tests of `cmd/funcd` and of `cmd/funcdctl` with `-tags dev`.
10. **Critique**: one subagent per page, reused across rounds, checks the page against the code, the template and the
    reader, briefed with the files, not your conclusions. Fix what the page owns; route the rest (an issue, a board
    card, a question in the PR).
11. **Fresh-reader check**: an agent other than the writer reads what the site publishes, not the source: after the
    site's build, the page's Markdown copy with its includes expanded (`build/client/docs/<page path>.md` in
    funcd-frontend's `packages/docs`, which the site serves at the page's URL plus `.md`) and its Spec page's copy. It
    first checks that each copy is complete: no `::include` line is left, every code block is present, and each link
    resolves from the copy's own URL. It may
    follow the links that the page names as prerequisites or examples, and runs the first example in the documented
    environment, but never reads funcd's code to fill a gap: each step it cannot do and each term it cannot resolve is
    a finding. Its report keeps what it ran apart from what it only read, and puts a source or renderer problem in a
    part of its own. The PR description carries the report with the commit and the checks, and names each kind of
    evidence apart: the critic's code-checked review, the fresh reader's docs-only reading, and each run, with what
    ran. The page carries none of it.
12. **Stop** when the checks pass, the critic has no finding the page owns, the fresh reader finished the first
    example, and the fact ledger has no open line.

## Rules

- No ADR number and no internal name on a page.
- No manifest or handler code in a fence: only `sh`, `bash` or `console` blocks, and none of them holds a manifest,
  in YAML or in JSON.
- Code from a language repo sits under its language's `### TypeScript` or `### Python` heading, wherever it appears.
  The site shows each run of these sections as one group of language tabs, and a section runs to the next heading, so
  the text that holds for both languages comes before them.
- No hand-written table of methods, fields or keys: the Spec tabs hold them.
- Links to a pyvvo repository point at its main branch, never at a tag: only `go.mod` names a language-module version.
  An Examples link points at a folder that a lane in `scripts/lanes.yaml` runs, which exists at the pinned version.
- A page's facts are checked in the code; a doc tag or a comment that disagrees with the code is a finding, not a
  source.

`TestDevelopPagesFollowTheTemplate` enforces the sections and these rules as far as the text shows them: it refuses a
manifest in a fence and a table whose first column is Method, Field or Key. The critic checks the rest: internal
names, handler code in a shell block, a table headed otherwise, and every fact.
