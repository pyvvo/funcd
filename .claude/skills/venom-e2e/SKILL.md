---
name: venom-e2e
description: Write or extend a declarative end-to-end test suite for a funcd Lima/containerd lane using OVH Venom (YAML, not bash). Use whenever asked to add/convert an e2e for a `just lima-*` lane, replace a `scripts/lima-*-invoke.sh` bash invoke with a declarative suite, assert HTTP behaviour against a self-deploying VM, or test a fail-closed/negative path on a real sandbox — "venom e2e", "declarative e2e", "convert the invoke script to Venom", "add an e2e suite for the <X> lane". Encodes the exact architecture + the hard-won rules (in-VM mutation, retry-as-wait, no helper scripts) that the kv-counter lane proved on real containerd.
---

# venom-e2e — declarative e2e for funcd Lima lanes

The Lima/containerd lanes (`just lima-example-*`) self-deploy a VM, then drive the running platform.
The **invoke/assertion phase is written in [OVH Venom](https://github.com/ovh/venom)** (Apache-2.0) — a
YAML suite of `http`/`exec` steps with assertions — **not** a hand-written `scripts/lima-*-invoke.sh`.
The canonical, working example is **[e2e/kv-counter.venom.yml](../../../e2e/kv-counter.venom.yml)** (ADR-0076);
copy its shape. This skill is the *why* behind that shape, so a new lane is mechanical.

## Architecture (how a lane runs)

```
just lima-example <name>  ──►  scripts/lane.py reads the lane's section of scripts/lanes.yaml: builds, stages
                               its files for /opt/lane
                          ──►  limactl start scripts/lima-lane.yaml (one generic VM: scripts/lane-provision.sh
                               pushes and applies, scripts/lane-ready.sh gates on the lane's `ready`)
                          ──►  venom run <the lane's `venom` suite>   (host-side)
                          ──►  trap: limactl stop/delete on EXIT
just lima-example-all     ──►  every lane not marked `optional`, in turn, on one shared VM (ADR-0230)
```

- **Venom runs HOST-SIDE.** Its `http` steps hit the VM's **forwarded `127.0.0.1` ports** (Lima auto-forwards
  guest `127.0.0.1` → host): data plane `:8081`, control plane `:8080`.
- **The VM is already deployed + Ready** before Venom runs (the lane's `ready` gates `limactl start`); a lane
  without `ready` deploys its resources in its own suite.
- Venom is the **flake-pinned `venom` binary** (ADR-0077 — `buildGoModule`, v1.3.0; not in nixpkgs); the dev
  shell puts it on `PATH`. Invoke **`venom`**, never `go run …@version`.

## The rules (each one cost a containerd run to learn)

1. **No helper scripts. The suite is the test.** The thesis is "Venom removes the bash scripts" — honour it.
   The positive path is `http` steps; a mutation is **one inline `exec` line**; a wait is **`retry`/`delay`
   on the assertion**, never a hand-rolled poll loop in a `.sh`. If you find yourself writing a helper
   script, stop — fold it into an `exec` step or a static fixture.
2. **Mutations run IN the VM, co-located with the daemon — never host-side over a forwarded port.** A
   control-plane mutation (`funcdctl apply`) driven host-side **races the redeploy**: the running sandbox
   serves the old state until the new revision is Ready, so the assertion sees the *old* behaviour inside any
   retry window. Do the mutation via `limactl shell {{.vm}} -- sudo … funcdctl apply …`; keep the assertion
   host-side. (The data-plane `http` assertion over the forwarded port is fine host-side — only *mutations*
   must be in-VM.)
3. **`retry`/`delay` IS the wait.** After a mutation that triggers a redeploy, do **not** poll Ready in a
   script — set a generous `retry`/`delay` on the next step (e.g. `retry: 30`, `delay: 2`) so Venom re-issues
   until it succeeds. That is Venom's whole value over bash. **`retry` works on `exec` steps too**, not just
   `http`: for a non-http lane (e.g. metastore — the suite starts the daemon itself), retry the in-VM `exec`
   that needs the daemon (the `funcdctl apply` / `get`) until it returns `result.code 0` — that is the wait,
   with no poll loop and no dependency on host port-forwarding.
4. **Mutate by applying a STATIC fixture, not runtime config surgery.** To unbind/alter a resource, ship a
   static variant YAML (e.g. `counter-unbound.yaml` = `counter.yaml` minus `spec.bindings.kv`) and `apply` it — not a
   `python -c` strip at runtime. Declarative, reviewable, bundled with the lane.
5. **No machine-specific paths in the tracked suite — inject them with `--var`.** The suite uses `{{.vm}}`
   (and any host path) as a variable; the recipe passes `--var "vm=…"`. In-VM paths under `/opt/lane/`, where a
   lane's files are staged, may appear literally. **Never** write a dev-machine path (`/Users/…`, `/home/…`) into the suite.
6. **Keep Venom's logs out of the repo.** Venom writes `venom.log` + rotated `venom.N.log` in its CWD — run
   it **from the scratch dir** (the recipe's `lima_deps`, outside the repo) with an absolute suite path and
   `--output-dir` there for the JUnit results; the generic recipe already does. `*.log` is also gitignored.

## Declare the lane (scripts/lanes.yaml)

A lane is one section of `scripts/lanes.yaml`, read by the one generic recipe and the one generic VM: no per-lane
recipe or VM YAML. Its header documents every key: `module`, `dir`, `copy`, `build`, `stage`, `push`, `apply`,
`ready`, `tokens`, `venom` and `optional`. The recipe's `trap` tears the VM down regardless of result.

## Suite skeleton (copy + adapt)

```yaml
name: <lane> — <what it proves> (ADR-XXXX, containerd lane)
vars:
  dp: http://127.0.0.1:8081   # data plane, forwarded from the Lima guest

testcases:
  - name: <happy path — declarative http asserts>
    steps:
      - type: http
        method: POST
        url: "{{.dp}}/function/<fn>"
        headers:
          content-type: application/json
        body: '{"data":{"name":"venom"}}'
        retry: 10            # first call: absorb any warm-up after Ready
        delay: 1
        assertions:
          - result.statuscode ShouldEqual 200
          - result.bodyjson.<field> ShouldEqual <value>

  - name: <fail-closed / negative — mutate in-VM, then assert>
    steps:
      - type: exec          # the ONE mutation line, co-located with the daemon
        script: |
          limactl shell {{.vm}} -- sudo env FUNCD_SERVER=http://127.0.0.1:8080 FUNCD_TOKEN=funcd-dev-token \
            funcdctl apply -f /opt/lane/fixtures/<static-mutated>.yaml
        assertions:
          - result.code ShouldEqual 0
      - type: http          # retry/delay IS the reconcile wait — no poll loop
        method: POST
        url: "{{.dp}}/function/<fn>"
        headers:
          content-type: application/json
        body: '{"data":{"name":"venom"}}'
        retry: 30
        delay: 2
        info: "denied response → status {{.result.statuscode}} body {{.result.body}}"
        assertions:
          - result.body ShouldContainSubstring <expected-error-token>
```

Assertion vocabulary you'll reuse: `ShouldEqual`, `ShouldNotEqual`, `ShouldContainSubstring`,
`ShouldMatchRegex`, `ShouldBeEmpty`, `result.statuscode`, `result.body`, `result.bodyjson.<path>`,
`result.code` / `result.systemout` (exec exit code / stdout). Verify any others against `venom run --help` / the docs.

## Steps to add a new lane suite

1. **Read or add the lane's section** in `scripts/lanes.yaml`: the module and directory its files come from,
   what it stages into `/opt/lane`, what it pushes and applies, and its `ready` gate.
2. **Write `e2e/<X>.venom.yml`** from the skeleton: positive `http` testcases first; any negative as an
   in-VM `exec` mutation + a `retry`-ed `http` assert. If the negative needs a mutated resource, add a
   **static fixture** (beside the example, or in `e2e/fixtures/`) and list it under the lane's `stage`.
3. **Name the suite** in the lane's `venom:` key, then run the lane guard tests:
   `go test -run Lane ./cmd/funcd/ ./internal/testkit/langmod/` and `go test ./scripts/`.
4. **Run it green**: `scripts/agent/d just lima-example <X>` → the run must end `final status: PASS`. Each
   failed assertion prints the testcase/step — read it and fix the suite (not the platform). Budget for a
   couple of full VM boots; that is normal.
5. **Update `e2e/README.md`** if the lane set changed.

## Verify

A lane is green when the run ends:

```
final status: PASS
```

A non-zero `just` exit / `final status: FAIL` names the failing testcase+step+assertion. The VM tears down
on EXIT regardless (`trap`); confirm with `limactl list` (no `funcd-*` instance) after a run.

## Project conventions (apply silently)

- Run everything through **`scripts/agent/d …`** or `nix develop -c …` (the pinned toolchain provides `just`, Go, `limactl`).
- Deps are **Apache-2.0/MIT/BSD** only — Venom is Apache-2.0. ✓
- **Identity / paths**: never write a dev-machine username or an OS-absolute path (`/Users/…`, `/home/…`)
  into the suite, the recipe, or a fixture — inject host paths via `--var`; in-VM `/opt/lane/…` paths are
  fine. Grep changed files before finishing.
- Venom is standardised + flake-pinned by **ADR-0077**, and every lane is a Venom suite declared in
  `scripts/lanes.yaml` (ADR-0230: one shared VM for `just lima-example-all`). A further cross-cutting change
  (e.g. CI-gating the lanes, a Venom version bump) is an **ADR** (`/adr`), not an ad-hoc change.
