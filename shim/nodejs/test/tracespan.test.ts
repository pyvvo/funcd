import { test } from 'node:test';
import assert from 'node:assert/strict';

import { createApp, type Validator } from '../src/shim.ts';
import { installConsoleCapture } from '../src/funclog.ts';
import { parseTraceparent } from '../src/tracespan.ts';

// ADR-0101 behavioral-span tier: the shim mints one per-invocation SERVER span and correlates logs.
// These drive createApp with a STUB channel sink (a line collector), asserting the emitted records.

const jsonReq = (body: string, headers: Record<string, string> = {}) =>
  ({ method: 'POST', headers: { 'content-type': 'application/json', ...headers }, body }) as const;

// collector is a stub Sink accumulating framed NDJSON lines, split back into records.
function collector() {
  const lines: string[] = [];
  const records = (): Record<string, unknown>[] =>
    lines
      .join('')
      .split('\n')
      .filter((l) => l.length > 0)
      .map((l) => JSON.parse(l) as Record<string, unknown>);
  return {
    sink: (line: string): void => {
      lines.push(line);
    },
    spans: () => records().filter((r) => r['funcd.signal'] === 'traces'),
    logs: () => records().filter((r) => r['funcd.source'] === 'console'),
  };
}

function snapshotConsole(): () => void {
  const saved = { debug: console.debug, log: console.log, info: console.info, warn: console.warn, error: console.error };
  return () => Object.assign(console, saved);
}

const failing: Validator = () => [{ path: '', message: 'mismatch' }];

// scenario: invocation-emits-span — one SERVER span, OK, name/start/end, well-formed ids.
test('invocation-emits-span: POST / emits exactly one SERVER span (OK, name, start≤end, hex ids)', async () => {
  const c = collector();
  const app = createApp(() => ({ ok: true }), {}, { sink: c.sink, fnName: 'greeter' });
  const res = await app.request('/', jsonReq('{}'));
  assert.equal(res.status, 200);

  const spans = c.spans();
  assert.equal(spans.length, 1, 'exactly one span per invocation');
  const s = spans[0];
  assert.equal(s.kind, 'SERVER');
  assert.equal(s.status, 'OK');
  assert.equal(s.name, 'greeter');
  assert.equal(typeof s.start, 'number');
  assert.equal(typeof s.end, 'number');
  assert.ok((s.end as number) >= (s.start as number), 'end ≥ start (a non-negative duration)');
  assert.match(s.trace_id as string, /^[0-9a-f]{32}$/);
  assert.match(s.span_id as string, /^[0-9a-f]{16}$/);
});

// scenario: mint-root-trace — no traceparent ⇒ a root (fresh 32-hex trace, empty parent).
test('mint-root-trace: no traceparent → root span (32-hex trace, empty parent)', async () => {
  const c = collector();
  const app = createApp(() => ({ ok: true }), {}, { sink: c.sink, fnName: 'f' });
  await app.request('/', jsonReq('{}'));
  const s = c.spans()[0];
  assert.match(s.trace_id as string, /^[0-9a-f]{32}$/);
  assert.equal(s.parent_id, '', 'a root span has no parent');
});

// scenario: adopt-traceparent — an incoming W3C header ⇒ span adopts its trace-id + parent span-id.
test('adopt-traceparent: span joins the caller trace (trace-id adopted, parent = header span-id, fresh span-id)', async () => {
  const c = collector();
  const traceId = 'a'.repeat(32);
  const callerSpan = 'b'.repeat(16);
  const tp = `00-${traceId}-${callerSpan}-01`;
  const app = createApp(() => ({ ok: true }), {}, { sink: c.sink, fnName: 'f' });
  await app.request('/', jsonReq('{}', { traceparent: tp }));

  const s = c.spans()[0];
  assert.equal(s.trace_id, traceId, 'adopts the caller trace-id');
  assert.equal(s.parent_id, callerSpan, 'parents on the caller span-id');
  assert.match(s.span_id as string, /^[0-9a-f]{16}$/);
  assert.notEqual(s.span_id, callerSpan, 'this invocation mints its OWN span-id');
});

// scenario: error-span-status — throw and output-mismatch → ERROR (+ msg); input-mismatch → no span.
test('error-span-status: handler throw → ERROR span with message', async () => {
  const c = collector();
  const app = createApp(() => { throw new Error('kaboom'); }, {}, { sink: c.sink, fnName: 'f' });
  const res = await app.request('/', jsonReq('{}'));
  assert.equal(res.status, 500);
  const s = c.spans()[0];
  assert.equal(s.status, 'ERROR');
  assert.match(s.status_msg as string, /kaboom/);
});

test('error-span-status: output-contract mismatch → ERROR span', async () => {
  const c = collector();
  const app = createApp(() => ({ bad: true }), { output: failing }, { sink: c.sink, fnName: 'f' });
  const res = await app.request('/', jsonReq('{}'));
  assert.equal(res.status, 500);
  assert.equal(c.spans()[0].status, 'ERROR');
});

test('error-span-status: input-contract mismatch (422) → NO span (handler never runs)', async () => {
  const c = collector();
  const app = createApp(() => ({ ok: true }), { input: failing }, { sink: c.sink, fnName: 'f' });
  const res = await app.request('/', jsonReq('{}'));
  assert.equal(res.status, 422);
  assert.equal(c.spans().length, 0, 'no invocation → no span');
});

// scenario: logs-correlated-to-span — a log emitted in the handler carries the span's trace/span ids.
test('logs-correlated-to-span: a console log inside the handler carries the invocation trace_id/span_id', async () => {
  const c = collector();
  const restore = snapshotConsole();
  // patch console onto the SAME stub channel (explicit sink); the log records share the collector.
  installConsoleCapture({} as NodeJS.ProcessEnv, c.sink);
  try {
    const app = createApp((ctx) => { ctx.log('inside'); return { ok: true }; }, {}, { sink: c.sink, fnName: 'f' });
    const res = await app.request('/', jsonReq('{}'));
    assert.equal(res.status, 200);
  } finally {
    restore();
  }

  const span = c.spans()[0];
  const log = c.logs().find((r) => r.body === 'inside');
  assert.ok(span, 'a span was emitted');
  assert.ok(log, 'the log line was captured');
  assert.notEqual(log!.trace_id, '', 'the log carries a (non-empty) trace id');
  assert.equal(log!.trace_id, span.trace_id, 'log trace_id == span trace_id (correlation)');
  assert.equal(log!.span_id, span.span_id, 'log span_id == span span_id');
});

// unit: parseTraceparent accepts a valid header, rejects malformed/all-zero (⇒ caller mints a root).
test('parseTraceparent: valid header parses; malformed/all-zero → null', () => {
  const ok = parseTraceparent(`00-${'a'.repeat(32)}-${'b'.repeat(16)}-01`);
  assert.deepEqual(ok, { traceId: 'a'.repeat(32), parentId: 'b'.repeat(16) });
  assert.equal(parseTraceparent(undefined), null);
  assert.equal(parseTraceparent('garbage'), null);
  assert.equal(parseTraceparent(`00-${'0'.repeat(32)}-${'b'.repeat(16)}-01`), null, 'all-zero trace-id rejected');
  assert.equal(parseTraceparent(`00-${'a'.repeat(32)}-${'0'.repeat(16)}-01`), null, 'all-zero parent rejected');
});

// scenario: engine-owns-span-id (ADR-0105) — a provided X-Funcd-Span-Id is used as the span-id, and
// X-Funcd-Span-Links are attached as fan-in links.
test('engine-owns-span-id: a provided X-Funcd-Span-Id is used and X-Funcd-Span-Links are attached', async () => {
  const c = collector();
  const app = createApp(() => ({ ok: true }), {}, { sink: c.sink, fnName: 'step' });
  const provided = 'abcdef0123456789';
  const res = await app.request('/', jsonReq('{}', {
    'x-funcd-span-id': provided,
    'x-funcd-span-links': '1111111111111111, 2222222222222222',
  }));
  assert.equal(res.status, 200);
  const s = c.spans()[0];
  assert.equal(s.span_id, provided, 'the span uses the engine-provided span-id (not a minted one)');
  assert.deepEqual(s.links, ['1111111111111111', '2222222222222222'], 'fan-in links attached');
});

// scenario: direct-invoke-unchanged (ADR-0105) — no X-Funcd-Span-Id → the shim mints its own id, no links.
test('direct-invoke-unchanged: no X-Funcd-Span-Id → the shim mints its span-id (ADR-0101 unchanged)', async () => {
  const c = collector();
  const app = createApp(() => ({ ok: true }), {}, { sink: c.sink, fnName: 'f' });
  await app.request('/', jsonReq('{}'));
  const s = c.spans()[0];
  assert.match(s.span_id as string, /^[0-9a-f]{16}$/, 'minted span-id');
  assert.deepEqual(s.links, [], 'no links on a direct invoke');
});
