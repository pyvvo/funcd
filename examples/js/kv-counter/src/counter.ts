// kv-counter — a function that uses durable KV (ADR-0069). Each invoke reads a per-name counter from
// the KV binding "counters" via context.kv, increments it, writes it back, and returns it. Proves the
// function-facing KV path: context.kv → worker-node local API (UDS) → PDP Facade → durable driver.
import type { CloudEvent, FunctionContext } from '@funcd/shim-nodejs';

export interface FuncInput {
  name?: string;
}
export interface FuncOutput {
  name: string;
  count: number;
}

export async function handle(ctx: FunctionContext, event: CloudEvent<FuncInput>): Promise<FuncOutput> {
  const name = event.data?.name ?? 'world';
  const cur = await ctx.kv.getText('counters', name);
  const count = Number(cur ?? 0) + 1;
  await ctx.kv.put('counters', name, String(count));
  ctx.log(`kv-counter: ${name} → ${count}`);
  return { name, count };
}
