import type { Handler } from '@funcd/shim-nodejs';

// A TYPE-only import of the callee's contract — erased at build time, so front.mjs does NOT bundle
// greeter's code. It only borrows greeter's input/output types so the invoke is type-checked.
import type { GreeterInput, GreeterOutput } from './greeter.ts';

/** What front accepts. */
export interface FrontInput {
  name: string;
}

/** What front returns — greeter's greeting, wrapped. */
export interface FrontOutput {
  via: string;
  greeting: string;
}

/**
 * front — the CALLER in the fn-to-fn link example (ADR-0064). It declares a link to greeter on its
 * Function resource:
 *
 *   spec:
 *     links:
 *       - alias: greeter      # the local name this handler passes to context.invoke
 *         target: greeter     # the Function it resolves to (same namespace, latest-Ready revision)
 *
 * and invokes it by that alias. The platform brokers the synchronous call over the per-sandbox
 * worker-node local API (HTTP-over-UDS) — front never knows greeter's address, and the link IS the
 * capability: with no matching link, context.invoke fails closed (default-deny).
 */
export const handle: Handler<FrontInput, FrontOutput> = async (context, event) => {
  const name = event.data?.name ?? 'world';
  // The invoke input is the CloudEvent delivered to greeter, so wrap the payload in `data`.
  // invoke<I, O> is generic: I is the body sent to greeter, O is greeter's typed output.
  const reply = await context.invoke<{ data: GreeterInput }, GreeterOutput>('greeter', {
    data: { name },
  });
  return { via: 'front', greeting: reply.greeting };
};
