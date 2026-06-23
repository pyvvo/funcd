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

Run on the real containerd lane (Lima VM), 4 iterations:

- ✅ **Venom owns the assertion phase.** The two positive testcases — each counter reads 1→2 with no read
  Policy — pass **green** against the VM's forwarded `127.0.0.1` ports. Port-forwarding works; the
  declarative `http` + `result.bodyjson.count` assertions are clearly better than the bash `curl | python`.
  Tooling is clean: `go run …/venom@v1.3.0` via the pinned Go toolchain, Apache-2.0.
- ✅ **Venom can own the fail-closed NEGATIVE too — with the mutation done IN the VM.** The first attempt
  drove the `spec.kv` strip **host-side** (`funcdcli` over the forwarded port) and raced the redeploy: the
  running sandbox served the old binding until the new revision was Ready, so the read stayed 200 inside the
  retry window. The fix was **not** to drop the negative — it was to do the *mutation* co-located with the
  daemon: a Venom `exec` step runs `limactl shell {{.vm}} -- sudo bash -s < scripts/lima-kv-strip.sh` (the
  HYBRID — mutate in-VM, assert host-side). With that, the negative is **green**: removing the binding makes
  the read return funcd's forbidden problem+json (`result.body ShouldContainSubstring forbidden`).

  So the earlier "mutation is Venom's rough edge" framing was wrong: it was a *host-side execution* artifact,
  not a Venom limit. Venom expresses the full positive **+** negative suite; the mutation just has to run
  where the bash version runs it — next to the control plane.

**Recommendation for the ADR**: adopt Venom for the lane's assertions **and** its mutation steps (the latter
via an `exec`/`ssh` step that runs co-located in the VM, never host-side over a forwarded port). `FUNCD_VENOM=1
just lima-example-kv` now runs the full positive + fail-closed suite green; the bash invoke remains the
default until the ADR decides whether to flip it. Pin Venom as a `buildGoModule` flake input (not in nixpkgs).
