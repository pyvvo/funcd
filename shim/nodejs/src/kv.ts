// context.kv — function-facing KV over the worker-node local API (HTTP-over-UDS, ADR-0069). Dials the
// same per-sandbox socket as context.invoke (FUNCD_INVOKE_SOCKET); the platform routes /kv/… to the
// PDP-authorized Facade with the sandbox's namespace identity. Reached only through the built-in shim.
import http from 'node:http';

interface Resp {
  status: number;
  body: Buffer;
}

function request(method: string, path: string, body?: Buffer): Promise<Resp> {
  return new Promise<Resp>((resolve, reject) => {
    const socketPath = process.env.FUNCD_INVOKE_SOCKET;
    if (!socketPath) {
      reject(new Error('context.kv: worker-node local API socket unavailable (FUNCD_INVOKE_SOCKET unset)'));
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

/** A function's view of its namespace-scoped KV (ADR-0069): get/put/del a binding's key, or list keys. */
export interface KVClient {
  get(binding: string, key: string): Promise<Uint8Array | null>;
  /** get() decoded as UTF-8 text — the common case (a missing key is null). Saves the caller a
   * TextDecoder dance; put() already accepts a string. */
  getText(binding: string, key: string): Promise<string | null>;
  /** get() decoded as UTF-8 text then JSON-parsed (a missing key is null). The structured-value
   * counterpart of getText; write with put(binding, key, JSON.stringify(value)). */
  getJSON<T = unknown>(binding: string, key: string): Promise<T | null>;
  put(binding: string, key: string, value: Uint8Array | string): Promise<void>;
  del(binding: string, key: string): Promise<void>;
  list(binding: string, prefix?: string): Promise<string[]>;
}

const enc = encodeURIComponent;
// key may be hierarchical ("a/b"); keep the "/" separators (the server's {key...} captures them), encode segments.
const keyPath = (binding: string, key: string) => `/kv/${enc(binding)}/${key.split('/').map(enc).join('/')}`;
const fail = (verb: string, r: Resp) => new Error(`context.kv.${verb} failed: ${r.status} ${r.body.toString('utf8')}`);
const ok = (r: Resp) => r.status >= 200 && r.status < 300;

/** Build the context.kv client. */
export function makeKV(): KVClient {
  return {
    async get(binding, key) {
      const r = await request('GET', keyPath(binding, key));
      if (r.status === 404) return null;
      if (!ok(r)) throw fail('get', r);
      return new Uint8Array(r.body);
    },
    async getText(binding, key) {
      const r = await request('GET', keyPath(binding, key));
      if (r.status === 404) return null;
      if (!ok(r)) throw fail('get', r);
      return r.body.toString('utf8');
    },
    async getJSON(binding, key) {
      const r = await request('GET', keyPath(binding, key));
      if (r.status === 404) return null;
      if (!ok(r)) throw fail('get', r);
      return JSON.parse(r.body.toString('utf8'));
    },
    async put(binding, key, value) {
      const buf = typeof value === 'string' ? Buffer.from(value, 'utf8') : Buffer.from(value);
      const r = await request('PUT', keyPath(binding, key), buf);
      if (!ok(r)) throw fail('put', r);
    },
    async del(binding, key) {
      const r = await request('DELETE', keyPath(binding, key));
      if (!ok(r)) throw fail('del', r);
    },
    async list(binding, prefix) {
      const q = prefix ? `?prefix=${enc(prefix)}` : '';
      const r = await request('GET', `/kv/${enc(binding)}${q}`);
      if (!ok(r)) throw fail('list', r);
      return JSON.parse(r.body.toString('utf8') || '[]') as string[];
    },
  };
}
