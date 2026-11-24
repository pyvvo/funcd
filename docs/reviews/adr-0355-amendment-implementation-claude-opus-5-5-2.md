## Verdict: pass, 0 blockers, 0 majors, 0 minors (ADR-0355 amendment, implementation, round 3, model: claude-opus-5-5)

This review covers the final shape of the in-place amendment of ADR-0355 Decision 3, recorded on #1021. On
2026-11-24 the decider ruled "use what we answered for Functions", ADR-0090's void side: a module with no plain
variable takes `variables: {"type": "null"}`, and `variables` is required only when a plain variable has no default.
The work:
- funcd's `docs/adr-0355-closed-variables` at ef4b1e7d: a4cca570 and ef4b1e7d, on round 1's report c3052daa, change
  the header, Decision 3, the scenario `infra-sensitive-only-module`, the test plan and the checklist;
- pyvvo/funcd-addons PR #22 at 767dbc1: 88a5ac2 and 767dbc1, on round 1's 583963a, change the plugin's `VOID` and
  `needed`, the reconciler's `variablesOf()`, the runtime's docstring, their tests, `push_check.py` and the README.

Round 1 ([report](adr-0355-amendment-implementation-claude-opus-5-5.md)) routed the closed record with no member,
which funcd's contract profile refuses, to the decider. Round 2 returned changes requested, in chat and with no report:
one Major and three Minors, each fixed here. The reviewer did not write the work. Every check below was run, not read.

### Checks run

- `nix develop -c just ci` at 767dbc1: exit 0. gofmt, vet and `go test -race` pass, and so do the handler's 25 tests,
  the plugin's 16 and the 32 node tests.
- CI's push steps, run locally on colima: `just opentofu-image`, then `just funcdctl` at `e2e/funcd.ref` (fe9c7360),
  then `just infra-push-check`. It prints PASS, after "a module whose only variable is sensitive pushes too" and "its
  variables are void". The same check fails with round 1's closed record ("funcdctl push: the contract file's input is
  outside the funcd profile") and with the `{}` of before the amendment ("its variables are void").
- A real funcdctl built from funcd's main fe2c6c2f pushes the plugin's contract of four modules, and
  `funcdctl inspect` reads each back: `{"type": "null"}` with `required: [action]` for a module whose only variable is
  sensitive and for one with no variable; the optional record with `[action]` for one whose variables all have
  defaults; the required record with `[action, variables]` for one with a required variable. The plugin has not
  changed since 88a5ac2, where this ran.
- A run's creation, through funcd's admission (`JobRunInputDetails`) on each contract after the push's profile check:

  | The run's input | void | defaults only | a required `prefix` |
  |---|---|---|---|
  | `{"action": "apply"}` | admitted | admitted | 422 `/variables` |
  | with `"variables": null` | admitted | 422 `/variables` | 422 `/variables` |
  | with `"variables": {}` | 422 `/variables` | admitted | 422 `/variables/prefix` |
  | with `"variables": {"token": "x"}` | 422 `/variables` | 422 `/variables/token` | 422 `/variables/prefix,/variables/token` |

- The reconciler's input, from its code with a stand-in for `ctx.job`, for the stacks of the examples and the lanes:
  random-stack with `prefix: example`, `prefx: a` and `prefix: 3`; token-stack with `prefix`, and with `prefix` and
  `token`; frp-bunny's four variables. Against the plugin's contracts and frp-bunny's hand-written bunny and fake ones
  (pyvvo/funcd-functions v0.29.0), every input and every answer equals main's, so no lane case changes.
- Mutants, each caught:
  - eight of the plugin: `VOID` dropped, made `{}`, made round 1's closed record or YAML's null; `variables` always
    or never required; `needed` counting the sensitive variables, or ignoring defaults;
  - eight of the reconciler, among them round 2's `variablesOf()`, an empty list taken as none, `{}` or null passed
    on, and the drift check's start sending `variables: {}` as main does, which survived round 2;
  - two of the runtime: a null `variables` kept, and `variables` required.
- The revert check of round 2: on main's code the plugin's scenario test fails, its bare check errors, and the
  reconciler's test of a stack with no variables fails. The handler's new test passes on main, since it pins how the
  runtime already reads absent or null `variables` (`handler.py:132`).
- PR #22's checks at 767dbc1 passed: `ci`, `infrastack`, whose push step is the one above, `e2e` and `pr-title`.
- In funcd at ef4b1e7d: `just check-hygiene` and `just check-behavior` exit 0. `just check-specs` passed at 6e94910d,
  and the later commits change only the ADR's text.

### Round 2's findings: each fixed

- **Major, the reconciler dropped a `config.variables` that is not a non-empty object.** `variablesOf()` now omits
  `variables` only when it is absent, null or `{}`. So `"prefix=a"`, `3` and `[]` reach the run, and its creation
  refuses each with 422 at `/variables`, whatever the module. A module with no plain variable now refuses them at the
  run's creation, where main admitted them and its runtime failed the run. The test "malformed variables reach the run
  as they are" pins it, and round 2's code fails it.
- **Minor, no real push of the scenario's Given.** `push_check.py` pushes a module whose only variable is sensitive,
  and the test plan names it (above).
- **Minor, the drift check's start was untested.** Its mutant now fails "a stack that sets no variables starts its
  drift check with none".
- **Minor, an unwrapped line in Decision 3.** Only the table row at line 279 passes 120 characters, as on main.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

None.

### Routed (not this work's)

- **A stack that sets no variables, against a module with a required variable, is refused naming `/variables`**, where
  main named `/variables/prefix` · home: above, the decider; uncovered. It follows from omitting `variables`, and the
  422 and `InvalidVariables` stay. The coordinator takes it to the decider.
- **nerdctl** stays untested: every push ran on Docker or on the plugin tests' fake engine (env).

### ✅ Verified correct (keep it)

- `contract()` gives `VOID` to `variables` only, and `required` names `variables` only when a plain variable has no
  default (`needed`). `record()` keeps `{}` for every other record with no member. The YAML is block style, with
  `type: "null"` quoted, as the scenario's test pins.
- The ADR's amended lines agree with each other and with the code: the header line below Status, citing #1021 and
  ADR-0090; Decision 3; the scenario, now with `funcdctl push` accepting the contract; the test plan, now naming
  `push_check.py`; the checklist line. Each is marked `(amended 2026-11-24)`, the status stays `Implemented` and no
  feat row changes. ADR-0090 decides "void as `{"type":"null"}`". No wording of round 1 is left.
- The README's contract and reconciler sections and the handler's docstring say what the code does.
- No other caller relies on `variables: {}` (round 1's list holds), and every stack of the lanes sets variables.

### Definition of Done

14 of 14 items hold: ADR-0355's Review checklist (5, as amended) and the generic DoD (9).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0355 (implementation, amendment, round 3) → pass, 0/0/0, 0 model-attributed, DoD
14/14. See docs/reviews/model-scorecard.md.

### Recommendation

Merge PR #22 and this branch together: the ADR's text and the code agree, and PR #22's checks are green. funcd's lanes
take the new plugin and reconciler with the add-on's next release.
