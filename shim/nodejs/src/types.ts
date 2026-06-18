// The funcd function programming model (typed contract). A function module exports
// `handle(context, event)`; the runtime shim invokes it once per CloudEvent. Authors
// import these types for a typed handler signature + autocomplete.

/** A CloudEvent — the normalized trigger envelope (ADR-0023). */
export interface CloudEvent<T = unknown> {
  id: string;
  source: string;
  type: string;
  specversion?: string;
  time?: string;
  datacontenttype?: string;
  subject?: string;
  data?: T;
  /** Forward-compatible extension attributes. */
  [key: string]: unknown;
}

/** The per-invocation context the shim passes to the handler. */
export interface FunctionContext {
  /** Structured log line → stdout (collected by the platform, ADR-0010). */
  log(...args: unknown[]): void;
}

/** A function handler: receives the context + CloudEvent, returns a response (or nothing). */
export type Handler<In = unknown, Out = unknown> = (
  context: FunctionContext,
  event: CloudEvent<In>,
) => Out | Promise<Out>;
