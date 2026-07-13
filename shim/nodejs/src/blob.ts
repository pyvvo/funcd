// context.blob — function-facing blob over the worker-node local API (HTTP-over-UDS, ADR-0127). Dials the
// same per-sandbox socket as context.kv/context.invoke (FUNCD_INVOKE_SOCKET); the platform routes /blob/…
// to the binding-gated, PDP-authorized Facade with the sandbox's function identity (bind-as-grant on
// spec.blob). Reached only through the built-in shim — no @aws-sdk, no keypair. The blob twin of context.kv;
// v1 is bytes-in-memory (streaming is a v2 follow-up).
import http from 'node:http';

interface Resp {
  status: number;
  body: Buffer;
}

function request(method: string, path: string, body?: Buffer): Promise<Resp> {
  return new Promise<Resp>((resolve, reject) => {
    const socketPath = process.env.FUNCD_INVOKE_SOCKET;
    if (!socketPath) {
      reject(new Error('context.blob: worker-node local API socket unavailable (FUNCD_INVOKE_SOCKET unset)'));
      return;
    }
    const headers: Record<string, number> = {};
    if (body) headers['content-length'] = body.byteLength;
    const req = http.request({ socketPath, path, method, headers }, (res) => {
      const chunks: Buffer[] = [];
      res.on('data', (c: Buffer) => chunks.push(c));
      res.on('end', () => resolve({ status: res.statusCode ?? 0, body: Buffer.concat(chunks) }));
    });
    req.on('error', reject);
    if (body) req.end(body);
    else req.end();
  });
}

/** Options for a presigned URL: the HTTP method it grants (default GET) and its lifetime (a Go duration
 * string, e.g. "15m"; the driver's default when omitted). A PUT/DELETE URL requires s3::write. */
export interface SignOptions {
  method?: 'GET' | 'PUT' | 'DELETE';
  expiry?: string;
}

/** A function's binding-scoped blob storage (ADR-0127): get/put/delete a bound prefix's objects, list
 * keys, or mint a presigned URL — the blob twin of KVClient. */
export interface BlobClient {
  get(binding: string, key: string): Promise<Uint8Array | null>;
  put(binding: string, key: string, value: Uint8Array): Promise<void>;
  del(binding: string, key: string): Promise<void>;
  list(binding: string, prefix?: string): Promise<string[]>;
  signedUrl(binding: string, key: string, opts?: SignOptions): Promise<string>;
}

const enc = encodeURIComponent;
// key may be hierarchical ("a/b"); keep the "/" separators (the server's {key...} captures them), encode segments.
const keyPath = (binding: string, key: string) => `/blob/${enc(binding)}/${key.split('/').map(enc).join('/')}`;
const fail = (verb: string, r: Resp) => new Error(`context.blob.${verb} failed: ${r.status} ${r.body.toString('utf8')}`);
const ok = (r: Resp) => r.status >= 200 && r.status < 300;

/** Build the context.blob client. */
export function makeBlob(): BlobClient {
  return {
    async get(binding, key) {
      const r = await request('GET', keyPath(binding, key));
      if (r.status === 404) return null;
      if (!ok(r)) throw fail('get', r);
      return new Uint8Array(r.body);
    },
    async put(binding, key, value) {
      const r = await request('PUT', keyPath(binding, key), Buffer.from(value));
      if (!ok(r)) throw fail('put', r);
    },
    async del(binding, key) {
      const r = await request('DELETE', keyPath(binding, key));
      if (!ok(r)) throw fail('del', r);
    },
    async list(binding, prefix) {
      const q = prefix ? `?prefix=${enc(prefix)}` : '';
      const r = await request('GET', `/blob/${enc(binding)}${q}`);
      if (!ok(r)) throw fail('list', r);
      return JSON.parse(r.body.toString('utf8') || '[]') as string[];
    },
    async signedUrl(binding, key, opts) {
      let path = `${keyPath(binding, key)}?sign=1&method=${enc(opts?.method ?? 'GET')}`;
      if (opts?.expiry) path += `&expiry=${enc(opts.expiry)}`;
      const r = await request('GET', path);
      if (!ok(r)) throw fail('signedUrl', r);
      return r.body.toString('utf8');
    },
  };
}
