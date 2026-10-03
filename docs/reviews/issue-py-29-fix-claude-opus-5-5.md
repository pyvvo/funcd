## Verdict: pass — 0 blockers, 0 majors, 2 minors  (pyvvo/funcd-python issue #29 fix, model: claude-opus-5-5)

This reviews a **pyvvo/funcd-python** change: branch `fix/r29-py`, commit `db51afb fix(bundle): replace the
manifest's main key however it is written`, against `origin/main`. Issue #29: `funcd-bundle` rewrote `main`
with the regex `^main:.*$`, so a quoted key (`"main": src/x.py`) did not match and a second `main` key was
appended, which funcd's yaml.v3 manifest decoder rejects.

The fix replaces the regex with `_with_main` (`bundle/src/funcd_bundle/bundle.py`): it locates the top-level
`main` key with `yaml.compose` and splices `main: <handler>` over the key-and-value span of the original text,
appending one only when the manifest has no `main`.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **Minor 1 — the block-scalar branch has no test (a surviving mutant)** · attribution: `model`.
  Evidence: mutant M1 deleted `if text[start:end].endswith("\n"): entry += "\n"`; the whole bundle suite
  still passed (`15 passed`). Without that line, `main: |\n  src/h.py\nhandler: handle\n` becomes
  `main: h.pyhandler: handle`, a corrupt manifest. The fix handles it correctly today (probe: block and
  folded `>-` scalars rewrite to `main: h.py\nhandler: handle\n`), but nothing pins it.
  Fix: add a `main: |\n  src/handler.py\n` case to the `test_issue_r29_…` parametrization.

- **Minor 2 — the text splice mis-handles an aliased value and an explicit `? main` key** · attribution: `model`.
  Evidence (direct probe of `_with_main(text, "h.py")`):
  - `x: &h src/h.py\nmain: *h\n` → `x: &h src/h.py\nmain: h.py\nmain: *h\n`. The composed alias resolves to
    the anchored node, whose marks point at the anchor, so the span is inverted and `main` is duplicated —
    the issue's own symptom.
  - `? main\n: src/h.py\n` → `? main: h.py`, which no longer parses as the intended mapping (the key node's
    start mark sits after `? `).
  Both forms are rare in a `funcdctl.yaml`, so this does not block. Fix: when the value node is not a
  scalar/collection lying after the key (`value.start_mark.index < key.end_mark.index`), or the key is
  explicit, fall back to a safer rewrite (or fail with a `BundleError` naming the unsupported form).

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** The `origin/main` `bundle.py` overlaid on
  the fix branch: all 3 cases of `test_issue_r29_main_key_is_rewritten_not_duplicated` fail — the quoted-key
  cases show `"main": src/handler.py` (or `'main': …`) kept plus `main: handler.py` appended (the duplicate
  key); the next-line case shows the old `src/handler.py` left as a continuation line. `3 failed`.
  (A plain `git revert --no-commit db51afb` also removes the test, since fix and test share the commit, so
  the source-only overlay is the meaningful check.)
- **It passes with the fix**: `-k "r29 or 154 or main"` → `4 passed`, including the earlier
  `test_issue_154_…` append case, which still yields `manifest + "main: extract.py\n"`.
- **Mutants**: M2 (end the span at the key, keeping the old value) → `4 failed`; M3 (never match the key, so
  always append) → `4 failed`. M1 survives (Minor 1).
- **Cause, not symptom**: the regex was the cause named in the issue; the parser now locates the key, the same
  parser `discover` already uses (`yaml.safe_load`, `bundle.py:87`), so the bundler and discovery agree on
  which key is `main`. Probes: an inline comment is kept (`main: h.py  # the handler`), an empty value, a
  flow mapping, a manifest with no trailing newline, and a nested non-top-level `main` (left alone, top-level
  one appended) all yield exactly one top-level `main: h.py`.
- **Keeping the rest of the text verbatim** instead of re-dumping is the right call, and the docstring says
  why (PyYAML would re-resolve YAML 1.1 scalars that funcd reads as YAML 1.2).
- **Scope**: two files — the fix in `bundle.py` (removes the now-unused `re` import and `MAIN_LINE`) and one
  parametrized test. No other hunk; no test weakened or deleted.
- **Reuse**: PyYAML is already the module's dependency and already imported at top level; no new helper
  duplicates an existing one.
- **Conventions**: ruff format and lint clean; mypy clean; imports at module top; the two comments explain why,
  not what; test naming follows the file's `test_issue_<N>_…` pattern, with `r29` distinguishing this repo's
  issue from the funcd issue numbers the other tests use.
- **ADR conformance**: funcd ADR-0144 Decision 2 (the copied manifest's `main` names the bundled handler;
  handlers come from the manifest's `main`) — now holds for every common way of writing the key. No
  funcd ↔ shim contract surface (`FUNCD_*`, health endpoints, invoke socket, log capture, trace spans) is
  touched; no ADR edited.
- **Checks**: `just ci` with `TMPDIR=/tmp` through the cached dev shell → **exit 0** (ruff, mypy, pytest for
  shim 121 passed, bundle 15 passed, the four examples; `go vet`/`build`/`test`; clean tree afterwards).
- **Shape**: `fix(bundle):` Conventional Commit subject, a Cause/Fix/Test body, the attribution trailer, one
  issue in one commit. The body says `Refs #29`; the PR description must carry `Fixes #29`.
- The worktree was left at `db51afb`, clean.

### Definition of Done

10 / 11 items hold. Miss: item 4 (mutating the fix's key lines fails a test) — the block-scalar newline line
survives a mutant (Minor 1, `model`). Item 3's `-race` is not applicable to Python; pytest passes un-skipped.

### Model scorecard

Recorded: claude-opus-5-5 on pyvvo/funcd-python issue #29 (fix) → pass, 0/0/2, 2 model-attributed, DoD 10/11.

### Recommendation

Pass. Before opening the PR, optionally add a block-scalar case to the regression test (Minor 1) and guard the
aliased-value / explicit-key forms (Minor 2); put `Fixes #29` in the PR description.
