// funcd Node Path-B log capture (ADR-0081). The runtime shim is the Path-B *producer*: it patches
// the function's `console.*` so each call becomes one NDJSON record written to a side channel (fd or
// UDS), channel-only — never echoed to fd 1/2 — so Path A (raw stdout/stderr) never re-captures a
// Path B line. The host `Reader` splits the channel on `\n` and decodes each line; both languages
// (Node + Python) emit the SAME wire, so the reader is language-agnostic.
//
// Channel selection at startup (env, in order):
//   FUNCD_LOG_FD   = a numeric fd (e.g. "3")  → fs.writeSync(fd, line)  [SYNCHRONOUS — minimal crash-tail loss]
//   FUNCD_LOG_SOCK = a unix socket path        → net.connect(path), write lines to it
//   neither set                                → no capture; console stays as-is (Path A / stdout)
//
// Pure Node, built-ins only (node:fs, node:net) — no npm deps, so it bundles into shim.mjs/pool.mjs.
import { writeSync } from 'node:fs';
import { connect, type Socket } from 'node:net';

/** The NDJSON record shape the host Reader parses (ADR-0081, "Harness capture contract"). All
 *  attr VALUES are strings — the host decodes `attrs` as map[string]string. */
interface LogRecord {
  ts: number; // epoch nanos (Date.now() ms → ns)
  sev: 'DEBUG' | 'INFO' | 'WARN' | 'ERROR';
  body: string; // first arg IFF it is a string, else ""
  attrs: Record<string, string>;
  inv: string;
  trace_id: string;
  span_id: string;
  'funcd.source': 'console';
}

type ConsoleMethod = 'debug' | 'log' | 'info' | 'warn' | 'error';

const SEVERITY: Record<ConsoleMethod, LogRecord['sev']> = {
  debug: 'DEBUG',
  log: 'INFO',
  info: 'INFO',
  warn: 'WARN',
  error: 'ERROR',
};

/** A channel sink: write one already-framed line (`…\n`) to the side channel. */
type Sink = (line: string) => void;

/** safeStringify serializes an arbitrary value to JSON, tolerating circular refs and BigInt; any
 *  value that still can't be represented falls back to String(x). Used for the lossless attrs.args
 *  and for stringifying non-string attr values. */
function safeStringify(value: unknown): string {
  const seen = new WeakSet<object>();
  try {
    return JSON.stringify(value, (_k, v) => {
      if (typeof v === 'bigint') return v.toString();
      if (typeof v === 'object' && v !== null) {
        if (seen.has(v)) return '[Circular]';
        seen.add(v);
      }
      return v as unknown;
    });
  } catch {
    try {
      return String(value);
    } catch {
      return '[Unserializable]';
    }
  }
}

/** isPlainObject — a plain `{}`-style object whose own string-keyed entries we merge to attrs top
 *  level for queryability (not arrays, null, Dates, Errors, etc.). */
function isPlainObject(v: unknown): v is Record<string, unknown> {
  if (typeof v !== 'object' || v === null) return false;
  const proto = Object.getPrototypeOf(v) as object | null;
  return proto === Object.prototype || proto === null;
}

/** buildRecord maps a console call (method + raw args, captured BEFORE util.format) to the wire
 *  record per the contract: body = first string arg else ""; attrs.args = a lossless JSON-safe
 *  representation of ALL args; each plain-object arg's own string-valued keys merged to the top
 *  level; ALL attr values are strings. */
function buildRecord(method: ConsoleMethod, args: unknown[]): LogRecord {
  const body = typeof args[0] === 'string' ? args[0] : '';

  const attrs: Record<string, string> = { args: safeStringify(args) };
  for (const arg of args) {
    if (!isPlainObject(arg)) continue;
    for (const [k, v] of Object.entries(arg)) {
      if (k === 'args') continue; // never clobber the lossless capture
      attrs[k] = typeof v === 'string' ? v : safeStringify(v);
    }
  }

  return {
    ts: Date.now() * 1e6,
    sev: SEVERITY[method],
    body,
    attrs,
    inv: '',
    trace_id: '',
    span_id: '',
    'funcd.source': 'console',
  };
}

/** openSink resolves the side channel from the env contract, returning a synchronous-ish line sink
 *  (fd: truly synchronous write; UDS: net.Socket.write), or null when no channel env is set. */
function openSink(env: NodeJS.ProcessEnv): Sink | null {
  const fdRaw = env.FUNCD_LOG_FD;
  if (fdRaw !== undefined && fdRaw !== '') {
    const fd = Number(fdRaw);
    if (!Number.isInteger(fd) || fd < 0) return null;
    return (line) => {
      try {
        writeSync(fd, line);
      } catch {
        // a closed/broken channel must never crash the function — drop the line.
      }
    };
  }

  const sockPath = env.FUNCD_LOG_SOCK;
  if (sockPath !== undefined && sockPath !== '') {
    let sock: Socket | null = connect(sockPath);
    // a connection error must never crash the function; drop the channel on failure.
    sock.on('error', () => {
      sock = null;
    });
    sock.unref(); // don't keep the process alive on the log channel alone
    return (line) => {
      try {
        sock?.write(line);
      } catch {
        /* drop */
      }
    };
  }

  return null;
}

/** installConsoleCapture patches console.debug/log/info/warn/error to emit one NDJSON record per
 *  call to the side channel selected by env — CHANNEL-ONLY (no fd 1/2 echo, so Path A never
 *  double-captures). When no channel env is set it does nothing (console behaves normally → Path A).
 *  Returns true if capture was installed. The `env` arg is for testing; defaults to process.env. */
export function installConsoleCapture(env: NodeJS.ProcessEnv = process.env): boolean {
  const sink = openSink(env);
  if (!sink) return false;

  const methods: ConsoleMethod[] = ['debug', 'log', 'info', 'warn', 'error'];
  for (const method of methods) {
    console[method] = (...args: unknown[]): void => {
      try {
        sink(JSON.stringify(buildRecord(method, args)) + '\n');
      } catch {
        // capture must be best-effort: never let a logging failure break the function.
      }
    };
  }
  return true;
}
