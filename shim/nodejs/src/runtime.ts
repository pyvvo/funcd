// Shared handler/contract resolution for the funcd Node shims (single-tenant shim.ts and the
// pooled pool.ts, ADR-0037/0038/0044). No HTTP, no run-guard — just the materialization shape-gate.
import { isSchema, type Schema, validate } from 'jtd';

import type { Handler } from './types.ts';

export type { Schema as EventSchema } from 'jtd';
export { validate };

/** resolveHandler picks the handler export: `<name>`, `default.<name>`, or `default`. A
 *  non-function (missing handler) throws — the materialization shape-gate (ADR-0030). */
export function resolveHandler(mod: Record<string, unknown>, name: string): Handler {
  const candidate =
    mod?.[name] ??
    (mod?.default as Record<string, unknown> | undefined)?.[name] ??
    mod?.default;
  if (typeof candidate !== 'function') {
    throw new Error(`export "${name}" is not a function`);
  }
  return candidate as Handler;
}

/** resolveSchema picks the optional `eventSchema` export — the function's event-data contract
 *  (JSON Type Definition, RFC 8927). Absent → no contract; present-but-malformed throws (ADR-0038). */
export function resolveSchema(mod: Record<string, unknown>): Schema | undefined {
  const schema = mod?.eventSchema;
  if (schema === undefined) return undefined;
  if (!isSchema(schema)) {
    throw new Error('export "eventSchema" is not a valid JTD schema');
  }
  return schema;
}
