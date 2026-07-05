import type { Handler } from '@funcd/shim-nodejs';

/** score — the second step. Its input is ingest's output verbatim ({ n }). */
export interface FuncInput {
  n: number;
}

/** score adds a derived `score` the branch conditions gate on (score = n * 10). */
export interface FuncOutput {
  n: number;
  score: number;
}

export const handle: Handler<FuncInput, FuncOutput> = (context, event) => {
  const n = event.data!.n;
  const score = n * 10;
  context.log('score', score);
  return { n, score };
};
