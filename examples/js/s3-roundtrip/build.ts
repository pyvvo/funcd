// Build for the s3-roundtrip example (ADR-0080/0085): bundle the handler + the @aws-sdk/client-s3
// dependency to a single self-contained roundtrip.mjs. Unlike log-burst (a pure logging example),
// this example DEPENDS on a runtime library (the AWS SDK), so esbuild bundles it in — the resulting
// .mjs is fully self-contained and needs no node_modules at runtime in the sandbox.
//
// Run from the example dir with esbuild + @aws-sdk/client-s3 installed (see package.json `build`,
// which runs `npm install` first when needed).
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { build } from 'esbuild';

const here = dirname(fileURLToPath(import.meta.url));
const src = join(here, 'src', 'roundtrip.ts');

await build({
  entryPoints: [src],
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

// eslint-disable-next-line no-console
console.log('built roundtrip.mjs');
