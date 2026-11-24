# ADR-0356: A stack's outputs, plain and sensitive, reach its readers through the Secret its Job owns

- **Status**: Proposed
- **Date**: 2026-11-24
- **Deciders**: green-0-rabbit
- **Tags**: infrastack, secrets, outputs, funcdctl, addons
- **Realizes**: [FEAT-0012/F144](../feat/0012-feat-infrastack.md) (stack outputs for Functions)
- **Relates to**: [ADR-0327](0327-a-function-writes-the-secrets-it-owns.md) (Secret writes) ·
  [ADR-0249](0249-ctx-secret-reads-from-funcd.md) (`ctx.secret` reads) ·
  [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) (an engine's `secretKeys`) ·
  [ADR-0351](0351-a-jobs-worker-reaches-the-platform-as-the-job-its-own-principal.md) (the Job as owner) ·
  [ADR-0347](0347-a-job-runs-its-handler-once-per-jobrun-in-a-worker-of-its-own.md) ·
  [ADR-0355](0355-an-opentofu-module-is-pushed-with-its-providers-and-a-contract-from-its-variables.md) (the result's
  `outputs`, the plugin) · ADR-0357 (an App's part waits for them) · ADR-0057 · ADR-0093

## Context & Need

An app uses what its stack created, such as a queue's URL or an access key. F144 decided that a sensitive output
reaches Functions through a Secret that the stack's Job owns and its OpenTofu runtime writes, left the plain ones to
this ADR, and asked for `funcdctl infra outputs`. Today the runtime writes the sensitive outputs into the Secret its
Job's ConfigMap names in `INFRASTACK_OUTPUTS_SECRET` and answers the plain ones as the run's result (pyvvo/funcd-addons
`infrastack/README.md`). A Function reads a result only through `ctx.job.get` of a Job it binds (ADR-0349), and an
add-on object's engine, such as the frp-bunny App's Tunnel, not at all.

**Purpose.** Every output of a stack's last apply reaches the Functions and engines that bind the stack's outputs
Secret, the way they read any Secret, with nothing copied by hand and no sensitive value in a status or a log.

## Scenarios

- `scenario: stack-outputs-reach-function`: Given a stack whose module outputs `pullzone_host` and the sensitive
  `FRP_TOKEN`, and whose Job owns the Secret `outputs`; when its apply run succeeds; then a Function that binds
  `outputs` reads both through `ctx.secret.get`, and the run's result is `{"outputs": {"pullzone_host": "…"}}`.
- `scenario: stack-outputs-reach-engine`: Given an add-on object that binds `outputs`, whose Addon lists `FRP_TOKEN` in
  `secretKeys`; when the apply writes it; then the object's engine is replaced, with `FRP_TOKEN` in its environment.
- `scenario: stack-output-changed`: Given a Function that reads `pullzone_host`; when a later apply changes it; then
  the Function's next read returns the new value, and the Function keeps its revision.
- `scenario: stack-output-name-refused`: Given a module with an output named `pull-zone`; when its run previews; then
  the preview fails, naming `pull-zone`, and nothing is applied.
- `scenario: stack-output-write-refused`: Given a Job that does not own its outputs Secret; when its apply run applies;
  then the apply's call fails, naming the output and the Secret but no value.
- `scenario: stack-outputs-never-shown`: Given a run that applies the sensitive `FRP_TOKEN`; then its value is in no
  run status, preview document, log line or `funcdctl infra outputs` line.
- `scenario: stack-destroy-empties-outputs`: Given an applied stack, whose module has since gained the output
  `pull-zone`; when its destroy run succeeds; then each output key holds an empty value.
- `scenario: stack-outputs-feed-no-run`: Given an applied stack whose module has the variable `region`, default `DE`,
  and the output `TF_VAR_region`, `US`; when its next run plans without `region`; then the plan takes `DE`.
- `scenario: infra-outputs-lists-them`: Given an applied stack; when `funcdctl infra outputs <stack>` runs; then it
  prints each output's name, the plain ones with their value and `FRP_TOKEN` as `(sensitive)`.

## Scope

**In**: where every output goes, its key and its value; a refused write; the destroy; how Functions and engines read
the outputs; keeping them out of OpenTofu's environment; `funcdctl infra outputs`.

**Out**:
- Creating the outputs Secret: a write never creates one (ADR-0327 Decision 4), so a person creates it, owned by the
  Job, before the stack's first apply, as every Secret an App declares (ADR-0213 Decision 6). Outside an App, the Job's
  own declaration of it names at least one key, and the Job waits `Pending` until each exists, so the person creates
  those keys, empty.
- A part of an App that waits for the outputs: ADR-0357.
- One Secret update for all outputs: ADR-0327's write is one key, and a route for several would change both shims, for
  the window that a crash between two writes opens and that the run's `Interrupted` end already shows.

## Constraints & Decision drivers

- No sensitive output in a status or a log (F144).
- Since ADR-0249 a Function reads a Secret only through `ctx.secret`; an engine gets the bound Secrets' keys its Addon
  lists (ADR-0332).
- The Job writes only a Secret it owns and binds (ADR-0327 Decision 4, ADR-0351 Decision 3).
- The change stays in the add-on's runtime and plugin; funcd's code stays as it is.

## Alternatives considered

- **Plain outputs in a ConfigMap**: no workload writes a ConfigMap, and an add-on object binds Secrets only
  (ADR-0332), so an engine would still lack them; a ConfigMap reaches a Function's environment at its start, so a
  change would need a roll. Rejected.
- **Plain outputs only in the result** (today): a Function reads them through a Job binding only, an engine never.
  Rejected.
- **A second Secret for the plain outputs**: a reader of plain outputs only would not bind the sensitive ones, at the
  cost of a second Secret per stack that a person creates, an App declares and the Job owns and binds. Rejected: the
  first reader, frp-bunny's Tunnel, needs the sensitive token anyway, and every stack would pay for the second.
- **An automatic name mapping** (`-` to `_`): `a-b` and `a_b` would share a key, and a reader's key would differ from
  the module's name. Rejected: a rename in the module is one line.
- **A roll of the readers on a changed output**: a Function reads each Secret from funcd at each read (ADR-0249
  Decision 2), and an engine is replaced on a changed value (ADR-0332). Rejected as needless.

## Decision

1. **The owner and the writer.** The stack's Job owns the outputs Secret (`spec.owner` of kind `Job`, ADR-0351
   Decision 3), for the plain and the sensitive outputs alike, and only its runs write it, through its OpenTofu
   runtime: neither the add-on's Function nor funcd writes an output.
2. **Where they go.** At the end of an apply, the runtime writes each output in the state, plain or sensitive, into the
   Secret its Job's ConfigMap names in `INFRASTACK_OUTPUTS_SECRET`, one `ctx.secret.put` per output (ADR-0327
   Decision 4), and answers only after the last write, so a run that succeeds has delivered every output (ADR-0347
   Decision 5). The plain ones are also its result, under `outputs`, as ADR-0355 Decision 3 has the runtime answer; a
   sensitive one is in no result, preview document, status or log line. A Job whose ConfigMap names no outputs Secret
   keeps its plain outputs in its result alone and its sensitive ones nowhere, and its run logs each such name. Decided
   on the decider's delegation of 2026-11-24: the plain outputs go to the Job's Secret, which outlives the runs that
   `job.retention` removes.
3. **A refused write.** A write that funcd refuses, such as a 403 for a Secret the Job does not own, fails the run
   after its apply, naming the output and the Secret, never the value. The applied change stays in the state, and the
   stack's next apply, which its add-on starts at the stack's next generation, writes every output again.
4. **Names and values.** An output's key is its name, and its value is a string as it is, any other value its JSON. An
   apply or a `plan` run of a module with an output whose name is not an env-var name (`^[A-Za-z_][A-Za-z0-9_]*$`, a
   Secret key's rule) or starts with `FUNCD_`, which no reader loads (ADR-0327 Decision 7), or with `INFRASTACK_`, the
   prefix of the runtime's own settings such as `INFRASTACK_STATE_PASSPHRASE`, fails its first call before it plans,
   naming the output: the preview with `PreviewFailed`, a `bypass` run's one call with `HandlerFailed`. A destroy
   skips the check, since it empties only the keys of the outputs its last apply wrote.
5. **A destroy.** A destroy run empties each output key it read before the destroy, keeps the key, and answers `{}`.
   It deletes no key: a deleted one would make an App's readers wait for it again, or stop the App's pass once the App
   no longer declares the owner Job (ADR-0357 Decision 2), and hold a Job outside an App that declares it `Pending`
   with `SecretKeyMissing`. An emptying that funcd refuses is logged by the output's name, and the run goes on: the
   outputs are gone with what made them.
6. **The readers.** A Function reads an output through `ctx.secret` of the outputs Secret it binds, from funcd at each
   read (ADR-0249 Decision 2), so a changed output reaches its next read and rolls nothing. An add-on object's engine
   gets each key its Addon lists in `secretKeys`, and a changed value replaces it (ADR-0332). No Secret value reaches
   a Function's environment since ADR-0249, so F144's exit criterion reads "through `ctx.secret`", not "as env vars".
7. **No output feeds a run.** The runtime gives OpenTofu no key of its bound Secrets named like one of the module's
   outputs, read with `tofu show -json -module`, so an output never sets a later run's variable (`TF_VAR_<name>`) or a
   provider's setting.
8. **`funcdctl infra outputs <stack> [-n <namespace>] [-o json]`**, the plugin's (ADR-0355 Decision 1). Through
   `$FUNCDCTL`, it reads the stack; the Job its `config.job` names, the alias of the add-on Function's binding, which
   the add-on's README requires to be the Job's name; that Job's `status.contract`; the `INFRASTACK_OUTPUTS_SECRET` of
   its bound ConfigMaps, merged as the runtime reads them, a later ConfigMap's key winning (`envresolve.Resolve`); then
   that Secret. It prints each key of the Secret, with the string it holds when the contract's `outputs` names the
   key, else `(sensitive)`, so it never prints a sensitive value, nor one that a contract written by hand does not
   name. A read funcd refuses ends it with funcdctl's error.

## Design model

- **Spec**: [`docs/design/job/stack-outputs.fizz`](../design/job/stack-outputs.fizz) · state machine
  [`stack-outputs.md`](../design/job/stack-outputs.md)
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `ReadyStackHasItsOutputs` | always | `scenario: stack-outputs-reach-function` | `test_apply_writes_every_output_before_it_answers` |
  | `RunEnds` | eventually always | `scenario: stack-outputs-reach-function` | `test_apply_answers_after_its_last_write` |

- **Bounds**: one run of a second generation, two outputs, 4 actions: the shortest run that ends, through the apply,
  both writes and the answer.
- **Faults**: a restart at any step, which ends the run `Interrupted` (ADR-0347 Decision 7); crash on yield off, since
  each step is one store write.
- **Environment**: each write is one Secret update (ADR-0327 Decision 4); the runtime holds the outputs in memory from
  the apply to its answer.
- **Mutants**: `answer-first` (the runtime answers before it writes) fails `ReadyStackHasItsOutputs`; `no-answer`
  fails liveness.
- **Result**: pass, 9 states, 0.6 s, FizzBee v0.5.4; wider (5 actions): pass, 9 states.
- **Left to tests**: the destroy's order and a refused write, in `handler_test.py`.
- **Oracle markers**: the oracle tests live in pyvvo/funcd-addons, where the review checks their markers;
  `just check-specs` greps funcd only, so it lists both assertions as having no oracle test yet.

## Behavior rows

None: funcd's behavior does not change. The runtime's writes and `funcdctl infra outputs` are the InfraStack
add-on's, and its README records them.

## Temporary workarounds

None.

## Contracts

```python
# infrastack/runtime/handler.py (delta)
OUTPUT_KEY = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")  # new: an output's name (Decision 4)
def check_outputs(names: list[str]) -> None: ...      # new: Decision 4; raises StackError naming the first refused
def write_outputs(context: Any, secret: str, outputs: dict[str, Any]) -> None: ...  # new: Decisions 2 and 3

# infrastack/plugin/funcdctl-infra (delta)
def outputs(stack: str, namespace: str, as_json: bool) -> int: ...  # new: Decision 8
```

`funcdctl infra outputs frp-bunny-stack` prints (new):

```text
NAME           VALUE
pullzone_host  frp-lane.b-cdn.net
FRP_TOKEN      (sensitive)
```

With `-o json`: `{"outputs": {"pullzone_host": "frp-lane.b-cdn.net"}, "sensitive": ["FRP_TOKEN"]}`, each value the
string the Secret holds.

| Dependency & I/O | Direction | Shape |
|---|---|---|
| `ctx.secret.put` on the outputs Secret | consumes | ADR-0327 Decision 4, as the Secret's owner Job |
| `INFRASTACK_OUTPUTS_SECRET` in the Job's bound ConfigMap | consumes | the Secret's name |
| the outputs Secret, one key per output | exposes | strings; JSON for any other value |
| `$FUNCDCTL get … -o json` | consumes | the stack, its Job and contract, the ConfigMap, the Secret |

## Implementation plan

In pyvvo/funcd-addons, on the InfraStack add-on; funcd's code does not change:
1. `infrastack/runtime/handler.py`: Decisions 2 to 7; `handler_test.py` with a fake context that records each write
   and its order; `image_check.py`: the destroy that empties every key, and a refused name.
2. `infrastack/plugin/funcdctl-infra`: `outputs`, with `plugin_test.py` over a fake funcdctl.
3. The README's result, outputs and plugin sections.

Tests, each written to pass: `stack-output-name-refused`, `stack-output-write-refused`, `stack-outputs-feed-no-run`
and the two oracle tests in `handler_test.py`, each oracle test under `# oracle:
docs/design/job/stack-outputs.fizz#<Assertion>`; `stack-destroy-empties-outputs` in `image_check.py`;
`infra-outputs-lists-them` in `plugin_test.py`; `stack-outputs-reach-function`, `stack-output-changed` and
`stack-outputs-never-shown` as cases of funcd's InfraStack lane (`e2e/addon-infrastack.venom.yml`);
`stack-outputs-reach-engine` in the frp-bunny lane (`e2e/addon-frp-bunny.venom.yml`). Done when those pass.

## Review checklist

- [ ] Every scenario has its named, passing test, and each assertion its oracle marker.
- [ ] No sensitive value reaches a result, a preview, a log line or the plugin's output.
- [ ] The runtime answers an apply only after its last output write.

## Consequences

- A Function and an engine read a stack's outputs as they read any Secret, and see a change at the next read or with a
  new engine. The frp-bunny App's Tunnel gets frps' token there; the pull zone's host is there too, for any part that
  binds the Secret, though the App's values know it before the apply.
- Plain outputs sit in a Secret, so reading them, with `funcdctl infra outputs` too, takes the right to read it, and a
  Function that needs one plain output binds the Secret that holds every sensitive one, since a binding reads all its
  keys (ADR-0249 Decision 2).
- A person's apply of the outputs Secret replaces what the Job wrote (ADR-0327 Decision 7), and a restart between two
  writes leaves some outputs of the new apply beside the old ones, the run ending `Interrupted`. Either lasts until the
  stack's next apply, which its add-on starts at the stack's next generation.

## Open questions

None.

## References

- Feature issue #1022 (FEAT-0012 F144), under tracker #1028.
- pyvvo/funcd-addons `infrastack/README.md`, `infrastack/runtime/handler.py`; pyvvo/funcd-functions `apps/frp-bunny`.
- ADR-0057, ADR-0093, ADR-0249, ADR-0327, ADR-0332, ADR-0347, ADR-0349, ADR-0351, ADR-0355, ADR-0357.
