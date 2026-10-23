## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0265 implementation, model: claude-opus-5-5)

This report reviews branch `feat/adr-0265-tuning-keys` at da494a1c, on main 621f7f25. The work is `git log
origin/main..HEAD`, all committed:

- 6b9926a7 the Proposed draft;
- b1770e5b the judge round folded (its Major: the `bad-value-refused` scenario) and the ADR Accepted, with the
  `Superseded in part by ADR-0265` back-links on ADR-0087 and ADR-0263 and F57's `tuning keys` entry;
- 9fcfe987 feat(lake): `TUNING` and `_tune` in `images/runtime/duckdb/shim.py`, the scenario tests in `shim_test.py`
  and the `tuning-keys-apply` step in `e2e/duckdb.venom.yml`;
- da494a1c ADR-0265 → Reviewing, F57's `tuning keys` → reviewing, and the ADR-0265 row in `docs/PROJECT-SUMMARY.md`.

ADR-0265 realizes FEAT-0003/F57; the PR that carries this branch closes issue #1186 (`Fixes #1186` in its
description), as the ADR's References name it.

### ✅ Verified correct (keep it)
- **`TUNING` and `_tune` match the Contracts (Decision 1-3; checklist 1, 2).** `TUNING` holds exactly the three keys
  with their settings (`shim.py:65-69`). `_tune` first refuses any `DUCKDB_*` key outside `TUNING` but
  `DUCKDB_EXTENSION_DIRECTORY`, with exit 2 and the Decision 3 message on stderr; then runs `SET <setting> = ?` with
  the value bound, in sorted key order, and turns DuckDB's refusal into exit 2 with the Decision 2 message
  (`shim.py:134-150`). `main` calls `_tune(con, os.environ)` between `_connect` (which loads the extensions) and
  `_configure` (`shim.py:290-292`), so the keys come first, as Decision 2 and the Contracts say.
- **ADR-0263's order from the S3 pins on is unchanged (checklist 3).** `TestScenarioOnlyAllowedPaths` gains only the
  three tuning `SET`s at the head of its expected list; the rest of the list is main's. The test now runs `main` on an
  environment it sets whole (`mock.patch.dict(..., clear=True)`), as Implementation plan item 2 asks, so a host's
  `DUCKDB_*` variable cannot change it.
- **Scenarios → tests, all un-skipped and passing.** `python3 -m unittest shim_test -v` (Python 3.9.6, CI's version)
  ran 9 tests, OK:
  - `tuning-keys-apply`: `TestScenarioTuningKeysApply` asserts the three `SET`s with their bound values in a fixed
    order, and that `DUCKDB_EXTENSION_DIRECTORY` and other variables set nothing; the duckdb lane's
    `tuning-keys-apply` step read `{"rows":[["2.7 GiB"]]}` on Lake `lake` (lane-0265-duckdb.log), and the lane's
    existing spill step read the same engine's `memory_limit` and `threads` as `["2.7 GiB",2]`.
  - `other-key-refused`: `TestScenarioOtherKeyRefused` runs `main` with `DUCKDB_ENABLE_EXTERNAL_ACCESS`, asserts exit
    2, the exact message with the allowed keys, and that no `SET` ran.
  - `bad-value-refused`: `TestScenarioBadValueRefused` runs `main` on an engine that refuses `threads`, asserts exit 2,
    the message naming `DUCKDB_THREADS=two` with DuckDB's cast error, and that no sandbox `SET` ran.
- **Revert and mutant checks.** Each change to `shim.py` fails tests (run in place, restored with `git checkout`):
  - main's `shim.py`: 1 failure, 4 errors (all four new tests and the order test);
  - the refusal's `SystemExit` dropped: `TestScenarioOtherKeyRefused` errors;
  - the value bound as a literal: 3 failures (`TuningKeysApply`, `BadValueRefused`, the order test);
  - `_tune` called after `_configure`: 3 failures (`OtherKeyRefused`, `BadValueRefused`, the order test).
- **Lanes (checklist 4; DoD).** Evidence read, not re-run (each runs once per PR): the duckdb lane on this branch's
  code, final status PASS, with the `tuning-keys-apply` step passing on `2.7 GiB` (lane-0265-duckdb.log); the app-todo
  lane, final status PASS, 50 test cases and 0 failures, whose Lake's ConfigMap now takes effect.
- **Scope.** The diff touches only the shim, its tests, the lane step and the tracking docs, as Scope **In** says. The
  example's comments moved in pyvvo/funcd-python #103 (merged, no release), as Implementation plan item 4 says.
- **Tracking.** ADR-0265's substance is unchanged from acceptance (b1770e5b) to HEAD: the only diff is the status line
  gaining `Reviewing`. F57's `tuning keys` entry is at `reviewing`. ADR-0087 and ADR-0263 carry only their
  `Superseded in part by ADR-0265` back-links, which name the lines the ADR supersedes (ADR-0087's `DUCKDB_*` → `SET`s
  and its `provider-bindings-injected` "applies its config"; ADR-0263's shim statement order). The
  `docs/PROJECT-SUMMARY.md` row is present.
- **Checks on da494a1c** (through `scripts/agent/d`, `GOWORK=off`):
  - `go test -count=1 -run TestIssue998_ShimCheckpointsPerCommit -v ./internal/runtime/embedimg/` passes (0.71 s);
  - `go test -count=1 -run Lane ./cmd/funcd/ ./internal/testkit/langmod/` passes.

### Definition of Done
5 / 5: the 4 Review-checklist items plus the Definition-of-done paragraph (both lanes pass; `scripts/agent/gate.sh`
runs once after this review, and CI again on the PR).

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0265 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 5/5. See
docs/reviews/model-scorecard.md.

### Recommendation
Pass. ADR-0265 is stamped `Reviewing → Implemented` (2026-10-23) and F57's `tuning keys` `reviewing → implemented`.
The PR closes #1186.
