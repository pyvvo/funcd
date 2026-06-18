import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createApp, type EventSchema, resolveHandler, resolveSchema } from '../src/shim.ts';

const jsonReq = (body: string) =>
  ({ method: 'POST', headers: { 'content-type': 'application/json' }, body }) as const;

// scenario: invoke-returns-object — POST / runs the handler and returns its object as 200 JSON.
test('POST / invokes the handler and returns its object as 200 JSON', async () => {
  const app = createApp((_ctx, event) => ({ echoed: event.data }));
  const res = await app.request('/', jsonReq(JSON.stringify({ id: '1', source: 's', type: 't', data: { hi: 1 } })));
  assert.equal(res.status, 200);
  assert.deepEqual(await res.json(), { echoed: { hi: 1 } });
});

// scenario: invoke-no-return → 204.
test('POST / with no return value → 204', async () => {
  const app = createApp(() => undefined);
  const res = await app.request('/', jsonReq('{}'));
  assert.equal(res.status, 204);
});

// scenario: invoke-throws → 500 with the error message.
test('POST / when the handler throws → 500', async () => {
  const app = createApp(() => { throw new Error('boom'); });
  const res = await app.request('/', jsonReq('{}'));
  assert.equal(res.status, 500);
  const body = (await res.json()) as { error: string };
  assert.match(body.error, /boom/);
});

// scenario: invalid-json → 400.
test('POST / with invalid CloudEvent JSON → 400', async () => {
  const app = createApp(() => ({}));
  const res = await app.request('/', jsonReq('not json'));
  assert.equal(res.status, 400);
});

// scenario: health-endpoints → 200.
test('GET /health/readiness and /health/liveness → 200', async () => {
  const app = createApp(() => ({}));
  assert.equal((await app.request('/health/readiness')).status, 200);
  assert.equal((await app.request('/health/liveness')).status, 200);
});

// scenario: handler-resolution — name, default.name, or default; else throws (shape gate).
test('resolveHandler picks the named export / default.named / default; else throws', () => {
  assert.equal(typeof resolveHandler({ handle: () => {} }, 'handle'), 'function');
  assert.equal(typeof resolveHandler({ default: { handle: () => {} } }, 'handle'), 'function');
  assert.equal(typeof resolveHandler({ default: () => {} }, 'handle'), 'function');
  assert.throws(() => resolveHandler({ nope: 1 }, 'handle'), /not a function/);
});

// scenario: context-log — the handler receives a context with log().
test('the handler context exposes log()', async () => {
  const app = createApp((ctx) => { ctx.log('handling'); return { ok: true }; });
  assert.equal((await app.request('/', jsonReq('{}'))).status, 200);
});

// The event-data contract (JTD schema bundled in the artifact, engine in the shim).
const helloSchema: EventSchema = { optionalProperties: { hello: { type: 'string' } } };
const ce = (data: unknown) => jsonReq(JSON.stringify({ id: '1', source: 's', type: 't', data }));

// scenario: contract-valid → handler runs (200).
test('POST / with a schema and matching event.data runs the handler', async () => {
  const app = createApp((_ctx, event) => ({ echoed: event.data }), helloSchema);
  const res = await app.request('/', ce({ hello: 'funcd' }));
  assert.equal(res.status, 200);
  assert.deepEqual(await res.json(), { echoed: { hello: 'funcd' } });
});

// scenario: contract-mismatch → 422, handler never runs.
test('POST / with a schema and mismatching event.data → 422 (handler not called)', async () => {
  let called = false;
  const app = createApp(() => { called = true; return { ok: true }; }, helloSchema);
  const res = await app.request('/', ce({ hello: 123 }));
  assert.equal(res.status, 422);
  const body = (await res.json()) as { error: string; details: unknown[] };
  assert.match(body.error, /contract/);
  assert.ok(body.details.length > 0, 'carries the JTD validation errors');
  assert.equal(called, false, 'a bad-shaped event never reaches user code');
});

// scenario: no-schema → no validation (backward compatible).
test('POST / without a schema validates nothing (today’s behavior)', async () => {
  const app = createApp((_ctx, event) => ({ echoed: event.data }));
  const res = await app.request('/', ce({ anything: [1, 2, 3] }));
  assert.equal(res.status, 200);
});

// scenario: schema-shape-gate — a malformed eventSchema export is rejected (like a missing handler).
test('resolveSchema returns the schema, undefined when absent, throws when malformed', () => {
  assert.deepEqual(resolveSchema({ eventSchema: helloSchema }), helloSchema);
  assert.equal(resolveSchema({}), undefined);
  assert.throws(() => resolveSchema({ eventSchema: { type: 'not-a-jtd-type' } }), /not a valid JTD schema/);
});
