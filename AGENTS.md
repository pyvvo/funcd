# Agent instructions

funcd's working agreement for every coding agent is [`.claude/CLAUDE.md`](.claude/CLAUDE.md); read it first.

- A rule for some files lives in `.claude/rules/*.md`, and its `paths` header names the files it covers. Read each rule
  whose paths match a file before editing that file: `.claude/rules/docs-guide.md` covers the user guide
  (`docs/guide/**`).
- A workflow is a skill in `.claude/skills/<name>/SKILL.md`. A page of the user guide's Developer chapter is written
  with `.claude/skills/develop-page/SKILL.md` and its `template.yaml`.
