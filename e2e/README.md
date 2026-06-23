# e2e — declarative end-to-end suites (Venom)

This directory holds the declarative e2e suites for the Lima/containerd lanes, written for
[OVH Venom](https://github.com/ovh/venom) (Apache-2.0). Venom is the **invoke phase** of the lanes: the VM
self-deploys (Lima provisioning), then Venom drives the assertions. It replaced the hand-written
`scripts/lima-*-invoke.sh` bash scripts.

## What's here

- `kv-counter.venom.yml` — the KV containerd lane (ADR-0069/0076). Proves two properties declaratively:
  1. **binding-as-read-grant** — each counter (nodejs22 + python314) reads 1→2 with **no read Policy**.
  2. **fail-closed** — applying `examples/js/kv-counter/counter-unbound.yaml` (the same config minus
     `spec.kv`) makes `context.kv.get` **Forbidden** (bound-only, not default-allow).
- `fn-to-fn.venom.yml` — the fn-to-fn link lane (ADR-0064/0058). Proves: front invokes greeter (the link
  is the capability); a missing name makes greeter's contract reject (422) and that **422 propagates** back
  through front; a bad-typed greeter call hits the **contract gate** (422) before the handler; and only the
  linked caller (front) has an invoke socket.
- `metastore.venom.yml` — the metastore durability lane (ADR-0065). The **exec-shaped** lane: not data-plane
  http but a daemon-lifecycle test — start funcd (containerd + file/Badger metastore), apply a `Config`,
  **restart the daemon**, and prove the `Config` is **recovered** from the durable metastore. Every step is an
  in-VM `exec` (the suite starts the daemon itself); the **wait is `retry` on the `exec` steps** (no poll
  loop, no host port-forwarding). Fixtures in `e2e/fixtures/`.

## Run it

```bash
just lima-example-kv     # boots the self-deploying VM, then runs this suite
```

Venom is obtained via the flake's pinned Go toolchain (`go run github.com/ovh/venom/cmd/venom@v1.3.0`); a
follow-up ADR would pin it as a `buildGoModule` flake input (Venom is not in nixpkgs). Venom runs from the
scratch dir so its `venom.log` lands outside the repo; `*.log` is also gitignored.

## Shape (what the spike settled)

- **HTTP assertions** (`result.bodyjson.count ShouldEqual 2`) read far better than `curl | python -c`, and
  **`retry`/`delay`** replace hand-rolled readiness/redeploy polling — so there is **no helper script**: the
  positive testcases are `http` steps; the fail-closed mutation is one inline `exec` line and the *wait* is
  the negative `http` step's `retry`/`delay`.
- **Mutations run IN the VM, not host-side.** The one `exec` step does `limactl shell {{.vm}} -- sudo … funcdctl
  apply -f /opt/kv-counter/counter-unbound.yaml` — co-located with the daemon. An earlier attempt drove the
  apply host-side over the forwarded control-plane port and raced the redeploy (the old sandbox served the
  binding until the new revision was Ready, so the read stayed 200). Co-locating the mutation fixes it; the
  assertion stays host-side over the forwarded data-plane port. So the "mutation is Venom's weak spot" worry
  was a host-side-execution artifact, not a Venom limit — it expresses the full positive **+** negative suite.
- **`{{.vm}}`** is injected by the recipe (`--var`) so no machine-specific path lives in this tracked file;
  the in-VM deploy path (`/opt/kv-counter/…`) is the lane's own convention (see `scripts/lima-kv.yaml`).

A follow-up ADR would pin Venom in the flake and (optionally) convert the other lanes
(`scripts/lima-fn-to-fn-invoke.sh`, …) the same way.
