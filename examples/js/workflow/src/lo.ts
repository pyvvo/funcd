import type { Handler } from '@funcd/shim-nodejs';

/** lo — the low branch. The exclusive complement of hi: it runs only when score <= 30. Exactly
 * one of hi/lo runs per run; the other is Skipped (its `when` was false). */
export interface FuncInput {
  n: number;
  score: number;
}

export interface FuncOutput {
  n: number;
  score: number;
  tier: string;
}

export const handle: Handler<FuncInput, FuncOutput> = (context, event) => {
  const { n, score } = event.data!;
  return { n, score, tier: 'lo' };
};
