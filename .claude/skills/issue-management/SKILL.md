---
name: issue-management
description: File and triage funcd's GitHub issues (pyvvo/funcd) in the fixed issue shape and label taxonomy — create an issue, list or show issues, relabel one, sync the repo's labels, or regenerate the issue forms. Use whenever asked to open, record, file, triage or label an issue (a bug, a flaky test, a task, a feature request), or to change the issue shapes or the labels. The shapes and labels live in driver.py, which checks every issue before filing it.
---

# issue-management — funcd's GitHub issues, in one shape

Wrong behavior in what is built, flaky tests and concrete work items live in **GitHub Issues** on
<https://github.com/pyvvo/funcd/issues>. This skill drives them through one script — **[driver.py](driver.py)** —
which holds the **shapes** (the sections each kind of issue has) and the **label taxonomy**, and refuses an
issue that does not fit them. `gh` (system PATH) + `python3` are the only requirements; the Nix dev shell is
not needed. Paths below are relative to the repo root.

## Issue, board card, or ADR?

| You have… | It goes to |
|---|---|
| Wrong behavior in what is built, a flaky test, a concrete task | an **issue** — this skill |
| An un-scoped idea for later | a **board card** — [`/project-management`](../project-management/SKILL.md) |
| A security vulnerability | a **private draft security advisory** (`gh api -X POST repos/pyvvo/funcd/security-advisories`), never a public issue; fixed with [`/fix`](../fix/SKILL.md)'s advisory flow |
| A decision on how something works | an **ADR** — [`/adr`](../adr/SKILL.md) |

A bug whose fix needs a design decision is both: the issue records the defect and carries `needs-adr`, the ADR
decides the fix and cites the issue in its References, and the PR that implements it closes the issue
(`Fixes #N` in the PR description).

## The labels

Every issue carries **exactly one `kind/`**, **exactly one `priority/`** and **at least one `area/`**.

| Kind | Use for |
|---|---|
| `kind/bug` | The platform does something wrong: it breaks an ADR contract, the docs, or what a user can reasonably expect |
| `kind/feature` | A capability funcd does not have (an un-scoped idea of our own stays a board card) |
| `kind/flake` | A test that fails intermittently |
| `kind/task` | Docs, tooling, CI or cleanup work |

| Area | Covers |
|---|---|
| `area/function` | Function and Revision lifecycle — `internal/function`, `pooling`, `contract`, `artifact`, `scheduler` |
| `area/runtime` | Worker drivers, runtime shims and images, isolation — `internal/runtime`, `workernode` |
| `area/control-plane` | API server, admission, controller engine, store — `internal/controlplane`, `controller`, `store`, `platform`, `pkg/funcd` |
| `area/data-plane` | Invoke path — `internal/dataplane`, `edge`, `gateway`, `activator`, `route` |
| `area/services` | Platform services and their providers — `internal/blob`, `kvstore`, `catalog`, `services`, `provider`, `site` |
| `area/eventing` | Bus, event sources, sensors, workflows — `internal/bus`, `eventing`, `sensor`, `workflow`, `expr` |
| `area/security` | Authentication, authorization policies, secrets, egress — `internal/auth`, `secrets`, `network`, `envresolve` |
| `area/observability` | Logs, traces, metrics — `internal/funclog` |
| `area/cli` | `cmd/funcdctl`, `pkg/sdk` |
| `area/ci` | `.github/workflows`, `Justfile`, `scripts/`, `e2e/`, `flake.nix`, `internal/testkit` |
| `area/docs` | `blueprint.md`, `docs/`, `.claude/` |

| Priority | When |
|---|---|
| `priority/critical` | A security hole, data loss, or the platform down |
| `priority/high` | A core path is broken, with no good workaround |
| `priority/medium` | Wrong behavior, with a workaround |
| `priority/low` | Minor or cosmetic |

Process labels: **`needs-adr`** — the fix needs a design decision before any code (ADR-0000); **`needs-triage`**
— set by the web forms, removed at triage. GitHub's resolution labels (`duplicate`, `invalid`, `wontfix`,
`question`, `good first issue`, `help wanted`, `accessibility`) stay available, and release-please owns
`autorelease: *` — never touch those.

## The shapes

Each kind is one issue form in `.github/ISSUE_TEMPLATE/` and the same `### ` sections in the body, in this order
(* = optional; an empty optional section reads `_No response_`, as a web form writes it):

| Kind | Sections |
|---|---|
| bug | Summary · Steps to reproduce · Expected behavior · Actual behavior · Environment · Root cause* · Workaround* · References* |
| feature | Summary · Problem · Proposed behavior · Alternatives* · References* |
| flake | Test · Failure · Frequency · Suspected cause* · References* |
| task | Summary · Details · Done when · References* |

## Filing an issue

```bash
# 0. look for a duplicate first
python3 .claude/skills/issue-management/driver.py list --state all

# 1. print the empty body for the kind, then fill every section
python3 .claude/skills/issue-management/driver.py template bug

# 2. check it (prints the final labels and body), then rerun without --dry-run — it prints the issue URL
python3 .claude/skills/issue-management/driver.py create --kind bug \
  --title "<what is wrong, as a plain sentence>" \
  --area function --priority high --needs-adr \
  --body-file - --dry-run <<'EOF'
### Summary

…
EOF
```

`create` refuses a body whose sections differ from the shape, an empty required section, a title over 80
characters or with a `[tag]`/`type:` prefix or a trailing period, an unknown label, an open issue with the same
title, and a local absolute path.

To change an issue later, edit its current body and run `edit`, which applies the same checks against the
issue's `kind/` label:

```bash
python3 .claude/skills/issue-management/driver.py show 13 --body > issue.md   # the body only
python3 .claude/skills/issue-management/driver.py edit 13 --body-file issue.md [--title "<new title>"]
```

## Writing a good issue

- **One defect per issue.** Two symptoms with one cause are one issue; two causes are two issues that link
  each other.
- **Evidence you ran.** Paste the real output, and say what ran it: the command or test, the version or commit,
  the runtime driver. Never claim a result you did not observe; mark what is inferred ("not run on containerd").
- **Grounded references.** Cite `file:line`, ADR numbers and commits; read them, don't recall them.
- **Expected behavior is the observable outcome**, not the fix design — that belongs in the ADR.
- **Say whether a workaround was tested.**
- **No local paths, usernames or emails** — issues are public. Paths are project-root-relative; the driver
  rejects the common absolute prefixes, and the rest is on you.

## Triage

An issue opened from a web form carries its `kind/` label and `needs-triage`. Triage adds the `area/` and
`priority/` labels (and `needs-adr` if the fix needs a decision) and removes `needs-triage`:

```bash
python3 .claude/skills/issue-management/driver.py relabel 12 --add area/runtime --add priority/medium --remove needs-triage
```

## Changing the shapes or the labels

`SHAPES`, `LABELS` and `RENAMES` at the top of `driver.py` are the single source. After editing them:

```bash
python3 .claude/skills/issue-management/driver.py forms          # regenerate .github/ISSUE_TEMPLATE/
python3 .claude/skills/issue-management/driver.py labels --sync  # create, rename or update the repo's labels
```

Commit the driver and the regenerated forms together; `forms --check` fails when they drift. `labels --sync`
never deletes a label: to retire one, check no open issue uses it, then delete it by hand.
