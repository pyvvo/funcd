## Verdict: pass on what the work can meet, 0 blockers, 0 majors, 1 minor (ADR-0355 amendment, implementation, model: claude-opus-5-5); the amendment cannot ship as decided

This review covers the in-place amendment of ADR-0355 Decision 3 that the decider chose on 2026-11-24, recorded on
#1021: a run's `variables` record stays closed when the module has no plain variable, and every other record with no
member keeps `{}`. The work is two commits:
- funcd 6e94910d on `docs/adr-0355-closed-variables` (main fe2c6c2f): the header, Decision 3, the scenario
  `infra-sensitive-only-module`, the test plan and the checklist;
- pyvvo/funcd-addons PR #22, 583963a on main 2d70d2c: `CLOSED` and `contract()` in `infrastack/plugin/funcdctl-infra`,
  `plugin_test.py` and the README's contract bullet.

The reviewer did not write the work. Every check below was run, not read. The code and the text follow the decider's
choice faithfully, but funcd's contract profile refuses the closed record with no member, so the push of every module
with no plain variable fails (Routed). PR #22 and the ADR commit wait for the decider's ruling.

### Checks run

- `nix develop -c just test-infrastack` at 583963a: exit 0. The handler's 24 tests and the plugin's 16 pass, and so do
  the reconciler's 29.
- Revert check: main's `infrastack/plugin` (2d70d2c), with main's `infrastack/app`, which the image test reads, and the
  PR's `plugin_test.py`. `test_scenario_infra_sensitive_only_module` fails (`{} != {'type': 'object',
  'additionalProperties': False}`), `test_the_contract_is_made_from_the_variables_and_outputs` errors on the missing
  `CLOSED`, and the other 14 pass.
- Five mutants of `funcdctl-infra`, each caught:
  - `or CLOSED` dropped fails both tests above;
  - `CLOSED` open, or without its `type`, fails the scenario's test;
  - the outputs record closed too fails the bare check;
  - every record with no member closed fails the bare check and the type table's `["object", {}]` row.
- The contract the plugin writes for a module whose only variable is sensitive is block style. Its one `{}` is the
  empty `outputs` schema.
- PR #22's `ci`, `infrastack`, `e2e` and `pr-title` checks passed.
- In funcd at 6e94910d: `just check-hygiene`, `just check-specs` (29 specs, 0 failing) and `just check-behavior` exit 0.
- The push, end to end: the plugin's `push()` with the plugin tests' fake engine in place of OpenTofu, and a real
  funcdctl built from funcd's main fe2c6c2f, into an OCI layout:

  | Module | main's plugin (2d70d2c) | PR head (583963a) |
  |---|---|---|
  | the plain `prefix` and the sensitive `token` | pushed | pushed |
  | only the sensitive `token` | pushed | refused |
  | no variable | pushed | refused |

  Each refusal is `funcdctl push`'s: "the contract file's input is outside the funcd profile: … (root).variables:
  unsupported construct "open record (additionalProperties is not false)"".

### 🔴 Blockers

None placed here.

### 🟡 Majors

None.

### Minor

- **The scenario's test never pushes its Given** · attribution: model.
  - `test_scenario_infra_sensitive_only_module` checks the contract's dict and its YAML, and the plugin tests' fake
    funcdctl takes any contract.
  - `infrastack/plugin/push_check.py`, the one push in CI with a real funcdctl, pushes a module with `prefix`
    (`push_check.py:18`).
  - So CI is green on a contract that funcdctl refuses. Whatever the decider rules, a real push of a module with no
    plain variable belongs in `push_check.py`.

### Routed (not this work's)

- **The amended `variables` record is outside funcd's contract profile, so `funcdctl infra push` fails for every module
  with no plain variable** · home: elsewhere, the decider (ADR-0355 Decision 3 as amended, against ADR-0058) ·
  attribution: adr. It holds the merge of PR #22 and of the ADR commit.
  - Decision 2's step 3 pushes each bundle with `$FUNCDCTL push`, which gates the contract against ADR-0058's profile
    (`cmd/funcdctl/contract.go:49`, `:54`). The profile admits a closed record only with `properties` (ADR-0058's type
    table, line 197; `internal/contract/contract.go:93`), and refuses the record with none under the message of an
    open record (`:114`).
  - No shape in the profile closes a record with no member. An overlay test of `contract.Check` refused it, also with
    `properties: {}`, with `maxProperties: 0`, and as `maxProperties: 0` alone. The typed map `additionalProperties:
    {type: "null"}` is in the profile, but it admits `{"token": null}`.
  - funcd's validator would hold the rule: compiled without the profile gate, the plugin's input schema refuses
    `{"action": "apply", "variables": {"token": "x"}}` at `/variables/token` and admits `"variables": {}`, which the
    reconciler sends for a stack with no variable (`infrastack-reconciler.mjs:69`, `:256`).
  - So the amended rule cannot be built as written, and the plugin follows it. The scenario's own module cannot be
    pushed, and neither can a module with no variable at all, which pushes on main and has no sensitive variable to
    keep out.
  - The two checks before calling the refused push a defect. It happens: the table above. Nothing decided it: ADR-0355
    has no Scope item, Alternative, Consequence or open question that accepts a refused push; ADR-0058 names no closed
    record without properties among its supported or its excluded constructs; no issue of funcd or funcd-addons and no
    board card speaks of one; and no test of `internal/contract` pins it. Verdict: `bug` of this change, whose remedy
    is a decision, so it goes to the decider and not to `/fix`.
  - The decider's options, none built: funcd's profile admits a closed record with no member (a funcd change and an
    ADR over ADR-0058, and the plugin then needs a funcdctl of that version or later); a shape in the profile, such as
    the typed map of null, which still admits `{"token": null}`; or Decision 3's `{}` as before the amendment.
- **nerdctl** stays untested: every push above ran on the fake engine or, in CI, Docker (env), as in rounds 1 to 3.

### ✅ Verified correct (keep it)

- `contract()` closes only the `variables` record (`funcdctl-infra:118`), and `record()` keeps `{}` for every other
  record with no member (`:81`): an object type with no attribute and the outputs of a module with no plain output, as
  the amended Decision 3 says.
- The ADR's amended lines agree with each other and with the code: the header line below Status, citing #1021;
  Decision 3's exception; the scenario; the test plan; the checklist line. Each changed line is marked
  `(amended 2026-11-24)`, the status stays `Implemented` and no feat row changes, as in ADR-0290's and ADR-0345's
  amendments. No other line of ADR-0355 states the old rule: its other `{}` are a variable with no type, an output's
  value and a destroy's answer.
- No other caller relies on `variables: {}`:
  - the runtime reads a run's `variables`, else `{}`, and never the contract (`handler.py:132`);
  - `image_check.py` sends `prefix` to modules that declare it;
  - `addon.yaml`'s `config.variables: {}` is the Addon's config schema, which ADR-0355's Scope leaves to a run's
    creation;
  - random-stack's contract, the token-stack module, and frp-bunny's two contracts (pyvvo/funcd-functions v0.29.0)
    hold plain variables;
  - funcd's lanes `addon-infrastack` and `addon-frp-bunny*` read `variables.properties` with fallbacks, on modules
    with plain variables.
- The README's contract bullet says what the code does.

### Definition of Done

13 of 14 items hold: ADR-0355's Review checklist (5, as amended) and the generic DoD (9). The miss is real behavior in
the shipped path: the push of a module with no plain variable fails (adr, Routed).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0355 (implementation, amendment) → pass, 0/0/1, 1 model-attributed, DoD 13/14. See
docs/reviews/model-scorecard.md.

### Recommendation

Merge neither PR #22 nor the ADR commit until the decider rules on the Routed item. Once a rule that funcd can push is
chosen, the change is small. It also needs a real push of a module with no plain variable in `push_check.py`.
