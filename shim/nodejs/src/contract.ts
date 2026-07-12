// Runtime-compiled I/O validators from the delivered contract schema (ADR-0123).
//
// The pushed artifact is schema-only — it carries the ADR-0059 {dialect, input, output} contract
// blob but NO baked __funcdValidate* callable (ADR-0123 supersedes ADR-0060's build-time bake). The
// materializer delivers the exact digest-pinned blob into the worker and points FUNCD_CONTRACT_PATH
// at it; the shim compiles a validator per side ONCE at worker warm-up via `ajv.compile` — over a
// schema contract.Check-gated at push and digest-pinned, BEFORE any handler module is imported (the
// bounded eval-free reversal, Decision 6). Each compiled validator is wrapped to the shim's
// `(data) => errors[]` shape ([] ⇒ valid), so createApp/the pool worker are unchanged. A void side
// is {"type":"null"} → its validator accepts only null (the shim normalizes an absent return to null).
//
// Fail-closed (no fail-open): loadFromPath throws ContractError when the file is missing, unparseable,
// or not a {input, output} document, so the worker exits 3 rather than serving un-validated. When
// FUNCD_CONTRACT_PATH is unset, loadValidators returns null and the caller falls back to the
// module-baked validators (transition back-compat).
import Ajv from 'ajv';
import { readFileSync } from 'node:fs';

import type { Validator } from './types.ts';

/** ContractError marks a delivered contract that could not be read/parsed/compiled — the worker
 *  must fail closed (never serve un-validated); the shim turns this into exit code 3. */
export class ContractError extends Error {}

// One Ajv instance per worker. strict:false so in-profile constructs (e.g. an ignored `format`
// keyword, or a `discriminator` on a tagged union) never throw at compile; allErrors so a 422/500
// carries every mismatch, matching the fastjsonschema side's error detail.
const ajv = new Ajv({ strict: false, allErrors: true });

function compileSide(schema: unknown): Validator {
  const validate = ajv.compile(schema as object);
  return (data: unknown) => (validate(data) ? [] : (validate.errors ?? []));
}

/** loadFromPath compiles both validators from the ADR-0059 contract blob at `path`. Throws
 *  ContractError on any gap (fail closed) — missing file, invalid JSON, a missing side, or a schema
 *  Ajv cannot compile. */
export function loadFromPath(path: string): { input: Validator; output: Validator } {
  let raw: string;
  try {
    raw = readFileSync(path, 'utf8');
  } catch (err) {
    throw new ContractError(`cannot read contract ${path}: ${err instanceof Error ? err.message : err}`);
  }
  let blob: { input?: unknown; output?: unknown };
  try {
    blob = JSON.parse(raw) as { input?: unknown; output?: unknown };
  } catch (err) {
    throw new ContractError(`contract ${path} is not valid JSON: ${err instanceof Error ? err.message : err}`);
  }
  if (blob === null || typeof blob !== 'object' || blob.input === undefined || blob.output === undefined) {
    throw new ContractError(`contract ${path} must carry both an "input" and an "output" schema (ADR-0090)`);
  }
  try {
    return { input: compileSide(blob.input), output: compileSide(blob.output) };
  } catch (err) {
    throw new ContractError(`contract ${path} failed to compile: ${err instanceof Error ? err.message : err}`);
  }
}

/** loadValidators returns the compiled validators from FUNCD_CONTRACT_PATH, or null when the env is
 *  unset (the caller falls back to module-baked validators). A set-but-broken path throws
 *  ContractError (fail closed). */
export function loadValidators(
  env: NodeJS.ProcessEnv,
): { input?: Validator; output?: Validator } | null {
  const path = env.FUNCD_CONTRACT_PATH;
  if (!path) return null;
  return loadFromPath(path);
}
