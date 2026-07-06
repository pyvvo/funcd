// funcd Node runtime shim (TypeScript + Hono over node:http). Loads the function artifact,
// resolves the handle(context, event) export (the authoritative materialization shape-gate),
// and serves the runtime-shim HTTP contract:
//   POST /                 CloudEvent -> [optional input contract] -> handler -> [optional output contract] -> response
//                          (object->200 JSON, none/void->204, throw/output-mismatch->500, input-mismatch->422)
//   GET  /health/readiness 200 once the handler resolved
//   GET  /health/liveness  200 while up
// If the bundle carries precompiled validators (ADR-0058, generated at push from the author's
// FuncInput/FuncOutput types), event.data is validated before the handler runs (mismatch -> 422)
// and the handler's result after (mismatch -> 500). The validators are eval-free (compiled at
// push); the shim runs no schema compiler. A `void`/`None` output contract -> 204 (non-empty -> 500).
// Env: FUNCD_ARTIFACT (local path), FUNCD_HANDLER (export, default "handle"); FUNCD_PORT
// (container: bind 0.0.0.0:PORT) else FUNCD_PORTFILE (process: bind 127.0.0.1:0 + write the port).
// Bundled (Hono inlined) to shim.mjs, so the runtime stays a single self-contained file.
import { serve } from '@hono/node-server';
import { Hono } from 'hono';
import { realpathSync, writeFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

import { resolveHandler, resolveValidators } from './runtime.ts';
import type { CloudEvent, FunctionContext, Handler, Validator } from './types.ts';
import { makeInvoke } from './invoke.ts';
import { makeKV } from './kv.ts';
import { installConsoleCapture, openChannel, type Sink } from './funclog.ts';
import { startSpan } from './tracespan.ts';

export type { CloudEvent, FunctionContext, Handler, Json, Validator } from './types.ts';
export { resolveHandler, resolveValidators } from './runtime.ts';

/** createApp builds the shim's HTTP app (the runtime contract) around a handler. The optional
 *  precompiled validators (ADR-0058) gate the I/O: `input` validates event.data BEFORE the handler
 *  (mismatch -> 422, handler never called); `output` validates the result AFTER (mismatch -> 500, a
 *  bad-shaped result never goes out as 200). A `void`/`None` output contract is just an output
 *  validator that accepts only an empty result, so empty -> 204 and a non-empty return -> 500. */
export function createApp(
  handler: Handler,
  validators: { input?: Validator; output?: Validator } = {},
  trace: { sink?: Sink | null; fnName?: string } = {},
): Hono {
  const app = new Hono();
  const ctx: FunctionContext = { log: (...args) => console.log(...args), invoke: makeInvoke(), kv: makeKV() };
  const traceSink = trace.sink ?? null; // ADR-0101: per-invocation span emitter (null ⇒ context only)
  const fnName = trace.fnName ?? 'invoke';

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
    if (validators.input) {
      const errors = validators.input(event.data);
      if (errors.length > 0) {
        // ADR-0101: an input-mismatch short-circuits BEFORE the handler → no invocation, no span.
        return c.json({ error: 'event data does not match the input contract', details: errors }, 422);
      }
    }
    // ADR-0101: a real invocation begins → open its SERVER span (adopts traceparent or mints a root);
    // the handler runs inside the span's context so its logs correlate.
    const span = startSpan(traceSink, fnName, c.req.header('traceparent'));
    try {
      const result = await span.run(() => handler(ctx, event));
      if (validators.output) {
        // normalize an absent return to null so a `void` validator (accepts empty) and a typed
        // validator (rejects empty) both see a concrete value.
        const errors = validators.output(result === undefined ? null : result);
        if (errors.length > 0) {
          span.end('ERROR', 'handler result does not match the output contract');
          return c.json({ error: 'handler result does not match the output contract', details: errors }, 500);
        }
      }
      span.end('OK');
      if (result === undefined || result === null) return c.body(null, 204);
      return c.json(result as Record<string, unknown>);
    } catch (err) {
      span.end('ERROR', String(err instanceof Error ? err.message : err));
      return c.json({ error: String(err instanceof Error ? err.message : err) }, 500);
    }
  });

  return app;
}

/** main loads the artifact, resolves the handler, and serves the contract. */
async function main(): Promise<void> {
  // ADR-0081 Path B + ADR-0101 traces: open the telemetry channel ONCE and share it between console
  // capture and the per-invocation span (a single channel per process — a second UDS connect would
  // double-capture). No channel env ⇒ null ⇒ both are no-ops (console stays Path A). The shim's OWN
  // operational lines go to process.stderr directly — never through the patched console.
  const channel = openChannel(process.env);
  installConsoleCapture(process.env, channel);

  const artifact = process.env.FUNCD_ARTIFACT;
  const handlerName = process.env.FUNCD_HANDLER ?? 'handle';
  const fixedPort = process.env.FUNCD_PORT ? Number(process.env.FUNCD_PORT) : 0;
  const portFile = process.env.FUNCD_PORTFILE;

  if (!artifact) {
    process.stderr.write('funcd-shim: FUNCD_ARTIFACT is required\n');
    process.exit(2);
  }

  let handler: Handler;
  let validators: { input?: Validator; output?: Validator };
  try {
    const mod = (await import(pathToFileURL(artifact).href)) as Record<string, unknown>;
    handler = resolveHandler(mod, handlerName);
    validators = resolveValidators(mod);
  } catch (err) {
    process.stderr.write(`funcd-shim: shape error: ${err instanceof Error ? err.message : err}\n`);
    process.exit(3); // materialization shape-gate failure (ADR-0030)
  }

  const hostname = fixedPort > 0 ? '0.0.0.0' : '127.0.0.1';
  const fnName = process.env.FUNCD_FUNCTION ?? 'invoke';
  const appTrace = { sink: channel, fnName };
  serve({ fetch: createApp(handler, validators, appTrace).fetch, hostname, port: fixedPort }, (info) => {
    if (portFile) writeFileSync(portFile, String(info.port));
    process.stderr.write(`funcd-shim: listening on ${hostname}:${info.port}\n`);
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
