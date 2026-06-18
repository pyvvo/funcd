import type { Handler } from '@funcd/shim-nodejs';

/** The incoming event payload this function expects (the CloudEvent `data`). */
interface Hello {
  hello?: string;
}

/** What the function returns — becomes the HTTP 200 JSON body. */
interface Echoed {
  echoed: unknown;
  by: string;
}

/**
 * The event-data contract (JSON Type Definition, RFC 8927). The bundler embeds it into
 * `handler.mjs`; the shim validates `event.data` against it before `handle` runs, so a
 * bad-shaped event returns 422 and never reaches this code. One neutral schema reusable
 * across runtimes (JS today, Python/Go/Rust later) — and `jtd-codegen` can generate `Hello`
 * from it, making schema and type one source of truth.
 */
export const eventSchema = {
  optionalProperties: { hello: { type: 'string' } },
};

/**
 * hello-world funcd function. Typed against the shim's `Handler` contract, so `context`,
 * the CloudEvent `event`, and the return type are checked at compile time
 * (`npm run typecheck`). `npm run build` bundles this to `handler.mjs` — the artifact
 * `funcdcli push` ships. The export name (`handle`) is what `FUNCD_HANDLER` resolves.
 */
export const handle: Handler<Hello, Echoed> = (context, event) => {
  context.log('handling event:', JSON.stringify(event.data));
  return { echoed: event.data, by: 'funcd' };
};
