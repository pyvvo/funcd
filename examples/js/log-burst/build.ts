// Contract-aware build for the log-burst example (ADR-0081/0090): bundle the handler to a single
// self-contained burst.mjs AND bake its FuncInput/FuncOutput contract + write burst.schema.json, so
// the single-file push carries its mandatory `{input,output}` I/O contract (ADR-0090). The example's
// PURPOSE is still the log BURST, but a push now requires a contract, and burst.ts already declares a
// real, meaningful I/O shape (an optional {items,batch} in, {emitted} out — the count the e2e reads),
// so the honest contract is that typed shape (a void {"type":"null"} output would reject the
// {emitted} body at runtime). Mirror examples/js/kv-counter/build.ts:
//   1. generate the closed JSON Schema from FuncInput/FuncOutput (ts-json-schema-generator),
//   2. compile a precompiled, eval-free AJV-standalone validator baked into the bundle as
//      __funcdValidateInput/__funcdValidateOutput (what the shim runs around the handler),
//   3. write burst.schema.json (one `{"input":…,"output":…}` doc — ADR-0090) for
//      `funcdctl push --schema burst.schema.json`.
//
// Run from the example dir with the shim's toolchain resolvable (see package.json `build`).
import { unlinkSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { build } from 'esbuild';
import { buildContract } from '../../../shim/nodejs/src/build.ts';

const here = dirname(fileURLToPath(import.meta.url));
const src = join(here, 'src', 'burst.ts');
const { validatorSource, inputSchema, outputSchema } = buildContract(src);

// Entry that re-exports the handler plus the baked validators, bundled into one self-contained .mjs.
let entry = `export { handle } from ${JSON.stringify(src)};\n`;
if (validatorSource) {
  const vfile = join(here, '.burst.validator.mjs');
  writeFileSync(vfile, validatorSource);
  entry += `export * from ${JSON.stringify(vfile)};\n`;
}
const entryFile = join(here, '.burst.entry.ts');
writeFileSync(entryFile, entry);

await build({
  entryPoints: [entryFile],
  bundle: true,
  platform: 'node',
  format: 'esm',
  target: 'node22',
  outfile: join(here, 'burst.mjs'),
});

unlinkSync(entryFile); // drop the transient entry + validator scratch files
if (validatorSource) unlinkSync(join(here, '.burst.validator.mjs'));

// ADR-0090: one mandatory `--schema` doc — both sides always present (a void side is {"type":"null"}).
const contract = { input: inputSchema, output: outputSchema };
writeFileSync(join(here, 'burst.schema.json'), JSON.stringify(contract, null, 2) + '\n');
// eslint-disable-next-line no-console
console.log('built burst.mjs' + (validatorSource ? ' (+ baked contract validators + burst.schema.json)' : ' (no contract)'));
