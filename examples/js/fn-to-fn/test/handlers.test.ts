import { test } from 'node:test';
import assert from 'node:assert/strict';

import type { CloudEvent, FunctionContext } from '@funcd/shim-nodejs';
import { handle as greeter, type FuncInput as GreeterInput } from '../src/greeter.ts';
import { handle as front, type FuncInput as FrontInput } from '../src/front.ts';

// The author's unit tests exercise each handler directly. front's call to context.invoke is mocked
// here — the real brokered round-trip is proven by the e2e (pkg/funcd/invoke_e2e_test.go). The cast
// to FunctionContext is because invoke is a generic method a concrete mock can't structurally model.

test('greeter greets by name', () => {
  const ctx = { log: () => {}, invoke: async () => ({}) } as unknown as FunctionContext;
  const event: CloudEvent<GreeterInput> = { id: '1', source: '/t', type: 'x', data: { name: 'funcd' } };
  assert.deepEqual(greeter(ctx, event), { greeting: 'Hello, funcd!' });
});

test('front invokes the greeter link and wraps the reply', async () => {
  const calls: Array<{ alias: string; input: unknown }> = [];
  const ctx = {
    log: () => {},
    invoke: async (alias: string, input: unknown) => {
      calls.push({ alias, input });
      return { greeting: 'Hello, funcd!' };
    },
  } as unknown as FunctionContext;
  const event: CloudEvent<FrontInput> = { id: '2', source: '/t', type: 'x', data: { name: 'funcd' } };

  const out = await front(ctx, event);

  assert.deepEqual(out, { via: 'front', greeting: 'Hello, funcd!' });
  assert.equal(calls.length, 1);
  assert.equal(calls[0]!.alias, 'greeter');
  assert.deepEqual(calls[0]!.input, { data: { name: 'funcd' } });
});
