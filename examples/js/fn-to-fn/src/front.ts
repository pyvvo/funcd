import type { Handler } from '@funcd/shim-nodejs';

// A TYPE-only import of the callee's contract — erased at build, so front.mjs does NOT bundle
// greeter's code. It borrows greeter's input/output types so the invoke is type-checked, aliased
// because this module declares its OWN FuncInput/FuncOutput (its contract).
import type { FuncInput as GreeterInput, FuncOutput as GreeterOutput } from './greeter.ts';

/**
 * What front accepts. `name` is OPTIONAL here (front is permissive) — so front can forward a payload
 * to greeter that greeter's stricter contract rejects, demonstrating that a target's 422 propagates
 * back through context.invoke (ADR-0064).
 */
export interface FuncInput {
  name?: string;
}

/** What front returns — greeter's greeting, wrapped. */
export interface FuncOutput {
  via: string;
  greeting: string;
}

/**
 * front — the CALLER. It declares a link to greeter on its Function resource:
 *   spec: { links: [{ alias: greeter, target: greeter }] }
 * and invokes it. The platform brokers the synchronous call over the per-sandbox worker-node local
 * API; the link IS the capability (no link ⇒ invoke fails closed). It FORWARDS the caller's `name`
 * verbatim — so a missing `name` reaches greeter, whose contract rejects it (422), and that 422
 * propagates back here (the awaited invoke throws).
 */
export const handle: Handler<FuncInput, FuncOutput> = async (context, event) => {
  const reply = await context.invoke<{ data: GreeterInput }, GreeterOutput>('greeter', {
    data: { name: event.data?.name as string },
  });
  return { via: 'front', greeting: reply.greeting };
};
