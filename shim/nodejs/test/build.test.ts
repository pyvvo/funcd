import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { buildContract } from '../src/build.ts';

const FIXTURE =
  'export interface FuncInput { orderId: string; qty: number }\n' +
  'export interface FuncOutput { accepted: boolean }\n' +
  'export function handle() {}\n';

interface ObjSchema {
  additionalProperties?: unknown;
  properties: Record<string, { type: string }>;
}

// scenario: validator-generated-from-schema (Node) — a TS type -> closed JSON Schema +
// a precompiled AJV-standalone __funcdValidate* that the shim calls ([] = valid).
test('buildContract emits closed schemas + a working precompiled validator', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'funcd-build-'));
  const tsPath = join(dir, 'fn.ts');
  writeFileSync(tsPath, FIXTURE);

  const r = buildContract(tsPath);
  const input = r.inputSchema as unknown as ObjSchema;
  const output = r.outputSchema as unknown as ObjSchema;

  // records are CLOSED, types derived from the TS interfaces (ts-json-schema-generator).
  assert.equal(input.additionalProperties, false);
  assert.equal(input.properties.qty.type, 'number');
  assert.equal(output.properties.accepted.type, 'boolean');
  assert.ok(r.validatorSource && r.validatorSource.includes('__funcdValidateInput'));

  // the precompiled validator works — write it where `ajv` resolves (the package dir) + import it.
  const vfile = join(process.cwd(), `.tmp-validator-${process.pid}.mjs`);
  writeFileSync(vfile, r.validatorSource as string);
  try {
    const mod = (await import(pathToFileURL(vfile).href)) as {
      __funcdValidateInput: (d: unknown) => unknown[];
      __funcdValidateOutput: (d: unknown) => unknown[];
    };
    assert.deepEqual(mod.__funcdValidateInput({ orderId: 'a', qty: 3 }), [], 'valid input -> []');
    assert.ok(mod.__funcdValidateInput({ orderId: 'a', qty: 'no' }).length > 0, 'wrong type -> errors');
    assert.ok(mod.__funcdValidateInput({ orderId: 'a' }).length > 0, 'missing field -> errors');
    assert.deepEqual(mod.__funcdValidateOutput({ accepted: true }), [], 'valid output -> []');
    assert.ok(mod.__funcdValidateOutput({ accepted: 'yes' }).length > 0, 'wrong output -> errors');
  } finally {
    rmSync(vfile, { force: true });
    rmSync(dir, { recursive: true, force: true });
  }
});

// no contract declared -> nothing baked.
test('buildContract with no FuncInput/FuncOutput bakes nothing', () => {
  const dir = mkdtempSync(join(tmpdir(), 'funcd-build-'));
  const tsPath = join(dir, 'fn.ts');
  writeFileSync(tsPath, 'export function handle() { return { ok: true }; }\n');
  try {
    const r = buildContract(tsPath);
    assert.equal(r.validatorSource, null);
    assert.equal(r.inputSchema, null);
    assert.equal(r.outputSchema, null);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

// scenario: discriminated-union-contract (Node) — a TS union -> the profile's tagged `oneOf` +
// `discriminator` (ts-json-schema-generator emits `anyOf`; the build converts it, ADR-0058) and a
// validator that accepts the right branch and rejects a bad tag / an extra property.
test('buildContract converts a tagged union to a discriminated oneOf + working validator', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'funcd-build-'));
  const tsPath = join(dir, 'fn.ts');
  writeFileSync(
    tsPath,
    'export interface FuncInput { qty: number }\n' +
      'export type FuncOutput =\n' +
      "  | { kind: 'accepted'; id: string }\n" +
      "  | { kind: 'rejected'; reason: string };\n" +
      'export function handle() {}\n',
  );

  const r = buildContract(tsPath);
  const out = r.outputSchema as unknown as {
    anyOf?: unknown;
    oneOf?: unknown[];
    discriminator?: { propertyName: string };
  };

  assert.equal(out.anyOf, undefined, 'no bare anyOf (the gate rejects it)');
  assert.equal(Array.isArray(out.oneOf), true, 'a oneOf union');
  assert.equal(out.discriminator?.propertyName, 'kind', 'discriminated on the tag field');

  const vfile = join(process.cwd(), `.tmp-validator-u-${process.pid}.mjs`);
  writeFileSync(vfile, r.validatorSource as string);
  try {
    const mod = (await import(pathToFileURL(vfile).href)) as {
      __funcdValidateOutput: (d: unknown) => unknown[];
    };
    assert.deepEqual(mod.__funcdValidateOutput({ kind: 'accepted', id: 'x' }), [], 'right branch -> []');
    assert.ok(mod.__funcdValidateOutput({ kind: 'nope' }).length > 0, 'bad tag -> errors');
    assert.ok(
      mod.__funcdValidateOutput({ kind: 'accepted', id: 'x', extra: 1 }).length > 0,
      'extra property -> errors (closed branch)',
    );
  } finally {
    rmSync(vfile, { force: true });
    rmSync(dir, { recursive: true, force: true });
  }
});
