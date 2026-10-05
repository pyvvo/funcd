# ADR-0168: Raw output through two pipes funcd reads, and a bound on one log record

- **Status**: Accepted (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: observability, logs, runtime, process, containerd, shim, funclog
- **Realizes**: [FEAT-0004/F50](../feat/0004-feat-platform-observability.md) (function-log capture, ADR-0081's row)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0168` back-link at acceptance:
  [ADR-0011](0011-runtime-sandbox-port.md) scenario `sandbox-logs-captured` (:68-69, `Logs` now reads the last 64 KiB),
  Scope In (:100-101, :107-108), Decision 1 (:183), 2 (:193-194), 3 (:205-206), Contracts (:270 `LogPath`), plan
  (:341-342), checklist (:373): two pipes, `WorkerSpec.LogPath` removed; [ADR-0045](0045-rename-sandbox-to-worker.md)
  Contracts (:141) and [ADR-0143](0143-redeploy-by-revision-switch.md) Contracts (:183): `LogPath`;
  [ADR-0081](0081-function-log-capture-side-channel-blob.md) Decision, Capture (:112), Dependencies & I/O, Wire
  contract (:252), Harness `→ body` (:263) and `→ attrs` (:264, lossless `attrs.args`; Python's `logger`/`funcName`):
  bounded, marked records; Config comment (:238) and Open question (:365): capture off or no bucket ⇒ tail only (OQ 8).
- **Relates to**: [ADR-0030](0030-function-execution-runtime-shim-node.md) §4b (:177-184) ·
  [ADR-0101](0101-trace-capture-invocation-span.md) (:116, :152) · [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md)
  · [ADR-0084](0084-funclog-read-funcdctl-logs.md) · [ADR-0152](0152-runtime-worker-owner-kind.md) (Proposed;
  `LogPath` :169, `OwnerKind` :161; takes this ADR's change while not Accepted, else those clauses join Supersedes) ·
  ADR-0158 (Proposed; `funcd.member`; carries this ADR's change: Scope Out, `PoolMembers` names)
  · [ADR-0160](0160-worker-exit-reason.md) (Proposed) and [ADR-0167](0167-process-worker-crash-recovery.md) (Proposed:
  restart reap, boot sweep; lifeline deferred to a board card) also edit process `Start`/`wait` (`process.go:172-190`),
  the port file, containerd `Create` and `Stop`: the later PR rebases; `fifoDir` takes 0160's private `os.MkdirTemp`
  fallback (Decision 1, containerd) · [ADR-0151](0151-external-invoke-deadline.md) (Proposed; `pool.ts` call timeout)
  and [ADR-0165](0165-fn-to-fn-trace-propagation.md) (Proposed; `tracespan`, `makeInvoke` in `shim.ts`/`pool.ts`) edit
  the same shim sources and rebuild `shim.mjs`/`pool.mjs`: the later release rebases.

## Context & Need

Lines at main 1193be6 (origin/main shifts `containerd_linux.go`, `funcd.go`, `pool.go` slightly; `shimEnv` is
`function.go:1694` from #605). **#78**: funcd ingests only the shim's NDJSON channel (Path B, `runtime.go:111-124`,
`funcd.go:990-1020`); both drivers write fd 1 and fd 2 into one file (`process.go:124-125`, `containerd_linux.go:308`
`cio.LogFile`) read only for load errors (`function.go:1467-1490`), truncated on restart, deleted on re-create;
`funclog.NewRawReader` has no caller. `console.log('a')` then `process.stderr.write('b')` shows only `a`; Python's
`ctx.log` is lost. **funcd-typescript#31**: both shims serialize the whole value first (`funclog.ts:115`, `:219`;
`funclog.py:145`, `:197`): 16 shared levels give a 3.5 MB line in 30 ms (depth 20: 56 MB, 453 ms), and the host drops
a line over 1 MiB (`reader.go:13-14`, `route.go:82-85`).

**Purpose**: stdout and stderr reach `funcdctl logs` as INFO and ERROR without making the function wait; one log call
yields one bounded record, never a lost one. Callers: function authors; the reconciler's load-error read.

## Scenarios

- `scenario: raw-output-severity` — Given a Node handler calling `console.log('a')`, `process.stdout.write('b\n')`,
  `process.stderr.write('c\n')`, and a Python one calling `ctx.log('b')`, `print('d')`, `print('c', file=sys.stderr)`,
  When invoked, Then, while each runs, `funcdctl logs` shows `b` at INFO (`source=stdout`), `c` at ERROR
  (`source=stderr`), Node's `a` once at INFO, Python's `d` at INFO. Test: `TestScenarioRawOutputSeverity`.
- `scenario: load-error-in-logs` — Given a module that writes `booting` to stdout then throws at load, When deployed,
  Then `ShapeValid` is False with the shim's error line (not `booting`), shown at ERROR in `funcdctl logs`. Test:
  `TestScenarioLoadErrorInLogs`, also with `-count=20`.
- `scenario: raw-output-survives-restart` — Given a worker that prints `run-1` and exits 1, When restarted and it
  prints `run-2`, Then `funcdctl logs` shows both. Test: `TestScenarioRawOutputSurvivesRestart`.
- `scenario: raw-output-never-blocks` — Given a hook that takes both Readers and never reads, When a worker writes
  8 MiB to stdout and exits 0, Then it is Stopped within 5 s and `Dropped()` > 0. Test: `runtimecontract` subtest
  `raw-output-never-blocks`, both drivers.
- `scenario: record-cut-at-bound` — Given the default bound, When a handler logs `('big', obj)`, `obj` 3.5 MB, Then
  one record is stored with `attrs.truncated` `"true"`, `attrs.keptBytes` ≤ 65536, `sev` and `body` intact, `obj` not
  read past the budget. Tests: `TestScenarioRecordCutAtBound`; funcd-typescript `'scenario record-cut-at-bound: …'`;
  funcd-python `test_scenario_record_cut_at_bound`.
- `scenario: record-bound-reaches-shim` — Given `funclog.maxRecordBytes: 8192`, When a worker logs 100 KB, Then its
  env has `FUNCD_FUNCLOG_MAX_RECORD_BYTES=8192` and the record is cut at 8192. Tests: `TestScenarioRecordBoundReachesShim`;
  funcd-typescript `'scenario record-bound-reaches-shim: …'`; funcd-python `test_scenario_record_bound_reaches_shim`.
- `scenario: record-bound-over-reader-cap` — Given `funclog.maxRecordBytes: 2097152`, When funcd starts, Then it fails
  with `fault.Invalid` naming the key and the 1048576 cap. Test: `TestScenarioRecordBoundOverReaderCap`.
- `scenario: pooled-raw-output` (Open question 1) — Given `a`, `b` in one Node pool worker and `p`, `q` in one Python
  pool worker, When `a` writes `x` to stderr and `p` calls `print('y')`, Then, while each runs, `funcdctl logs a`/`b`
  show `x` at ERROR and `p`/`q` show `y` at INFO. Test: `TestScenarioPooledRawOutput`.

## Scope

In: fd 1/fd 2 to funclog, both drivers; `Runtime.Logs`' tail; no-read and funcd-restart behavior; record bound, both
shims' bounded serializers, cut marker, span records' bound; shim releases, the pin bump. Out: Path B transport
(ADR-0081, ADR-0101); lifeline (deferred by ADR-0167 to a board card); an engine's raw output (Open question 15); wazero.

## Constraints & Decision drivers

Path A never makes a function wait; RAM-bound host (~100 workers): no process per worker, bounded buffers; serializer
cost follows the bound. ADR-0081's fd 1 → INFO `source=stdout`, fd 2 → ERROR `source=stderr`; ADR-0030 §4b; ADR-0141
releases; no new module.

## Alternatives considered

- Chosen: two pipes funcd reads (FIFOs under containerd); a bounded queue that drops and counts; the shim stops at the
  budget and marks the cut.
- Rejected: containerd `binary://` logger (a process per worker); tailing the merged file with one severity (crash
  trace as INFO); re-attach via `cio.NewAttach` (Open question 11); backpressure (a slow sink stalls the function);
  serialize then cut (453 ms, 56 MB); lossless `attrs.args`; a bound fixed in the wire contract (the decider wants it tunable).

## Decision

1. **Raw output: two pipes per worker run, read as written** (`internal/runtime/workerpipe`, new). One `Output` per
   run holds funcd's end of fd 1 and fd 2. A write never blocks: it splits lines (over 64 KiB into 64 KiB lines),
   appends to a 64 KiB tail and queues each for funclog; each stream has its own 128 KiB budget in the 256 KiB queue, so
   a stdout flood cannot push out stderr lines; a line its stream's budget cannot hold is dropped and counted.
   A drop Warn with `dropped` (new key: lines since the last Warn) is logged at most once a minute per worker, plus one
   at `Close` when nonzero, through a Logger with namespace, name, revision and replica (process: `slog.Default()`;
   containerd: `Config.Logger`). `WorkerSpec.LogPath` is removed; a later lifeline would be a sibling type in
   `workerpipe`. Every error return after the `Output` is made closes it and the pipe ends made so far.
   - **Process**: `Start` makes an `Output`, two `os.Pipe`s (and the fd-3 pipe with a Path B hook); write ends are
     `cmd.Stdout`/`cmd.Stderr`; the Path A hook is called last, just before `cmd.Start`; then the parent closes both
     write ends (else Pumps and `logRoutes` entries leak); read ends go to `Drain`; `wait` calls `Close` after the
     group kill. The port file becomes its own `os.CreateTemp("", "funcd-worker-*.port")` (new name).
   - **containerd**: `Create` makes an `Output` just before `NewTask` and passes
     `cio.NewCreator(cio.WithStreams(nil, out.Writer(Stdout), out.Writer(Stderr)), cio.WithFIFODir(fifoDir))` instead of
     `cio.LogFile`; the hook runs after network setup, just before registration (a `StateCreated` task cannot write).
     The worker keeps the `NewTask` task's `cio.IO`. A waiter runs `io.Wait`, `out.Close`, `io.Close` (removes the
     run's FIFO dir). `Stop` kills, waits ≤ 1 s on `out.Done()`, deletes the task, then `io.Close`. `fifoDir` is
     `<stateDir>/fifo`, emptied by `New`; with no `StateDir`, a private `os.MkdirTemp` dir (0700, `funcd-fifo-*`, new)
     that `Close` removes (Open question 10).
   - **Ingest**: both drivers implement `runtime.OutputCapturer`; funcd installs the hook beside Path B's, on its gate
     (capture enabled and a blob). Per run of a Function's worker it feeds `out.Reader(Stdout)`/`(Stderr)` to
     `funclog.Pump(ctx, funclog.NewRawReader(r, src), …)` (`SourceStdout`/`SourceStderr`) into Path B's sink (dev's tee
     included), tracked by `logRoutes`. A Function's worker has `spec.OwnerKind == v1.KindFunction` (ADR-0152) or, if
     this ADR lands first, a non-empty `spec.Command`; other workers get no Reader (Open question 15). `rawReader`
     stamps the read time.
   - **Pool workers** (Open question 1): this ADR's funcd PR lands after ADR-0158's; a raw line is stored once under
     each member of ADR-0158's member set (`PoolMembers` names or the set the hook snapshots, ADR-0158 Decision 3).
   - **Load error**: `Runtime.Logs` returns the current or last run's tail with an ended run's last line: process
     `wait` sets the terminal state only after `out.Done()` (≤ 1 s); containerd's `Logs` waits ≤ 1 s only for a
     terminal worker. The tail holds stdout's lines, then stderr's; `loadError` is unchanged, so ShapeValid carries the
     last stderr line (the shim's load error), else the last stdout line (Open questions 6, 10).
   - **Python stdout**: the shim's `main` and each member subinterpreter's `_poolworker.init` call
     `sys.stdout.reconfigure(line_buffering=True)` (Open question 14).
   - **Restarts**: a worker restart gets a new `Output`; after a funcd restart nothing is re-attached and output until
     ADR-0167 reaps the worker is lost (process: `EPIPE`; containerd: blocks once two pipe buffers fill; OQ 11). The Node
     shim drops `EPIPE` in an `'error'` listener on `process.stdout`/`stderr` (else `containStrayFaults` busy-loops, measured on Node 23).
2. **Record bound.** `funclog.maxRecordBytes` (env `FUNCD_FUNCLOG_MAX_RECORD_BYTES`), default 65536, `0` = default;
   startup refuses a nonzero value above `funclog.MaxLineBytes` (1 MiB, exported) or below 1024 (Open question 5).
   Every shim and pool host gets it under the same name (`shimEnv`, pool host env); funcd's reader keeps
   its 1 MiB cap. The shims bound the NDJSON line without `\n`, after JSON encoding (Python escapes non-ASCII: 6 bytes,
   12 outside the BMP). Fill order (Open question 4): envelope (`ts`, `sev`, `inv`, `trace_id`, `span_id`,
   `funcd.source`, `funcd.member`, Python's `lineno`) and a marker reserve; `body`; Python's `logger`, `funcName`;
   structured attrs in record order (Node's merged keys; Python's `exception.*`, `code.stacktrace`, extras);
   `attrs.args` last. What fits keeps today's text; at the budget the serializer stops, reading nothing past the cut.
   - Node: `boundedStringify` replaces `safeStringify` for `attrs.args` and merged keys, reading lazily: merged keys
     and an `Error`'s properties key by key (not `Object.entries`/`{...v}`), `Map`/`Set` by iterator (not `[...v]`),
     typed arrays, `DataView`, `ArrayBuffer` element by element (not `Array.from`/`bytesOf`). A `Buffer` is written as
     today's `{"type":"Buffer","data":[` then its bytes to the budget; `Buffer#toJSON` never runs. Lines are built
     before `installConsoleCapture` takes the channel lock.
   - Python: `_bounded_json` replaces `_args_json`; `body` is `_bounded_message` (`getMessage()`'s text: non-str
     `msg` via `_bounded_str`; `%s` args by `_bounded_str`, `%r`/`%a` by `_bounded_repr`, any other (`%d`) as `%`
     renders it, then through `%s`; one mapping serves `%(key)s`); extras, `exception.message` use `_bounded_str`
     (`logging.error(ValueError('boom'))` keeps `boom`); `bytes`/`bytearray` walked as `repr()`, `memoryview` not.
   - What the walker cannot enter (own `__str__`/`__repr__`, a non-`Buffer` `toJSON`, `util.inspect` output, a stack
     trace) runs once; a `toJSON` result is then walked, other text is cut.
3. **Cut marker.** A cut record carries `attrs.truncated: "true"` and `attrs.keptBytes: "<decimal>"`: UTF-8 bytes of
   `body` plus every `attrs` value except `lineno` and the markers, after the cut and before JSON escaping (`attrs.args`
   as its JSON text), in both shims (Open questions 2, 3). `sev` is never cut; `body` only past what the envelope and
   reserve leave. A merged key or Python extra named `truncated` or `keptBytes` is skipped, as `args` is (Open question 13).
   Span records take the same bound in each shim's shared span emit helper (`tracespan`), which covers ADR-0165's
   CLIENT span: after JSON encoding, `status_msg` is cut and marked as above, `keptBytes` = its kept bytes (Open question 12).
4. **Releases** (ADR-0141): funcd-typescript and funcd-python each release; funcd pins both tags in one PR. A tag
   holding ADR-0158's shim half is pinned only with or after ADR-0158's funcd PR (ADR-0158 Decision 7).

## Contracts

```go
// Package workerpipe (ADR-0168): funcd's end of a worker's raw stdout and stderr, read as written into a bounded queue
// for funclog and a bounded tail. Both drivers use it; it imports no container or observability package.
package workerpipe

type Stream int
const (Stdout Stream = 1; Stderr Stream = 2)
const (DefaultLineBytes = 64 << 10; DefaultQueueBytes = 256 << 10; DefaultTailBytes = 64 << 10) // split; half per stream; tail
type Options struct { LineBytes, QueueBytes, TailBytes int; Logger *slog.Logger } // zero ⇒ default; nil ⇒ slog.Default()
type Output struct{ /* unexported */ }
func New(o Options) *Output
func (o *Output) Drain(s Stream, r io.ReadCloser) // own goroutine until EOF or error, then closes r
func (o *Output) Writer(s Stream) io.Writer       // never blocks, returns len(p), nil; a full queue drops and counts lines
type StreamReader interface { io.ReadCloser; CloseRead() error } // CloseRead = Shutdown seal: EOF after queued lines
func (o *Output) Reader(s Stream) StreamReader    // "\n"-ended lines queued from the first call; EOF once Closed, drained, empty
func (o *Output) Tail() io.ReadCloser             // copy of ≤ TailBytes, oldest evicted; stdout's lines, then stderr's
func (o *Output) Dropped() uint64
func (o *Output) Close()                          // never blocks, idempotent; queues last partial lines once Drains end;
	// last drop Warn; later Writes dropped and counted
func (o *Output) Done() <-chan struct{}           // closed once Close ran and every Drain returned

// internal/runtime — WorkerSpec loses LogPath. Logs returns Output.Tail of the current or last run (empty before the
// first Start) with an ended run's last line: terminal state only after Output.Done, or a terminal worker's Logs waits ≤ 1 s.
Logs(ctx context.Context, id InstanceID) (io.ReadCloser, error)
type OutputCaptureFunc func(spec WorkerSpec, out *workerpipe.Output) // before the worker writes; takes both Readers
	// for a stored worker and reads them on its own goroutines, else none; must not block
type OutputCapturer interface { SetOutputCapture(OutputCaptureFunc) } // both drivers; nil clears; no hook ⇒ tail only

// internal/funclog — exported (was maxLineBytes); rawReader.Read stamps Entry.Time.
const MaxLineBytes = 1024 * 1024
const DefaultMaxRecordBytes = 64 << 10 // when funclog.maxRecordBytes is 0
// internal/platform/config — Config.Funclog gains:
MaxRecordBytes int `json:"maxRecordBytes,omitempty" env:"FUNCD_FUNCLOG_MAX_RECORD_BYTES" validate:"min=0"`
// pkg/funcd — 0 keeps the default; nonzero below 1024 or above MaxLineBytes is fault.Invalid naming the key and cap.
func WithFunclogMaxRecordBytes(n int) Option
// internal/function — Deps gains (FUNCD_FUNCLOG_MAX_RECORD_BYTES for every shim and pool host):
LogMaxRecordBytes int
```

```ts
// funcd-typescript shim/src/funclog.ts
export const DEFAULT_MAX_RECORD_BYTES = 65536;
export function recordBound(env: NodeJS.ProcessEnv): number; // unset or not a positive integer ⇒ default
interface Budget { left: number; kept: number; cut: boolean }
function boundedStringify(value: unknown, budget: Budget): string; // safeStringify's text until the budget
function buildLine(method: ConsoleMethod, args: unknown[], bound: number): string; // bound read once at install
```

```python
# funcd-python shim/src/funcd_shim/funclog.py
_DEFAULT_MAX_RECORD_BYTES = 65536
def _record_bound() -> int: ...  # unset or not a positive integer gives the default
class _Budget: ...  # left: int, kept: int, cut: bool
def _bounded_json(value: object, budget: _Budget) -> str: ...  # a cycle falls back to _bounded_repr
def _bounded_str(value: object, budget: _Budget) -> str: ...  # containers, bytes, bytearray walked as repr()
def _bounded_repr(value: object, budget: _Budget) -> str: ...  # containers, str, bytes, bytearray, scalars walked
def _bounded_message(record: logging.LogRecord, budget: _Budget) -> str: ...
class FuncLogHandler(logging.Handler):
    def __init__(self, channel: Channel, max_record_bytes: int = _DEFAULT_MAX_RECORD_BYTES) -> None: ...
```

Wire (ADR-0081's): record and span line ≤ `FUNCD_FUNCLOG_MAX_RECORD_BYTES` bytes before `"\n"` (new); a cut record adds
`"truncated": "true"`, `"keptBytes": "<decimal>"` to `attrs` (new); `attrs.args` lossless while the record fits, else a
prefix of its JSON text (changed); `funcd.member` per ADR-0158. Consumes: fd 1/2 of each worker run (process: two
`os.Pipe`s; containerd: two FIFOs under `fifoDir`), config key `funclog.maxRecordBytes`. Exposes: Path A entries of
Function workers; `Runtime.Logs` = the tail; the drop Warn; `FUNCD_FUNCLOG_MAX_RECORD_BYTES` in every shim and pool host env.

## Implementation plan

1. **funcd-typescript**: `funclog.ts` per Contracts; `shim.ts:156`, `pool.ts:354` listening lines to stdout (Open
   question 9), `shim/test/shim.test.ts:223` reads the port from stdout; the `EPIPE` listener; `examples/log-burst`
   writes one stdout and one stderr line. Tests (`shim/test/funclog.test.ts`): both scenario titles; counting `Proxy`
   (argument, merged object) and `Set` subclass stop at the cut; no typed-array copy; bound holds for multi-byte and
   quote-heavy text; `safeStringify`'s text for the `issue 82`, `r23`, `r24`, `r32` values, a small `Buffer`, a `Date`;
   a 50 MiB `Buffer` never calls `Buffer#toJSON` (spy); `keptBytes`; reserved keys; a span's long `status_msg` cut.
   `just build`, commit `shim.mjs`, `pool.mjs`; release.
2. **funcd-python**: `funclog.py` per Contracts; stdout reconfigure in `main` and `_poolworker.init`, a pool test on a
   member's `sys.stdout.line_buffering`; `examples/log-burst` prints one stdout (`flush=True`) and one stderr line.
   Tests (`shim/tests/test_funclog.py`): both scenario tests; a counting `dict` subclass; 50 MB `bytes` stops at the
   cut; bounded `body` for `logging.info("%s", obj)`; bound holds for non-ASCII, astral, quote-heavy text and a long
   `logger`; today's text for `ValueError('boom')`, `'%r'` of `'x'`, `'%(k)s'`; a span's long `status_msg` cut. Release.
3. **funcd** (one PR after both releases and after ADR-0158's funcd PR): `workerpipe` unit tests (non-blocking Write,
   splits, tail order and eviction, drops and Warns, `CloseRead`, `Close` before Drains, `Done`); both drivers;
   `runtimecontract` loses `LogPath`, gains a split-stream subtest and `raw-output-never-blocks`; no Pump left after a
   run, a failed `cmd.Start` or a failed Create; `TestIssue46_ReplaceRemovesDriverFiles`,
   `TestIssue365_RejectedCreateLeaksNoLog`, `TestIssue366_CloseRemovesDriverFiles` count `funcd-worker-*.port` files
   (Issue366 drops its `LogPath` worker); containerd: FIFOs, Stop's wait, no FIFO dir left, `fifoDir` cleanup;
   `funclog`, config, `cmd/funcd`, `pkg/funcd` (an engine stores nothing; the `WithoutFunclog` doc), `internal/function`;
   pool raw lines via ADR-0158's member set; `go get` both tags; scenario tests in `pkg/funcd`; the `funclog` Lima
   lane gains `raw-output-severity` and `record-cut-at-bound` for both languages. The F50 row links this ADR
   (`capture: implemented · raw output + record bound: adr`, as F51 does). Done: every scenario test passes
   (`TestScenarioLoadErrorInLogs` with `-count=20`); `just ci`, `funclog` lane green.

## Review checklist

- [ ] No `LogPath`, `cio.LogFile` or temp log file; error returns close the `Output` and pipe ends; drivers per Decision 1.
- [ ] Write never blocks; drops counted; Warns at most once a minute plus one at `Close`, with the instance's identity.
- [ ] Function workers only: stdout → INFO, stderr → ERROR, read time, sealed at Shutdown; `loadError` unchanged.
- [ ] A nonzero bound outside [1024, `MaxLineBytes`] fails startup; the env reaches every shim and pool host.
- [ ] Both shims stop at the budget (Decision 2) and mark per Decision 3; Python stdout line-buffered everywhere.
- [ ] `go.mod` pins both releases; each scenario has its named, passing test.

## Consequences

- Positive: raw output, load errors and `ctx.log` reach `funcdctl logs` with a severity; no record vanishes for its size.
- Negative: overload drops counted lines; large values cut; ≤ ~448 KiB per backed-up worker; two more goroutines per
  worker; ended runs wait ≤ 1 s; hand-written serializers; engine raw output not stored (OQ 15); pooled lines per member (OQ 1).
- Risks accepted: output while funcd is down is lost; FIFO behavior is runc shim v2.3.1's.

## Temporary workarounds

None.

## Open questions (each: the proposed choice and its alternative; to confirm before acceptance unless marked Decided)

1. Pool raw lines under every member (ADR-0158's `PoolMembers` returns names). Alt: drop and count (#78 stays open).
2. String markers (`attrs` is `map[string]string`). Alt: typed OTLP values. 3. `keptBytes` per Decision 3.
4. `attrs.args` last keeps queryable keys and the exception; the crossing value is cut, later ones omitted. Alt: `args` first.
5. Floor 1024 bytes, so envelope and reserve always fit; `logger`/`funcName` are cut like any value.
6. Decided: per-stream queue budgets, 64 KiB tail, over-64 KiB raw lines split unmarked; tail with stderr last (alt
   arrival order can misreport `loadError`). 7. Drops: counter and `dropped` Warn, no metric. 8. Capture off or no blob: tail only.
9. Node shim and pool host listening lines go to stdout, as Python's do, so a normal start is not ERROR.
10. Wait ≤ 1 s for an ended run's last line (alt: report at once, may miss the load error); FIFOs in funcd's own
    `fifoDir` (alt: containerd's shared `/run/containerd/fifo`). 11. No re-attach after a funcd restart. Alt:
    `cio.NewAttach` for workers ADR-0167 reaps anyway.
12. Span records bounded in the shared emit helper (`tracespan.ts:28`, `:82`). Alt: unbounded (a callee's reply in
    `status_msg` can pass the 1 MiB reader cap and be dropped). 13. User keys `truncated`/`keptBytes` skipped on every
    record. Alt: override only on a cut record.
14. Python stdout via `reconfigure` in `main` and each member's `_poolworker.init`. Alt: `PYTHONUNBUFFERED=1` in
    `shimEnv` and the pool env (unbuffers partial writes); or neither (pooled output lost on SIGKILL).
15. Engine raw output tailed, not stored. Alt: stored under the engine's name, readable by Function readers and merged
    into a same-named Function's stream (#18).

## References

- [pyvvo/funcd#78](https://github.com/pyvvo/funcd/issues/78), [pyvvo/funcd-typescript#31](https://github.com/pyvvo/funcd-typescript/issues/31),
  reproduced on 1193be6 with funcd-typescript v0.4.4 and funcd-python v0.3.5.
- containerd v2.3.1: `pkg/cio/io.go:119-158`, `pkg/cio/io_unix.go:35-105`, `client/container.go:233-238`,
  `client/task.go:409-439`, `cmd/containerd-shim-runc-v2/process/io.go:188-194`. Kubernetes per-line stream tag;
  Docker json-file `stream`; Node `util.inspect` limits; OTel attribute length limits.
