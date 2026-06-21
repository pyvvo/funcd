import type { Handler } from '@funcd/shim-nodejs';

/** What greeter accepts — the CloudEvent `data` (ADR-0058 I/O contract). */
export interface GreeterInput {
  name: string;
}

/** What greeter returns — the 200 JSON body. */
export interface GreeterOutput {
  greeting: string;
}

/**
 * greeter — the CALLEE in the fn-to-fn link example (ADR-0064). An ordinary function: it greets the
 * name on the incoming CloudEvent's data. It declares no links and doesn't know it's being called —
 * another function reaches it only through a declared link (see front.ts), never by name or address.
 */
export const handle: Handler<GreeterInput, GreeterOutput> = (context, event) => {
  const name = event.data?.name ?? 'world';
  context.log('greeting', name);
  return { greeting: `Hello, ${name}!` };
};
