import type { Handler } from '@funcd/shim-nodejs';

/** report — the join step (join: any). Its input is the fan-in composite keyed by parent step
 * name: exactly one of `hi`/`lo` is present (the surviving branch), the other Skipped. */
export interface FuncInput {
  hi?: { amount: number; score: number; tier: string };
  lo?: { amount: number; score: number; tier: string };
}

/** The leaf output IS the run output (ADR-0094: leaf outputs compose the run output). */
export interface FuncOutput {
  done: boolean;
  tier: string;
}

export const handle: Handler<FuncInput, FuncOutput> = (context, event) => {
  const branch = event.data!.hi ?? event.data!.lo;
  context.log('report', branch?.tier);
  return { done: true, tier: branch!.tier };
};
