# ADR-0077: Declarative e2e for the containerd lanes — OVH Venom

- **Status**: Implemented (2026-06-23)
- **Date**: 2026-06-23 (judged 2026-06-23 — sound, one-topic tooling decision, no Blockers; Venom facts
  WebFetch-verified (Apache-2.0, v1.3.0, Go module w/ `cmd/venom`, not in nixpkgs). Folded 1 Major (the
  "`scripts/` holds *only* `lima-*.yaml`" over-claim → the true, narrower "no per-lane invoke `.sh` remains",
  since `scripts/` legitimately also carries `build.sh`/`demo/`/`lima.yaml`/the metastore smoke) + minors
  (define the `{{lima_deps}}` scratch var; `doCheck=false` rationale). Correctly **relates to** (does not
  touch) ADR-0025's four-tier taxonomy + `InMemory()` embed e2e; CI-posture framed honestly (Lima is not
  macOS-bound; CI-gating deferred).)
- **Deciders**: green-0-rabbit
- **Tags**: testing, e2e, venom, lima, containerd, tooling
- **Realizes**: [FEAT-0001/F46](../feat/0001-feat-v1.1.md) (declarative e2e via Venom)
- **Relates to**: [ADR-0025](0025-testing-strategy-and-e2e-harness.md) (the four-tier taxonomy + `funcd.InMemory()`
  embed harness — Venom **adds** the containerd-example-lane assertion mechanism; the taxonomy, contract suites,
  and embed e2e are unchanged), [ADR-0034](0034-end-user-journey-acceptance-e2e.md) (the acceptance journey
  driving the real `funcdcli` + HTTP — Venom is its **declarative form** for the self-deploying Lima lanes),
  [ADR-0052](0052-bench-containerd-cgroup-footprint-lane.md) (the precedent containerd Lima lane + the
  `nixpkgs-lima` flake pin this follows), [ADR-0069](0069-kv-data-plane.md)/[ADR-0076](0076-cedar-kv-read-binding-grant.md)/[ADR-0064](0064-fn-to-fn-rpc-links.md)
  (the lanes' subjects)

## Context & Need

The **containerd example lanes** (`just lima-example-kv`, `just lima-example-fn-to-fn`) self-deploy a Lima VM
running funcd on real containerd, then drive the deployed examples and assert behaviour. Until now the
**assertion phase** was a hand-written bash script (`scripts/lima-*-invoke.sh`) piped into the VM — brittle
nested quoting, hand-rolled readiness/redeploy poll loops, and **no structured result** (a `curl` whose status
nobody asserted). That last point is not hypothetical: converting the fn-to-fn lane surfaced a real behaviour
the bash had hidden — a contract-propagation case returns **HTTP 500 carrying greeter's 422**, which the bash
printed but never checked.

This ADR makes **OVH Venom** (Apache-2.0, a YAML e2e runner) the **official assertion mechanism** for these
lanes, and **pins it in the flake**. Two lanes are already converted and green on real containerd (the spike →
conversion arc); this ADR ratifies the pattern (captured in the `venom-e2e` skill), replaces the spike's
unpinned `go run …venom@v1.3.0` with a flake-pinned binary, and sets the results/log convention.

## Scenarios

- **scenario: venom-pinned** — Given the dev shell, When a lane runs, Then it invokes the **flake-pinned**
  `venom` (a fixed version on `PATH`), not an ad-hoc `go run …@version` download.
- **scenario: lane-asserts-declaratively** — Given the converted kv (or fn-to-fn) lane, When `just
  lima-example-kv` runs, Then a Venom suite drives the invokes + assertions and the run ends `final status:
  PASS`.
- **scenario: no-helper-scripts** — Given a converted lane, Then **no per-lane invoke/mutation helper `.sh`**
  remains for it (`scripts/lima-{kv,fn-to-fn}-invoke.sh` are gone) — the suite + static fixtures are the whole
  test. (`scripts/` still legitimately carries the `lima-*.yaml` provisioning + unrelated tooling.)
- **scenario: mutation-in-vm** — Given a lane that mutates control-plane state mid-suite (kv unbind), When the
  suite runs it, Then the mutation is an **in-VM** `exec` (`limactl shell`) and the next assertion's
  `retry`/`delay` absorbs the reconcile — no host-side redeploy race.
- **scenario: results-discoverable** — Given any lane run, When it finishes, Then a JUnit `test_results_*.xml`
  + `venom.log` are written to a stable scratch path and that path is **echoed** at the end of the run.

## Scope

**In**: pin Venom (v1.3.0) in the flake via `buildGoModule` (all supported systems — it is not in nixpkgs);
adopt the `venom-e2e` pattern as the standard (host-side `http` assertions over the forwarded `:8081`/`:8080`;
in-VM `exec` mutations; `retry`/`delay` as the wait; static fixtures not runtime config surgery; `--var` for
host paths; no helper scripts); the **results/log convention** (scratch dir + echoed path + JUnit XML); switch
the **kv + fn-to-fn** lane recipes from `go run …@version` to the pinned `venom`.

**Out**: converting `scripts/lima-metastore-smoke.sh` (a **follow-up** — it needs an in-VM daemon **restart** +
recovery assertion, a different shape from http-invoke); **CI-gating** these lanes (they stay opt-in; gating
needs a containerd-capable runner — see Open questions); changing the **four-tier taxonomy** (ADR-0025 stands:
unit / port-contract / `InMemory()` embed e2e / Linux-integration are untouched — Venom is an added
*containerd-lane* mechanism, not a replacement); the lanes' **subject behaviour** (owned by ADR-0069/0076/0064).

## Constraints & Decision drivers

- **Reproducible tooling** — the project pins its toolchain in the flake (`go`, `just`, Lima via `nixpkgs-lima`).
  An unpinned `go run …@version` in a committed lane is the gap this closes.
- **Apache-2.0/MIT-only deps** — Venom is **Apache-2.0**. ✓ It is **test-only tooling**: never linked into the
  single-binary product, so the library-first / embed-first / pure-Go-binary constraints are unaffected.
- **One proven pattern, not per-lane reinvention** — the `venom-e2e` skill is the authoring contract; the rules
  in it each cost a real containerd run to learn (in-VM mutation, retry-as-wait, no scripts).

## Alternatives considered

| Decision | Chosen | Rejected (why) |
|---|---|---|
| Assertion mechanism | **OVH Venom** (YAML; `http`/`exec`/`ssh` executors; `retry`/`delay`; JUnit; Apache-2.0; single binary) | **Keep bash invoke scripts** — brittle quoting, hand-rolled poll loops, no structured result (hid the fn-to-fn 500-carries-422). **bats** — still bash. **hurl** — http-only, no exec/orchestration for the in-VM mutation. **k6** — load-testing, not functional asserts. **plain Go** — duplicates the `InMemory()` embed e2e (ADR-0025) and can't easily drive a self-deploying VM. |
| How Venom is obtained | **Flake-pinned `buildGoModule` binary** (fixed version, on the dev-shell `PATH`) | **`go run …venom@v1.3.0`** (the spike) — not reproducible (unpinned transitive build, network each run); fine to *evaluate*, not to standardize. |
| Where Venom runs | **Host-side** (assertions over forwarded ports); mutations **in-VM** via `limactl shell exec` | **In-VM Venom** — needs a Linux `venom` binary shipped into the VM for no benefit on the assertion side; a host-side mutation over the forwarded control-plane port **races the redeploy** (proven in the spike). |

## Decision

Venom is the official declarative assertion mechanism for the containerd example lanes, pinned in the flake.

1. **Pin Venom v1.3.0 in the flake** via `buildGoModule` — a `venom` package added to the dev-shell `packages`
   for **all** `supportedSystems` (cross-platform, unlike the darwin-only Lima). Lane recipes invoke `venom`
   (the pinned binary), never `go run …@version`.
2. **`e2e/` holds the declarative suites** (`e2e/<lane>.venom.yml`) + a README; the **`venom-e2e` skill** is the
   authoring playbook. This `e2e/` (containerd-lane Venom suites) is **distinct** from `tests/e2e/` (the
   ADR-0025 `funcd.InMemory()` Go embed tests) — different substrate, different tool.
3. **Results convention**: each run writes `test_results_<suite>.venom.xml` (JUnit) + `venom.log` to the scratch
   dir (`{{lima_deps}}` — the existing Justfile var for the Lima deps mount dir under the user cache, outside the
   repo tree); the recipe **echoes that path** at the end of the run; `*.log` is gitignored. JUnit makes the
   lanes CI-consumable when a runner exists (Open questions).
4. **The kv + fn-to-fn lanes** are converted (already green) and switch to the pinned `venom`; their **per-lane
   bash invoke scripts are retired** (`scripts/lima-{kv,fn-to-fn}-invoke.sh` gone). `scripts/` still carries the
   `lima-*.yaml` provisioning, the metastore smoke, and unrelated tooling (`build.sh`, `demo/`, `lima.yaml`) —
   out of scope here; the claim is only that no *per-lane invoke/mutation helper* `.sh` remains.

## Temporary workarounds

The kv + fn-to-fn recipes currently invoke `go run github.com/ovh/venom/cmd/venom@v1.3.0` (the spike's unpinned
form). **Exit criterion**: replaced by the flake-pinned `venom` binary when this ADR is implemented (Decision §1).

## Contracts

```nix
# flake.nix — a pinned Venom, added to the dev-shell packages for ALL supportedSystems (it is cross-platform;
# Lima stays darwin-only). src + vendor hashes are discovered at implementation via a build attempt.
venomFor = system: (pkgsFor system).buildGoModule rec {
  pname = "venom"; version = "1.3.0";
  src = (pkgsFor system).fetchFromGitHub { owner = "ovh"; repo = "venom"; rev = "v${version}"; hash = "<sri>"; };
  vendorHash = "<sri>";
  subPackages = [ "cmd/venom" ];
  doCheck = false;          # upstream tests need network/containers; we pin the already-proven-green v1.3.0 (the lanes run on it today)
};
# devShell.packages = [ go gopls just git ] ++ limaFor system ++ [ (venomFor system) ];
```

```just
# the lane recipe tail (the venom-e2e pattern) — uses the pinned `venom`, echoes the results path:
suite="$(pwd)/e2e/<lane>.venom.yml"
( cd {{lima_deps}} && venom run --output-dir {{lima_deps}} --var "vm={{lima_<lane>_vm}}" "$suite" )
echo "venom results: {{lima_deps}}/test_results_<lane>.venom.xml"
```

```yaml
# e2e/<lane>.venom.yml — testcases of `http` (data plane :8081) + in-VM `exec` (limactl shell) steps with
# assertions + retry/delay; JUnit via --output-dir. The full skeleton + rules live in the venom-e2e skill.
```

| consumes | exposes |
|---|---|
| the flake-pinned `venom` + `limactl` (dev shell) | declarative lane suites (`e2e/*.venom.yml`) |
| the self-deploying Lima VM's forwarded ports (`:8081` data, `:8080` control) | a `final status: PASS/FAIL` + JUnit `test_results_*.xml` |
| `--var vm=<lima-vm>`; in-VM deploy paths (`/opt/<lane>/…`) | the echoed results path |

## Implementation plan

**Files**: `flake.nix` (add the `venomFor` `buildGoModule` derivation + wire into `devShell.packages` for all
systems; discover the `src` hash + `vendorHash` via `nix build`). `Justfile` (switch the `lima-example-kv` +
`lima-example-fn-to-fn` recipes from `go run …venom@v1.3.0` to `venom`; add the `echo "venom results: …"` line).

**Already in place (this ADR ratifies)**: `e2e/kv-counter.venom.yml`, `e2e/fn-to-fn.venom.yml`, `e2e/README.md`;
`examples/js/kv-counter/counter-unbound.yaml` (the unbind fixture); retired `scripts/lima-{kv,fn-to-fn}-invoke.sh`;
`.gitignore` `*.log`; the `venom-e2e` skill.

**go.mod / deps**: none in the product; Venom is a flake-pinned **test tool** (no entry in the funcd module).

**Test plan** — one check per scenario: **venom-pinned** — `nix develop -c command -v venom` resolves into the
nix store (not a `go run` shim) and `venom version` reports 1.3.0. **lane-asserts-declaratively /
no-helper-scripts / mutation-in-vm / results-discoverable** — `nix develop -c just lima-example-kv` **and**
`… fn-to-fn` end `final status: PASS`; no `scripts/lima-*-invoke.sh` remain; the results path is echoed. These
are containerd lanes (run locally, like ADR-0052's footprint lane); the embed e2e / contract suites / `just ci`
are unaffected.

**Definition of done**: Venom pinned in the flake + on the dev-shell `PATH`; both lane recipes use the pinned
binary + echo the results path; both lanes green on real containerd; `scripts/` free of invoke scripts; no new
*product* dep; no identity/path leak.

## Review checklist

- [ ] `flake.nix` pins Venom v1.3.0 via `buildGoModule` (pinned `src` + `vendorHash`), added to `devShell`
      `packages` for all `supportedSystems`; `nix develop -c venom version` = 1.3.0.
- [ ] `lima-example-kv` + `lima-example-fn-to-fn` invoke the **pinned** `venom` (no `go run …@version`); each
      echoes the results path.
- [ ] Both lanes end `final status: PASS` on real containerd; `e2e/` holds the suites + README; no
      `scripts/lima-*-invoke.sh` remain (the per-lane invoke helpers are retired).
- [ ] Results: JUnit XML + `venom.log` in the scratch dir; `*.log` gitignored; no identity/path leak; Venom is
      the only new (test-only) dep, Apache-2.0.
- [ ] The four-tier taxonomy (ADR-0025) + `InMemory()` embed e2e + contract suites are unchanged.

## Consequences

**Positive**: the containerd example lanes assert **declaratively** (readable `http`/`exec` + `retry`/`delay` +
JUnit) instead of brittle bash; pinning makes them **reproducible**; the `venom-e2e` skill makes new lanes
mechanical; the conversion already caught a hidden behaviour (fn-to-fn 500-carries-422). The `e2e/` suites are
the natural target for an end-to-end demonstration of each declarative feature on real containerd.
**Negative (accepted)**: one more pinned **dev tool** (test-only, Apache-2.0). The lanes still need a container
runtime (colima on macOS) and a Lima VM, so they are **opt-in** — not in the default `just ci` gate (matching
ADR-0052's footprint lane).
**Neutral**: metastore-smoke conversion + CI-gating are deferred; the four-tier taxonomy is unchanged.

## Open questions

- **Convert `scripts/lima-metastore-smoke.sh`** — it asserts metastore restart-recovery (apply a `Config`,
  **restart the daemon in-VM**, read it back). Expressible in Venom (an in-VM `exec` `systemctl restart` + a
  retried `http` re-read) but a different shape; a follow-up PR/ADR.
- **CI-gating the Venom containerd lanes** — needs a containerd-capable CI runner. Lima is **not** macOS-bound
  (it runs on Linux), but the flake currently provisions it only on darwin, and a Linux runner could instead
  drive **native** containerd (no Lima). Which path (cross-platform Lima vs a native-containerd Linux lane) is a
  follow-up decision; the JUnit output already makes either consumable.
- **Venom version bump cadence** — pinned at v1.3.0; bumped deliberately via a flake edit (like `nixpkgs-lima`),
  never floated.

## References

- [OVH Venom](https://github.com/ovh/venom) — Apache-2.0, v1.3.0 (Jan 2026), Go; `http`/`exec`/`ssh` executors,
  `retry`/`delay`, JUnit output. Not in nixpkgs → `buildGoModule`.
- [ADR-0025](0025-testing-strategy-and-e2e-harness.md) (taxonomy + embed harness) ·
  [ADR-0034](0034-end-user-journey-acceptance-e2e.md) (acceptance journey) ·
  [ADR-0052](0052-bench-containerd-cgroup-footprint-lane.md) (containerd lane + `nixpkgs-lima` pin).
- The `venom-e2e` skill (`.claude/skills/venom-e2e/`) — the authoring playbook + the hard-won rules.
- [FEAT-0001](../feat/0001-feat-v1.1.md) — F46.
