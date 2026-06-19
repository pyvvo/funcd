import { test } from 'node:test';
import assert from 'node:assert/strict';

import type { CloudEvent } from '@funcd/shim-nodejs';
import { handle, type FuncInput } from '../src/handler.ts';

// The author's unit tests exercise the handler directly — the platform owns input/output
// *validation* (the build bakes the validators from FuncInput/FuncOutput; the shim runs them),
// so these tests assert behavior on already-valid input.

test('handle greets by name and echoes the trigger', () => {
  const logs: unknown[][] = [];
  const context = { log: (...args: unknown[]) => logs.push(args) };
  const event: CloudEvent<FuncInput> = {
    id: '1',
    source: '/demo',
    type: 'com.example.hello',
    data: { name: 'funcd' },
  };

  const out = handle(context, event);

  assert.deepEqual(out, { greeting: 'Hello, funcd.' });
  assert.equal(logs.length, 1);
});

test('the optional `excited` flag switches the punctuation', () => {
  const context = { log: () => {} };
  const event: CloudEvent<FuncInput> = {
    id: '2',
    source: '/demo',
    type: 'com.example.hello',
    data: { name: 'funcd', excited: true },
  };

  const out = handle(context, event);

  assert.deepEqual(out, { greeting: 'Hello, funcd!' });
});
