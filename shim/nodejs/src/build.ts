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
  // strictSchema:false ⇒ AJV ignores the `discriminator` keyword we add for the gate (it validates
  // a tagged union via `oneOf` alone; `discriminator` is metadata the Go profile gate requires).
  const ajv = new Ajv({ code: { source: true, esm: true }, allErrors: true, strictSchema: false });
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
    const stripped = stripMeta(schema);
    discriminateUnions(stripped); // tagged `anyOf` → the profile's discriminated `oneOf`
    return stripped;
  } catch {
    return null; // the type is not declared in this module
  }
}

/** Rewrite every tagged `anyOf` into the profile's discriminated `oneOf` + `discriminator`
 *  (ADR-0058). ts-json-schema-generator emits a TS union as `anyOf`, but the gate accepts a union
 *  only as a tagged `oneOf`; when the branches share a required, distinct-`const` property (the
 *  discriminant) the two are equivalent, so convert in place (recursing all nested schemas). */
function discriminateUnions(node: unknown): void {
  if (Array.isArray(node)) {
    for (const child of node) discriminateUnions(child);
    return;
  }
  if (!node || typeof node !== 'object') return;
  const obj = node as Record<string, unknown>;
  for (const value of Object.values(obj)) discriminateUnions(value);
  if (Array.isArray(obj.anyOf)) {
    const tag = discriminatorTag(obj.anyOf);
    if (tag) {
      obj.oneOf = obj.anyOf;
      delete obj.anyOf;
      obj.discriminator = { propertyName: tag };
    }
  }
}

/** The property that discriminates an `anyOf`'s branches: present + required + a single `const` in
 *  every branch, with distinct const values. Returns null when the union isn't a clean tagged one. */
function discriminatorTag(branches: unknown[]): string | null {
  const objs = branches.map((b) => (b && typeof b === 'object' ? (b as Record<string, unknown>) : null));
  if (objs.some((b) => b === null)) return null;
  const firstProps = objs[0]?.properties as Record<string, unknown> | undefined;
  if (!firstProps) return null;
  for (const name of Object.keys(firstProps)) {
    const consts: string[] = [];
    const taggedByAll = objs.every((b) => {
      const props = b?.properties as Record<string, { const?: unknown }> | undefined;
      const required = b?.required as string[] | undefined;
      const field = props?.[name];
      if (!props || !Array.isArray(required) || !required.includes(name) || !field || !('const' in field)) {
        return false;
      }
      consts.push(JSON.stringify(field.const));
      return true;
    });
    if (taggedByAll && new Set(consts).size === consts.length) return name;
  }
  return null;
}

/** Drop JSON-Schema meta keys AJV/the gate don't need (the contract is the type shape). */
function stripMeta(schema: Schema): Schema {
  const { $schema, $ref, definitions, ...rest } = schema as Record<string, unknown>;
  void $schema;
  void $ref;
  void definitions;
  return rest as Schema;
}
