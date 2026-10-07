# ADR-0197: Log durations as milliseconds, with the unit in the key

- **Status**: Proposed
- **Date**: 2026-10-07
- **Deciders**: green-0-rabbit
- **Tags**: observability, logging, slog
- **Realizes**: [FEAT-0000/F17a](../feat/0000-feat-v1.md) (the logger root; the row ADR-0009 realizes)
- **Relates to**: refines [ADR-0009](0009-observability-logger-root.md) Decision 1 ("`HandlerOptions{Level:
  levelVar}`", line 132) with a `ReplaceAttr`; ADR-0009 fixes no duration encoding, so nothing is superseded.
  [ADR-0196](0196-utc-millisecond-timestamps.md) (#821) owns the `time` attribute in the same hook.
  [ADR-0114](0114-edge-observability-shaping.md); [ADR-0194](0194-api-duration-strings.md) (Proposed, unchanged).
- **Blueprint**: at acceptance, "Platform logging" (`blueprint.md:449`, canonical fields) gains one line: a duration
  field's key ends in `_ms` and its value is a number of milliseconds with at most three decimals.

## Context & Need

slog writes a `time.Duration` in nanoseconds in JSON (`log/slog/json_handler.go:121-123`, `"timeout":60000000000`) and
as a Go duration string in text (`log/slog/value.go:470-471`, `timeout=1m0s`): the unit is hidden and the formats
disagree. A type-checked scan of every non-test package for a `log/slog` argument that is a `time.Duration`, a
struct holding one, or a conversion of one finds three lines: the configured values at
`internal/function/bootbackoff.go:154-155` (key `timeout`) and `internal/sensor/retry.go:416` (key `bound`), and the
edge access log (`internal/edge/observ/observ.go:120`, ADR-0114), whose `"duration_ms", dur.Milliseconds()` truncates
a measured 412 µs to `0`.

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
- **scenario: no-nanoseconds-in-logs** — Given a logger of each format, When a duration of 1234567891 ns reaches it by
  each path an attribute can take (a key-value pair, `slog.Duration`, `slog.Any`, `Logger.With`, `slog.Group`,
  `Logger.WithGroup`, a `LogValuer` that resolves to a duration, one that resolves to a group holding one, and a
  `v1alpha1.Duration`), Then every line carries the key with `_ms` and the value `1234.568`, and neither `1234567891`
  nor `1.234567891s` appears.

## Scope

- **In**: every attribute whose resolved value has `slog.KindDuration` or is a `v1alpha1.Duration`, at any group
  depth, in a handler that carries the hook: the two handlers `NewLogger` builds and, through ADR-0196, the audit
  recorder's (`audit.go:53`; `AuditEvent` holds only strings, `audit.go:37-43`). The access log's duration (Decision 5).
- **Out**: the `time` attribute (ADR-0196); the OTLP log bridge (Open questions); the metric
  `funcd.edge.duration_ms` (`observ.go:111`); loggers the hook cannot reach: an embedder's `funcd.WithLogger`
  (`pkg/funcd/options.go:506`) and `funcdctl`'s `slog.Default()` (`cmd/funcdctl/dev.go:776`, `dev_reload.go:173`); a
  duration a caller turns into a number (`d.Milliseconds()`), and any other value that holds a duration without being
  one (a `*time.Duration`, a slice, a struct): the hook cannot recognize them, and after Decision 5 the scan finds none.

## Constraints & Decision drivers

- The decider's rule (#822): milliseconds with at most three decimals, the unit in the key, no fraction for a whole
  millisecond, nothing in nanoseconds, the same key and value in text and JSON, applied once in the handler.
- The daemon's loggers derive from `NewLogger` (`cmd/funcd/main.go:406`, `pkg/funcd/presets.go:38`, `:78`,
  `pkg/funcd/funcd.go:420`); the edge chain receives that logger (`pkg/funcd/funcd.go:1114`).
- A JSON number written as an exact decimal; stdlib only. `internal/platform` may import `api/**` (depguard
  `platform-leaf` in `.golangci.yml`; `logger.go` already imports `api/fault`).

## Alternatives considered

Probes ran on 2026-10-07 with go1.26.4 through the stdlib JSON and text handlers.

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **A. Exact decimal from integer microseconds, as `json.Number`** | Exact; JSON encodes a `json.Number` as a bare number (`json_handler.go:126-133`), text writes the same characters unquoted (probe: `0.412`, `1000000`, `-0.412` in both) | One call to the handler's pooled `json.Encoder` per duration attribute (`json_handler.go:157-169`) | **Chosen** |
| B. `float64` milliseconds | A native slog kind | Text formats a float with `%g`: 1000 s is `1e+06` in text, `1000000` in JSON (probe) | Rejected: text differs from JSON |
| C. String value `"0.412"` | Simple | JSON writes a quoted string, not a number | Rejected (decider: a JSON number) |
| D. Rename at each call site (`"timeout_ms", d.Milliseconds()`) | No hook | Each future call site must remember; the fraction is lost (the access log today) | Rejected (decider: in the handler) |
| E. A handler decorator that rewrites the record | Would also reach a fanned-out OTLP handler and an embedder's handler | Re-walks `WithAttrs`, groups and `LogValuer`s, which `ReplaceAttr` already does | Rejected |
| F. A `LogValue` method on ADR-0194's `Duration` returning `slog.DurationValue` | One method; slog resolves it before the hook; observability imports no API type | Changes ADR-0194 after its review, while it is before the decider | Rejected |
| **G. The hook also matches a `v1alpha1.Duration` value** | Without it the type is `KindAny` and logs `"1m0s"` (probe); ADR-0194 unchanged; the rule stays in one place | `observability` imports `api/types/v1alpha1`; built after ADR-0194 | **Chosen** |
| **H. A call site names a duration without a unit: the access log logs `"duration", dur`** | Through `NewLogger` the line still reads `duration_ms`, with its fraction; a logger without the hook writes an honest `duration` | The access-log key differs between the daemon (`duration_ms`) and a hookless logger (`duration`) | **Chosen** |
| I. Keep `"duration_ms"` at the call site and pass `dur` | One key everywhere | A hookless logger (`funcd.WithLogger`, `observ_test.go:113`) writes nanoseconds under `duration_ms` (probe: `"duration_ms":459416`; text `458.167µs`), where the current code writes whole milliseconds | Rejected: a wrong unit under a key that names one |

## Decision

1. **One hook, two owners.** `NewLogger` sets `HandlerOptions.ReplaceAttr` to the unexported `replaceAttr` on the text
   and the JSON handler. Its time branch (`len(groups) == 0`, key `slog.TimeKey`, `KindTime`) is ADR-0196's. Its
   duration branch is this ADR's: a `KindDuration` value, or a `KindAny` value holding a `v1alpha1.Duration`, under
   any key and group. Every other attribute passes unchanged. The ADR implemented first adds the hook; the other adds
   its branch.
2. **Key.** A call site never puts a unit in a duration attribute's key; the hook appends `_ms`. A key that already
   ends in `_ms` is kept, as a safety net (an empty key becomes `_ms`). Group names stay (`"retry":{"wait_ms":1500}`,
   `retry.wait_ms=1500`). A record with a duration `timeout` and another `timeout_ms` carries two `timeout_ms` keys;
   the hook does not merge them.
3. **Value.** `millis` (Contracts): round to the nearest microsecond with `Duration.Round` (halves away from zero, so
   1.2345 ms is 1.235), then integer arithmetic only, carried as `json.Number`.
4. **Negative durations keep their sign** (−412 µs is `-0.412`), unclamped: a deadline already passed is information.
5. **One call site changes.** `observ.go:120` logs `"duration", dur` instead of `"duration_ms", dur.Milliseconds()`.
   Through `NewLogger` the line still reads `duration_ms`, so the daemon's output and ADR-0114's field (line 123) keep
   their key, and the value keeps its fraction (412 µs is `0.412`, where the current code writes `0`). A logger
   without the hook writes `duration` in slog's default encoding. The two other lines become `timeout_ms` and
   `bound_ms`.

| Duration | 60 s | 412 µs | 1.2345 ms | −412 µs | 400 ns | 500 ns | `math.MaxInt64` ns |
|---|---|---|---|---|---|---|---|
| Value (JSON and text) | `60000` | `0.412` | `1.235` | `-0.412` | `0` | `0.001` | `9223372036854.775` |

## Temporary workarounds

None.

## Contracts

```go
// internal/platform/observability/logger.go; v1 is github.com/pyvvo/funcd/api/types/v1alpha1.
// NewLogger: opts := &slog.HandlerOptions{Level: level, ReplaceAttr: replaceAttr}

// replaceAttr is the ReplaceAttr of the platform's slog handlers; each branch names its ADR.
func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	// ADR-0196's time branch goes here.
	switch a.Value.Kind() {
	case slog.KindDuration: // ADR-0197
		return durationAttr(a.Key, a.Value.Duration())
	case slog.KindAny:
		if d, ok := a.Value.Any().(v1.Duration); ok { // ADR-0197, ADR-0194's type
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
```

| Consumes | Exposes |
|---|---|
| stdlib `log/slog`, `encoding/json`, `strconv`, `strings`, `fmt`, `time`; `v1alpha1.Duration` (ADR-0194) | Each duration attribute as `<key>_ms`, a number of milliseconds; no API or config change |

## Implementation plan

Built after ADR-0194's `v1alpha1.Duration` lands; if ADR-0194 is not accepted, without the `KindAny` case and its path.

1. Prove first: write the five scenario tests (steps 3 to 5) and run them on main; they fail (`60000000000`, `1m0s`,
   `"duration_ms":0`).
2. `internal/platform/observability/logger.go`: `replaceAttr` (or only its duration branch if ADR-0196 has landed),
   `durationAttr`, `millis`; `ReplaceAttr` in `NewLogger`'s options.
3. `internal/platform/observability/logger_test.go`: `TestMillis` (the Decision table plus `math.MinInt64` ns,
   `-9223372036854.775`); `TestScenarioMeasuredLatencyKeepsFraction` (each line also decodes with `json.Unmarshal` to a
   `float64`); `TestScenarioTextFormatSameKey`; `TestScenarioNoNanosecondsInLogs` (nine paths × two formats);
   `TestDurationKeyAlreadyMs` (a `wait_ms` duration stays `wait_ms`, never `wait_ms_ms`).
4. `internal/function/bootbackoff_internal_test.go`: `TestScenarioConfiguredTimeoutWholeMs` builds `newBootBackoff`
   with a 60 s boot timeout and a JSON `observability.NewLogger` over a buffer, then calls `timedOut`.
5. `internal/edge/observ/observ.go:120`: `"duration", dur`. `observ_test.go`: `TestScenarioAccessLogCorrelated` (`:113`)
   takes a JSON `observability.NewLogger`, so its `duration_ms` assertion (`:132`) holds;
   `TestScenarioEdgeAccessLogKeepsFraction` uses the same logger and a handler that sleeps 100 µs, serves until the
   test's own bound around `serve` is under 900 µs, and after 100 tries without one fails with a message that the host
   is too loaded. It decodes `duration_ms` to a `float64` and asserts `0.1 ≤ v < 1` with at most three decimals.
6. Through `scripts/agent/d`: `go test -race -count=1`, `go vet` and `go tool golangci-lint run` on the three packages
   of steps 3 to 5. The PR says `Fixes #822`.

Definition of done: the five scenario tests failed on main and pass after steps 2 and 5; `TestMillis` passes; one
call site changed; no config key or dependency added.

## Review checklist

- [ ] `NewLogger` sets `ReplaceAttr: replaceAttr` on both handlers; no other code rewrites durations.
- [ ] The duration branch matches `KindDuration` and a `KindAny` `v1.Duration` at any group depth; group names are
      unchanged; `_ms` is appended once.
- [ ] `millis` uses `Duration.Round(time.Microsecond)` and integer arithmetic, no `float64`; values are `json.Number`.
- [ ] `observ.go:120` logs `"duration", dur`; through `NewLogger` the access-log key is still `duration_ms`.

## Consequences

- **Positive**: every logged duration is numeric with its unit, identical in both formats, sub-millisecond in the
  access log; new call sites need nothing.
- **Negative**: the keys `timeout` and `bound` become `timeout_ms` and `bound_ms`, so a saved query on the old key stops
  matching. An access-log line costs about 0.38 µs more (measured on a 9-attribute line on 2026-10-07: 0.25 µs for any
  `ReplaceAttr`, 0.13 µs and 5 allocations for `json.Number`); the access log is opt-in (`observ.Config.AccessLog`).
  A logger without the hook names the access-log field `duration`, not `duration_ms`.
- **Risks accepted**: a future call site that converts a duration, or logs a value holding one, bypasses the hook.

## Open questions

Items 1 to 5 are answered at acceptance.

1. **Agent-made choice:** call sites never put a unit in a duration's key, so the access log logs `"duration", dur`
   (H), not `"duration_ms", dur` (I), which would write nanoseconds under `duration_ms` through a hookless logger; a
   key that already ends in `_ms` is kept as a safety net; confirm.
2. **Agent-made choice:** other unit suffixes (`_s`, `_ns`) are not special-cased (none exists); confirm.
3. **Agent-made choice:** negative durations keep their sign; halves round away from zero (−1.2345 ms is `-1.235`);
   confirm.
4. **Agent-made choice:** `v1alpha1.Duration` reaches the rule through a hook case (G), not a `LogValue` method (F),
   so ADR-0194 does not change; confirm.
5. **Agent-made choice:** an embedder's `funcd.WithLogger` logger and `funcdctl`'s `slog.Default()` stay outside;
   confirm.
6. **Agent-made choice:** the OTLP log bridge is deferred; confirm. It writes a duration as an `int64` of nanoseconds
   under the original key (`otelslog` v0.19.0 `handler.go:32`, `:496-497`), and `ReplaceAttr` cannot reach its
   handler, so wiring it needs a decorator (Alternative E) or a conversion in the bridge. It is not wired today:
   `Telemetry.LogHandler` has no caller outside tests. Answered by the ADR that wires the bridge into the root logger
   (blueprint "Platform logging", Export).

## References

- Issue #822 (the decider's comments of 2026-10-07); #821 and ADR-0196; ADR-0009; ADR-0114; ADR-0194; ADR-0002 §6.
- Go 1.26.4: `slog.HandlerOptions.ReplaceAttr` doc ("The attribute's value has been resolved", so a `LogValuer` is
  resolved first; "never called for Group attributes, only their contents"), `time.Duration.Round`, the `json.Number`
  encoding; the probe also shows `Logger.With` attributes rewritten when they are added.
- Code at origin/main ec274537, as cited inline.
