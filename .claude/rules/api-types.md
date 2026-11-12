---
paths:
  - "api/types/**/*.go"
---

# API types

- Every field of a kind carries a `doc` tag written for users: what it does, its default, and any limit the schema
  cannot hold, such as a rule across fields or the meaning of 0. Never an ADR number or an internal name. huma reads
  only the tag, never the Go comment (ADR-0323).
- A limit the schema holds (`minimum`, `maximum`, an enum, a pattern) is not repeated in the tag. A tag that copies a
  named constant gets an entry in `TestScenarioDocNamesTheConstant`.
- A type that several kinds reach is described without naming one of them.
- A tag is a raw string literal: it cannot hold a backtick.
- After a change, run `scripts/agent/d just generate`: it updates `api/openapi/funcd.v1alpha1.yaml` and the Spec pages
  (`docs/guide/develop/**/spec.md`). Remove each field you describe from
  `internal/controlplane/kindref/testdata/undescribed.txt`.
