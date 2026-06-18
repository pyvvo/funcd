import { test } from 'node:test';
import assert from 'node:assert/strict';

import type { CloudEvent } from '@funcd/shim-nodejs';
import { handle } from '../src/handler.ts';

test('handle echoes the event back and logs once', () => {
  const logs: unknown[][] = [];
  const context = { log: (...args: unknown[]) => logs.push(args) };
  const event: CloudEvent<{ hello?: string }> = {
    id: '1',
    source: '/demo',
    type: 'com.example.hello',
    data: { hello: 'funcd' },
  };

  const out = handle(context, event);

  assert.deepEqual(out, { echoed: { hello: 'funcd' }, by: 'funcd' });
  assert.equal(logs.length, 1);
});
