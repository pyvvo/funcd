// funcd Node runtime shim (TypeScript + Hono over node:http). Loads the function artifact,
// resolves the handle(context, event) export (the authoritative materialization shape-gate),
// and serves the runtime-shim HTTP contract:
//   POST /                 CloudEvent -> [optional event-data contract] -> handler -> response
//                          (object->200 JSON, none->204, throw->500, contract mismatch->422)
//   GET  /health/readiness 200 once the handler resolved
//   GET  /health/liveness  200 while up
// If the artifact exports `eventSchema` (a JTD schema, RFC 8927), event.data is validated
// against it before the handler runs — the engine ships in the shim, the contract in the artifact.
// Env: FUNCD_ARTIFACT (local path), FUNCD_HANDLER (export, default "handle"); FUNCD_PORT
// (container: bind 0.0.0.0:PORT) else FUNCD_PORTFILE (process: bind 127.0.0.1:0 + write the port).
// Bundled (Hono inlined) to shim.mjs, so the runtime stays a single self-contained file.
import { serve } from '@hono/node-server';
import { Hono } from 'hono';
import { type Schema } from 'jtd';
import { realpathSync, writeFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

import { resolveHandler, resolveSchema, validate } from './runtime.ts';
import type { CloudEvent, FunctionContext, Handler } from './types.ts';

export type { CloudEvent, FunctionContext, Handler } from './types.ts';
export { resolveHandler, resolveSchema } from './runtime.ts';
export type { EventSchema } from './runtime.ts';

/** createApp builds the shim's HTTP app (the runtime contract) around a handler. When a
 *  `schema` is given, the event's `data` is validated against it before the handler runs;
 *  a mismatch returns 422 with the JTD errors, so a bad-shaped event never reaches user code. */
export function createApp(handler: Handler, schema?: Schema): Hono {
  const app = new Hono();
  const ctx: FunctionContext = { log: (...args) => console.log(...args) };

  app.get('/health/liveness', (c) => c.text('ok'));
  app.get('/health/readiness', (c) => c.text('ready'));

  app.post('/', async (c) => {
    let event: CloudEvent;
    try {
      const text = await c.req.text();
      event = (text ? JSON.parse(text) : {}) as CloudEvent;
    } catch {
      return c.text('invalid CloudEvent JSON', 400);
    }
    if (schema !== undefined) {
      const errors = validate(schema, event.data);
      if (errors.length > 0) {
        return c.json({ error: 'event data does not match the contract', details: errors }, 422);
      }
    }
    try {
      const result = await handler(ctx, event);
      if (result === undefined || result === null) return c.body(null, 204);
      return c.json(result as Record<string, unknown>);
    } catch (err) {
      return c.json({ error: String(err instanceof Error ? err.message : err) }, 500);
    }
  });

  return app;
}

/** main loads the artifact, resolves the handler, and serves the contract. */
async function main(): Promise<void> {
  const artifact = process.env.FUNCD_ARTIFACT;
  const handlerName = process.env.FUNCD_HANDLER ?? 'handle';
  const fixedPort = process.env.FUNCD_PORT ? Number(process.env.FUNCD_PORT) : 0;
  const portFile = process.env.FUNCD_PORTFILE;

  if (!artifact) {
    console.error('funcd-shim: FUNCD_ARTIFACT is required');
    process.exit(2);
  }

  let handler: Handler;
  let schema: Schema | undefined;
  try {
    const mod = (await import(pathToFileURL(artifact).href)) as Record<string, unknown>;
    handler = resolveHandler(mod, handlerName);
    schema = resolveSchema(mod);
  } catch (err) {
    console.error(`funcd-shim: shape error: ${err instanceof Error ? err.message : err}`);
    process.exit(3); // materialization shape-gate failure (ADR-0030)
  }

  const hostname = fixedPort > 0 ? '0.0.0.0' : '127.0.0.1';
  serve({ fetch: createApp(handler, schema).fetch, hostname, port: fixedPort }, (info) => {
    if (portFile) writeFileSync(portFile, String(info.port));
    console.log(`funcd-shim: listening on ${hostname}:${info.port}`);
  });
}

// Run main only when executed as the entrypoint (not when imported by tests).
// realpathSync resolves symlinks (e.g. macOS /var -> /private/var) so argv[1] matches
// import.meta.url, which Node reports in realpath form — otherwise the guard never fires
// when the shim is launched by absolute path from a symlinked dir (the daemon's data dir).
const entry = process.argv[1] ? pathToFileURL(realpathSync(process.argv[1])).href : '';
if (import.meta.url === entry) {
  void main();
}
