import type { Handler } from '@funcd/shim-nodejs';

/** ingest — the root step. It receives the run input as the CloudEvent `data` (ADR-0094: the
 * dispatcher envelopes the flowing input; the shim validates it against this contract). */
export interface FuncInput {
  n: number;
}

/** What ingest passes to the next step (verbatim single-parent flow). */
export interface FuncOutput {
  n: number;
}

export const handle: Handler<FuncInput, FuncOutput> = (context, event) => {
  const n = event.data!.n;
  context.log('ingest', n);
  return { n };
};
