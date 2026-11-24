## Verdict: changes requested — 0 blockers, 1 major, 1 minor (ADR-0355 implementation, model: claude-opus-5-5)

This report reviews pyvvo/funcd-addons branch `feat/infra-push` at e869eb6: four commits on `feat/stack-outputs`
679fc14, whose ADR-0356 runtime has its own report,
[adr-0356-implementation-claude-opus-5-5.md](adr-0356-implementation-claude-opus-5-5.md). The work holds:
- the `funcdctl-infra` plugin, with its `push` and `outputs` commands, `plugin_test.py`, `push_check.py`, `stamp.py`,
  the CI job and the release stamp;
- the runtime's providers, local files and `plan` action;
- the README and the contract's shape;
- a guard for a null `module_calls`.

The reviewer did not write the work. Every check below was run, not read. Nothing is stamped, since a Major is open.

### 🔴 Blockers

None.

### 🟡 Major

- **`funcdctl infra outputs` prints neither format that ADR-0356 Contracts gives** · model ·
  `infrastack/plugin/funcdctl-infra:357-362`, pinned by `infrastack/plugin/plugin_test.py:383-406`.
  - ADR-0356 (Accepted) Contracts says the command prints a `NAME  VALUE` table, and that `-o json` prints
    `{"outputs": {"pullzone_host": "frp-lane.b-cdn.net"}, "sensitive": ["FRP_TOKEN"]}`.
  - The plugin prints `key: value` lines instead, and `-o json` prints one flat map, such as
    `{"FRP_TOKEN": "(sensitive)", "host": "a.b-cdn.net"}`. In that map a script cannot tell the `(sensitive)` marker
    from a real value.
  - The per-key rule is right: Decision 8's choice of which keys print `(sensitive)` holds, so the gap is the output
    shape only.
  - Fix: print the Contract's table, with a header and aligned columns. Make `-o json` the Contract's
    `{"outputs": {<plain key>: <string>}, "sensitive": [<keys>]}`. Move the test to both shapes.

### Minor

- **The repository's plugin rule contradicts the plugin** · model · `CLAUDE.md:24-25`.
  - The rule reads "A plugin is one Go command under `<addon>/plugin/funcdctl-<addon>/` … it reaches funcd through
    `$FUNCDCTL`".
  - ADR-0355 Decision 1, which is Accepted, decides on a Python standard-library script named `funcdctl-infra`.
  - The repository follows funcd's ADRs, so the rule must change in this PR, or the next agent will "fix" the plugin
    back to Go.
  - Fix: add the exception to the rule: InfraStack's plugin is the Python 3.9+ script
    `infrastack/plugin/funcdctl-infra` (funcd ADR-0355 Decision 1). Say too that it uses `$FUNCDCTL`, or, when run
    directly, the `funcdctl` on PATH.

### Routed (not this work's)

- **The lane scenarios.** `infra-variables-refused` and `infra-sensitive-variable-refused` belong to funcd's
  `addon-infrastack` lane, on `test/frp-bunny-lane`, whose PR is opening now.
- **Item 3 of ADR-0355's plan, the funcd-functions part.** `apps/frp-bunny/infra/bunny/bunny.contract.yaml` and
  `apps/frp-bunny/infra/fake/fake.contract.yaml` still have the old output shape at `feat/frp-bunny-example` ad2e739.
  With this runtime, the frp-bunny lane refuses every apply until they change. Home: pyvvo/funcd-functions.
- **nerdctl** is untested, since this machine has none (env).
- **The status moves.** ADR-0355 and F143 move at the PR assembly. `Implemented` waits for the Major and for the
  routed items above, and that PR closes #1021.

### ✅ Verified correct (keep it)

**Runs and tests**
- `nix develop -c just ci` exits 0: gofmt, vet, `go test -race`, 24 handler tests, 15 plugin tests and 9 Node tests.
  The plugin and its tests also pass on the macOS Python 3.9.6.
- `just opentofu-image "" funcd-addons/opentofu:review-0355` exits 0, and `just opentofu-image-check` on that image
  exits 0 with `PASS` twice, once with the state's encryption off and once on.
  - On a network that reaches only the S3 engine, a module that carries its own providers plans and applies.
  - A package off the lock file fails the preview, naming `hashicorp/random`.
  - The `infra-plan-run` answer is exactly the ADR's. The plan run keeps no saved plan, writes no output and applies
    nothing.
- `just funcdctl` and `just infra-push-check` exit 0 with real OpenTofu. A module with a local child is pushed into
  an OCI layout:
  - the index holds `linux/amd64` and `linux/arm64`;
  - `funcdctl inspect` prints the variables, the sensitive one left out, and the output `name`;
  - each platform's bundle holds only its own `terraform-provider-random_3.6.3_linux_<arch>.zip`;
  - the lock file is copied back into the module.
- The plugin also runs through funcdctl's own lookup: `funcdctl plugin list` names it.
- **Twenty mutants of my own, all killed:**
  - the refusals: a remote call accepted, a call leaving the folder accepted, the local files ignored,
    `*.auto.tfvars` ignored, a digest ref accepted;
  - the push: both platforms mirrored into each bundle, the lock not copied back;
  - the contract: every variable required, a sensitive variable kept, a sensitive output kept, a tuple's items typed;
  - `outputs` printing every value;
  - the plan finding and the runtime: a plan run that keeps its plan, a plan run that applies, the address list
    unbounded, the module's own providers ignored, the local files copied, `*.auto.tfvars` copied;
  - the digest stamp: a stamp that does nothing, a stamp that reads the wrong runtime.
- The generated contract is byte-identical to ADR-0355's Contracts example.

**The builder's decisions: keep all ten**
- **The Python plugin** is decided by ADR-0355 Decision 1. Only the repository's rule lags (Minor).
- **`$FUNCDCTL`, else PATH.** funcdctl always sets `FUNCDCTL` for a plugin (ADR-0319 Decision 4), and the fallback
  still goes through a funcdctl.
- **The fixed driver in the image.** A distroless image has no shell, and `tofu get`'s `.terraform/` must last into
  the lock and mirror steps. Its paths match the Dockerfile.
- **Child modules read level by level.** `show -json -module` prints only the root module and its `module_calls`.
- **"Required" means no `default` key**, which matches OpenTofu 1.13.9's `"required": true`.
- **A digest ref is refused.** Decision 2 takes a tag, and the `-linux-<arch>` tags need one.
- **The build folder's exclusions** are the ADR's, plus `__funcd_contract.json` and a stale `.funcd-providers/`, which
  push and the mirror rewrite.
- **The YAML emitter** produces the ADR's contract byte for byte.
- **`(sensitive)` for a value the contract does not name** is Decision 8's rule.
- **The `/tmp` exec mount** in the image check matches a funcd worker's `/tmp`: a tmpfs with `nosuid,nodev` and no
  `noexec` (`internal/runtime/containerd/containerd_linux.go:1418-1422`).

### Definition of Done

10 of 14 items hold: ADR-0355's Review checklist (5) and the generic DoD (9).
- Two misses are the model's: Contracts honoured, which is the Major, and the repository's conventions, which is the
  Minor.
- Two misses are routed: the lane scenarios, and the status moves.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0355 (implementation) → changes-requested, 0/1/1, 2 model-attributed, DoD 10/14. See
docs/reviews/model-scorecard.md.

### Recommendation

The builder fixes the `outputs` formats and the repository rule, then a second round reviews those two items. The
funcd-functions contracts and the lane scenarios land with their own PRs.
