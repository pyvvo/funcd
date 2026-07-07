// Shared handler/contract resolution for the funcd Node shims (single-tenant shim.ts and the
// pooled pool.ts, ADR-0037/0058/0044). No HTTP, no run-guard — just the materialization shape-gate.
import type { Handler, Validator } from './types.ts';

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

/** resolveValidators picks the optional precompiled, eval-free validators the push build inlined
 *  into the bundle from the author's FuncInput/FuncOutput types (ADR-0058): `__funcdValidateInput`
 *  / `__funcdValidateOutput`. Absent ⇒ that side is unchecked. The shim runs them; it compiles no
 *  schema at runtime (the validator was compiled at push — no `new Function`/`eval` in the worker). */
export function resolveValidators(mod: Record<string, unknown>): { input?: Validator; output?: Validator } {
  const pick = (v: unknown): Validator | undefined => (typeof v === 'function' ? (v as Validator) : undefined);
  return { input: pick(mod?.__funcdValidateInput), output: pick(mod?.__funcdValidateOutput) };
}
