import type { Handler } from '@funcd/shim-nodejs';

/** hi — the high branch. It receives score's output ({ n, score }); it runs only when the
 * workflow's `when` condition (score > 30) held (ADR-0095 native-JS predicate). */
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
  return { n, score, tier: 'hi' };
};
