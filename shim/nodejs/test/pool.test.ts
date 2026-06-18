import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import type { Hono } from 'hono';

import { createPool } from '../src/pool.ts';

// writeHandlers writes each handler to a temp .mjs and returns the pool manifest.
function writeHandlers(handlers: Record<string, string>): { name: string; artifact: string }[] {
  const dir = mkdtempSync(join(tmpdir(), 'funcd-pool-test-'));
  return Object.entries(handlers).map(([name, code]) => {
    const artifact = join(dir, `${name}.mjs`);
    writeFileSync(artifact, code);
    return { name, artifact };
  });
}

const post = (app: Hono, name: string, data: unknown) =>
  app.request(`/function/${name}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ id: '1', source: 's', type: 't', data }),
  });

// scenario: pool-routes — POST /function/<name> reaches the right handler's worker; unknown → 404.
test('routes each function to its own worker; unknown → 404', async () => {
  const pool = createPool(
    writeHandlers({
      a: 'export function handle(_, e) { return { from: "a", data: e.data }; }',
      b: 'export function handle(_, e) { return { from: "b", data: e.data }; }',
    }),
  );
  await pool.ready;
  try {
    const ra = await post(pool.app, 'a', { x: 1 });
    assert.equal(ra.status, 200);
    assert.deepEqual(await ra.json(), { from: 'a', data: { x: 1 } });

    const rb = await post(pool.app, 'b', { y: 2 });
    assert.deepEqual(await rb.json(), { from: 'b', data: { y: 2 } });

    assert.equal((await post(pool.app, 'nope', {})).status, 404);
    assert.equal((await pool.app.request('/health/readiness', { method: 'GET' })).status, 200);
  } finally {
    await pool.close();
  }
});

// scenario: pool-isolation — a throwing handler 500s and a worker-killing handler 503s, while a
// sibling keeps serving and the pool process survives.
test('a faulting handler is isolated; siblings keep serving', async () => {
  const pool = createPool(
    writeHandlers({
      boom: 'export function handle() { throw new Error("kaboom"); }',
      die: 'export function handle() { process.exit(1); }', // exits the worker thread, not the process
      ok: 'export function handle() { return { ok: true }; }',
    }),
  );
  await pool.ready;
  try {
    assert.equal((await post(pool.app, 'boom', {})).status, 500); // caught throw → 500, worker survives
    assert.equal((await post(pool.app, 'die', {})).status, 503); // worker exits → 503 (then restarts)

    const ok = await post(pool.app, 'ok', {}); // sibling unaffected; the pool process is alive
    assert.equal(ok.status, 200);
    assert.deepEqual(await ok.json(), { ok: true });
  } finally {
    await pool.close();
  }
});

// scenario: pool-quota — a handler exceeding its resourceLimits OOMs its worker thread (not the
// process); its request fails and a sibling keeps serving — the per-artifact memory quota holds.
test('a handler over its memory quota OOMs its thread, not the pool', async () => {
  const pool = createPool(
    writeHandlers({
      greedy: 'export function handle() { const a = []; for (;;) a.push(new Array(1e6).fill(7)); }',
      ok: 'export function handle() { return { ok: true }; }',
    }),
    { maxOldMB: 16 }, // a tiny per-handler heap cap
  );
  await pool.ready;
  try {
    const r = await post(pool.app, 'greedy', {});
    assert.notEqual(r.status, 200, 'the greedy handler did not return a result (its thread OOMed)');

    const ok = await post(pool.app, 'ok', {});
    assert.equal(ok.status, 200, 'the sibling handler is unaffected by the OOM');
  } finally {
    await pool.close();
  }
});

// scenario: pool-contract — each pooled handler keeps the ADR-0038 JTD contract: a mismatching
// event.data is rejected with 422 before the handler runs.
test('a pooled handler enforces its embedded eventSchema (422 on mismatch)', async () => {
  const pool = createPool(
    writeHandlers({
      c: 'export const eventSchema = { optionalProperties: { hello: { type: "string" } } };\n' +
        'export function handle(_, e) { return { echoed: e.data }; }',
    }),
  );
  await pool.ready;
  try {
    assert.equal((await post(pool.app, 'c', { hello: 'world' })).status, 200);
    const bad = await post(pool.app, 'c', { hello: 123 });
    assert.equal(bad.status, 422);
    assert.match(((await bad.json()) as { error: string }).error, /contract/);
  } finally {
    await pool.close();
  }
});
