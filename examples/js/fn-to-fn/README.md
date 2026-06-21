# fn-to-fn links — synchronous `context.invoke` (ADR-0064)

A **TypeScript** example with **two functions in one project**: **`greeter`** (callee) and **`front`**
(caller). `front` declares a **link** to `greeter` and calls it synchronously with
`context.invoke("greeter", …)`. The platform brokers the call over the per-sandbox **worker-node local
API** (HTTP-over-UDS) — `front` never knows `greeter`'s address, and the **link is the capability**
(no link ⇒ `invoke` fails closed, default-deny).

```
client ──HTTP──▶ front ──context.invoke("greeter")──▶ [worker-node local API, UDS]
                                                          │ resolve alias→greeter (the link = grant)
                                                          ▼
                                                       data plane ──▶ greeter ──▶ "Hello, …!"
                                                          │
            { via:"front", greeting:"Hello, …!" } ◀───────┘
```

## Layout

```
src/greeter.ts   # the callee  — Handler<GreeterInput, GreeterOutput>
src/front.ts     # the caller  — calls context.invoke<…>("greeter", …); type-only import of greeter's contract
test/handlers.test.ts
package.json     # build = esbuild src/greeter.ts src/front.ts --bundle … --outdir=. (TWO entry points → TWO .mjs)
tsconfig.json    # @funcd/shim-nodejs → ../../../shim/nodejs/src/types.ts (so context.invoke is typed)
```

Authored in TypeScript against the `@funcd/shim-nodejs` typed contract; one `esbuild` build bundles
**both** entry points to `greeter.mjs` + `front.mjs` (the deployed artifacts, git-ignored).

```bash
npm install
npm run typecheck   # both handlers type-checked against the same contract the platform enforces
npm test            # unit tests (front's invoke is mocked)
npm run build       # → greeter.mjs + front.mjs
```

## The link (on `front`'s Function resource)

```yaml
apiVersion: funcd.io/v1alpha1
kind: Function
metadata: { name: front, namespace: default, resourceGroup: rg1 }
spec:
  runtime: nodejs22
  handler: handle
  artifact: { uri: "file://…/front.mjs" }
  replicas: 1
  links:
    - alias: greeter   # the local name front's handler passes to context.invoke
      target: greeter  # the Function it resolves to (same namespace, latest-Ready revision)
```

`greeter` is an ordinary Function with no links — it doesn't know it's being called.

## Deploy it (push → apply), the kubectl way

Build the artifacts, push them to an OCI store (a registry, or a **local layout** — no server), then
`apply` the manifests; the daemon pulls the artifacts by digest and runs them:

```bash
npm run build                                              # → greeter.mjs + front.mjs
funcdcli push greeter.mjs oci-layout://./registry:greeter  # prints <ref>@<digest>
funcdcli push front.mjs   oci-layout://./registry:front
funcdcli apply -f greeter.yaml                             # the daemon pulls + runs
funcdcli apply -f front.yaml
curl -sX POST "$DATA_PLANE/function/front" -d '{"data":{"name":"funcd"}}'
# → {"via":"front","greeting":"Hello, funcd!"}
```

`funcdcli apply` accepts **YAML or JSON**; the manifests carry a plain ref (tag), and the daemon
resolves it to a digest at apply time — so these `.yaml` files are the deployable unit, re-appliable
like any `kubectl apply -f`.

**On Lima (containerd lane):** push into `~/.cache/funcd-lima/registry`, which the VM mounts read-only
at `/mnt/funcd-deps/registry` (the path the manifests' `artifact.uri` already references); then
`funcdcli apply` against the in-VM daemon. Same flow, real sandboxes — the per-function invoke socket
is bind-mounted into each container at `/run/funcd/invoke.sock`.

## Launched as a real cross-process test

This example is **executed** end-to-end (it is built from these very sources and run) by
`pkg/funcd/invoke_e2e_test.go`:

- `TestScenarioHandlerInvokesLinkedFunction` — the **real deploy path**: pushes both handlers to an OCI
  layout, applies `greeter.yaml` + `front.yaml` (the daemon pulls the artifacts by digest), POSTs to
  `front` over the data plane, and asserts `greeter`'s reply (`"Hello, funcd!"`) flowed back through
  `front` — the full broker round-trip (shim → `FUNCD_INVOKE_SOCKET` → local API → resolver → invoker →
  data plane → `greeter` → back).
- `TestScenarioUnlinkedAliasDeniedE2E` — a function calling an **undeclared** alias fails closed.

```bash
nix develop -c go test ./pkg/funcd/ -run TestScenarioHandlerInvokesLinkedFunction -v   # needs node on PATH
```

The **containerd lane** (Lima) is wired the same way — the per-function socket is bind-mounted into
the sandbox at `/run/funcd/invoke.sock`; its live run belongs to the Linux/containerd integration lane.
