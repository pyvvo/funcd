// Build for the log-burst example (ADR-0081): bundle the handler to a single self-contained burst.mjs.
// This example's purpose is the log BURST, not an I/O contract, so the build is a plain esbuild
// bundle (no FuncInput/FuncOutput schema baking) — mirror examples/js/kv-counter/build.ts in shape,
// minus the contract step. funcdctl push burst.mjs ships it to the OCI layout the manifest points at.
//
// Run from the example dir with the shim's toolchain resolvable (see package.json `build`).
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { build } from 'esbuild';

const here = dirname(fileURLToPath(import.meta.url));
const src = join(here, 'src', 'burst.ts');

await build({
  entryPoints: [src],
  bundle: true,
  platform: 'node',
  format: 'esm',
  target: 'node22',
  outfile: join(here, 'burst.mjs'),
});

// eslint-disable-next-line no-console
console.log('built burst.mjs');
