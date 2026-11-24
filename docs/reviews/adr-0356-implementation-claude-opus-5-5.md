## Verdict: pass — 0 blockers, 0 majors, 0 minors (ADR-0356 implementation, model: claude-opus-5-5)

This report reviews ADR-0356's runtime part: pyvvo/funcd-addons branch `feat/stack-outputs` at 679fc14, one commit
`feat(infrastack)!` on origin/main v0.4.1. The commit touches `infrastack/runtime/handler.py`, `handler_test.py`,
`image_check.py`, the add-on's README and the random-stack example and contract. The reviewer did not write the work.
Every check below was run, not read.

The stamp waits. ADR-0356's status line and its F144 row live in funcd, so this branch moves no status. They move at
the PR assembly, and `Implemented` waits for the tests this part does not own (Routed).

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

None.

### Routed (not this work's)

- **`funcdctl infra outputs`** (Decision 8) and `scenario: infra-outputs-lists-them` belong to the add-on's plugin,
  which ADR-0355's implementation builds next. Home: ADR-0355's implementation.
- **The lane cases** `stack-outputs-reach-function`, `stack-output-changed` and `stack-outputs-never-shown` belong to
  funcd's InfraStack lane (`e2e/addon-infrastack.venom.yml`). `stack-outputs-reach-engine` belongs to the frp-bunny
  lane (`e2e/addon-frp-bunny.venom.yml`). Both lanes are on `test/frp-bunny-lane` and need the add-on's release.
  Home: the lanes' PR.
- **The new result shape breaks funcd-functions' hand-written contracts.** An apply now answers `{"outputs": …}`, so
  `apps/frp-bunny/infra/bunny/bunny.contract.yaml` and `apps/frp-bunny/infra/fake/fake.contract.yaml`, which still
  name `pullzone_host` at the top of `output`, would refuse every apply. They must take ADR-0355 Decision 3's shape
  before the frp-bunny lane runs this runtime. Home: pyvvo/funcd-functions (ADR-0355's plan, item 3).
- **Tracking.** ADR-0356 still reads `Accepted`, and F144 reads `accepted`. The `Reviewing` and `Implemented` moves
  happen at the PR assembly, the second one once the routed tests above pass.

### ✅ Verified correct (keep it)

- **Every output is written before the answer** (Decisions 2 and 3, `write_outputs` at handler.py:68 and `apply` at :176):
  - one `ctx.secret.put` per key, in name order;
  - a string as it is, any other value as its JSON;
  - then `{"outputs": plain}`.
  - A refused write raises a `StackError` that names the output and the Secret but no value. It drops the cause with
    `from None`.
  - The shim's error text carries only the server's answer (funcd-python `secret.py`), and funcd's key and value
    checks name the key, never the value.
- **No Secret named:** the plain outputs are the result alone, and each sensitive one is logged by name only.
- **Names** (Decision 4):
  - `check_outputs` (handler.py:57) refuses a name that is no env-var name, or one that starts with `FUNCD_` or `INFRASTACK_`.
  - It runs on every call but a destroy's, after `tofu show -json -module=.` and before `init` and `plan`, so a
    preview fails with nothing planned.
- **Destroy** (Decision 5): it reads the state's outputs before it applies, empties every key, plain and sensitive
  alike, and keeps each one. A refused emptying is logged by name and the run goes on. It answers `{}`.
- **No output feeds a run** (Decision 7): `_env` (handler.py:248) gives OpenTofu no bound Secret key named like one of the module's
  outputs.
- **Contracts:**
  - `OUTPUT_KEY`, `check_outputs(names)` and `write_outputs(context, secret, outputs)` have the ADR's shapes.
  - The random-stack contract takes ADR-0355 Decision 3's `outputs` record.
  - The README states the new rules and lists `funcdctl infra outputs` as not built yet.
- **`nix develop -c just ci` exits 0:** gofmt is clean, `go vet` and `go test -race` pass, `Ran 20 tests … OK` for
  the handler, and 9 Node tests pass.
- **The OpenTofu image check passes.** I built the image from 679fc14 with the recipe's own `docker build` command,
  under the tag `funcd-addons/opentofu:review-0356`, so the shared `:dev` tag stays as it was. Then
  `just opentofu-image-check funcd-addons/opentofu:review-0356` exited 0, with `PASS` with the state's encryption off
  and on, on real OpenTofu 1.13.9:
  - the result is `{'outputs': {'name': …}}`;
  - the plain output and the 24-character token are both in the Secret;
  - a module that gained `pull-zone` fails its preview, naming it;
  - the destroy leaves `name` and `token` present and empty;
  - the log shows each sensitive output only as `<sensitive>` or `(sensitive value)`.
- **Scenarios with named, passing tests:**
  - `stack-output-name-refused`, `stack-output-write-refused` and `stack-outputs-feed-no-run` in `handler_test.py`;
  - `stack-destroy-empties-outputs` in `image_check.py`, marked `# scenario:`.
- **Oracle tests:** `test_apply_writes_every_output_before_it_answers` and `test_apply_answers_after_its_last_write`
  carry `# oracle: docs/design/job/stack-outputs.fizz#ReadyStackHasItsOutputs` and `#RunEnds`. They are the tests the
  Design model names.
- **Mutants:** ten mutants of my own, each applied to a copy of the runtime folder, were all killed by the test
  suite:
  - an answer before the writes;
  - only the sensitive outputs written;
  - no name check;
  - a destroy that checks names;
  - outputs that feed OpenTofu;
  - a destroy that empties only the sensitive keys;
  - a refused write swallowed;
  - strings written as JSON;
  - a bare result;
  - the check after `init`.

### Definition of Done

10 of 12 items hold: ADR-0356's Review checklist (3) and the generic implementation DoD (9).
- The two misses are routed, not attributed to the model.
- One is the scenarios that other parts own: the plugin and the lanes.
- The other is the ADR and feat-row status moves, which wait for the PR assembly.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0356 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 10/12. See
docs/reviews/model-scorecard.md.

### Recommendation

Merge the runtime part with ADR-0355's plugin, and with funcd-functions' contracts in the new shape. Stamp ADR-0356
`Implemented`, and F144 `implemented`, once `infra-outputs-lists-them` and the four lane cases pass. The PR that does
that closes #1022.
