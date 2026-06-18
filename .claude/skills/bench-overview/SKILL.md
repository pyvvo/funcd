---
name: bench-overview
description: Run the funcd benchmark & sustainability harness and regenerate the report. Use whenever the user wants to (re)measure or refresh sustainability/performance — "run the bench", "benchmark funcd", "is it still sustainable", "refresh the bench report/overview", "re-bench", "update bench-overview". It runs funcd-bench (ADR-0040) to produce the RAW report (docs/reports/report.{md,json}) and then regenerates the authored docs/reports/bench-overview.md FROM that raw output — the raw numbers are the source of truth; the overview is the human narrative derived from them.
---

# Benchmark overview — run the bench, regenerate the report

Two artifacts, one derives from the other:

| File | What | Produced by |
|---|---|---|
| `docs/reports/report.json` + `report.md` | the **RAW** machine output (per-substrate numbers) | `funcd-bench` (ADR-0040) — `just bench` |
| `docs/reports/pool-report.json` + `pool-report.md` | the **RAW** worker-pool comparison (density + throughput, pooled vs per-function) | `funcd-bench` (ADR-0044) — `just bench` |
| `docs/reports/bench-overview.md` | the **authored** narrative + verdict | **you**, derived from the raw reports |

The raw report is the source of truth. `bench-overview.md` interprets it — never invent a number that isn't in
`report.json`.

## Step 0 — Preconditions (node-gated)

The harness runs real JS, so it needs **node** on PATH and the bundled shim present:

```bash
command -v node            # required; without it the bench skips (say so, stop)
ls shim/nodejs/shim.mjs    # the embedded shim; if missing/stale, run: just build-shim
```

If `shim.mjs` is missing (or `shim/nodejs/src/*.ts` changed since it was built), run `just build-shim` first.

## Step 1 — Produce the RAW report

```bash
just bench    # = go run ./cmd/funcd-bench --out docs/reports
```

This writes `docs/reports/report.md` (a numbers table) and `docs/reports/report.json` (the machine data), one entry per
substrate (`memory`, `file`), **plus** `docs/reports/pool-report.{md,json}` — the worker-pool comparison (ADR-0044):
pooled (K handlers in one `worker_threads` process) vs per-function (separate shims), reporting **both** density
(MB/function) and throughput/tail-latency. Tune with flags if asked: `--concurrency`, `--duration`, `--density` (also the
pool size K), `--mem-budget-mb`, `--target-fns`, `--backends`, `--pool-shim`. **Sanity-check the run** before trusting it:

- **Port-exhaustion regression** — grep the bench's stderr for `can't assign requested address`. A non-trivial count
  means ADR-0041's pooling regressed (or the load is extreme); flag it, don't bury it.
- **Skipped substrate** — `funcd-bench`'s `Run` is resilient: a starved substrate is warn-skipped, so `report.json` may
  have **fewer than 2** entries. If so, say which substrate is missing and why (don't present a partial run as complete).

## Step 2 — Regenerate `bench-overview.md` FROM the raw report

Read `docs/reports/report.json` and refresh `docs/reports/bench-overview.md` so every number traces to the raw output:

1. **Headline table** — one column per substrate in `report.json`: throughput (`rps`), `latency.p99`/`p50`, platform
   baseline + per-sandbox + idle RSS, cold-start, marginal MB/function, max density, fits-target. (Durations in
   `report.json` are nanoseconds — render as ms.)
2. **Verdict** — recompute from the numbers: density (per-sandbox MB → MB for `target-fns`), headroom (`maxDensity` vs
   `target-fns`), idle-reclaim (idle ≈ 0), cold-start, throughput/tail. Keep the **method** + **what-the-bench-found**
   sections; update them only if the run changed the story.
   - **Worker-pooling section** — refresh from `pool-report.json`: the density gain (`densityGain`), the throughput cost
     (`throughputRetained` — pooled is a *fraction* of the single-tenant baseline, not parity), and the latency delta
     (`pooledP99` vs `singleDirectP99`). The throughput pair is **fair by construction** — both the pooled handler and
     the `singleDirect` baseline are hit directly on the shim port (no data plane), so the ratio isolates the
     `worker_threads` hop. Keep the trade honest: pooling buys memory density and *pays* a modest per-request hop; link
     `pool-report.md` for the full head-to-head. Don't present the density gain without its throughput cost, and don't
     compare pooled-direct against the end-to-end `rps` (that's the unfair mixed-path comparison this replaced).
3. **Honesty (do not skip)** — keep the caveats current: **process RSS is optimistic** vs cgroup; **bench numbers vary
   run-to-run** (don't over-claim "equal" off one favorable run — report what *this* run shows and note variance); the
   **file substrate's higher baseline RSS** (durable file-JetStream) + any residual throughput gap is **platform-side,
   not the invoke path** (the invoke path touches no blob/bus/store); **node-only** until the Python shim; **dev machine**.

Preserve the doc's structure and links; change the numbers and any claim the new run contradicts. Don't bloat it.

## Step 3 — Verify

```bash
python3 -c "import json;print(len(json.load(open('docs/reports/report.json'))),'substrate(s)')"  # matches the table?
python3 -c "import json;d=json.load(open('docs/reports/pool-report.json'));print('pool K=%d density=%.1fx tput=%.0f%%'%(d['k'],d['densityGain'],d['throughputRetained']*100))"
grep -c 'req/s' docs/reports/bench-overview.md   # table populated
grep -nE '/Users/[a-z]|/home/[a-z]' docs/reports/bench-overview.md docs/reports/report.json && echo "LEAK — fix before finishing" || echo "no absolute-path/username leak"
```

Confirm every number in `bench-overview.md` appears in (or is computed from) `report.json`. **No identity/path leak in
either file** (both are tracked): never write the local machine username or an absolute OS path — every path is
project-root-relative (`docs/reports/…`, `cmd/funcd-bench/…`), never `/Users/<user>/…` or `/home/<user>/…`; even in an
example or quoted command, write the token generically (`<user>`, `/Users/`). The raw report + overview are committed
together as a snapshot — re-running this skill refreshes both.

## Notes

- **The raw report is a committed snapshot**, regenerated by this skill — machine-specific numbers are expected to change;
  that's fine, it's a reference point, not a CI gate.
- This is **not** a CI gate (node-gated, dev-machine). A perf-regression gate with thresholds is a future follow-up
  (ADR-0040 open question) — this skill is the manual, on-demand refresh.
- Stays in its lane: it runs the existing harness (`cmd/funcd-bench`, ADR-0040) + authors the overview. It does **not**
  change the harness or decide architecture — if the bench needs new metrics, that's an ADR, not this skill.
