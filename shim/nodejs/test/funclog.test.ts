import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createServer, type Server, type Socket } from 'node:net';
import { mkdtempSync, openSync, readFileSync, closeSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { installConsoleCapture } from '../src/funclog.ts';

// snapshotConsole restores the five patched methods after installConsoleCapture() mutates the global,
// keeping the tests isolated.
function snapshotConsole(): () => void {
  const saved = {
    debug: console.debug,
    log: console.log,
    info: console.info,
    warn: console.warn,
    error: console.error,
  };
  return () => Object.assign(console, saved);
}

// parseLines splits an NDJSON buffer into decoded records (trailing newline tolerated).
function parseLines(buf: string): Record<string, unknown>[] {
  return buf
    .split('\n')
    .filter((l) => l.length > 0)
    .map((l) => JSON.parse(l) as Record<string, unknown>);
}

// scenario: console-no-double-capture (FUNCD_LOG_SOCK / UDS transport) — the patched console writes
// each call as ONE NDJSON line to the side channel, channel-only. We prove "no double-capture" by
// installing sentinel spies as the ORIGINAL console methods BEFORE patching: the patched wrappers
// write channel-only and never delegate, so the originals must stay untouched (zero stdout echo).
test('console-no-double-capture over FUNCD_LOG_SOCK: two NDJSON lines, body/sev/attrs correct, no original-console delegation', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'funcd-funclog-'));
  const sockPath = join(dir, 'log.sock');

  // a UDS server standing in for the host Reader: accumulate everything the shim writes. Track the
  // live connection so we can destroy it on teardown — otherwise server.close() blocks on it.
  let received = '';
  const conns: Socket[] = [];
  const server: Server = createServer((conn) => {
    conns.push(conn);
    conn.on('data', (chunk) => {
      received += chunk.toString('utf8');
    });
  });
  await new Promise<void>((resolve) => server.listen(sockPath, resolve));

  const restoreConsole = snapshotConsole();
  // sentinels standing in for the real stdout/stderr writers: if the patched console delegated to the
  // original method (a double-capture), these would fire. They must NOT.
  let originalCalls = 0;
  console.log = () => {
    originalCalls++;
  };
  console.error = () => {
    originalCalls++;
  };

  try {
    const installed = installConsoleCapture({ FUNCD_LOG_SOCK: sockPath } as NodeJS.ProcessEnv);
    assert.equal(installed, true, 'capture installs when FUNCD_LOG_SOCK is set');

    console.log('user', { id: 7 });
    console.error('boom');

    // give the async UDS write time to flush to the in-process server.
    await new Promise<void>((resolve) => setTimeout(resolve, 150));
  } finally {
    restoreConsole();
    for (const c of conns) c.destroy(); // drop live connections so close() can complete
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }

  // CRUCIAL: the patched console never called the original method → no Path A double-capture.
  assert.equal(originalCalls, 0, 'patched console writes channel-only — never delegates to stdout/stderr');

  const records = parseLines(received);
  assert.equal(records.length, 2, 'exactly two NDJSON lines, one per console call');

  const [first, second] = records;

  // console.log("user", {id:7}) → body "user", sev INFO, args lossless, object keys merged.
  assert.equal(first.body, 'user');
  assert.equal(first.sev, 'INFO');
  assert.equal(first['funcd.source'], 'console');
  const firstAttrs = first.attrs as Record<string, string>;
  assert.match(firstAttrs.args, /"id":7/, 'attrs.args preserves the object losslessly');
  assert.equal(firstAttrs.id, '7', 'plain-object arg keys merged to top level as strings');
  assert.equal(first.inv, '');
  assert.equal(first.trace_id, '');
  assert.equal(first.span_id, '');
  assert.equal(typeof first.ts, 'number');

  // console.error("boom") → body "boom", sev ERROR.
  assert.equal(second.body, 'boom');
  assert.equal(second.sev, 'ERROR');
});

// scenario: FUNCD_LOG_FD transport — the SYNCHRONOUS fd write path lands NDJSON on the given fd.
test('FUNCD_LOG_FD: synchronous write lands NDJSON on the fd, debug→DEBUG / warn→WARN', () => {
  const dir = mkdtempSync(join(tmpdir(), 'funcd-funclog-fd-'));
  const file = join(dir, 'channel.ndjson');
  const fd = openSync(file, 'w'); // a regular file fd is a fine stand-in for the pipe fd 3

  const restoreConsole = snapshotConsole();
  try {
    const installed = installConsoleCapture({ FUNCD_LOG_FD: String(fd) } as NodeJS.ProcessEnv);
    assert.equal(installed, true);
    console.debug('dbg', { phase: 'init' });
    console.warn('careful');
  } finally {
    restoreConsole();
    closeSync(fd);
  }

  const records = parseLines(readFileSync(file, 'utf8'));
  assert.equal(records.length, 2);
  assert.equal(records[0].body, 'dbg');
  assert.equal(records[0].sev, 'DEBUG');
  assert.equal((records[0].attrs as Record<string, string>).phase, 'init');
  assert.equal(records[1].body, 'careful');
  assert.equal(records[1].sev, 'WARN');
});

// scenario: no channel env → no capture (console stays as-is, → Path A / stdout).
test('no channel env → installConsoleCapture is a no-op (returns false, console untouched)', () => {
  const before = console.log;
  const installed = installConsoleCapture({} as NodeJS.ProcessEnv);
  assert.equal(installed, false);
  assert.equal(console.log, before, 'console.log not patched when no channel env is set');
});
