## Verdict: pass — 0 blockers, 0 majors  (ADR-0108 implementation, model: claude-opus-4-8)

EventSource v2 (F72) reshapes the resource from the v1 `spec.{type,timer,function}` flat union to a
kind-keyed union (`spec.timer.events: [{name,interval}]`, exactly one source kind hosting a list of
NAMED events), drops the binding (`spec.function`/`type`), and turns a firing from a direct function
invocation into a PUBLISH of a named CloudEvent onto a new in-process `Fanout` `Publisher` seam that
the F69 Sensor (ADR-0109) will subscribe to. The clean break is complete and grep-verified; the whole
tree is green.

### Verification (evidence)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...` | exit 0 |
| vet | `go vet ./...` | exit 0 |
| tests | `go test ./internal/eventing/... ./api/types/... ./internal/function/... ./internal/controlplane/...` | exit 0 (all ok) |
| OpenAPI golden | `go test ./internal/controlplane -run Spec` | `TestSpecGeneratedFromGo` PASS, `TestSpecReflectsGoShape` PASS |
| lint | `golangci-lint run` (touched pkgs) | `0 issues` |
| deps | `go mod verify` | all modules verified; **no go.mod/go.sum diff** |

**Clean break — grep-verified complete.** Every v1-shape symbol returns ZERO matches across the tree:
`NewTimerEvent`, `EventSourceType`, `EventSourceTypeTimer`, `EventSourceTypeHTTP`, `TimerSpec`,
`timerEventType`, `contentTypeCE`, and `Spec.Function` (none anywhere). Every `Spec.Timer` reference is
the new `*TimerSource` v2 usage. No v1-shape EventSource manifests remain in the repo. The migrated
test sites match the ADR's enumerated list exactly: `eventing_test.go`, `cloudevent_test.go`,
`validate_test.go`, `shim_test.go` (timer-invokes-real-handler removed + eventing import dropped), and
`dataplane_e2e_test.go` (`TestScenarioTimerWakesColdFunctionE2E` removed, with an explicit NOTE that
ADR-0109 restores it as timer→Sensor→wake).

### 🔴 Blocker — none

### 🟡 Major — none

### Minor
- `Fanout` is implemented as a concrete exported struct (`*eventing.Fanout`, stored on `Platform.eventFanout`)
  rather than the exported `Fanout` interface the ADR Contract sketched (`interface { Publisher; Subscribe(...) }`).
  The Subscribe capability is present and exported, so the F69 Sensor can bind to it; a concrete type also
  avoids an interface/struct name collision. Attribution: `model` — a defensible design deviation, non-blocking
  (ADR-0109 defines the subscriber-side dependency it consumes). `internal/eventing/fanout.go:15`.

### ✅ Verified correct (keep it)
- **Scenario coverage — all 8 + the fanout routing test have named, un-skipped, passing tests:**
  - `timer-source-reconciles-ready` → `TestScenarioTimerSourceReconcilesReady` (registers 1 timer, Phase=Ready).
  - `named-event-emitted` → `TestScenarioNamedEventEmitted`: publishes exactly one CloudEvent (source URI +
    `type==tick`) **and asserts the store holds zero Invocations** — the no-Invocation half of the ADR is proven.
  - `multiple-named-events` → `TestScenarioMultipleNamedEvents` (two events → `ActiveTimers()==2`).
  - `empty-spec-rejected` → validate matrix rows "no source kind" and "timer with no events" (both `fault.Invalid`).
  - `event-names-unique` → validate matrix "duplicate event names" row.
  - `deregister-on-delete` → `TestScenarioDeregisterOnDelete` (delete → `ActiveTimers()==0`).
  - `no-binding-field-schema` → covered by the schema/golden test rather than a dedicated 422 test: the
    regenerated `EventSourceSpec` carries `additionalProperties: false` with only the `timer` key (the removed
    `function`/`type` keys are gone), which is exactly what makes a v1 manifest a huma-422 at the schema edge.
    `TestSpecGeneratedFromGo` guards that shape. Noted as schema-covered per the ADR.
  - `kind-union-exactly-one` → structurally enforced: only `timer:` exists in the V1 struct, so a 2-kind
    manifest is not constructible in Go; `Validate` counts non-nil kinds and rejects `kinds != 1` (the "no
    source kind" matrix row exercises the 0-kind arm, and the counter is future-proof for `webhook:`). The ADR
    anticipated this; the implementation handles it correctly.
  - fanout routing → `TestFanoutRoutesByKey`: match delivers, a different event name does not, publish-to-nobody
    is a no-op, a foreign (non-`funcd://`) source URI is dropped (never an error), and `cancel()` deregisters.
- **Routing derivation is a correct inverse.** `SourceURI(ns,source) = funcd://<ns>/eventsource/<source>`;
  `ParseSourceURI` cuts the scheme, splits into exactly `[ns, "eventsource", name]`, and returns `ok=false`
  for any foreign/malformed URI. `Fanout.Publish` derives `(ns, source, event=Type)` from the envelope and
  drops a non-funcd source silently — the publish-to-nobody / foreign-URI no-op holds by construction, and
  `cloudevent_test.go` proves the round-trip. Delivery runs the subscriber funcs outside the lock.
- **Publish, not invoke.** `Source.Fire` builds `NewNamedEvent` and calls `Publisher.Publish`; the Invoker/
  Waker/Endpoints deps and the Invocation record are gone from `Deps`/`Source`. The tick engine (`Run`/
  `dueTimers`) and per-`(ns,source,event)` keyed timer set are reused in shape; `registerTimer` preserves
  `lastFire` on unchanged intervals and prunes dropped events.
- **Validate** enforces exactly-one-kind, ≥1 event, unique DNS-1123 names, and the 100ms–24h interval bounds,
  all as `fault.Invalid` — the matrix exercises the floor, ceiling, non-DNS-1123 name, and duplicate cases.
- **Conventions:** no `any`/`interface{}` in exported/port signatures (`Publisher.Publish(ctx, CloudEvent)`,
  `Subscribe(...func(context.Context, CloudEvent))`); ctx-first; `log/slog` only; no `panic`/`fmt.Print`;
  `api/fault` for every error. Wiring in `pkg/funcd/funcd.go` builds one `NewFanout()`, passes it as the
  Source's `Publisher`, and holds it on `Platform.eventFanout` for the F69 Sensor.
- **OpenAPI** regenerated: `EventSourceSpec` → `{timer: $ref TimerSource}` with `additionalProperties:false`;
  new `TimerSource{events:[TimerEvent]}` and `TimerEvent{name,interval}` schemas; `TimerSpec` and the `type`
  enum removed.

### Definition of Done
7 / 7 ADR Review-checklist items hold (kind-union + type/Function removed; timer hosts named events;
publish-not-invoke + no Invocation; Validate rejection matrix + schema-edge 422; Fanout routing/Subscribe/
deregister; clean break grep-clean + e2e removed/restored + OpenAPI regen; documented deferrals with exits).
Generic phase DoD also holds: full targeted suite green, real logic (no stubs), Contracts honoured, tree
matches the ADR's surface, no dep drift, tracking consistent. No misses.

### Model scorecard
Recorded: claude-opus-4-8 on ADR-0108 (implementation) → pass, 0 blockers / 0 majors / 1 minor,
1 model-attributed, DoD 7/7. See docs/reviews/model-scorecard.md.

### Recommendation
Sign off. The producer half of the eventing reshape is complete and internally consistent; the sole Minor
(concrete `Fanout` vs the sketched interface) is a defensible choice that ADR-0109 consumes directly — no
rework required. Advance ADR-0108 `Reviewing → Implemented` and the F72 row to `implemented`.
