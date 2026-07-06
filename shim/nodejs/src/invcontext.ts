// funcd per-invocation context carrier (ADR-0101). A tiny AsyncLocalStorage the shim sets around
// each handler call so BOTH the trace span (tracespan.ts) and the log capture (funclog.ts read it in
// buildRecord) tag their records with the SAME inv/trace/span ids — the logs↔trace correlation that
// closes ADR-0081's provenance open question. ADR-0081 specified this carrier but never shipped it
// (inv/trace_id/span_id were hardcoded empty); this is where it is introduced.
//
// Built-ins only (node:async_hooks) so it bundles into shim.mjs/pool.mjs.
import { AsyncLocalStorage } from 'node:async_hooks';

/** The identity of the currently-executing invocation. */
export interface InvContext {
  inv: string; // per-invocation id (hex16)
  traceId: string; // hex32 — adopted from traceparent or minted (root)
  spanId: string; // hex16 — this invocation's span
  parentId: string; // hex16 — the traceparent span-id, "" for a root
}

/** invStore holds the active InvContext for the duration of a handler call (and its async
 *  continuations). Per-thread: each worker/process imports its own instance, which is correct —
 *  the context is set and read within the same execution. */
export const invStore = new AsyncLocalStorage<InvContext>();

/** currentInv returns the active invocation context, or undefined outside an invocation (e.g. a
 *  pre-invocation log line) — in which case log records emit empty inv/trace/span (back-compat). */
export function currentInv(): InvContext | undefined {
  return invStore.getStore();
}
