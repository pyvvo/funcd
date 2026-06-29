# log-burst — function-log capture, Path B (ADR-0081)

A **TypeScript** function whose handler emits a **burst of ≥100 logs** in a single invocation — a mix
of `console.log` (structured, with an object arg), `console.warn`, and `console.error` — then returns
`{ emitted: <count> }`. It proves the runtime-shim's **Path B** log-capture producer: the shim patches
the function's `console.*` onto a side channel and the host captures each call as a structured record,
**channel-only** (never echoed to stdout, so Path A does not double-capture).

```
handler console.log/warn/error ──patched by shim──▶ fd 3  (crun)  ─┐
                                                    │  or UDS (containerd) ├─▶ host Reader ─▶ funcd-system log store
                                                    └──────────────────────┘   (NDJSON, channel-only)
```

## The capture wire (per ADR-0081)

Each `console.*` call becomes one NDJSON record:
`{"ts":<epoch ns>,"sev":"INFO|WARN|ERROR|DEBUG","body":"<first string arg>","attrs":{"args":"<all args, lossless>", …merged object keys},"inv":"","trace_id":"","span_id":"","funcd.source":"console"}`.
`debug→DEBUG`, `log`/`info`→`INFO`, `warn→WARN`, `error→ERROR`. The shim selects the channel from
`FUNCD_LOG_FD` (a numeric fd) or `FUNCD_LOG_SOCK` (a unix socket); with neither set, console behaves
normally (Path A / stdout).

## The handler

`handle(context, event)` loops over `max(100, items)` "items" emitting a structured
`console.log("processing item", {i, batch})` each, plus a `console.warn` every 10th and a
`console.error` every 25th, then a final `console.log("burst complete", …)` — totalling **well over 100**
log records (≈115 for the default 100 items). It returns `{ emitted }` (the exact count).

## Build it

```bash
node --experimental-strip-types build.ts   # → burst.mjs (run via the shim's toolchain; see package.json)
```

`build.ts` is a plain esbuild bundle (no I/O contract — the logs are the point), mirroring
`examples/js/kv-counter/build.ts` minus the contract step.

## Run it (the e2e)

The Lima/containerd e2e deploys `burst.yaml`, invokes the function once, and asserts **≥100** captured
log records land in the `funcd-system` log store with `funcd.source=console` and the right severities —
end-to-end proof of the Path B console producer on real containerd.
