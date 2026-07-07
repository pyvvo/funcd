// Contract-aware build for the s3-roundtrip example (ADR-0080/0085/0090): it bundles the handler +
// the @aws-sdk/client-s3 dependency to a single self-contained roundtrip.mjs AND — like kv-counter —
// bakes the FuncInput/FuncOutput contract into the bundle + writes roundtrip.schema.json, so the
// single-file push carries its mandatory `{input,output}` I/O contract (ADR-0090).
//   1. generate the closed JSON Schema from FuncInput/FuncOutput (ts-json-schema-generator),
//   2. compile a precompiled, eval-free AJV-standalone validator and BAKE it into the bundle as
//      __funcdValidateInput/__funcdValidateOutput (what the shim runs around the handler),
//   3. write roundtrip.schema.json (one `{"input":…,"output":…}` doc — ADR-0090) for
//      `funcdctl push --schema roundtrip.schema.json`.
// Unlike log-burst (a pure logging example), this example DEPENDS on a runtime library (the AWS SDK),
// so esbuild bundles it in — the resulting .mjs is fully self-contained and needs no node_modules at
// runtime in the sandbox.
//
// Run from the example dir with esbuild + @aws-sdk/client-s3 + the shim toolchain installed (see
// package.json `build`, which runs `npm install` first when needed).
import { unlinkSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { build } from 'esbuild';
import { buildContract } from '../../../shim/nodejs/src/build.ts';

const here = dirname(fileURLToPath(import.meta.url));
const src = join(here, 'src', 'roundtrip.ts');
const { validatorSource, inputSchema, outputSchema } = buildContract(src);

// Entry that re-exports the handler plus the baked validators, bundled into one self-contained .mjs
// (esbuild inlines AJV's runtime so the artifact is eval-free, no node_modules at runtime).
let entry = `export { handle } from ${JSON.stringify(src)};\n`;
if (validatorSource) {
  const vfile = join(here, '.roundtrip.validator.mjs');
  writeFileSync(vfile, validatorSource);
  entry += `export * from ${JSON.stringify(vfile)};\n`;
}
const entryFile = join(here, '.roundtrip.entry.ts');
writeFileSync(entryFile, entry);

await build({
  entryPoints: [entryFile],
  bundle: true,
  platform: 'node',
  format: 'esm',
  target: 'node22',
  outfile: join(here, 'roundtrip.mjs'),
  // The AWS SDK pulls in CJS deps (@smithy/*, @aws-crypto/*) that call require("buffer") etc. In an
  // ESM bundle esbuild rewrites those to a synthetic require that throws "Dynamic require of … is not
  // supported" at runtime. Re-establish a real `require` (via createRequire) so bundled CJS can load
  // Node builtins, and keep the Node builtins external (don't try to bundle node:* into the ESM file).
  banner: {
    js: "import { createRequire as __cr } from 'node:module'; const require = __cr(import.meta.url);",
  },
});

unlinkSync(entryFile); // drop the transient entry + validator scratch files
if (validatorSource) unlinkSync(join(here, '.roundtrip.validator.mjs'));

// ADR-0090: one mandatory `--schema` doc — both sides always present (a void side is {"type":"null"}).
const contract = { input: inputSchema, output: outputSchema };
writeFileSync(join(here, 'roundtrip.schema.json'), JSON.stringify(contract, null, 2) + '\n');
// eslint-disable-next-line no-console
console.log('built roundtrip.mjs' + (validatorSource ? ' (+ baked contract validators + roundtrip.schema.json)' : ' (no contract)'));
