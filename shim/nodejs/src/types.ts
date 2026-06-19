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

/** Json — the explicit "arbitrary JSON value" contract type (ADR-0058). Declare `FuncInput`/
 *  `FuncOutput = Json`, or a field `payload: Json`, when the shape is genuinely unknown; the
 *  generated contract is the empty schema `{}` (accepts any JSON). Typed as `unknown` so the
 *  handler must narrow before use — deliberately NOT the unsafe `any`. */
export type Json = unknown;

/** A single error from a precompiled validator. Shape kept loose (AJV vs pydantic differ); the
 *  shim only inspects the array length and echoes the errors as 422/500 details. */
export type ValidationError = unknown;

/** A precompiled, eval-free validator the push build inlines into the bundle from the author's
 *  FuncInput/FuncOutput type (ADR-0058). Returns [] when `data` is valid. The shim runs it; it
 *  never compiles a schema at runtime. */
export type Validator = (data: unknown) => ValidationError[];
