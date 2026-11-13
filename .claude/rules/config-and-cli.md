---
paths:
  - "internal/platform/config/**/*.go"
  - "cmd/funcd/**/*.go"
  - "cmd/funcdctl/**/*.go"
---

# Config keys and CLI help

- Every key of `config.Config` carries a `doc` tag written for users: what it does, the meaning of 0 or empty, and
  any limit that its `validate` tag does not map, such as a rule across keys, a check in `Validate` or a default
  derived when the key is empty. Never an ADR number or an internal name. The Spec page takes the type, the default,
  the variable and the mapped limits from the code, so the tag does not repeat them (ADR-0324).
- A command's `Short` and `Long` texts and a flag's usage are written for users and name no ADR: the Spec page copies
  them, and its test refuses an ADR number.
- A flag's default is the same on every machine. A default that comes from a variable or from the host stays empty in
  the command tree; the command resolves it when it runs, and its usage says the rule.
- A command that only the `-tags dev` build registers carries the cobra annotation `build: dev`.
- A tag is a raw string literal: it cannot hold a backtick.
- After a change, run `scripts/agent/d just generate`: it updates `docs/guide/develop/running-funcd/spec.md` and
  `docs/guide/develop/tools/funcdctl/spec.md`. Remove each key you describe from
  `internal/reference/testdata/undescribed-config.txt`.
