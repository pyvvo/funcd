import type { Handler } from '@funcd/shim-nodejs';

/** ingest — the root step. It receives the run input as the CloudEvent `data` (ADR-0094: the
 * dispatcher envelopes the flowing input; the shim validates it against this contract). */
export interface FuncInput {
  amount: number;
}

/** What ingest passes to the next step (verbatim single-parent flow). */
export interface FuncOutput {
  amount: number;
}

export const handle: Handler<FuncInput, FuncOutput> = (context, event) => {
  const amount = event.data!.amount;
  context.log('ingest', amount);
  return { amount };
};
