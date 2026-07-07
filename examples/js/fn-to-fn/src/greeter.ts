import type { Handler } from '@funcd/shim-nodejs';

/**
 * What greeter accepts — the CloudEvent `data` (ADR-0058 I/O contract). The push build generates a
 * closed JSON Schema from this type and bakes an eval-free validator the shim runs BEFORE the
 * handler: a wrong-shaped input (missing/!string `name`) is rejected with **422** and never reaches
 * `handle`. `name` is required.
 */
export interface FuncInput {
  name: string;
}

/** What greeter returns — the 200 JSON body (also validated; a bad return is a 500). */
export interface FuncOutput {
  greeting: string;
}

/**
 * greeter — the CALLEE in the fn-to-fn link example (ADR-0064). An ordinary function; another
 * function reaches it only through a declared link (see front.ts). The shim already validated
 * `event.data` against FuncInput, so `name` is present and a string here.
 */
export const handle: Handler<FuncInput, FuncOutput> = (context, event) => {
  const name = event.data!.name;
  context.log('greeting', name);
  return { greeting: `Hello, ${name}!` };
};
