import type { Handler } from '@funcd/shim-nodejs';

/** score — the second step. Its input is ingest's output verbatim ({ amount }). */
export interface FuncInput {
  amount: number;
}

/** score adds a derived `score` the branch conditions gate on (score = amount * 10). */
export interface FuncOutput {
  amount: number;
  score: number;
}

export const handle: Handler<FuncInput, FuncOutput> = (context, event) => {
  const amount = event.data!.amount;
  const score = amount * 10;
  context.log('score', score);
  return { amount, score };
};
