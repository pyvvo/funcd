// log-burst — a function that emits a BURST of ≥100 function logs in one invocation (ADR-0081,
// Path B). Each invoke loops, mixing structured `console.log` (an object arg → captured into
// attrs/attrs.args), `console.warn`, and `console.error`, then returns the count it emitted. The
// Lima e2e deploys this and asserts ≥100 captured log records land in the funcd-system log store —
// proving the runtime-shim's console.* capture producer end to end on real containerd.
//
// Authored in TypeScript against @funcd/shim-nodejs; build.ts bundles it to burst.mjs.
import type { CloudEvent, FunctionContext } from '@funcd/shim-nodejs';

export interface FuncInput {
  /** How many items to "process" — defaults to 100 (the e2e floor). */
  items?: number;
  /** A batch label echoed into the structured logs. */
  batch?: string;
}
export interface FuncOutput {
  emitted: number;
}

export function handle(_ctx: FunctionContext, event: CloudEvent<FuncInput>): FuncOutput {
  const items = Math.max(100, event.data?.items ?? 100); // never below the e2e floor of 100
  const batch = event.data?.batch ?? 'default';
  let emitted = 0;

  for (let i = 0; i < items; i++) {
    // structured INFO: the object arg is captured losslessly into attrs.args and its string keys
    // merged to the top level (queryable). This is the bulk of the burst.
    console.log('processing item', { i, batch });
    emitted++;

    // sprinkle a WARN every 10th item and an ERROR every 25th — so the burst exercises every
    // severity the capture maps (INFO/WARN/ERROR), not just INFO.
    if (i % 10 === 0) {
      console.warn('slow item', { i, batch, latencyMs: 120 });
      emitted++;
    }
    if (i % 25 === 0) {
      console.error('item failed', { i, batch, reason: 'simulated' });
      emitted++;
    }
  }

  // a final structured summary line (still INFO) — total emitted ≫ 100.
  console.log('burst complete', { batch, emitted });
  emitted++;

  return { emitted };
}
