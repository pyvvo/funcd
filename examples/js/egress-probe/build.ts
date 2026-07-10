// Contract-aware build for the egress-probe example (ADR-0058/0090/0117): it
//   1. generates the closed JSON Schema from FuncInput/FuncOutput (ts-json-schema-generator),
//   2. compiles a precompiled, eval-free AJV-standalone validator and BAKES it into the bundle as
//      __funcdValidateInput/__funcdValidateOutput (what the shim runs around the handler),
//   3. writes probe.schema.json (one `{"input":…,"output":…}` doc, both sides — ADR-0090) for
//      `funcdctl push --schema probe.schema.json`.
//
// Run from the example dir with the shim's toolchain resolvable (see package.json `build`).
import { unlinkSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { build } from 'esbuild';
import { buildContract } from '../../../shim/nodejs/src/build.ts';

const here = dirname(fileURLToPath(import.meta.url));
const src = join(here, 'src', 'handler.ts');
const { validatorSource, inputSchema, outputSchema } = buildContract(src);

let entry = `export { handle } from ${JSON.stringify(src)};\n`;
if (validatorSource) {
  const vfile = join(here, '.probe.validator.mjs');
  writeFileSync(vfile, validatorSource);
  entry += `export * from ${JSON.stringify(vfile)};\n`;
}
const entryFile = join(here, '.probe.entry.ts');
writeFileSync(entryFile, entry);

await build({
  entryPoints: [entryFile],
  bundle: true,
  platform: 'node',
  format: 'esm',
  target: 'node22',
  outfile: join(here, 'probe.mjs'),
});

unlinkSync(entryFile);
if (validatorSource) unlinkSync(join(here, '.probe.validator.mjs'));

const contract = { input: inputSchema, output: outputSchema };
writeFileSync(join(here, 'probe.schema.json'), JSON.stringify(contract, null, 2) + '\n');
// eslint-disable-next-line no-console
console.log('built probe.mjs' + (validatorSource ? ' (+ baked contract validators + probe.schema.json)' : ' (no contract)'));
