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

You supply a *type*, never a validator. At `funcdcli push` the build:

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
export type FuncOutput = void;                      // returns nothing → 204
```

A `Json` field inside a record works the same way — the field becomes the empty schema `{}` while
the record stays closed. (Discriminated unions are part of the documented profile, but the current
TS→schema codegen emits `anyOf`, which the push-time profile gate doesn't yet accept — so a
top-level union contract isn't wired end-to-end yet. Stick to records / `Json` / `void` for now.)

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
funcdcli push handler.mjs oci-layout:///tmp/funcd-demo/layout:v1
funcdcli apply -f function.yaml      # no digest — the platform pins it (ADR-0035)
curl -XPOST <data-plane>/function/hello -d '{"name":"funcd"}'
```

`just demo` runs this whole journey end to end.

## Notes

- The typed contract is **path-mapped** to the in-repo shim types (`tsconfig.json` →
  `shim/nodejs/src/types.ts`). When `@funcd/shim-nodejs` is published, this becomes a plain
  `npm i -D @funcd/shim-nodejs` (ADR-0037 open question).
- `import type { Handler }` is erased at build time, so `handler.mjs` carries **no** shim
  dependency — your function bundles only the libraries it actually uses at runtime. The
  generated validators are baked in by the push build, not imported.
