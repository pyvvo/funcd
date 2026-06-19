// funcd Node contract compiler (BUILD-TIME, ADR-0058/0060). The TS analog of funcd_build.py:
// for an author module declaring `FuncInput`/`FuncOutput` types it generates the JSON Schema
// (ts-json-schema-generator — closed records, no $ref) and compiles a precompiled validator
// (AJV-standalone) baked into the bundle as `__funcdValidateInput`/`__funcdValidateOutput`
// (`(data) => Error[]`, [] ⇒ valid) — exactly what shim.ts's resolveValidators reads.
//
// Runs on the push box; NOT bundled into shim.mjs. The TS interfaces vanish at compile (esbuild
// erases types), so unlike Python there is no source-stripping — only the validator is injected.
//
// (ADR-0060 names typia; typia needs a TS *transformer* (ts-patch) that does not compose with the
//  esbuild bundle. ts-json-schema-generator is the no-transformer equivalent — same "TS type ->
//  JSON Schema", same MIT licence; the runtime validator is still AJV-standalone as the ADR says.)
import Ajv from 'ajv';
import standaloneCode from 'ajv/dist/standalone/index.js';
import { createGenerator, type Schema } from 'ts-json-schema-generator';

export interface ContractBuild {
  /** an ESM module source defining the precompiled __funcdValidate* (null ⇒ no contract). */
  validatorSource: string | null;
  inputSchema: Schema | null;
  outputSchema: Schema | null;
}

/** Compile the FuncInput/FuncOutput contract of the TS module at `tsPath`. */
export function buildContract(tsPath: string): ContractBuild {
  const inputSchema = schemaFor(tsPath, 'FuncInput');
  const outputSchema = schemaFor(tsPath, 'FuncOutput');
  if (!inputSchema && !outputSchema) {
    return { validatorSource: null, inputSchema: null, outputSchema: null };
  }

  // Register each schema under an $id, then standalone-code by id (the multi-export API takes
  // schema ids, not compiled fns). standaloneCode emits `export const _funcdInput = …` — eval-free
  // (compiled here, at build).
  const ajv = new Ajv({ code: { source: true, esm: true }, allErrors: true });
  const refs: Record<string, string> = {};
  if (inputSchema) {
    ajv.addSchema({ ...(stripMeta(inputSchema) as object), $id: 'funcdInput' });
    refs._funcdInput = 'funcdInput';
  }
  if (outputSchema) {
    ajv.addSchema({ ...(stripMeta(outputSchema) as object), $id: 'funcdOutput' });
    refs._funcdOutput = 'funcdOutput';
  }
  const base = (standaloneCode as unknown as (a: Ajv, v: Record<string, string>) => string)(ajv, refs);
  const wrap = (name: string, fn: string): string =>
    `export function ${name}(d) { return ${fn}(d) ? [] : (${fn}.errors ?? []); }\n`;
  const validatorSource =
    base + '\n' +
    (inputSchema ? wrap('__funcdValidateInput', '_funcdInput') : '') +
    (outputSchema ? wrap('__funcdValidateOutput', '_funcdOutput') : '');

  return { validatorSource, inputSchema, outputSchema };
}

/** Generate the JSON Schema for `type` from `tsPath`, inlined (no $ref) + closed records — the
 *  funcd profile shape; returns null when the type is not declared. */
function schemaFor(tsPath: string, type: string): Schema | null {
  try {
    const schema = createGenerator({
      path: tsPath,
      type,
      additionalProperties: false, // closed records (the profile forbids open ones)
      topRef: false, // inline the root type (no $ref to a definition)
      expose: 'none', // inline nested types too (no $ref/$defs — the profile forbids $ref)
      skipTypeCheck: true,
    }).createSchema(type);
    return stripMeta(schema);
  } catch {
    return null; // the type is not declared in this module
  }
}

/** Drop JSON-Schema meta keys AJV/the gate don't need (the contract is the type shape). */
function stripMeta(schema: Schema): Schema {
  const { $schema, $ref, definitions, ...rest } = schema as Record<string, unknown>;
  void $schema;
  void $ref;
  void definitions;
  return rest as Schema;
}
