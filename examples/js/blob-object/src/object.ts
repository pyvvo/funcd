// blob-object — a function that uses native blob via context.blob (ADR-0127), the in-function analogue
// of s3-roundtrip's S3-keypair path: NO @aws-sdk, NO injected keypair, just the binding. Each invoke:
//   (a) put   gold/roundtrip-<inv>.txt          — the fn is bound to (and owns) the gold prefix → allowed
//   (b) get   it back                            — body must match
//   (c) list  gold/ under roundtrip-            — the key must be present
//   (d) put   to the UNBOUND alias "other"       — no spec.blob entry → bind-as-grant default-deny → 403
// and returns {put, get, list, denied} (denied=true means step (d) was correctly rejected by the PDP).
// Proves the function-facing blob path: context.blob → worker-node local API (UDS) → binding-gated,
// S3Capability-authorized Facade → the SAME substrate the S3 frontend serves.
import type { CloudEvent, FunctionContext } from '@funcd/shim-nodejs';

export interface FuncInput {
  /** Optional invocation tag woven into the object key (defaults to a counter-free constant). */
  inv?: string;
}
export interface FuncOutput {
  put: boolean;
  get: boolean;
  list: number;
  denied: boolean;
}

const GOLD = 'gold'; // the alias this function is bound to AND owns (reads + writes)
const OTHER = 'other'; // an alias it did NOT declare in spec.blob → every op must be denied

export async function handle(ctx: FunctionContext, event: CloudEvent<FuncInput>): Promise<FuncOutput> {
  const inv = event.data?.inv ?? 'x';
  const key = `roundtrip-${inv}.txt`;
  const body = `hello from blob-object ${inv}`;

  await ctx.blob.put(GOLD, key, new TextEncoder().encode(body));
  const got = await ctx.blob.get(GOLD, key);
  const getOK = got !== null && new TextDecoder().decode(got) === body;
  const keys = await ctx.blob.list(GOLD, 'roundtrip-');

  // The unbound alias must be rejected (ADR-0073/0127 bind-as-grant default-deny).
  let denied = false;
  try {
    await ctx.blob.put(OTHER, 'x.txt', new TextEncoder().encode('nope'));
  } catch {
    denied = true;
  }

  ctx.log(`blob-object: put=true get=${getOK} list=${keys.length} denied=${denied}`);
  return { put: true, get: getOK, list: keys.length, denied };
}
