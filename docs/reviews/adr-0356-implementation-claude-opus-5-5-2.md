## Verdict: pass — 0 blockers, 0 majors, 1 minor (ADR-0356 implementation, round 2, model: claude-opus-5-5)

This round reviews the last part of ADR-0356: its four lane cases on funcd's real platform. The work is funcd's branch
`feat/infrastack-outputs` at 1d583a11, on the unmerged ADR-0357 branch `feat/infrastack-apps` (8d7721d0):
- 7cc4e69d pins pyvvo/funcd-addons v0.6.0 and pyvvo/funcd-functions v0.29.0;
- 1d583a11 adds `stack-outputs-reach-function`, `stack-output-changed` and `stack-outputs-never-shown` to
  `e2e/addon-infrastack.venom.yml`, and `stack-outputs-reach-engine` to `e2e/addon-frp-bunny.venom.yml`, with
  `scripts/lanes.yaml`.

Round 1 passed the runtime ([adr-0356-implementation-claude-opus-5-5.md](adr-0356-implementation-claude-opus-5-5.md)),
and ADR-0355's round 2 passed `funcdctl infra outputs`. v0.6.0's runtime equals the reviewed code. The reviewer did not
write the work. Every check below was run, not read. ADR-0356 is stamped `Implemented`, and F144 `implemented`.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

- **The two reader steps retry their value, so a late or cached read would pass** · model ·
  `e2e/addon-infrastack.venom.yml:242-253` and `:332-340` (`*reader`, `retry: 15`, `delay: 2`).
  - Decision 2 has a run that succeeds deliver every output, and Decision 6 has a Function read the Secret from funcd at
    each read; `docs/behavior/secrets.md:26` records no cache. So the first answer after the stack turns Ready must hold
    the value.
  - The retry calls the reader again for up to 30 s. A platform that served the old or the empty value for that long
    would pass "the Function's next read returns the new value".
  - In this run both steps passed at their first attempt, so the evidence holds.
  - Fix: assert on the first answer, and retry only a call that failed.

### Routed (not this work's)

- **`infra-outputs-lists-them`'s test has no `# scenario:` marker** · home: pyvvo/funcd-addons, at its next change.
  `test_outputs_prints_each_key_and_never_a_sensitive_value` (`infrastack/plugin/plugin_test.py:383`) checks the
  scenario, and the other eleven scenarios' tests in the add-on carry their markers. The lane's
  `stack-outputs-never-shown` runs the command on funcd too.
- **`just check-specs` lists `ReadyStackHasItsOutputs` and `RunEnds` as having no oracle test yet**, as the ADR's
  Design model says it will: the markers are in pyvvo/funcd-addons, `infrastack/runtime/handler_test.py:259` and
  `:270`, and v0.6.0 holds both.

### ✅ Verified correct (keep it)

**Both lanes, on this stacked branch, with ADR-0357's App rules in it**
- `scripts/agent/d timeout 1700 scripts/agent/lanes.sh feat/infrastack-outputs:addon-infrastack` printed
  `feat/infrastack-outputs:addon-infrastack @ 1d583a11: PASS (280s); final status: PASS`.
- The same for `addon-frp-bunny` printed `feat/infrastack-outputs:addon-frp-bunny @ 1d583a11: PASS (197s); final
  status: PASS`. Neither log has an `ERRO` line.

**Each case against its scenario**
- `scenario stack-outputs-reach-function`:
  - random-stack's Job owns `random-stack-outputs`, and its module outputs the plain `name` and the sensitive `token`;
  - its approved apply answers `KEYS=outputs OUTPUTS=name NAME=example-touched-pelican`, the shape
    `{"outputs": {"name": "…"}}`;
  - the reader Function, which binds the Secret, answers `READ_NAME=example-touched-pelican NAME_MATCH=True
    TOKEN_MATCH=True TOKEN_LEN=24` against the admin's read of the Secret.
- `scenario stack-outputs-never-shown`: the 24-character token is on no line of the run's status, its preview, its
  log, `funcdctl infra outputs` and its `-o json`, or funcd's journal. Each file holds what it should, so no count is
  empty for nothing: `LOGS_HOLD_THE_APPLY`, `STATUS_HOLDS_THE_PLAIN_OUTPUT`, `TABLE=name example-…;token (sensitive)`
  and `JSON_OUTPUTS=name JSON_SENSITIVE=token`.
- `scenario stack-output-changed`: the next apply answers `NAME=changed-pretty-yak`, the reader's next call reads it,
  and the reader keeps `GENERATION=1 REVISION=random-stack-reader-1` (`SAME_REVISION`).
- `scenario stack-outputs-reach-engine`:
  - the Tunnel binds `frp-outputs`, and its Addon lists `FRP_TOKEN` in `secretKeys`;
  - before the approval its engine runs (`ENGINE=8339 TOKEN_LEN=0`): the App wrote the Tunnel at deploy, since the key
    exists, empty, and ADR-0357's Secret check tests a key's presence only (`internal/app/reconcile.go:567-572`);
  - after the apply, `REPLACED old=8339 new=9500 OLD_GONE=yes TOKEN_IN_ENV=yes`: the new frpc's environment holds the
    exact 32-character `FRP_TOKEN` of the Secret, and the old one is gone.
  - frps then answered `hello from frp-bunny, through hello.frp.test` through the Tunnel.

**The static checks**: the lane guards exit 0, with 15 tests, and so do `go mod verify`, `go mod tidy -diff`,
`go build ./...` and the whole of `./internal/testkit/langmod/` and `./scripts/` (ADR-0355's round 3). `just
check-specs` exits 0 with `29 specs, 0 failing`: `stack-outputs.fizz` passes in 9 states, and its mutants
`answer-first` and `no-answer` fail. `just check-hygiene` and `just check-behavior` exit 0 with this stamp in place.

**The builder's decisions: keep all four of this ADR's**
- **random-stack's `name` and `token` stand in for `pullzone_host` and `FRP_TOKEN`.** The plan puts these three cases
  in the InfraStack lane, whose module is random-stack. Decision 4 makes a key its output's name, so the name decides
  nothing. The frp-bunny lane carries the real `FRP_TOKEN` key into an engine.
- **`funcdctl infra outputs` runs as admin**, the reader that may read every value of the Secret. A reader with no
  right to the Secret would print no line at all, so it would prove nothing. The Consequences say that reading the
  outputs takes that right.
- **The engine check reads frpc's `/proc/<pid>/environ`**: the engine's environment is where an Addon's `secretKeys`
  go (`docs/behavior/addons.md:58`). `grep -qxF` matches the exact value, and the old PID and `kill -0` show the
  replacement.
- **The OpenTofu image built from the pinned add-on's `runtime/`**: kept for the reasons in ADR-0355's round 3.

**Mutant controls**, each new assertion weakened in turn, by reading the steps:
- `KEYS=outputs OUTPUTS=name` catches the old bare result, a sensitive output in the result, and an empty `outputs`.
- `READ_NAME=example-` is the only check that catches a runtime that writes only the sensitive outputs, as v0.4.1 did:
  the reader would answer `""`, and `NAME_MATCH` would still read True.
- `TOKEN_LEN=24` is the only check that catches a token never written: with both sides empty, `TOKEN_MATCH` reads
  True.
- `NAME_MATCH` and `TOKEN_MATCH` catch a Function that reads another value than the Secret holds.
- Each zero count catches its own leak, and the four controls above keep a count from passing on an empty file.
  `preview.json` has no control of its own, but `status.json` holds the preview too, and funcd writes a run's preview
  once and never clears it (`internal/function/jobrun.go:330`).
- `READ_NAME=changed-` catches a value given at the worker's start; `SAME_REVISION` catches a roll on a changed
  Secret.
- `TOKEN_IN_ENV=yes` catches an engine that is not replaced on a changed value, and a `secretKeys` filter that drops
  `FRP_TOKEN`. `OLD_GONE=yes` catches the old engine left running. `ENGINE=` before the apply would catch a Tunnel
  held until its key exists.
- The gap: a cached read of up to 30 s passes, which is the Minor.

### Definition of Done

12 of 12 items hold: ADR-0356's Review checklist (3) and the generic DoD (9). Round 1's two routed misses are closed:
the plugin's and the lanes' scenarios pass, and the status moves at this stamp.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0356 (implementation, round 2) → pass, 0/0/1, 1 model-attributed, DoD 12/12. See
docs/reviews/model-scorecard.md.

### Recommendation

Stamped: ADR-0356 `Implemented`, F144 `implemented`. The PR that merges this branch carries `Fixes #1022`. The builder
may fix the Minor in that PR.

### Follow-up

Minor fixed in 79fe62e7, verified. Both reader steps lost `retry` and `delay`, so each asserts on the reader's first
answer. The script sends a call again only when `curl -sf` fails, 15 times at most, 2 s apart, and a reader that never
answers fails the step. That run's step dumps hold the new script. `addon-infrastack @ 79fe62e7: PASS (273s)` printed
`READ_NAME=example-helping-snipe … TOKEN_LEN=24`, then `READ_NAME=changed-relative-turtle …`, and its log has no
`ERRO` line. The lane guards exit 0.
