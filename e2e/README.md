# e2e — declarative end-to-end suites (Venom spike)

This directory holds a **spike** evaluating [OVH Venom](https://github.com/ovh/venom) (Apache-2.0) as a
declarative replacement for the hand-written `scripts/lima-*-invoke.sh` bash scripts. It is **not yet an
accepted approach** — if the spike proves out, an ADR pins Venom in the flake and converts the lanes.

## What's here

- `kv-counter.venom.yml` — the KV containerd lane (ADR-0069/0076) as a Venom suite: each counter reads
  1→2 with **no read Policy** (binding-as-read-grant), then removing the binding makes the read
  **Forbidden** (fail-closed). The declarative twin of `scripts/lima-kv-invoke.sh`.

## Run it

The suite runs **host-side** against the self-deploying Lima VM's forwarded ports, after the VM is Ready:

```bash
FUNCD_VENOM=1 just lima-example-kv     # boots the VM, then runs Venom instead of the bash invoke
```

Venom is obtained via the flake's pinned Go toolchain (`go run github.com/ovh/venom/cmd/venom@v1.3.0`).
A future ADR would pin it as a `buildGoModule` flake input (Venom is not in nixpkgs).

## Why (the spike's thesis)

- HTTP assertions (`result.bodyjson.count ShouldEqual 2`) read better than `curl | python -c`.
- `retry`/`delay` replace hand-rolled readiness/redeploy polling loops.
- JUnit XML output gives per-lane CI test reporting.
- Apache-2.0 — within the project's license gate.

## Spike verdict (what the runs showed)

Run on the real containerd lane (Lima VM), 3 iterations:

- ✅ **Venom owns the assertion phase.** The two positive testcases — each counter reads 1→2 with no read
  Policy — pass **green** against the VM's forwarded `127.0.0.1` ports. Port-forwarding works; the
  declarative `http` + `result.bodyjson.count` assertions are clearly better than the bash `curl | python`.
  Tooling is clean: `go run …/venom@v1.3.0` via the pinned Go toolchain, Apache-2.0.
- ⚠️ **Stateful mutation + redeploy mid-suite is the rough edge.** The fail-closed NEGATIVE (strip
  `spec.kv`, then assert the read is denied) driven **host-side** is a redeploy race: the running sandbox
  serves the old binding until the new revision is Ready, so the read returns 200 inside any fixed retry
  window. The in-VM bash lane does this robustly because the strip is co-located with the daemon. So the
  mutation step is as awkward in Venom (via `exec`/`ssh` + `limactl shell` nesting) as in bash — Venom's
  value is the assertions, not orchestrating redeploys.

**Recommendation for the ADR**: adopt Venom for the **assertion** phase of each lane; keep
provisioning/mutation in the lane's bash/Lima provisioning (or a settle-gated `ssh` step). The default
`just lima-example-kv` (bash) retains the full positive **+** fail-closed coverage; this suite is the
declarative assertion spike. Pin Venom as a `buildGoModule` flake input (it is not in nixpkgs).
