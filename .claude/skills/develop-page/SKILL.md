---
name: develop-page
description: Write or rewrite funcd Develop Usage pages with the service and workload pilots, complete tested examples, language tabs, and generated Spec links.
---

# develop-page

Write one feature page around its reader's tasks. Read the [pilot notes](references/usage-pilot.md) first. Secrets
(`docs/guide/develop/services/secrets/index.md`) is the service pilot; Site
(`docs/guide/develop/workloads/site/index.md`) is the workload pilot. Use the relevant page as a worked example,
not a word limit. KV's Access section (`docs/guide/develop/services/kv/index.md`) also shows the service access pattern.

## What is generated, what is written

| Part | Source | Written by |
| --- | --- | --- |
| Spec tab (`spec.md`): each kind's tested example, its spec and status fields, the Handler API in each language | the Go types' `doc` tags, `api/types/v1alpha1/examples/`, the shims' `api.json` | `just generate` (kindref); never edit it |
| Usage tab (`index.md`): short tasks, tested examples, Status and local CLI error tables | this skill and `template.yaml` | this skill |
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

## Wording-only rewrites

Shorten the prose around the examples. Preserve the existing tested includes and the code they show, including
imports, types, manifests, commands and expected outputs. Keep each example inside its original language tab.
Do not replace a complete example with an excerpt or a link during this pass.

Assume the reader knows the language. Language names belong in the tabs. Explain funcd-specific requirements and
consequences; link method contracts to Spec instead of narrating the code. Keep necessary setup, directories,
access, waits, recovery and behavior that a reader cannot infer safely.

Record where every useful concept remains or the precise reference that explains it now. A useful concept affects
setup, correct behavior, access, state or recovery. This does not require keeping every description of a helper's
implementation. Read each reference destination: a generic Spec link does not preserve a fact that Spec does not
explain. Avoid adding copies of an example already shown; link to the existing example instead.

Keep the baseline built Markdown, then compare the rewritten built copy before presenting the preview:

```sh
python3 .claude/skills/develop-page/scripts/check_examples.py before-expanded.md after-expanded.md
```

A missing, changed or moved example or tab fails this check. It does not check prose correctness; the critic still
reviews coverage and qualifiers. Use the normal site build for the preview and the Markdown, including its native
tabs and syntax highlighting. Keep runtime verification separate from content and rendering checks.

## Steps

1. **Read** `template.yaml` (beside this file), the rules in `.claude/rules/` whose `paths` cover `docs/guide/`, the
   relevant pilot, the current page and its Spec and Platform pages. Reuse this context for related pages; reread
   the pilot when it changes. Read KV's Access section when the feature uses roles or Cedar.
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
6. **Plan the sections** from the template: which optional ones apply, which free topics the feature needs. The first
   section reaches a runnable example after a short purpose and its prerequisites, counted in the rendered page with
   every include expanded: a long included file goes after the first commands or into its task. Link concepts and Spec
   fields instead of repeating them. Comprehensive operation, request/response and error catalogues belong in a
   separate API Reference, generated from authoritative schemas and operation metadata. Keep the minimal request
   payloads, response examples and recovery steps needed to run and verify the Usage example. A server error remains
   an API error when a CLI prints it. The navigation dropdown and reference generation are separate work; link that
   reference only once it exists.
7. **Add missing examples.** A manifest a page shows without a handler goes under
   `api/types/v1alpha1/examples/<feature>/`, one scenario per file with every object it needs;
   `TestScenarioGuideExamplesApply` applies it. Handler code comes from a lane example, marked by a region, each
   language from its own example; the regions of a batch go in one PR and one release per language repo, then the
   `go get` bump in funcd.
8. **Write** in the guide's voice, around the reader's task: the first section's example is the one that
   `template.yaml` names for the feature's type (service, workload, tool or cross-cutting), a free section is one task
   under a heading that names it, and an optional section appears only when it explains a real decision or behavior.
   Keep only text needed to perform the task, choose a setting, check the result or recover from a failure. Remove
   repeated explanations and commentary that restates code. Keep complete English and every required input,
   access requirement, wait, expected result, retry step and data-loss warning.
   When the page replaces an earlier version, check its coverage concept by concept against that version: list each
   concept and task the earlier page explained and the section that explains it now, or the reference catalogue it
   moved to. Brevity never drops a concept a reader or an LLM needs to use the feature.
   Run the first example before keeping it, exactly as the page documents it: each command in order, in an
   environment that contains only the prerequisites the page declares, with the documented release of each tool it
   uses. When the workflow starts from a repository, start from a fresh clone. Record the versions. A helper such as
   `scripts/example-copy.sh`, a copy of a module, or a setup the page does not declare does not count. A tool's command runs with
   the tool itself; a resource manifest, such as a namespace or a grant, is applied to a running platform with no
   push; the server's configuration file starts funcd with `--config`. `funcdctl dev` runs each Function from its
   local build, which it finds by the Function's name (`<name>.mjs`, or the Python entry), in place of its `image`
   (`cmd/funcdctl/dev_folder.go`).
9. **Check**: `scripts/agent/d just generate`; `go test` for `TestDevelopPagesFollowTheTemplate`,
   `TestScenarioGuideIncludesResolve` and `TestScenarioGuideExamplesApply` (`pkg/funcd`), and for kindref when its
   tables changed; then the site's build, in funcd-frontend's `packages/docs`, with `FUNCD_DIR` set to the funcd
   worktree (`yarn build`, whose last step, `check-includes.ts`, fails on an include that did not render). On the
   tool route: `just generate`, then the page tests of `cmd/funcd` and of `cmd/funcdctl` with `-tags dev`. A preview
   is reviewed only once it shows the revision under review: open it with a query that names the revision
   (`?rev=<commit>`), and find a sentence that the revision added.
10. **Critique**: a critic subagent other than the writer checks the page against the code, the template and the
    reader, briefed with the files, not your conclusions. Across several pages, keep one critic and resume it for each
    page instead of starting a new one, and keep each writer for its related pages the same way: they keep the shared
    instructions, the facts already verified with their sources and the coverage evidence, read the next page, its
    Spec and Platform pages and the semantic diff, and reopen the code only for a new or changed claim or when a cited
    source changed version. Fix what the page owns; route the rest (an issue, a board card, a question in the PR).
    After each round of fixes, read the whole page again from the top, so that a fix reaches every sentence with the
    same problem, not only the one quoted.
11. **Fresh-reader check**: an agent other than the writer reads the whole of what the site publishes, even after a
    small change, not the source: after the site's build, the page's Markdown copy with its includes expanded
    (`build/client/docs/<page path>.md` in funcd-frontend's `packages/docs`, which the site serves at the page's URL
    plus `.md`) and its Spec page's copy. It first checks that each copy is complete: no `::include` line is left,
    every code block is present, and each link resolves from the copy's own URL. It may follow the links that the page
    names as prerequisites or examples, and runs the first example in the documented environment, but never reads
    funcd's code to fill a gap: each step it cannot do and each term it cannot resolve is a finding. Its report keeps
    what it ran apart from what it only read, and puts a source or renderer problem in a part of its own. The PR
    description carries the report with the commit and the checks, and names each kind of evidence apart: the critic's
    code-checked review, the fresh reader's docs-only reading, and each run, with what ran. The page carries none of
    it.
12. **Stop** when the checks pass, the critic has no finding the page owns, the fresh reader finished the first
    example, and the fact ledger has no open line.
13. **Before a guide-wide rewrite or rollout ships**, run a fresh-app gate on the frozen build: freeze the built docs
    (llms.txt and the expanded Markdown) and the CLI and SDK versions, then give several small new agents, none of
    which wrote or checked a page, in bounded batches within the agent cap, only an application idea and llms.txt.
    Check each idea against the public docs first: an idea that the docs say cannot be built, such as an HTTP Function
    in Python, is an invalid task, not a gap. Start each probe with no project context and record how: a subagent of a
    session in this repository receives its CLAUDE.md and memory, so a probe runs from an empty folder outside it.
    Every file that the pages link and the change adds is reachable from the packet, through a recorded mirror in a
    trial copy. Each probe builds a new app from scratch, such as a to-do app with a frontend and a backend, runs it,
    and shows its user flow working; a plausible manifest or a copied example does not pass, and a second agent reruns
    the flow from the recorded commands. Sort each failure into a documentation gap, an environment or harness
    problem, an invalid task or the model's own assumption, retry a model-suspect failure with another model on the
    identical packet (and name only a model that actually ran), fix the pages a gap shows, refreeze, and rerun the
    affected idea and a fresh one. The PR reports the ideas, the build, the models with their token use, the pages
    each probe read, the apps and their runtime results.

## Rules

- No ADR number and no internal name on a page.
- No manifest or handler code in a fence: only `sh`, `bash` or `console` blocks, and none of them holds a manifest,
  in YAML or in JSON. A `cedar` block quotes one of the built-in policies in `internal/auth/cedar/` exactly, without
  its comments; the template test checks it.
- Code from a language repo sits under its language's `### TypeScript` or `### Python` heading, wherever it appears.
  The site shows each run of these sections as one group of language tabs, and a section runs to the next heading, so
  the text that holds for both languages comes before them.
- No hand-written table of methods, fields or keys: the Spec tabs hold them.
- Keep Status as a table of phases or conditions, reasons and meanings, with the command that checks them.
  Keep local CLI errors in a `CLI errors` table with the message or cause and the action to take. These tables
  must not become HTTP/API catalogues.
- The template retains `Errors` only for unchanged pages awaiting migration. A new or rewritten page uses
  `CLI errors` for local command/setup failures and moves API errors out of Usage.
- Links to a pyvvo repository point at its main branch, never at a tag: only `go.mod` names a language-module version.
  An Examples link points at a folder that a lane in `scripts/lanes.yaml` runs, which exists at the pinned version.
- A page's facts are checked in the code; a doc tag or a comment that disagrees with the code is a finding, not a
  source.

`TestDevelopPagesFollowTheTemplate` enforces the sections and these rules as far as the text shows them: it refuses a
manifest in a fence and a table whose first column is Method, Field or Key. The critic checks the rest: internal
names, handler code in a shell block, a table headed otherwise, and every fact.
