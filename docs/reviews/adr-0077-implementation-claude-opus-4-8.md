# ADR-0077 implementation review — Declarative e2e via OVH Venom (`claude-opus-4-8`)

- **ADR**: [0077](../adr/0077-declarative-e2e-venom.md) — Declarative e2e for the containerd lanes (OVH Venom, flake-pinned)
- **Phase**: implementation · **Model**: claude-opus-4-8
- **Verdict**: **pass** (DoD 5/5, no Blockers/Majors) · 2026-06-23

## Verification (evidence — run, not eyeballed)

| check | result |
|---|---|
| `nix develop -c command -v venom` | `/nix/store/…-venom-1.3.0/bin/venom` — **pinned binary from the nix store**, not a `go run` shim |
| `nix develop -c venom version` | `Version venom: v1.3.0` (ldflag-injected; buildGoModule otherwise leaves it "snapshot") |
| `git diff flake.lock` | **no change** — Venom is fetched inside the derivation (`fetchFromGitHub`), not a new flake input |
| `just lima-example-kv` (final flake state) | **`final status: PASS`** on real containerd; results path echoed |
| `just lima-example-fn-to-fn` | **`final status: PASS`** on real containerd; results path echoed |
| `go run …venom` occurrences in the run logs | **0** — both recipes use the pinned `venom` |
| identity/abs-path grep (flake.nix, Justfile, ADR, blueprint) | **clean** |

## Scenarios → checks (all pass)

- **venom-pinned** — `command -v venom` resolves into the nix store (not a `go run` shim); `venom version` = `v1.3.0`.
- **lane-asserts-declaratively** — `just lima-example-kv` + `… fn-to-fn` end `final status: PASS` via the Venom suites.
- **no-helper-scripts** — `scripts/` carries no `lima-*-invoke.sh` (the per-lane invoke/mutation helpers are
  retired); the suites + the static `counter-unbound.yaml` fixture are the whole test. (`scripts/` legitimately
  still holds the `lima-*.yaml` provisioning + unrelated tooling — as the ADR's corrected wording states.)
- **mutation-in-vm** — the kv negative applies `counter-unbound.yaml` IN the VM (`limactl shell` exec) and the
  retried `http` assert sees the Forbidden — green, no host-side redeploy race.
- **results-discoverable** — `test_results_<suite>.venom.xml` (JUnit) + `venom.log` land in the scratch dir and
  the recipe **echoes** `venom results: …/test_results_*.venom.xml` at the end of each run.

## DoD (ADR Review checklist) — 5/5

1. ✅ `flake.nix` pins Venom v1.3.0 via `buildGoModule` (pinned `src` + `vendorHash`, version ldflag), added to
   `devShell.packages` for all `supportedSystems`; `venom version` = v1.3.0.
2. ✅ Both lane recipes invoke the **pinned** `venom` (no `go run …@version`); each echoes the results path.
3. ✅ Both lanes `final status: PASS` on real containerd; `e2e/` holds the suites + README; no
   `scripts/lima-*-invoke.sh` remain.
4. ✅ JUnit XML + `venom.log` in the scratch dir; `*.log` gitignored; no identity/path leak; Venom is the only
   new (test-only, Apache-2.0) dep — no entry in the funcd `go.mod`.
5. ✅ The four-tier taxonomy (ADR-0025) + `InMemory()` embed e2e + the `internal/<port>contract` suites are
   untouched.

## ✅ Verified correct (keep)

- **Truthful pin** — the `-X github.com/ovh/venom.Version=v1.3.0` ldflag makes `venom version` report the pinned
  version (buildGoModule doesn't replicate the upstream release ldflag); `-s -w` strips debug like a release.
  The version-string fix is functionally inert (same `src`+`vendorHash`), so the both-lanes-green evidence holds.
- **Reproducible, no `flake.lock` churn** — pinning via `fetchFromGitHub` inside the derivation keeps Venom off
  the flake inputs (deliberate bumps via the version edit, like `nixpkgs-lima`).
- **Test-only** — Venom is a dev-shell binary the recipes shell out to; nothing links it into the single-binary
  product, so the pure-Go-binary / Apache-2.0 constraints are unaffected.

## Findings
None (Blocker/Major/Minor). The implementation ratifies the already-landed suites/skill/retired-scripts and
adds exactly the remaining work (the flake pin + the recipe switch off `go run` + the echo line).

## Recommendation
**pass** — `Accepted → Implemented`. The containerd example lanes now assert declaratively on a flake-pinned,
reproducible Venom; the `venom-e2e` skill makes the next lane mechanical. metastore-smoke conversion +
CI-gating remain the ADR's noted follow-ups.
