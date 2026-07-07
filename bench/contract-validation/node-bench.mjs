// Validation-overhead microbench (Node, ADR-0058/0060). Measures the per-request cost of the
// precompiled AJV-standalone validator the build bakes — raw validator ns/op, and the full
// request path (JSON.parse -> [validate] -> handler -> JSON.stringify) with validation ON vs OFF.
// Run: `node node-bench.mjs`  (N overridable: `N=5000000 node node-bench.mjs`). Self-contained.
import { __funcdValidateInput as validate } from './node-validator.mjs';

const data = { orderId: 'abc-123', qty: 3, tags: ['x', 'y', 'z'] };
const body = JSON.stringify({ id: '1', source: 's', type: 't', data });
const N = Number(process.env.N || 2_000_000);

function reqPath(withValidation) {
  const event = JSON.parse(body);
  if (withValidation) {
    const errs = validate(event.data);
    if (errs.length) return 422;
  }
  const result = { accepted: event.data.qty > 0 }; // the "handler"
  return JSON.stringify(result);
}

function bench(fn, n) {
  for (let i = 0; i < 50_000; i++) fn(); // warm up (JIT)
  const t = process.hrtime.bigint();
  for (let i = 0; i < n; i++) fn();
  return Number(process.hrtime.bigint() - t) / n; // ns/op
}

const validatorNs = bench(() => validate(data), N);
const onNs = bench(() => reqPath(true), N);
const offNs = bench(() => reqPath(false), N);

console.log(JSON.stringify({
  runtime: 'node',
  version: process.version,
  N,
  validator_ns: +validatorNs.toFixed(1),
  reqpath_off_ns: +offNs.toFixed(1),
  reqpath_on_ns: +onNs.toFixed(1),
  reqpath_overhead_ns: +(onNs - offNs).toFixed(1),
  reqpath_overhead_pct: +((100 * (onNs - offNs)) / offNs).toFixed(1),
  rps_off: Math.round(1e9 / offNs),
  rps_on: Math.round(1e9 / onNs),
}));
