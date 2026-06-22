// Build the kv-counter handler → counter.mjs (esbuild; no I/O contract — KV needs none).
import { build } from 'esbuild';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
await build({
  entryPoints: [join(here, 'src', 'counter.ts')],
  bundle: true,
  platform: 'node',
  format: 'esm',
  target: 'node22',
  outfile: join(here, 'counter.mjs'),
});
// eslint-disable-next-line no-console
console.log('built counter.mjs');
