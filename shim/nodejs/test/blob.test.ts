// Scenario tests for context.blob (ADR-0127): the Node client over a minimal fake worker-node local API
// (HTTP-over-UDS), proving the get/put/delete/list/signedUrl wire without a real platform.
import assert from 'node:assert';
import { mkdtempSync } from 'node:fs';
import http from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';

import { makeBlob, type BlobClient } from '../src/blob.ts';

interface Recorded {
  method: string;
  url: string;
  body: Buffer;
}

// withServer spins a UDS HTTP server that records requests and replies from `handler`, points
// FUNCD_INVOKE_SOCKET at it, runs fn(blob, recorded), then tears it all down.
function withServer(
  handler: (req: Recorded) => { status: number; body: string },
  fn: (blob: BlobClient, recorded: Recorded[]) => Promise<void>,
) {
  return async () => {
    const sock = join(mkdtempSync(join(tmpdir(), 'funcd-blob-')), 'api.sock');
    const recorded: Recorded[] = [];
    const server = http.createServer((req, res) => {
      const chunks: Buffer[] = [];
      req.on('data', (c: Buffer) => chunks.push(c));
      req.on('end', () => {
        const rec = { method: req.method ?? '', url: req.url ?? '', body: Buffer.concat(chunks) };
        recorded.push(rec);
        const r = handler(rec);
        res.statusCode = r.status;
        res.end(r.body);
      });
    });
    await new Promise<void>((resolve) => server.listen(sock, resolve));
    const prev = process.env.FUNCD_INVOKE_SOCKET;
    process.env.FUNCD_INVOKE_SOCKET = sock;
    try {
      await fn(makeBlob(), recorded);
    } finally {
      if (prev === undefined) delete process.env.FUNCD_INVOKE_SOCKET;
      else process.env.FUNCD_INVOKE_SOCKET = prev;
      await new Promise<void>((resolve) => server.close(() => resolve()));
    }
  };
}

test(
  'scenario blob-read-write: put sends bytes to /blob/{binding}/{key}, get returns them',
  withServer(
    (req) => (req.method === 'GET' ? { status: 200, body: 'hello' } : { status: 204, body: '' }),
    async (blob, recorded) => {
      await blob.put('files', 'report.txt', new TextEncoder().encode('hello'));
      assert.strictEqual(recorded[0].method, 'PUT');
      assert.strictEqual(recorded[0].url, '/blob/files/report.txt');
      assert.strictEqual(recorded[0].body.toString('utf8'), 'hello');

      const got = await blob.get('files', 'report.txt');
      assert.ok(got instanceof Uint8Array);
      assert.strictEqual(new TextDecoder().decode(got!), 'hello');
    },
  ),
);

test(
  'scenario blob-missing-is-null: get on a 404 → null',
  withServer(
    () => ({ status: 404, body: 'not found' }),
    async (blob) => {
      assert.strictEqual(await blob.get('files', 'absent'), null);
    },
  ),
);

test(
  'scenario blob-list: list parses the JSON key array; a prefix is a query param',
  withServer(
    () => ({ status: 200, body: '["bronze/a","bronze/b"]' }),
    async (blob, recorded) => {
      assert.deepStrictEqual(await blob.list('files', 'bronze/'), ['bronze/a', 'bronze/b']);
      assert.strictEqual(recorded[0].url, '/blob/files?prefix=bronze%2F');
    },
  ),
);

test(
  'scenario blob-signed-url: signedUrl hits ?sign=1 with the method + returns the URL body',
  withServer(
    () => ({ status: 200, body: 'https://signed.example/p/report.txt' }),
    async (blob, recorded) => {
      const url = await blob.signedUrl('files', 'report.txt', { method: 'PUT', expiry: '15m' });
      assert.strictEqual(url, 'https://signed.example/p/report.txt');
      assert.match(recorded[0].url, /\/blob\/files\/report\.txt\?sign=1&method=PUT&expiry=15m/);
    },
  ),
);

test(
  'scenario blob-error-throws: a non-2xx (e.g. 403 unbound) rejects',
  withServer(
    () => ({ status: 403, body: 'forbidden' }),
    async (blob) => {
      await assert.rejects(() => blob.get('nope', 'k'), /context\.blob\.get failed: 403/);
    },
  ),
);
