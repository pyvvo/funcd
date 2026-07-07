// Contract-aware build for the workflow example (ADR-0058/0060/0094). Each step is an ordinary
// funcd Function: for every src/<step>.ts it generates the closed JSON Schema from
// FuncInput/FuncOutput, bakes an eval-free validator into <step>.mjs, and writes the mandatory
// {input, output} contract to <step>.schema.json (for `funcdctl push --schema`). The step artifacts
// are pushed with `--runtime nodejs22` so the workflow materializer resolves each step's runtime
// from the manifest alone; the Workflow (workflow.yaml) materializes them into owned Functions.
//
// Run from the example dir with the shim's toolchain on NODE_PATH (see package.json `build`):
//   NODE_PATH=../../../shim/nodejs/node_modules node --experimental-strip-types build.ts
import { unlinkSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { build } from 'esbuild';
import { buildContract } from '../../../shim/nodejs/src/build.ts';

const here = dirname(fileURLToPath(import.meta.url));

for (const step of ['ingest', 'score', 'hi', 'lo', 'report']) {
  const src = join(here, 'src', `${step}.ts`);
  const { validatorSource, inputSchema, outputSchema } = buildContract(src);

  let entry = `export { handle } from ${JSON.stringify(src)};\n`;
  if (validatorSource) {
    const vfile = join(here, `.${step}.validator.mjs`);
    writeFileSync(vfile, validatorSource);
    entry += `export * from ${JSON.stringify(vfile)};\n`;
  }
  const entryFile = join(here, `.${step}.entry.ts`);
  writeFileSync(entryFile, entry);

  await build({
    entryPoints: [entryFile],
    bundle: true,
    platform: 'node',
    format: 'esm',
    target: 'node22',
    outfile: join(here, `${step}.mjs`),
  });

  unlinkSync(entryFile);
  if (validatorSource) unlinkSync(join(here, `.${step}.validator.mjs`));

  const contract = { input: inputSchema, output: outputSchema };
  writeFileSync(join(here, `${step}.schema.json`), JSON.stringify(contract, null, 2) + '\n');
  // eslint-disable-next-line no-console
  console.log(`built ${step}.mjs` + (validatorSource ? ` (+ baked contract validators + ${step}.schema.json)` : ' (no contract)'));
}
