---
name: develop-page
description: Write or update one feature page of funcd's user guide, Developer chapter (docs/guide/develop/<group>/<feature>/), to the shared template — the hand-written Usage tab (index.md) with tested, included examples, beside the generated Spec tab (spec.md). Use for "write the Function page", "bring the Bucket page to the template", "develop page for SQL", or the rollout of the Develop chapter. It writes prose and examples, checks every fact in the code, and stops on any conflict between sources.
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
5. **Prepare the Spec tab.** A field with no `doc` tag shows with an empty description and is listed in
   `internal/controlplane/kindref/testdata/undescribed.txt`: describe it as `.claude/rules/api-types.md` says and
   delete its line. To list a client under Handler API, add it to `clients`; first check that each of its methods has
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
8. **Write** in the guide's voice. Run the quick start before keeping it: `scripts/example-copy.sh js/<example>` (or
   `python/<example>`) prints a writable copy of a lane example, and `scripts/agent/d go run -tags dev ./cmd/funcdctl
   dev <that folder>` runs it as it stands. dev runs each Function from its local build, which it finds by the
   Function's name (`<name>.mjs`, or the Python entry), in place of its `image` (`cmd/funcdctl/dev_folder.go`). Call
   it on the gateway address that dev prints, then stop dev with SIGINT (Ctrl-C).
9. **Check**: `scripts/agent/d just generate`; `go test` for `TestDevelopPagesFollowTheTemplate`,
   `TestScenarioGuideIncludesResolve` and `TestScenarioGuideExamplesApply` (`pkg/funcd`), and for kindref when its
   tables changed; then the site's build, in funcd-frontend's `packages/docs`, with `FUNCD_DIR` set to the funcd
   worktree (`yarn build`, whose last step, `check-includes.ts`, fails on an include that did not render).
10. **Critique**: one subagent per page, reused across rounds, checks the page against the code, the template and the
    reader, briefed with the files, not your conclusions. Fix what the page owns; route the rest (an issue, a board
    card, a question in the PR).
11. **Stop** when the checks pass, the critic has no finding the page owns, and the fact ledger has no open line.

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
