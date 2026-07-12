// funcd pooled Node runtime shim (ADR-0044): a multi-tenant host that runs many handlers of ONE
// namespace in one Node process — amortizing the runtime baseline — each handler in its own
// worker_threads.Worker (a separate V8 isolate: own heap + event loop + crash isolation) with a
// per-handler memory quota (resourceLimits). It routes POST /function/<name> to the right worker.
//
// Trust: worker_threads share the process address space → a FAULT/RESOURCE boundary, not a
// SECURITY one. A pool hosts a single namespace only (the blueprint's tenancy boundary); the
// resource group is the placement grouping within it. This shim trusts its single-namespace
// manifest — boundary enforcement is the placement follow-up ADR's job.
//
// Bundled (Hono inlined) to pool.mjs. Env: FUNCD_POOL_MANIFEST (JSON [{name,artifact,handler?}]),
// FUNCD_PORT | FUNCD_PORTFILE, FUNCD_POOL_MAX_OLD_MB (64), FUNCD_POOL_MAX_YOUNG_MB (16).
import { serve } from '@hono/node-server';
import { Hono } from 'hono';
import { readFileSync, realpathSync, writeFileSync } from 'node:fs';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { isMainThread, parentPort, Worker, workerData } from 'node:worker_threads';

import { resolveHandler, resolveValidators } from './runtime.ts';
import { ContractError, loadFromPath } from './contract.ts';
import type { CloudEvent, FunctionContext, Handler, Validator } from './types.ts';
import { makeInvoke } from './invoke.ts';
import { makeKV } from './kv.ts';
import { installConsoleCapture, openChannel } from './funclog.ts';
import { startSpan, parseLinks } from './tracespan.ts';

// --- the wire between host and worker ---
interface WorkerSpec {
  name: string;
  artifact: string; // absolute local path, resolved like the single shim's FUNCD_ARTIFACT
  handler?: string; // export name, default "handle"
  contract?: string; // ADR-0123: delivered contract-blob path; the worker compiles its validator from it
}
interface Req {
  id: number;
  event: CloudEvent;
  traceparent?: string; // ADR-0101: the host forwards the incoming W3C header so the worker's span adopts it
  spanId?: string; // ADR-0105: the engine-provided span-id the worker's span uses (X-Funcd-Span-Id)
  links?: string[]; // ADR-0105: fan-in edges (X-Funcd-Span-Links)
}
interface Res {
  id: number;
  result?: unknown;
  none?: boolean; // handler returned undefined/null → 204
  status?: number; // 422 (contract) | 500 (throw)
  error?: string;
  details?: unknown;
}

const maxOldMB = Number(process.env.FUNCD_POOL_MAX_OLD_MB ?? 64);
const maxYoungMB = Number(process.env.FUNCD_POOL_MAX_YOUNG_MB ?? 16);
const requestTimeoutMs = 30_000;

// =====================================================================================
// Worker side: load one artifact, validate + run its handler on each request message.
// =====================================================================================
async function workerMain(): Promise<void> {
  const spec = workerData as WorkerSpec;
  const port = parentPort;
  if (!port) return;

  // ADR-0081 Path B + ADR-0101 traces: open the worker's telemetry channel ONCE and share it between
  // console capture and the per-invocation span (a single channel per worker). No channel env ⇒ null
  // ⇒ both no-op. The pool's OWN operational lines go to process.stderr, never the patched console.
  const channel = openChannel(process.env);
  installConsoleCapture(process.env, channel);

  // ADR-0123: compile the delivered contract AHEAD of the handler import (the m3 reorder). A
  // set-but-broken contract path fails the worker closed → the host fails pool readiness (exit 3).
  let delivered: { input?: Validator; output?: Validator } | null;
  try {
    delivered = spec.contract ? loadFromPath(spec.contract) : null;
  } catch (err) {
    process.stderr.write(`funcd-pool[${spec.name}]: contract error: ${err instanceof ContractError ? err.message : err}\n`);
    process.exit(3);
  }

  let handler: Handler;
  let validators: { input?: Validator; output?: Validator };
  try {
    const mod = (await import(pathToFileURL(spec.artifact).href)) as Record<string, unknown>;
    handler = resolveHandler(mod, spec.handler ?? 'handle');
    validators = delivered ?? resolveValidators(mod);
  } catch (err) {
    process.stderr.write(`funcd-pool[${spec.name}]: shape error: ${err instanceof Error ? err.message : err}\n`);
    process.exit(3); // boot shape error → host fails pool readiness (the materialization shape-gate)
  }
  const ctx: FunctionContext = { log: (...args) => console.log(`[${spec.name}]`, ...args), invoke: makeInvoke(), kv: makeKV() };

  port.on('message', (req: Req) => {
    void (async () => {
      const event = req.event ?? ({} as CloudEvent);
      if (validators.input) {
        const errors = validators.input(event.data);
        if (errors.length > 0) {
          // ADR-0101: input-mismatch short-circuits before the handler → no invocation, no span.
          port.postMessage({ id: req.id, status: 422, error: 'event data does not match the input contract', details: errors });
          return;
        }
      }
      // ADR-0101: a real invocation → its SERVER span (adopts req.traceparent or mints a root),
      // emitted on the worker's channel; the handler runs inside the span's context so logs correlate.
      const span = startSpan(channel, spec.name, req.traceparent, req.spanId, req.links ?? []);
      try {
        const result = await span.run(() => handler(ctx, event));
        if (validators.output) {
          const errors = validators.output(result === undefined ? null : result);
          if (errors.length > 0) {
            span.end('ERROR', 'handler result does not match the output contract');
            port.postMessage({ id: req.id, status: 500, error: 'handler result does not match the output contract', details: errors });
            return;
          }
        }
        span.end('OK');
        if (result === undefined || result === null) {
          port.postMessage({ id: req.id, none: true });
        } else {
          port.postMessage({ id: req.id, result });
        }
      } catch (err) {
        span.end('ERROR', String(err instanceof Error ? err.message : err));
        port.postMessage({ id: req.id, status: 500, error: String(err instanceof Error ? err.message : err) });
      }
    })();
  });
  port.postMessage({ ready: true });
}

// =====================================================================================
// Host side: one managed worker per handler, with restart-on-fault + request correlation.
// =====================================================================================
interface Pending {
  resolve: (r: Res) => void;
  timer: ReturnType<typeof setTimeout>;
}

class PooledHandler {
  private worker!: Worker;
  private readonly pending = new Map<number, Pending>();
  private nextID = 0;
  healthy = false;
  readonly ready: Promise<void>;
  private resolveReady!: () => void;
  private rejectReady!: (e: Error) => void;
  private booted = false;
  private closed = false;
  private readonly spec: WorkerSpec;
  private readonly entry: string;
  private readonly limits: { maxOld: number; maxYoung: number };

  constructor(spec: WorkerSpec, entry: string, limits: { maxOld: number; maxYoung: number }) {
    this.spec = spec;
    this.entry = entry;
    this.limits = limits;
    this.ready = new Promise<void>((res, rej) => {
      this.resolveReady = res;
      this.rejectReady = rej;
    });
    this.spawn();
  }

  private spawn(): void {
    this.worker = new Worker(this.entry, {
      workerData: this.spec,
      resourceLimits: { maxOldGenerationSizeMb: this.limits.maxOld, maxYoungGenerationSizeMb: this.limits.maxYoung },
    });
    this.worker.on('message', (msg: Res & { ready?: boolean }) => {
      if (msg.ready) {
        this.booted = true;
        this.healthy = true;
        this.resolveReady();
        return;
      }
      const p = this.pending.get(msg.id);
      if (p) {
        clearTimeout(p.timer);
        this.pending.delete(msg.id);
        p.resolve(msg);
      }
    });
    this.worker.on('error', () => this.fault());
    this.worker.on('exit', () => this.fault());
  }

  // fault handles a worker error/exit (incl. a resourceLimits OOM): fail in-flight requests with
  // 503, then — boot-time → fail readiness fast (shape error); post-boot → restart the worker so
  // siblings and the process are untouched.
  private fault(): void {
    if (this.closed) return;
    this.healthy = false;
    for (const [, p] of this.pending) {
      clearTimeout(p.timer);
      p.resolve({ id: -1, status: 503, error: `function ${this.spec.name} worker faulted` });
    }
    this.pending.clear();
    if (!this.booted) {
      this.rejectReady(new Error(`function ${this.spec.name}: worker exited at boot (shape error)`));
      return;
    }
    setTimeout(() => {
      if (!this.closed) this.spawn();
    }, 50);
  }

  async invoke(event: CloudEvent, traceparent?: string, spanId?: string, links?: string[]): Promise<Res> {
    if (!this.healthy) return { id: -1, status: 503, error: `function ${this.spec.name} unavailable` };
    const id = this.nextID++;
    return new Promise<Res>((resolve) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        resolve({ id, status: 503, error: `function ${this.spec.name} timed out` });
      }, requestTimeoutMs);
      this.pending.set(id, { resolve, timer });
      this.worker.postMessage({ id, event, traceparent, spanId, links } satisfies Req);
    });
  }

  async close(): Promise<void> {
    this.closed = true;
    await this.worker.terminate();
  }
}

export interface Pool {
  app: Hono;
  ready: Promise<void>;
  close: () => Promise<void>;
}

/** createPool spawns one worker per manifest entry and returns the routing host. The caller
 *  awaits `ready` (every worker loaded its handler) before serving. */
export function createPool(manifest: WorkerSpec[], limits?: { maxOldMB?: number; maxYoungMB?: number }): Pool {
  const entry = fileURLToPath(import.meta.url); // spawn this same file as the worker (isMainThread=false)
  const resolved = { maxOld: limits?.maxOldMB ?? maxOldMB, maxYoung: limits?.maxYoungMB ?? maxYoungMB };
  const handlers = new Map<string, PooledHandler>();
  for (const spec of manifest) {
    handlers.set(spec.name, new PooledHandler(spec, entry, resolved));
  }

  const app = new Hono();
  app.get('/health/liveness', (c) => c.text('ok'));
  app.get('/health/readiness', (c) => {
    for (const h of handlers.values()) {
      if (!h.healthy) return c.text('not ready', 503);
    }
    return c.text('ready');
  });
  app.post('/function/:name', async (c) => {
    const h = handlers.get(c.req.param('name'));
    if (!h) return c.json({ error: `unknown function ${c.req.param('name')}` }, 404);
    let event: CloudEvent;
    try {
      const text = await c.req.text();
      event = (text ? JSON.parse(text) : {}) as CloudEvent;
    } catch {
      return c.text('invalid CloudEvent JSON', 400);
    }
    // ADR-0101/0105: forward the trace + span-id + fan-in links headers to the worker.
    const res = await h.invoke(
      event, c.req.header('traceparent'),
      c.req.header('x-funcd-span-id'), parseLinks(c.req.header('x-funcd-span-links')),
    );
    if (res.status === 422) return c.json({ error: res.error, details: res.details }, 422);
    if (res.status === 503) return c.json({ error: res.error }, 503);
    if (res.status === 500) return c.json({ error: res.error, details: res.details }, 500);
    if (res.none) return c.body(null, 204);
    return c.json(res.result as Record<string, unknown>);
  });

  return {
    app,
    ready: Promise.all([...handlers.values()].map((h) => h.ready)).then(() => undefined),
    close: async () => {
      await Promise.all([...handlers.values()].map((h) => h.close()));
    },
  };
}

/** main loads the manifest, spawns the pool, and serves once every worker is ready. */
async function main(): Promise<void> {
  const manifestPath = process.env.FUNCD_POOL_MANIFEST;
  if (!manifestPath) {
    process.stderr.write('funcd-pool: FUNCD_POOL_MANIFEST is required\n');
    process.exit(2);
  }
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8')) as WorkerSpec[];
  const pool = createPool(manifest);
  try {
    await pool.ready;
  } catch (err) {
    process.stderr.write(`funcd-pool: ${err instanceof Error ? err.message : err}\n`);
    process.exit(3);
  }
  const fixedPort = process.env.FUNCD_PORT ? Number(process.env.FUNCD_PORT) : 0;
  const portFile = process.env.FUNCD_PORTFILE;
  const hostname = fixedPort > 0 ? '0.0.0.0' : '127.0.0.1';
  serve({ fetch: pool.app.fetch, hostname, port: fixedPort }, (info) => {
    if (portFile) writeFileSync(portFile, String(info.port));
    process.stderr.write(`funcd-pool: ${manifest.length} handler(s) listening on ${hostname}:${info.port}\n`);
  });
}

if (isMainThread) {
  // run main only when this file IS the process entrypoint (not when imported by tests); the
  // realpath compare handles symlinked dirs (macOS /var → /private/var), like shim.ts. Workers
  // (isMainThread=false) always run the worker side.
  const entryPath = process.argv[1] ? pathToFileURL(realpathSync(process.argv[1])).href : '';
  if (import.meta.url === entryPath) void main();
} else {
  void workerMain();
}
