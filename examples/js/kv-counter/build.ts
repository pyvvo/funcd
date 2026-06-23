// Contract-aware build for the kv-counter example (ADR-0058/0060/0069): it
//   1. generates the closed JSON Schema from FuncInput/FuncOutput (ts-json-schema-generator),
//   2. compiles a precompiled, eval-free AJV-standalone validator and BAKES it into the bundle as
//      __funcdValidateInput/__funcdValidateOutput (what the shim runs around the handler),
//   3. writes counter-{input,output}.schema.json for `funcdctl push --contract-*`.
// So the kv-counter contract is *enforced* (bad input → 422, bad output → 500), same as fn-to-fn.
//
// Run from the example dir with the shim's toolchain resolvable (see package.json `build`).
import { unlinkSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { build } from 'esbuild';
import { buildContract } from '../../../shim/nodejs/src/build.ts';

const here = dirname(fileURLToPath(import.meta.url));
const src = join(here, 'src', 'counter.ts');
const { validatorSource, inputSchema, outputSchema } = buildContract(src);

// Entry that re-exports the handler plus the baked validators, bundled into one self-contained .mjs
// (esbuild inlines AJV's runtime so the artifact is eval-free, no node_modules at runtime).
let entry = `export { handle } from ${JSON.stringify(src)};\n`;
if (validatorSource) {
  const vfile = join(here, '.counter.validator.mjs');
  writeFileSync(vfile, validatorSource);
  entry += `export * from ${JSON.stringify(vfile)};\n`;
}
const entryFile = join(here, '.counter.entry.ts');
writeFileSync(entryFile, entry);

await build({
  entryPoints: [entryFile],
  bundle: true,
  platform: 'node',
  format: 'esm',
  target: 'node22',
  outfile: join(here, 'counter.mjs'),
});

unlinkSync(entryFile); // drop the transient entry + validator scratch files
if (validatorSource) unlinkSync(join(here, '.counter.validator.mjs'));

if (inputSchema) writeFileSync(join(here, 'counter-input.schema.json'), JSON.stringify(inputSchema, null, 2) + '\n');
if (outputSchema) writeFileSync(join(here, 'counter-output.schema.json'), JSON.stringify(outputSchema, null, 2) + '\n');
// eslint-disable-next-line no-console
console.log('built counter.mjs' + (validatorSource ? ' (+ baked contract validators + schemas)' : ' (no contract)'));
