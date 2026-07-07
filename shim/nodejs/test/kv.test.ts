// Scenario tests for the context.kv typed read accessors (ADR-0070): getText/getJSON over a minimal fake
// worker-node local API (HTTP-over-UDS), proving the client-side decoders without a real platform.
import assert from 'node:assert';
import { mkdtempSync } from 'node:fs';
import http from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';

import { makeKV, type KVClient } from '../src/kv.ts';

// withServer spins a UDS HTTP server serving `routes` (path → {status, body}; 404 otherwise), points
// FUNCD_INVOKE_SOCKET at it, runs fn(kv), then tears it all down.
function withServer(routes: Record<string, { status: number; body: string }>, fn: (kv: KVClient) => Promise<void>) {
  return async () => {
    const sock = join(mkdtempSync(join(tmpdir(), 'funcd-kv-')), 'api.sock');
    const server = http.createServer((req, res) => {
      const r = routes[req.url ?? ''];
      if (!r) {
        res.statusCode = 404;
        res.end('not found');
        return;
      }
      res.statusCode = r.status;
      res.end(r.body);
    });
    await new Promise<void>((resolve) => server.listen(sock, resolve));
    const prev = process.env.FUNCD_INVOKE_SOCKET;
    process.env.FUNCD_INVOKE_SOCKET = sock;
    try {
      await fn(makeKV());
    } finally {
      if (prev === undefined) delete process.env.FUNCD_INVOKE_SOCKET;
      else process.env.FUNCD_INVOKE_SOCKET = prev;
      await new Promise<void>((resolve) => server.close(() => resolve()));
    }
  };
}

test(
  'scenario get-text-decodes: getText returns the UTF-8 string',
  withServer({ '/kv/counters/n': { status: 200, body: '5' } }, async (kv) => {
    assert.strictEqual(await kv.getText('counters', 'n'), '5');
  }),
);

test(
  'scenario get-json-parses: getJSON returns the parsed object',
  withServer({ '/kv/counters/n': { status: 200, body: '{"n":5}' } }, async (kv) => {
    assert.deepStrictEqual(await kv.getJSON('counters', 'n'), { n: 5 });
  }),
);

test(
  'scenario get-typed-missing-is-null: getText/getJSON on a miss → null',
  withServer({}, async (kv) => {
    assert.strictEqual(await kv.getText('counters', 'x'), null);
    assert.strictEqual(await kv.getJSON('counters', 'x'), null);
  }),
);

test(
  'scenario get-bytes-unchanged: get returns raw bytes',
  withServer({ '/kv/counters/n': { status: 200, body: '5' } }, async (kv) => {
    const b = await kv.get('counters', 'n');
    assert.ok(b instanceof Uint8Array);
    assert.strictEqual(new TextDecoder().decode(b!), '5');
  }),
);
