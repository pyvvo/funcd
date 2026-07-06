// funcd Node trace capture (ADR-0101). The runtime shim mints a per-invocation OTel SERVER span:
// it adopts an incoming W3C `traceparent` (else mints a root trace), runs the handler inside the
// invocation context, and emits ONE span record on the SAME funclog side channel as logs, tagged
// `"funcd.signal":"traces"`. The host demux (funclog.Route) routes it to the trace sink. Emitting
// the span is zero-touch for the function — the auto SERVER span, not an SDK-emitted tree.
//
// Built-ins only (node:crypto) so it bundles into shim.mjs/pool.mjs.
import { randomBytes } from 'node:crypto';

import { invStore, type InvContext } from './invcontext.ts';
import type { Sink } from './funclog.ts';

const ZERO_TRACE = '0'.repeat(32);
const ZERO_SPAN = '0'.repeat(16);

/** The span wire record (ADR-0101). Sibling to the log wire on the same channel; discriminated by
 *  `funcd.signal`. Field names match the Go decoder (internal/funclog spanWire). */
interface SpanRecord {
  'funcd.signal': 'traces';
  trace_id: string;
  span_id: string;
  parent_id: string;
  name: string;
  kind: 'SERVER';
  start: number; // epoch nanos (Date.now()*1e6 base, monotonic duration)
  end: number;
  status: 'OK' | 'ERROR';
  status_msg: string;
  attrs: Record<string, string>;
  inv: string;
}

/** parseTraceparent parses a W3C `traceparent` (`00-<trace32>-<span16>-<flags>`), returning the
 *  trace-id + parent span-id, or null when absent/malformed/all-zero (⇒ the caller mints a root). */
export function parseTraceparent(tp: string | undefined): { traceId: string; parentId: string } | null {
  if (!tp) return null;
  const parts = tp.trim().split('-');
  if (parts.length < 4) return null;
  const [version, traceId, parentId] = parts;
  if (!/^[0-9a-f]{2}$/.test(version) || version === 'ff') return null;
  if (!/^[0-9a-f]{32}$/.test(traceId) || traceId === ZERO_TRACE) return null;
  if (!/^[0-9a-f]{16}$/.test(parentId) || parentId === ZERO_SPAN) return null;
  return { traceId, parentId };
}

/** newInvContext establishes the invocation identity: adopt the traceparent's trace-id + parent
 *  span-id when present, else mint a root (fresh 16-byte trace). A fresh 8-byte span-id and inv id
 *  are always minted for this invocation. */
export function newInvContext(tp: string | undefined): InvContext {
  const adopted = parseTraceparent(tp);
  return {
    inv: randomBytes(8).toString('hex'),
    traceId: adopted ? adopted.traceId : randomBytes(16).toString('hex'),
    spanId: randomBytes(8).toString('hex'),
    parentId: adopted ? adopted.parentId : '',
  };
}

function emitSpan(sink: Sink, ctx: InvContext, name: string, start: number, end: number, status: 'OK' | 'ERROR', statusMsg: string): void {
  const rec: SpanRecord = {
    'funcd.signal': 'traces',
    trace_id: ctx.traceId,
    span_id: ctx.spanId,
    parent_id: ctx.parentId,
    name,
    kind: 'SERVER',
    start,
    end,
    status,
    status_msg: statusMsg,
    attrs: {},
    inv: ctx.inv,
  };
  try {
    sink(JSON.stringify(rec) + '\n');
  } catch {
    // span capture is best-effort: never let it break the function.
  }
}

/** A live invocation span: run the handler inside its context (so logs correlate), then end() it
 *  with the outcome (which emits the span record on the channel). end() is idempotent. */
export interface Span {
  readonly inv: InvContext;
  run<T>(fn: () => Promise<T> | T): Promise<T>;
  end(status: 'OK' | 'ERROR', statusMsg?: string): void;
}

/** startSpan opens a per-invocation SERVER span. `sink` null ⇒ the span is a no-op emitter (the
 *  context is still established so logs get ids). `name` is the function name (or "invoke"); `tp`
 *  is the incoming `traceparent` header. */
export function startSpan(sink: Sink | null, name: string, tp: string | undefined): Span {
  const inv = newInvContext(tp);
  const startNs = Date.now() * 1e6;
  const t0 = process.hrtime.bigint();
  let ended = false;
  return {
    inv,
    run<T>(fn: () => Promise<T> | T): Promise<T> {
      return invStore.run(inv, async () => fn());
    },
    end(status: 'OK' | 'ERROR', statusMsg = ''): void {
      if (ended) return;
      ended = true;
      if (!sink) return;
      const endNs = startNs + Number(process.hrtime.bigint() - t0);
      emitSpan(sink, inv, name, startNs, endNs, status, statusMsg);
    },
  };
}
