# ADR-0197: Log durations as milliseconds, with the unit in the key

- **Status**: Accepted (2026-10-07)
- **Date**: 2026-10-07
- **Deciders**: green-0-rabbit
- **Tags**: observability, logging, slog, funcdctl
- **Realizes**: [FEAT-0000/F17a](../feat/0000-feat-v1.md) (the logger root; the row ADR-0009 realizes)
- **Relates to**: refines [ADR-0009](0009-observability-logger-root.md) Decision 1 ("`HandlerOptions{Level:
  levelVar}`", line 132) with a `ReplaceAttr`; ADR-0009 fixes no duration encoding, so nothing is superseded.
  [ADR-0196](0196-utc-millisecond-timestamps.md) (#821) owns the `time` branch (Decision 1); its hook `replaceAttr` is
  exported as `observability.ReplaceAttr` (superseded in part, back-link added at acceptance).
  [ADR-0114](0114-edge-observability-shaping.md); [ADR-0194](0194-api-duration-strings.md).
- **Blueprint**: "Platform logging" gains Decision 8's bullet after "Canonical fields" (`blueprint.md:449`).

## Context & Need

slog writes a `time.Duration` in nanoseconds in JSON (`log/slog/json_handler.go:121-123`, `"timeout":60000000000`) and
as a Go duration string in text (`log/slog/value.go:470-471`, `timeout=1m0s`): the unit is hidden and the formats
disagree. A type-checked scan of the non-test packages for a `log/slog` argument that is, holds or converts a
`time.Duration` finds three lines: `internal/function/bootbackoff.go:154-155` (`timeout`),
`internal/sensor/retry.go:416` (`bound`), and the edge access log (`internal/edge/observ/observ.go:120`, ADR-0114),
whose `"duration_ms", dur.Milliseconds()` truncates 412 µs to `0`. `funcdctl dev` warns through `slog.Default()`.

Purpose: an operator who reads or queries funcd's logs sees every duration as a number with its unit in the key, which
log tools can filter and sum, with the same key and value in text and JSON, and no call site has to remember the rule.

## Scenarios

- **scenario: configured-timeout-whole-ms** — Given a JSON logger from `NewLogger` and a boot backoff with a 60 s boot
  timeout, When a worker misses the boot timeout, Then the warning carries `"timeout_ms":60000` and no `timeout` key.
- **scenario: measured-latency-keeps-fraction** — Given a JSON logger, When a component logs `latency` as 412 µs and
  then as 1.2345 ms, Then the lines carry `"latency_ms":0.412` and `"latency_ms":1.235`, each a JSON number.
- **scenario: text-format-same-key** — Given a text logger, When `timeout` is logged as 60 s and `latency` as 412 µs
  and as 1.2345 ms, Then the lines carry `timeout_ms=60000`, `latency_ms=0.412` and `latency_ms=1.235`.
- **scenario: edge-access-log-keeps-fraction** — Given the edge chain with `AccessLog: true` and a JSON logger from
  `NewLogger`, When a request finishes in under 1 ms, Then its access line carries `duration_ms` as a number above 0
  and below 1 with at most three decimals.
- **scenario: no-nanoseconds-in-logs** — Given a logger of each format, When 1234567891 ns reaches it by each path
  (a key-value pair, `slog.Duration`, `slog.Any`, `Logger.With`, `slog.Group`, `Logger.WithGroup`, a `LogValuer` to a
  duration or to a group holding one, a `v1alpha1.Duration`), Then every line carries the key with `_ms` and
  `1234.568`, and neither `1234567891` nor `1.234567891s` appears.
- **scenario: funcdctl-dev-logs-like-daemon** — Given `funcdctl dev` running a workflow, When the workflow file gains
  a step whose function is not running, Then the restart warning appears on funcdctl's output in `NewLogger`'s text
  form (`level=WARN msg="restart funcdctl dev to run a new workflow step function"`).
- **scenario: embedder-opts-in** — Given `funcd.WithLogger` over an embedder's JSON handler, When funcd logs `timeout`
  as 60 s, Then with `funcd.WithNormalizedLogFields()` the line carries `"timeout_ms":60000` and a UTC `time` with at
  most three fractional digits; without it, `"timeout":60000000000` and the local time, as the handler writes today.

## Scope

- **In**: every attribute whose resolved value is a `time.Duration` or a `v1alpha1.Duration`, at any group depth, in
  `NewLogger`'s handlers (the daemon, funcd's default logger, `funcdctl dev`), the audit recorder's (ADR-0196;
  string-only), and a `WithLogger` handler under `funcd.WithNormalizedLogFields()`; the access log (Decision 5).
- **Out**: the `time` text form (ADR-0196); an embedder's logger without the option; the metric
  `funcd.edge.duration_ms` (`observ.go:111`); the unwired `otelslog` bridge (int64 nanoseconds, v0.19.0
  `handler.go:496-497`), whose wiring ADR meets Decision 8, for example with Decision 7's decorator; four daemon
  sites that call `slog.Default()`, none with a duration (`internal/runtime/process/process.go:168`,
  `internal/workernode/local/local.go:246`, `internal/catalog/gateway/proxy.go:52`,
  `internal/workflow/kvmigration.go:34`); a duration a caller turns into a number, or a value holding one (a
  `*time.Duration`, a slice, a struct), which the hook cannot recognize (the scan finds none after Decision 5).

## Constraints & Decision drivers

- The decider's rule (#822): milliseconds with at most three decimals, the unit in the key, no fraction for a whole
  millisecond, nothing in nanoseconds, the same key and value in text and JSON, applied once in the handler.
- The daemon's components receive the `NewLogger` root (`cmd/funcd/main.go:406`, `pkg/funcd/presets.go:38`, `:78`,
  `pkg/funcd/funcd.go:420`, `:1114`). A JSON number written as an exact decimal; stdlib only.
- `internal/platform` may import `api/**` (depguard `platform-leaf`; `logger.go` already imports `api/fault`).

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **A. Exact decimal from integer microseconds, as `json.Number`** | Exact; JSON encodes a `json.Number` as a bare number (`json_handler.go:126-133`), text writes the same characters unquoted (probe: `0.412`, `1000000`, `-0.412` in both) | One call to the handler's pooled `json.Encoder` per duration attribute (`json_handler.go:157-169`) | **Chosen** |
| B. `float64` milliseconds | A native slog kind | Text formats a float with `%g`: 1000 s is `1e+06` in text, `1000000` in JSON (probe) | Rejected: text differs from JSON |
| C. String value `"0.412"` | Simple | JSON writes a quoted string, not a number | Rejected (decider: a JSON number) |
| D. Rename at each call site (`"timeout_ms", d.Milliseconds()`) | No hook | Each future call site must remember; the fraction is lost (the access log today) | Rejected (decider: in the handler) |
| E. A handler decorator instead of `ReplaceAttr` for `NewLogger`'s handlers | Would also reach a fanned-out OTLP handler | Rebuilds every record and re-walks `WithAttrs`, groups and `LogValuer`s, which `ReplaceAttr` does inside the stdlib handler | Rejected; the decorator serves only Decision 7 |
| F. A `LogValue` method on ADR-0194's `Duration` returning `slog.DurationValue` | One method; slog resolves it before the hook; observability imports no API type | Changes ADR-0194 after its review, while it is before the decider | Rejected |
| **G. The hook also matches a `v1alpha1.Duration` value** | Without it the type is `KindAny` and logs `"1m0s"` (probe); ADR-0194 unchanged; the rule stays in one place | `observability` imports `api/types/v1alpha1`; built after ADR-0194 | **Chosen** |
| **H. A call site names a duration without a unit: the access log logs `"duration", dur`** | Through `NewLogger` the line still reads `duration_ms`, with its fraction; a logger without the hook writes an honest `duration` | The access-log key differs between the daemon (`duration_ms`) and a hookless logger (`duration`) | **Chosen** |
| I. Keep `"duration_ms"` at the call site and pass `dur` | One key everywhere | A hookless logger (`funcd.WithLogger`, `observ_test.go:113`) writes nanoseconds under `duration_ms` (probe: `"duration_ms":459416`; text `458.167µs`), where the current code writes whole milliseconds | Rejected: a wrong unit under a key that names one |
| **J. `funcd.WithNormalizedLogFields()`, off by default, wraps the `WithLogger` handler** | Named for what it does; off, the embedder's logger is used exactly as given | Rebuilds each record; the embedder's handler decides the time's digits | **Chosen** (decider) |
| K. A public function the embedder puts in its own `HandlerOptions.ReplaceAttr` | No record rebuild | The name says nothing to an embedder | Rejected (decider) |

## Decision

1. **One hook, two owners.** The hook ADR-0196 names `replaceAttr` is exported as `observability.ReplaceAttr` (inside
   the module only); `NewLogger` sets it on the text and the JSON handler. Its time branch is ADR-0196's; its duration
   branch, `durationField`, is this ADR's: a `KindDuration` value, or a `KindAny` value holding a `v1alpha1.Duration`,
   under any key and group. The ADR implemented first adds the hook; the other adds its branch.
2. **Key.** A call site never puts a unit in a duration attribute's key; the hook appends `_ms`. A key that already
   ends in `_ms` is kept, as a safety net (an empty key becomes `_ms`). Group names stay (`"retry":{"wait_ms":1500}`,
   `retry.wait_ms=1500`). A record with a duration `timeout` and another `timeout_ms` carries two `timeout_ms` keys.
3. **Value.** `millis` (Contracts): round to the nearest microsecond with `Duration.Round` (halves away from zero, so
   1.2345 ms is 1.235), then integer arithmetic only, carried as `json.Number`.
4. **Negative durations keep their sign** (−412 µs is `-0.412`), unclamped: a deadline already passed is information.
5. **One call site changes.** `observ.go:120` logs `"duration", dur` instead of `"duration_ms", dur.Milliseconds()`.
   Through `NewLogger` ADR-0114's field (line 123) still reads `duration_ms`, now with its fraction (412 µs is `0.412`,
   where the current code writes `0`); a hookless logger writes `duration`. The others become `timeout_ms`, `bound_ms`.
6. **`funcdctl dev` logs like the daemon.** `startDev` builds a text `NewLogger` logger over the cli's `out`, kept on
   `devInstance`, for the six `slog.Default()` uses (`cmd/funcdctl/dev.go:776`, `:795`; `dev_reload.go:173`, `:260`,
   `:316`, `:358`).
7. **An embedder opts in with `funcd.WithNormalizedLogFields()`, off by default.** With it, `New` wraps the
   `WithLogger` handler in `observability.NormalizeHandler`: each record's attributes, and those given to `WithAttrs`,
   are resolved and pass `durationField` at any group depth, and the record time becomes UTC truncated to the
   millisecond. The embedder's handler prints that time, so three fractional digits are not guaranteed (the stdlib
   JSON handler trims zeros, `json_handler.go:100`: `…:56.55Z`). Off, the logger is used exactly as given.
8. **The blueprint states the rule**, so the ADR that wires the OTLP bridge follows it. The new bullet reads:
   "**Field encodings**: a duration field's key ends in `_ms` and its value is a number of milliseconds with at most
   three decimals, never nanoseconds (`"timeout_ms":60000`, `"latency_ms":0.412`), and a timestamp, the `time` of
   each line included, is RFC3339 UTC with exactly three fractional digits (`2026-10-07T09:30:00.000Z`, ADR-0196);
   every log handler funcd builds, the `otelslog` bridge included, follows both rules, and
   `funcd.WithNormalizedLogFields()` brings an embedder's `funcd.WithLogger` logger to the duration rule and UTC
   millisecond times (ADR-0197)."

| Duration | 60 s | 412 µs | 1.2345 ms | −412 µs | 400 ns | 500 ns | `math.MaxInt64` ns |
|---|---|---|---|---|---|---|---|
| Value (JSON and text) | `60000` | `0.412` | `1.235` | `-0.412` | `0` | `0.001` | `9223372036854.775` |

## Temporary workarounds

None.

## Contracts

```go
// internal/platform/observability/logger.go; v1 is github.com/pyvvo/funcd/api/types/v1alpha1.
// NewLogger: opts := &slog.HandlerOptions{Level: level, ReplaceAttr: ReplaceAttr}

// ReplaceAttr is the ReplaceAttr of the handlers NewLogger builds; each branch names its ADR.
func ReplaceAttr(groups []string, a slog.Attr) slog.Attr {
	// ADR-0196's time branch goes here.
	return durationField(a)
}

// durationField rewrites a time.Duration or v1.Duration attribute by ADR-0197; any other attribute is unchanged.
func durationField(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindDuration:
		return durationAttr(a.Key, a.Value.Duration())
	case slog.KindAny:
		if d, ok := a.Value.Any().(v1.Duration); ok {
			return durationAttr(a.Key, time.Duration(d))
		}
	}
	return a
}

// durationAttr is the attribute <key>_ms (a key already ending in _ms is kept) valued millis(d).
func durationAttr(key string, d time.Duration) slog.Attr {
	if !strings.HasSuffix(key, "_ms") {
		key += "_ms"
	}
	return slog.Any(key, json.Number(millis(d)))
}

// millis writes d in milliseconds, rounded to the nearest microsecond, with at most three decimals and no trailing
// zeros; a whole number of milliseconds has no fraction.
func millis(d time.Duration) string {
	us := int64(d.Round(time.Microsecond) / time.Microsecond)
	sign := ""
	if us < 0 {
		sign, us = "-", -us
	}
	s := sign + strconv.FormatInt(us/1000, 10)
	if frac := us % 1000; frac != 0 {
		s += "." + strings.TrimRight(fmt.Sprintf("%03d", frac), "0")
	}
	return s
}

// internal/platform/observability/normalize.go

// NormalizeHandler wraps an embedder's handler (funcd.WithNormalizedLogFields): Handle builds a new record with the
// time in UTC truncated to the millisecond and each attribute resolved and passed through durationField (a group is
// rebuilt from its members); WithAttrs does the same to its attributes. Enabled and WithGroup forward unchanged.
type NormalizeHandler struct{ inner slog.Handler } // Enabled, Handle, WithAttrs, WithGroup: slog.Handler

func NewNormalizeHandler(inner slog.Handler) *NormalizeHandler

// pkg/funcd/options.go

// WithLogger injects the root logger, used exactly as given unless WithNormalizedLogFields is set. If not set, a
// stdout text logger is built.
func WithLogger(l *slog.Logger) Option

// WithNormalizedLogFields makes funcd's own lines through a WithLogger logger write a duration as a <key>_ms number
// of milliseconds and the time in UTC, truncated to the millisecond (the handler decides how many fractional digits
// it prints). Off by default. New wraps the logger after the options (funcd.go:419): l = slog.New(
// observability.NewNormalizeHandler(l.Handler())). Without WithLogger it changes nothing.
func WithNormalizedLogFields() Option
```

| Consumes | Exposes |
|---|---|
| stdlib `log/slog`, `context`, `encoding/json`, `strconv`, `strings`, `fmt`, `time`; `v1alpha1.Duration` (ADR-0194) | Each duration attribute as `<key>_ms`, a number of milliseconds; `observability.ReplaceAttr` and `NormalizeHandler` (in-module); the public option `funcd.WithNormalizedLogFields`; no config key |

## Implementation plan

Built after ADR-0194's `v1alpha1.Duration` lands; if ADR-0194 is not accepted, without the `KindAny` case and its path.

1. Prove first: write the seven scenario tests (steps 3 to 7) and run them on main; they fail.
2. `internal/platform/observability`: `logger.go` gets the Contracts (renaming ADR-0196's `replaceAttr` if it has
   landed) and `NewLogger` sets `ReplaceAttr`; new `normalize.go` holds `NormalizeHandler`.
3. `logger_test.go`: `TestMillis` (the Decision table, `math.MinInt64` ns); `TestScenarioMeasuredLatencyKeepsFraction`
   (lines also decode to `float64`); `TestScenarioTextFormatSameKey`; `TestScenarioNoNanosecondsInLogs` (nine paths ×
   two formats, and through a `NormalizeHandler` over a plain JSON handler); `TestDurationKeyAlreadyMs` (`wait_ms`).
4. `internal/function/bootbackoff_internal_test.go`: `TestScenarioConfiguredTimeoutWholeMs`, a 60 s `newBootBackoff`
   over a JSON `observability.NewLogger`, then `timedOut`.
5. `internal/edge/observ/observ.go:120`: `"duration", dur`. In `observ_test.go`, `TestScenarioAccessLogCorrelated`
   (`:113`) takes a JSON `NewLogger`, so `:132` holds; `TestScenarioEdgeAccessLogKeepsFraction` (handler sleeps
   100 µs) serves until its own bound around `serve` is under 900 µs, fails with a host-load message after 100 tries,
   and asserts `0.1 ≤ v < 1` with at most three decimals for `duration_ms`.
6. `cmd/funcdctl`: a `logger` on `devInstance`, set in `startDev`, passed to the six sites.
   `TestIssue428_DevReloadsEditedWorkflow` reads the warning from its own `cli{out: …}` buffer instead of
   `slog.SetDefault` (`// scenario: funcdctl-dev-logs-like-daemon`).
7. `pkg/funcd`: the option, the doc comment and the wrap. `TestScenarioEmbedderOptsIn`, internal like
   `errorlog_internal_test.go:37`, logs through `p.logger` with and without the option.
8. Through `scripts/agent/d`: `go test -race -count=1`, `go vet`, `go tool golangci-lint run` on the five packages of
   steps 3 to 7. The PR says `Fixes #822`.

Definition of done: the seven scenario tests failed on main and pass after steps 2 and 5 to 7; no config key added.

## Review checklist

- [ ] `NewLogger` sets `ReplaceAttr`; it and `NormalizeHandler` share `durationField`; nothing else rewrites durations.
- [ ] `millis` uses `Duration.Round(time.Microsecond)` and integer arithmetic; values are `json.Number`.
- [ ] `observ.go:120` logs `"duration", dur`; no `slog.Default()` is left in `cmd/funcdctl` production code.
- [ ] `pkg/funcd` exports no hook, only the option, off by default; the blueprint carries Decision 8's bullet.

## Consequences

- **Positive**: every logged duration is numeric with its unit, the same in both formats, sub-millisecond in the
  access log; `funcdctl dev` logs like the daemon; an embedder opts in with one option.
- **Negative**: the keys `timeout` and `bound` become `timeout_ms` and `bound_ms`, so a saved query on the old key stops
  matching. An access-log line costs about 0.38 µs more (measured, 9 attributes; the access log is opt-in). A hookless
  logger names that field `duration`. `funcdctl dev`'s warnings move from stderr to its output. The option rebuilds
  each record; a non-stdlib handler decides how it prints a `json.Number`.
- **Risks accepted**: a future call site that converts a duration, or logs a value holding one, bypasses the hook.

## Open questions

None.

## References

- Issue #822 (the decider's comments of 2026-10-07); #821 and ADR-0196; ADR-0009; ADR-0114; ADR-0194; ADR-0002 §6.
- Go 1.26.4 `slog.HandlerOptions.ReplaceAttr` doc ("The attribute's value has been resolved"; "never called for Group
  attributes, only their contents"); code at origin/main ec274537; probes of 2026-10-07 (go1.26.4), as cited.
