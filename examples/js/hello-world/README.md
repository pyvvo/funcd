# hello-world — a funcd function in TypeScript

The minimal funcd function, authored in TypeScript against the shim's typed contract. It's
the reference for how a function author writes, types, tests, and bundles a handler.

## The handler

[`src/handler.ts`](src/handler.ts) exports `handle`, typed as `Handler<In, Out>` imported
from `@funcd/shim-nodejs` — so `context`, the CloudEvent `event`, and the return value are
checked at compile time against the same contract the shim enforces at runtime (ADR-0037).

```ts
import type { Handler } from '@funcd/shim-nodejs';

export const handle: Handler<{ hello?: string }, { echoed: unknown; by: string }> =
  (context, event) => {
    context.log('handling event:', JSON.stringify(event));
    return { echoed: event, by: 'funcd' };
  };
```

## Author workflow

```bash
npm install
npm run typecheck   # tsc --noEmit — proves the handler conforms to Handler
npm test            # node --test — exercises the handler's behavior
npm run build       # esbuild → handler.mjs (the single self-contained artifact to deploy)
```

`npm run build` bundles `src/handler.ts` (plus any npm libraries you import) into one
`handler.mjs` — the same way the shim itself is bundled. That file is the OCI artifact:

```bash
funcdcli push handler.mjs oci-layout:///tmp/funcd-demo/layout:v1
funcdcli apply -f function.yaml      # no digest — the platform pins it (ADR-0035)
curl -XPOST <data-plane>/function/hello -d '{"hello":"funcd"}'
```

`just demo` runs this whole journey end to end.

## Notes

- The typed contract is **path-mapped** to the in-repo shim types (`tsconfig.json` →
  `shim/nodejs/src/types.ts`). When `@funcd/shim-nodejs` is published, this becomes a plain
  `npm i -D @funcd/shim-nodejs` (ADR-0037 open question).
- `import type { Handler }` is erased at build time, so `handler.mjs` carries **no** shim
  dependency — your function bundles only the libraries it actually uses at runtime.
