# hello-world — a funcd function in TypeScript

The minimal funcd function, authored in TypeScript against the shim's typed contract. It's
the reference for how a function author writes, types, tests, bundles, and **types the I/O
contract** of a handler.

## The handler + its I/O contract

[`src/handler.ts`](src/handler.ts) exports `handle`, typed as `Handler<FuncInput, FuncOutput>`
imported from `@funcd/shim-nodejs` — so `context`, the CloudEvent `event`, and the return value
are checked at compile time against the same contract the shim enforces at runtime (ADR-0037).

The two exported interfaces **`FuncInput`** and **`FuncOutput`** *are* the I/O contract
(ADR-0058). You write them as ordinary TypeScript types; the push build does the rest:

```ts
import type { Handler } from '@funcd/shim-nodejs';

export interface FuncInput {
  name: string;
  excited?: boolean;            // optional → an optional property in the schema
}

export interface FuncOutput {
  greeting: string;
}

export const handle: Handler<FuncInput, FuncOutput> = (context, event) => {
  const { name, excited } = event.data!;     // already validated → present & well-shaped
  return { greeting: excited ? `Hello, ${name}!` : `Hello, ${name}.` };
};
```

### How the contract is enforced (ADR-0058 / ADR-0060)

You supply a *type*, never a validator. At `funcdctl push` the build:

1. generates a closed **JSON Schema** from `FuncInput` / `FuncOutput`
   (`ts-json-schema-generator`, `additionalProperties: false`);
2. gates it against the supported **profile** (the bounded subset — closed records, scalars,
   enums, arrays, string-keyed maps, discriminated unions, `Json`; no open records, no
   recursion). An out-of-profile type fails the push;
3. compiles a **precompiled, eval-free validator** (AJV-standalone) and bakes it into the bundle.

At runtime the shim runs those validators around your handler:

| Situation | Result |
|---|---|
| `event.data` doesn't match `FuncInput` | **422** — `handle` is never called |
| return value doesn't match `FuncOutput` | **500** — the bug is server-side |
| handler returns nothing (`void`/`undefined`) | **204** |
| no `FuncInput`/`FuncOutput` declared | unvalidated (the pre-contract V1 behavior) |

So inside `handle` the input is already valid — narrow with `!` (the envelope's `data?:` is
optional only so a contract-less function may omit it).

### Other shapes in the profile

```ts
export type FuncInput = Json;                       // accept any JSON value (empty schema {})
export type FuncOutput =                            // discriminated union — `kind` is the tag
  | { kind: 'accepted'; id: string }
  | { kind: 'rejected'; reason: string };
export type FuncOutput = void;                      // returns nothing → 204
```

A `Json` field inside a record works the same way — the field becomes the empty schema `{}` while
the record stays closed. A discriminated union (each branch a closed record sharing a required
literal tag, here `kind`) is generated as a tagged `oneOf` + `discriminator` and validated
end-to-end — the build converts the `anyOf` ts-json-schema-generator emits into the form the
profile gate accepts.

## Author workflow

```bash
npm install
npm run typecheck   # tsc --noEmit — proves the handler conforms to Handler<FuncInput, FuncOutput>
npm test            # node --test — exercises the handler's behavior
npm run build       # esbuild → handler.mjs (the single self-contained artifact to deploy)
```

`npm run build` bundles `src/handler.ts` (plus any npm libraries you import) into one
`handler.mjs` — the same way the shim itself is bundled. That file is the OCI artifact:

```bash
funcd --config ../../funcdconfig.yaml &           # start the daemon (zero-infra dev config, ADR-0061)
funcdctl push handler.mjs oci-layout:///tmp/funcd-demo/layout:v1
funcdctl apply -f function.yaml      # no digest — the platform pins it (ADR-0035)
curl -XPOST http://127.0.0.1:8081/function/hello -d '{"name":"funcd"}'
```

[`examples/funcdconfig.yaml`](../../funcdconfig.yaml) is the shared daemon config (in-memory
substrate + process runtime + localhost addresses); it's optional — `funcd` runs with all defaults
if omitted. `just demo` runs this whole journey end to end.

## Notes

- The typed contract is **path-mapped** to the in-repo shim types (`tsconfig.json` →
  `shim/nodejs/src/types.ts`). When `@funcd/shim-nodejs` is published, this becomes a plain
  `npm i -D @funcd/shim-nodejs` (ADR-0037 open question).
- `import type { Handler }` is erased at build time, so `handler.mjs` carries **no** shim
  dependency — your function bundles only the libraries it actually uses at runtime. The
  generated validators are baked in by the push build, not imported.
