# Fix review — pyvvo/funcd-python issue #39

This review covers a **pyvvo/funcd-python** change: branch `fix/r39-py`, commit `faafebd`
("fix(examples): ship the Function manifest the hello-world README applies").

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (pyvvo/funcd-python #39 fix, model: claude-opus-5-5)

### 🟡 Major / Minor

- **Minor — the commit says `Refs #39`, not `Fixes #39`** · attribution: model.
  The `/fix` commit shape asks for `Fixes #<N>`. The repo squash-merges with the PR title as the
  commit on `main`, so the commit body does not close the issue anyway: the PR body must carry
  `Fixes #39` (or the group PR must list it). Nothing else to change.

### ✅ Verified correct (keep it)

- **The regression tests fail without the fix, for the issue's reason.** `git revert --no-commit faafebd`
  with the new test file kept, then `pytest tests/test_readmes.py` in `examples/catalog-quack`:
  2 failed, 1 passed —
  `a README applies a manifest its example does not ship: ['hello-world/README.md: function.yaml']` and
  `a README or the manifest it applies names a runtime its funcdctl.yaml does not: ['hello-world/README.md: python312']`.
  These are the two causes the issue names.
- **They pass with the fix.** After `git reset --hard faafebd`: 3 passed. The worktree was left clean at `faafebd`.
- **Mutants: 3 of 3 killed.**
  1. `handler.yaml` `spec.runtime: python312` → the runtime test fails on `hello-world/handler.yaml: python312`.
  2. README `apply -f function.yaml` → the shipped-file test fails on `function.yaml`.
  3. README Deploy prose `runtime: python312` → the runtime test fails on `hello-world/README.md: python312`.
- **Cause, not symptom.** The example now ships the manifest the README applies
  (`examples/hello-world/handler.yaml`), and every runtime mention is `python314`, which matches the
  example's `funcdctl.yaml` and the only Python image funcd embeds (`internal/runtime/embedimg`). The
  tests check every example README, so the same defect cannot come back in another example.
- **The user-visible steps are now consistent.** `funcdctl push <path> <ref>` takes a ref (funcd
  `cmd/funcdctl/cli.go`), and the README's push ref `oci-layout:///tmp/funcd-demo/layout:hello-world`
  is exactly the manifest's `spec.image`. The `/tmp/funcd-demo` root matches funcd's
  `examples/funcdconfig.yaml` `dataDir` and `docs/demo/function.yaml`. The image carries no digest, as
  ADR-0035 (Implemented) requires: the platform pins the digest into the Revision.
- **Scope.** Four files, all serving the issue: the new manifest, the README Deploy section and layout
  list, the `funcdctl.yaml` comment that said the example ships no CRD (now false), and the tests. The
  separately filed `funcdconfig.yaml` path problem is left alone, as the issue asks. No test was weakened:
  the r32 test keeps its assertion and now calls the extracted `_apply_commands` helper.
- **Reuse.** The manifest follows `examples/log-burst/handler.yaml` (the pattern the issue names). The
  tests extend the existing `test_readmes.py` and factor its apply-command regex into one helper instead of
  copying it. The test names follow the existing `test_issue_r32_…` convention.
- **Conventions.** Block-style YAML, top-level imports, ruff clean, short why-only comments (the digest
  note in `handler.yaml`). No version, `version.txt` or `CHANGELOG.md` edit.
- **No funcd ↔ shim contract change** and no funcd ADR contradicted: this is example documentation and
  a manifest only.
- **Checks.** `just ci` through the cached dev shell with `TMPDIR=/tmp`: exit 0 (ruff, mypy, pytest for
  the shim, bundle and every example, `go test ./...`).

### Definition of Done

11 / 11 items hold: regression tests reproduce the issue (1), fail pre-fix for the reported reason (2),
pass with the fix (3), mutants killed (4), root cause fixed (5), scope clean (6), no ADR contradicted (7),
`just ci` green (8), conventions (9), reuse (10), commit shape `fix(examples):` with the trailer (11; the
closing keyword is the Minor above and belongs in the PR body).

### Model scorecard

claude-opus-5-5 on pyvvo/funcd-python #39 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation

Ship it. Put `Fixes #39` in the PR body so the merge closes the issue.
