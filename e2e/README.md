# e2e — declarative end-to-end suites (Venom)

This directory holds the declarative e2e suites for the Lima/containerd lanes, written for
[OVH Venom](https://github.com/ovh/venom) (Apache-2.0). Venom is the **invoke phase** of the lanes: the VM
self-deploys (Lima provisioning), then Venom drives the assertions. It replaced the hand-written
`scripts/lima-*-invoke.sh` bash scripts.

## What's here

`scripts/lanes.yaml` declares each lane: the module or directory its files come from, what it stages into the VM
and its suite. Every lane runs in one shared VM (ADR-0230).

| Suite | Lane | Proves |
|---|---|---|
| `env-echo.venom.yml` | `env-echo` | Config and Secret values reach the handler's environment (ADR-0093, ADR-0057), and the Route and edge checks of FEAT-0006 |
| `fn-to-fn.venom.yml` | `fn-to-fn` | Function links: invoke and contract propagation (ADR-0064, ADR-0058) |
| `http-api.venom.yml` | `http-api` | A Function that serves HTTP behind a Route (ADR-0243) |
| `duckdb.venom.yml` | `duckdb` | The Lake add-on: deploy, Ready and a live SQL round-trip (ADR-0086 to ADR-0091), its catalog in the catalog store (ADR-0293), a prefix at its quota (ADR-0292) and a worker's default limits (ADR-0291) |
| `s3.venom.yml` | `s3` | The S3 frontend round-trip (ADR-0080, ADR-0085) |
| `kv-counter.venom.yml` | `kv` | Binding as read grant and fail-closed (ADR-0076), and a store at its quota (ADR-0292) |
| `workflow.venom.yml` | `workflow` | The workflow engine: runs, chaining, fan-out, when and join, pause, resume and cancel (ADR-0094, ADR-0096) |
| `funclog.venom.yml` | `funclog` | Function log capture, reads and traces (ADR-0081, ADR-0084, ADR-0101) |
| `egress.venom.yml` | `egress` | Egress enforcement (ADR-0117) |
| `sql.venom.yml` | `sql` | SQL databases (ADR-0232 to ADR-0236), and a database at its quota (ADR-0292) |
| `sql-examples.venom.yml` | `sql-examples` | The SQL examples (FEAT-0011) |
| `volumes.venom.yml` | `volumes` | Function volumes, local and emptyDir (ADR-0262, ADR-0266, ADR-0268) |
| `app-todo.venom.yml` | `app-todo` | The todo App of [pyvvo/funcd-functions](https://github.com/pyvvo/funcd-functions) from its pushed template, and a disaster-recovery drill; every file it applies comes from funcd-functions |
| `metastore.venom.yml` | `just lima-example-metastore` | A ConfigMap survives a daemon restart (ADR-0065); the suite starts the daemon itself, so every step is an in-VM `exec` |

The kv lane, as an example, proves two properties: each counter (nodejs22 and python314) reads 1 then 2 with no read
Policy, because its `spec.bindings.kv` entry grants the read; and applying `fixtures/counter-unbound.yaml` from
`examples/kv-counter` of pyvvo/funcd-typescript (the same config without `spec.bindings.kv`) makes
`context.kv.get` Forbidden.

## Run it

```bash
just lima-example kv
```

`just lima-example <name>` runs one lane; `just lima-example-all` runs every lane except `app-todo`, which
`scripts/lanes.yaml` marks optional. Venom is the flake-pinned `venom` binary (ADR-0077, v1.3.0). It runs from
the scratch directory, so its `venom.log` lands outside the repo; `*.log` is also gitignored.

## Shape (what the spike settled)

- **HTTP assertions** (`result.bodyjson.count ShouldEqual 2`) read far better than `curl | python -c`, and
  **`retry`/`delay`** replace hand-rolled readiness/redeploy polling — so there is **no helper script**: the
  positive testcases are `http` steps; a mutation is one inline `exec` line and the *wait* is the next step's
  `retry`/`delay`.
- **Mutations run IN the VM, not host-side.** The kv lane's `exec` step runs `funcdctl apply -f
  /opt/lane/fixtures/counter-unbound.yaml` through `limactl shell`, co-located with the daemon. An earlier attempt
  drove the apply host-side over the forwarded control-plane port and raced the redeploy (the old sandbox served the
  binding until the new revision was Ready, so the read stayed 200). The assertion stays host-side over the
  forwarded data-plane port.
- **`{{.vm}}`** is injected by the recipe (`--var`) so no machine-specific path lives in a tracked suite; the
  in-VM paths under `/opt/lane/` are the lanes' own convention.
