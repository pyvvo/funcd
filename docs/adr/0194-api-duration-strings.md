# ADR-0194: API durations are duration strings — one `Duration` type, one grammar (h, m, s, ms)

- **Status**: Implemented (2026-10-08)
- **Date**: 2026-10-07
- **Deciders**: green-0-rabbit
- **Tags**: api, types, openapi, validation, funcdctl, config, workflow, breaking-change
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md) (API contracts & codegen; joined as a further entry)
- **Supersedes (in part)** (back-links `Superseded in part by: ADR-0194` added at acceptance; lines at ec274537):
  - [ADR-0016](0016-activator-scale-to-zero.md) (Implemented): Scope Out, "Human-friendly duration parsing
    (`idleTimeout: 5m` in YAML) — the API/CLI codec (F18)" (126–127); plan step 1, "marshals as int ns" (352–353).
  - [ADR-0048](0048-dto-validation-reference.md) (Implemented), for durations: the `nonNegInt` row (67), the
    "**duration cap** (literal ns)" row (69), the tag bounds at 124 and 196, "replica/duration bounds) is
    schema-enforced at the edge" (80), and Scope Out "`time.Duration` wire form — serializes as int64 ns" (274).
  - [ADR-0096](0096-engine-native-builtin-steps.md) (Implemented): `wait` as "A Go duration string (`"30s"`) or a
    `${{ … }}` goja **Select** expression evaluating to a **number of seconds**" (134–136), restated at 55–57, 205,
    229 (`evalWait`) and 319 ("(→ seconds)").
  - [ADR-0151](0151-external-invoke-deadline.md) (Implemented): Decision 1, "`spec.timeout` by the OpenAPI schema
    (422 at apply)" (89), restated at 43, 132, 234 and 249, and the settled "1 h cap in the OpenAPI schema" (262–264).
  - The Contracts declarations of the seven fields as `time.Duration` (with numeric tags where present): ADR-0016
    (233, 333), [ADR-0064](0064-fn-to-fn-rpc-links.md) (172), [ADR-0094](0094-workflow-engine-core.md) (320, 349),
    ADR-0096 (194), [ADR-0108](0108-eventsource-v2-named-events.md) (174).
  - The clauses that accept any Go duration or a bare `0` as a config value: ADR-0151 (174, "unset or 0 ⇒ default"),
    [ADR-0163](0163-retry-times-in-config.md) (124), [ADR-0167](0167-process-worker-crash-recovery.md) (121–122) and
    [ADR-0170](0170-owner-garbage-collector.md) (148).
- **Relates to**: [ADR-0005](0005-api-surface-code-first-huma.md), [ADR-0023](0023-eventing-core.md),
  [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md). **Conforms to** the YAML examples of ADR-0094
  (236–237, 258–259), ADR-0108 (36, 42–43, 109) and [ADR-0125](0125-funcdctl-dev-local-run.md) (193).
- **Blueprint**: no change; `blueprint.md:283` already writes `idleTimeout: 5m`.

## Context & Need

Seven API fields are `time.Duration` (`api/types/v1alpha1/eventsource.go:42`, `function.go:57`, `:126`, `:169`,
`workflow.go:36`, `:113`, `:167`), so manifests, bodies, the OpenAPI schema and stored objects carry int64
nanoseconds (#816): one hour is `3600000000000`, `timeout: 30` is accepted as 30 ns (the schema minimum is 0), and
`interval: 5s` fails to decode. Six fields are bounded only by huma schema tags (`function.go:222`,
`workflow.go:243`), so a write through `store.Create` (`internal/store/store.go:294`) skips their bounds.

Purpose: one `Duration` type is the only form of a span of time in API resource fields (manifests, bodies, the
OpenAPI file, stored objects, run records), with one grammar and one bound check that the platform config shares.

## Scenarios

- **scenario: string-duration-applies** — Given a Function with `spec.scaling.idleTimeout: 10m`, When
  `funcdctl apply` stores it and `funcdctl get -o json` reads it, Then it reads `"idleTimeout": "10m"`.
- **scenario: integer-duration-refused** — Given `spec.timeout: 30` in a manifest, When `funcdctl apply` runs, Then it
  fails before any request with an error that names the grammar; a raw HTTP body with `"timeout": 30` gets 422
  `expected string` at `body.spec.timeout`, and `"timeout": "30"` gets a 422 that names the grammar. Nothing is stored.
- **scenario: sub-millisecond-refused** — Given `spec.timeout: 500us` or `1.5s`, When applied, Then it is refused as above.
- **scenario: shortest-form-output** — Given `spec.timeout: 90m` (or `1500ms`, `3600s`, `90s`), When applied and read
  back, Then it reads `1h30m` (or `1s500ms`, `1h`, `1m30s`); an optional field given `0s` is omitted on read.
- **scenario: bounds-enforced-at-create** — Given a Function with `spec.timeout: 2h`, When `funcdctl apply` runs, Then
  the offline pre-flight refuses it, naming `spec.timeout`, `2h` and `[0s, 1h]`; raw HTTP gets 400
  `urn:funcd:problem:invalid` with nothing stored; `store.Create` returns `fault.Invalid`. `1h` is stored.
- **scenario: schedule-source-example-runs** — Given funcd-typescript's `examples/workflow/schedule-source.yaml` with
  `interval: 5s`, When the `workflow` Lima lane applies it, Then the EventSource ticks and the Sensor starts runs.
- **scenario: wait-literal-checked-at-apply** — Given `builtin.wait: 1.5s`, When applied, Then it is refused, naming
  the step and the grammar.
- **scenario: wait-expression-yields-string** — Given `wait: ${{ input.wait }}`, When a run gets `{"wait": "50ms"}`,
  Then the step waits about 50 ms and succeeds; with `{"wait": 0.05}` it fails, naming the grammar.
- **scenario: config-shares-grammar** — Given `runtime.supervisionPeriod: 1.5s` or `0s` (or `runtime.process.stopGrace`
  at `11s`) in the platform config, When funcd starts, Then startup fails, naming the key and the grammar or bounds.

## Scope

**In**: the type, its grammar, codec, schema and bound check; the seven fields and their bounds; `builtin.wait`; the
platform config's duration keys; `funcdctl` output; the integer fixtures; the funcd-typescript example and its pin.
**Out**, each with its own issue under #817: WorkflowRun step `startedAt`/`endedAt` (#821), JSON-log durations (#822),
the blob presign `expiry` (#823) and the logs `since` parameter (`internal/controlplane/logs.go:311`, #824). Also out:
internal integers (`X-Funcd-Timeout-Ms`, `FUNCD_POOL_LOAD_TIMEOUT_MS`, runstate, CORS), stored-data conversion and integer-field checks.

## Constraints & Decision drivers

- Decided by the decider (#816, 2026-10-07): a clean break (no legacy integers; no installation runs funcd), a strict
  grammar, the millisecond as the smallest unit, shortest-form output, and one helper that bounds every duration.
- `int64(d)` sites keep compiling (`engine.go:1623`); the others convert (plan step 3). `pkg/sdk` quotes a numeric
  YAML scalar only for a string-kind target (`pkg/sdk/sdk.go:484-487`), so `timeout: 30` stays a JSON number.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| Keep `time.Duration` (int64 ns) | no change | the defect in #816 | rejected |
| Integers with the unit in the name (Knative `timeoutSeconds`) | no parser | renames seven fields; the decider chose strings | rejected |
| `type Duration string` | simplest schema; no codec | every comparison and arithmetic site must parse the string and handle an error at use time (the grammar refuses `"30"` either way) | rejected |
| Struct wrapper (`struct{ time.Duration }`) | Kubernetes style; `omitzero` (Go ≥1.24) omits a zero value | same wire form and conversions, but comparisons with constants (`fn.Spec.Timeout > 0`, `dataplane.go:226`) also need `.Duration`, and every field changes `omitempty` to `omitzero` | rejected |
| `time.ParseDuration` alone | stdlib | accepts fractions, signs, `ns`/`us`/`µs`, any unit order and repeats; the parser would define the grammar | rejected as the rule; kept as step 2 of the parse |
| k8s `metav1.Duration` | widely used | `ParseDuration` plus Go's `String()` (`10m0s`); a new dependency | rejected |
| Prometheus `model.ParseDuration` and `Duration.String()` (Apache-2.0, indirect `v0.67.5`, `go.mod:317`) | closest prior art (ms floor, ordered units) | accepts a bare `0` and `d`, `w`, `y`; `String()` writes `d`, `w`, `y` (48h → `2d`, `model/time.go:272-310`), which the grammar refuses; wrapping it saves no code | rejected |
| A hand-written parser | exact | a second copy of the grammar beside the published pattern | rejected |
| OpenAPI `format: duration` | standard keyword | huma checks it with `time.ParseDuration` (huma v2.38.0 `validate.go:225-228`); JSON Schema means ISO 8601 | rejected |
| Bounds in a huma resolver (422 at the edge) | keeps today's 422 | an edge-only second layer that `store.Create` bypasses; `pkg/sdk` maps 400 and 422 alike to `fault.Invalid` | rejected by the decider |
| Accept legacy integers for one release | smooth upgrade | keeps `timeout: 30` meaning 30 ns; no installation needs it | rejected by the decider |

## Decision

1. **One type.** `v1alpha1.Duration` is `type Duration time.Duration`: int64 nanoseconds in memory, a JSON string on
   the wire. The seven fields take it; Go call sites convert with `time.Duration(d)` and `v1.Duration(x)`.
2. **The grammar** (ABNF, RFC 5234 with RFC 7405 case-sensitive `%s` strings; `DIGIT` is `0`–`9`):
   ```abnf
   duration = hours [minutes] [seconds] [millis] / minutes [seconds] [millis] / seconds [millis] / millis
   hours    = 1*DIGIT %s"h"
   minutes  = 1*DIGIT %s"m"
   seconds  = 1*DIGIT %s"s"
   millis   = 1*DIGIT %s"ms"
   ```
   The value is the sum of the components; no sign, fraction or space. Valid: `10m`, `1h30m`, `90s`, `500ms`,
   `1s500ms`, `0s`. Refused: `1.5s`, `-5m`, `10`, `1d`, `500us`, `30m1h`, `1m1m`, `""`, `0`. A sum above the int64
   nanosecond maximum is refused; the largest value, `2562047h47m16s854ms`, is `MaxDuration`.
3. **Parsing in two steps.** `ParseDuration` matches the compiled `DurationPattern` (the RE2 form of the grammar), then
   computes the value with `time.ParseDuration`, which is exact for every string the pattern admits and reports an
   overflow as an error; a one-line whole-millisecond check guards the invariant.
4. **Normalized output** (the decider's "shortest form"). `String()` writes h, m, s, ms, largest unit first, zero
   components dropped, zero as `0s`: `90m` → `1h30m`, `1500ms` → `1s500ms`, `3600s` → `1h`, and `90s` → `1m30s`
   (the rule decides, not the shortest string). `MarshalJSON` writes it as a JSON string and refuses a negative or
   sub-millisecond value (only Go code can make one). An optional zero field stays omitted (`omitempty`).
5. **Decoding.** `UnmarshalJSON` accepts only a JSON string in the grammar (`null` leaves the value unchanged); a
   number, a bool or an overflow is refused, naming the grammar (over raw HTTP an overflow passes the pattern and
   gets 422 at `body`). Stored objects and runstate records use this codec; nothing reads the old integers.
6. **Schema.** `Duration.Schema` returns a new `{type: string, pattern, patternDescription}` on each call (huma
   writes field tags into it, `schema.go:565-570`); huma answers a non-string or a malformed string with 422. The
   seven fields lose `minimum`/`maximum` (huma would copy them onto the string schema) and gain a `doc` tag with their
   bounds and zero meaning, copied into `description` (`schema.go:570`; new in `api/types`), so the spec shows them.
7. **One bound check** (decided by the decider, 2026-10-07). `CheckDuration(op, field, d, lo, hi)` bounds every API
   duration field and config duration key. Each kind's `Validate` calls it with the field's named constants (Contracts;
   the `doc` test reads them too); `funcdctl apply` runs `Validate` offline (`cmd/funcdctl/cli.go:152-162`) and
   `store.Create`/`Update` on every write. A value out of bounds is `fault.Invalid` naming the field, value and
   `[lo, hi]`, answered 400 (`api/fault/problem.go:25`); a malformed value stays 422 from the pattern. Zero keeps its meaning.
8. **`builtin.wait`** stays a `string` field (decided by the decider, 2026-10-07). A literal follows the grammar and
   `Workflow.Validate` checks it at apply. An expression must evaluate to a JSON string in the grammar; any other
   result fails the step at run time with `fault.Invalid` naming the grammar, the path a non-number takes today
   (`condition.go:201`), so a templated duration has one shape.
9. **Platform config** (decided by the decider, 2026-10-07). `parseDuration(key, s, def, lo, hi)` (`main.go:641-658`)
   returns `def` for an empty value, else `v1.ParseDuration`, then `v1.CheckDuration` with the key's named bounds. `lo`
   is `1ms` for a key that must be positive (today `parseDurationOr`), `0s` where 0 keeps its meaning, and a new
   `minRetryBackoffMax` (5ms, beside `maxStopGrace`, as at `pkg/funcd/options.go:624`) for `controller.retryBackoffMax`
   (`main.go:608`). `hi` is `MaxDuration` ("no upper bound") except `maxStopGrace` (10s) for `runtime.process.stopGrace`
   (`:631`), `v1.MaxRetryBackoff` for `workflow.defaultRetryBackoff` (`:614`, ADR-0163:127) and `Duration(v1.MaxInvokeTimeout)`
   for `invoke.defaultTimeout` (`options.go:182` stays the embedders' guard); the cross-key orderings (`:610`, `:612`,
   `:616`) stay. A bare `0`, `1.5s` and `us`/`ns` stop startup; `0s` is the zero (`config.go:301` follows).
10. **funcdctl.** `get`, `describe` and `workflow describe` (`cmd/funcdctl/workflow.go:339`, rounded to 1 ms) print `String()`.
11. **Clean break.** funcd-typescript's `schedule-source.yaml` (lines 4, 15) moves to `5s` in a release the #816 PR pins.

## Temporary workarounds

None.

## Contracts

```go
package v1alpha1 // api/types/v1alpha1/duration.go

// DurationPattern is the duration grammar (ADR-0194): the single source for ParseDuration and the OpenAPI schema.
const DurationPattern = `^([0-9]+h([0-9]+m)?([0-9]+s)?([0-9]+ms)?|[0-9]+m([0-9]+s)?([0-9]+ms)?|[0-9]+s([0-9]+ms)?|[0-9]+ms)$`
// Duration is a span of time: int64 nanoseconds in memory, a duration string on the wire (ADR-0194).
type Duration time.Duration
// MaxDuration is the largest value the grammar admits (2562047h47m16s854ms); hi = MaxDuration means "no upper bound".
const MaxDuration = Duration(math.MaxInt64 - math.MaxInt64%int64(time.Millisecond))
// ParseDuration matches s against DurationPattern, then computes it with time.ParseDuration; a mismatch or an
// overflow is fault.Invalid naming s and the grammar.
func ParseDuration(s string) (Duration, error)
// CheckDuration bounds every API duration field and config duration key: fault.Invalid(op) naming field (a field
// path or config key), d and [lo, hi] in the normalized form ("at least lo" when hi is MaxDuration) when d < lo,
// d > hi or d is not a whole millisecond.
func CheckDuration(op, field string, d, lo, hi Duration) error
// String writes the normalized form; a negative or sub-millisecond value falls back to time.Duration's String.
func (d Duration) String() string
func (d Duration) MarshalJSON() ([]byte, error)
func (d *Duration) UnmarshalJSON(b []byte) error
// Schema returns a new schema on each call: {type: string, pattern: DurationPattern, patternDescription}.
func (Duration) Schema(huma.Registry) *huma.Schema
```

Field bounds are `Duration` constants beside `MaxInvokeTimeout` (`function.go:24-27`; it stays `time.Hour` for `pkg/funcd`):

| Field (file:line at ec274537) | `[lo, hi]` in `Validate` and `doc` | Zero means (unchanged) |
|---|---|---|
| EventSource `spec.timer.events[].interval` (`eventsource.go:42`, required) | `MinTimerInterval` = 100ms, `MaxTimerInterval` = 24h | refused |
| Function `spec.timeout` (`function.go:57`) | 0s, `Duration(MaxInvokeTimeout)` = 1h | `invoke.defaultTimeout` (60 s) |
| Function `spec.links[].timeout` (`function.go:126`) | 0s, `MaxLinkTimeout` = 5m | the 30 s link default |
| Function `spec.scaling.idleTimeout` (`function.go:169`) | 0s, `MaxIdleTimeout` = 24h | reclaim disabled |
| Workflow `spec.timeout` (`workflow.go:36`) | 0s, `MaxWorkflowTimeout` = 168h | no run bound |
| step `function.timeout` (`workflow.go:113`) | 0s, `MaxStepTimeout` = 24h | `workflow.defaultStepTimeout` |
| step `function.retry.backoff` (`workflow.go:167`) | 0s, `MaxRetryBackoff` = 1h | `workflow.defaultRetryBackoff` |

| Direction | What |
|---|---|
| Consumes | `math`, `regexp`, `time.ParseDuration` (stdlib); huma `SchemaProvider` (precedent `ids.go:45-60`); no new dependency |
| Exposes | `Duration`, `ParseDuration`, `DurationPattern`, `CheckDuration`, `MaxDuration` and the bound constants, to the API types, `cmd/funcd`, `funcdctl` and `internal/workflow` |
| Wire and schema | a JSON string in manifests, bodies, metastore objects and runstate pinned specs; OpenAPI `type: string`, `pattern`, `patternDescription`, `description` from `doc`; no `minimum`, `maximum` or `format` |

## Implementation plan

1. `api/types/v1alpha1/duration.go` and `duration_test.go`; the bound constants in `function.go` (Contracts).
2. The seven fields take `Duration` and a `doc` tag, without numeric tags. `CheckDuration` calls go into
   `EventSource.Validate` (replacing `eventsource.go:125-126`), `FunctionSpec.Validate` and `Workflow.Validate`, with
   a literal-`wait` parse beside `workflow.go:330-342`; the "schema-enforced at the edge" comments drop durations
   (`function.go:222`, `workflow.go:243`). `evalWait` (`condition.go:184-206`) parses a literal or a string result.
3. Go call sites convert: `activator.go:579-602`, `dataplane.go:226-227`, `workernode/local/resolver.go:39-42`,
   `eventing.go:220-223`, `engine.go:950`, `:1275`, `:1391`, `:1623`, `reconcile_workflow.go:480`, and the tests;
   `engine.go:1375` reads `v1.MaxRetryBackoff`; `cmd/funcd/main.go` takes Decision 9; `cmd/funcdctl/workflow.go:339` uses `String()`.
4. Fixtures: `tests/e2e/journey_test.go:100` → `"idleTimeout":"1h"`; `pkg/sdk/manifest_test.go:375`, `:401`, `:416`
   → `interval: 5s`; `internal/workflow/builtin_test.go:77` → `${{ input.wait }}` with `{"wait":"50ms"}`, `:231` →
   `? "0s" : "60s"`, and `engine_test.go:315` → `? "1s" : "0s"` (a run-time failure, not a compile error);
   `TestScenarioSpecTimeoutBounds` (`api_test.go:318`) and `TestFunctionSpecTimeoutTagIsMaxInvokeTimeout`
   (`api/types/v1alpha1/function_test.go:10`) become the bounds and `doc` tests.
5. `just generate` regenerates `api/openapi/funcd.v1alpha1.yaml` (drift test `api_test.go:141`).
6. Cross-repo: a funcd-typescript PR sets `interval: 5s` and rewrites the comment at line 4; after its release, the
   funcd PR runs `go get github.com/pyvvo/funcd-typescript@<that tag>` and carries `Fixes #816`, `!`, `BREAKING CHANGE:`.

**Test plan** — one test per scenario, named after it, plus the contract tests:

| Scenario | Where |
|---|---|
| string-duration-applies, shortest-form-output | `pkg/sdk` decode, then a store-backed server built as in `admission_wiring_test.go:48-49` |
| integer-duration-refused, sub-millisecond-refused | `sdk.DecodeManifest` and humatest bodies (`api_test.go`) |
| bounds-enforced-at-create | a table over every Contracts row at lo − 1ms, lo, hi and hi + 1ms through `Validate`, plus a sub-millisecond value set from Go, and each field's `doc` naming `lo.String()` and `hi.String()` of its constants; the store-backed server (2h ⇒ 400, `-1s` ⇒ 422, 1h stored); `store.Create` |
| wait-literal-checked-at-apply, wait-expression-yields-string, config-shares-grammar | `api/types/v1alpha1`, `internal/workflow`, `cmd/funcd/main_test.go` (per-key lo and hi, including `MaxDuration`) |
| schedule-source-example-runs | `just lima-example workflow` (`scripts/lanes.yaml:270`, `e2e/workflow.venom.yml:305`) |
| contract (`duration_test.go`) | Decision 2's table through `ParseDuration` and `regexp.MustCompile(DurationPattern)` (which must agree), the overflow pair, `MaxDuration`, `CheckDuration` messages, `String` round trips, `MarshalJSON` refusals, `UnmarshalJSON` for a string, number, bool and `null`, `Schema` |

**Definition of done**: `just ci` and the `workflow` lane (new funcd-typescript pin) green; the generated spec has no
integer duration; `grep -rn 'time\.Duration' --include='*.go' --exclude='*_test.go' api/types/v1alpha1` finds only `duration.go`.

## Review checklist

- [ ] `DurationPattern` is the only grammar copy, used by `Schema` and by `ParseDuration` before `time.ParseDuration`;
      no other parser, no new module; the table test holds every example of Decision 2.
- [ ] `UnmarshalJSON` refuses a JSON number; `MarshalJSON` writes the normalized form; zero optional fields are omitted.
- [ ] The seven fields carry `doc` and no `minimum`/`maximum`; `CheckDuration` with the named constants bounds them.
- [ ] Every API duration field and config duration key is bounded by `CheckDuration` (library guards such as
      `options.go:182`, `internal/gc/gc.go:74` and the `WithBootBackoff` checks remain); `wait` uses `v1.ParseDuration`.
- [ ] Every scenario has a same-name test; the spec is regenerated; the funcd-typescript pin is the new tag.

## Consequences

- (+) A manifest reads `10m`; nothing finer than 1 ms or without a unit can be written; every write enforces bounds.
- (−) A v0.7.x data dir with a duration no longer loads; one bad record fails a whole List (`store.go:216-234`,
  `internal/workflow/runstate/badger/badger.go:116-146`). Accepted: no installation runs.
- (−) Old `funcdctl` and `pkg/sdk` clients cannot read a new server's objects, and the reverse.
- (−) A config value that starts funcd today, such as a bare `0`, `1.5s` or `500us`, stops it at startup.
- (−) A bound violation answers 400 `invalid` where the schema answered 422; a malformed value still answers 422.

## Open questions

None.

## References

- Issue #816 and the decider's comments of 2026-10-07; tracker #817. huma v2.38.0 `schema.go:268-274`, `:565-570`,
  `validate.go:225-228`, `:500-508`; Prometheus `common@v0.67.5` `model/time.go:189-310`; `metav1.Duration`; RFC 5234, 7405.
