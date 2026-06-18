# Review — ADR-0043 implementation (model: claude-opus-4-8)

## Verdict: pass — 0 blockers, 0 majors (ADR-0043 implementation, model: claude-opus-4-8)

Single-binary substrate selection: `Production()` stops wiring blob/bus (they join
store/runtime as deployment-injected), the daemon builds the file (default) or memory
substrate from a cobra `--memory` flag, and `serve(ctx, memoryOnly)` wires it. The
implementation matches the ADR's Contracts, every Scenario has a passing test, and the
full suite is green. One **`adr`-attributed** Minor doc nit (over-claimed "zero disk"
wording) — not the model's fault, does not block.

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor 1 — ADR "zero disk" / "writes nothing to disk" wording slightly over-claims · attribution: `adr`

Evidence: `docs/adr/0043-single-binary-substrate-selection.md:24` (Context) says
`funcd --memory` "writes **nothing** to disk" unqualified, and `:119` (Consequences) says
"zero disk" in the parenthetical. But the runtime shim is extracted to
`<dataDir>/shim.mjs` by `executionOptions` (ADR-0036) whenever `node` is present —
orthogonal to the substrate. Smoke test with node present + `--memory`:

```
=== mem-mode (node present) dir contents ===
shim.mjs
```

So `--memory` makes the **substrate** ephemeral, not the literal daemon process (the shim
is still written). The ADR's own **scenario `daemon-memory-flag`** is correctly qualified
("nothing is written under `<dataDir>` **for the substrate**", `:31-32`), as is the DoD
(`:106`, "no disk writes **for blob/bus**") and the Review checklist (`:112`, in the
blob+bus context). Only the Context narrative (`:24`) and the Consequences parenthetical
(`:119`) drop the qualifier.

Attribution: `adr` — the document's prose, not the implementation; the code does exactly
what the (correctly qualified) scenario specifies. Owner: a future doc-clarifying
superseding ADR if ever revisited; does **not** loop back to the builder and does **not**
count against the model. Severity Minor — the load-bearing spec (scenario + DoD) is
accurate; only two narrative phrases are loose.

### ✅ Verified correct (keep it)

- **Build / vet / lint**: `go build ./...` exit 0; `go vet ./cmd/funcd/... ./pkg/funcd/...`
  exit 0; `go tool golangci-lint run ./cmd/funcd/... ./pkg/funcd/...` → `0 issues.` exit 0.
- **Tests**: `go test ./cmd/funcd/ -run 'Substrate|ProductionRequires|DaemonVersion'` ok;
  `go test ./pkg/funcd/` ok; **full `go test ./...` exit 0** (all packages, incl. e2e +
  dataplane — they wire their own substrate via `InMemory()`/explicit drivers, unaffected).
- **scenario: production-injects-substrate** — `TestProductionRequiresSubstrate`
  (`cmd/funcd/main_test.go:30`) asserts `funcd.New(Production(), store, runtime, auth)`
  errors without `WithBlob`/`WithBus`. Verified in `presets.go`: `Production()` no longer
  sets `c.blob`/`c.bus` (only a deployment-injected comment at `:96`); `InMemory()` keeps
  wiring them (`:48-49`) — correctly scoped change.
- **scenario: daemon-file-default** — `TestDaemonSubstrate/file-default` passes; smoke
  (`FUNCD_DATA_DIR` set, killed after ~1.5s) wrote `blob` + `nats` under the data dir and
  logged `substrate=file`.
- **scenario: daemon-memory-flag** — `TestDaemonSubstrate/memory-flag` passes; smoke with
  `--memory` wrote **nothing** under the data dir and logged `substrate=memory`.
- **`--memory` in help** — `funcd --help` shows
  `--memory   run fully in memory (ephemeral — no disk); default is file-backed/durable (ADR-0043)`.
- **Leak check (the ADR's core justification)** — verified leak-free:
  `Production()` constructs **no** bus at all (presets.go), and the daemon calls
  `nats.Open` exactly once per run with the chosen storage (`main.go:128` MemoryStorage /
  `:145` FileStorage). So `--memory` never constructs a file NATS server that is then
  orphaned — the substrate is chosen *before* construction, as the ADR demands.
  `TestDaemonSubstrate` calls `p.Shutdown()` on both branches (`main_test.go:67`), so even
  the file-path test closes its NATS server rather than leaking it. (Note: the nats driver's
  MemoryStorage path uses an internal `os.MkdirTemp` for JetStream metadata cleaned on
  close — pre-existing ADR-0008 behavior under `/tmp`, not `<dataDir>`, orthogonal to this
  ADR; the "nothing under `<dataDir>` for the substrate" claim holds, confirmed by smoke.)
- **Contracts honoured** — Production() minus blob/bus; `substrateOptions(ctx, memoryOnly,
  dataDir)` returns `[]funcd.Option` + label (file ? `file://<dataDir>/blob` +
  `FileStorage(<dataDir>/nats)` : `mem://` + `MemoryStorage`); `--memory` bool persistent
  flag on `newRootCmd`; `serve(ctx, memoryOnly)`. Matches the ADR Contracts block exactly.
- **Store stays memory in both modes** (caveat honored) — `serve` always wires
  `WithStore(store.New(memory.New()))` regardless of `--memory` (`main.go:92`).
- **Conventions (ADR-0002)** — no `any`/`interface{}` in the new code; no `panic`; no
  `fmt.Print*`; `memoryOnly` is a function-local var (not a package global); ctx-first
  `substrateOptions(ctx, …)`. The `fmt.Errorf` startup wrapping matches the pre-existing
  `executionOptions` pattern (consistent boot-error style, not a regression).
- **Deps** — no new dependency; reuses `internal/blob/gocloud` + `internal/bus/nats`;
  `go mod verify` → `all modules verified`.
- **No scope creep** — implements ADR-0043 only; no SDK/API change; e2e + bench/dataplane
  tests untouched in behavior (only a one-line e2e doc-comment updated to show
  `WithBlob(file), WithBus(file)`).
- **Tree vs ADR surface** — exactly the 4 sanctioned surfaces touched: `pkg/funcd/presets.go`,
  `cmd/funcd/main.go`, `cmd/funcd/main_test.go`, `tests/e2e/linux_integration_test.go`
  (doc-comment) + the feat row. Nothing missing, nothing unexplained-extra.
- **Hygiene** — identity grep over all changed files: no local username, no `/Users/`,
  no `/home/` paths. ADR at `Reviewing`; F19 + F21 rows already at `implemented` and linked
  to ADR-0043 (multi-ADR rows — not walked back).

### Definition of Done

8 / 8 hold (ADR Review checklist [4] + applicable generic DoD [4: full suite green, every
scenario passing, real behaviour/no stubs, conventions+hygiene]).

- [x] `Production()` no longer wires blob/bus; `New(Production())` without `WithBlob`/`WithBus` fails — `TestProductionRequiresSubstrate`.
- [x] file default + `--memory` memory, no disk for substrate — `TestDaemonSubstrate` both branches + smoke.
- [x] No file NATS server constructed in `--memory` (chosen before construction) — verified leak-free.
- [x] `--memory` in `--help`; store stays memory in both; no new dependency; no identity/path leak.

Misses: none `model`-attributed. The one Minor is `adr`-attributed (narrative "zero disk"
wording) and does not subtract from a DoD item — the scenario/DoD/checklist that the items
test are all correctly qualified.

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0043 (implementation) → pass, 0 blockers / 0 majors /
1 minor, 0 model-attributed, DoD 8/8. See docs/reviews/model-scorecard.md.

### Recommendation

**Pass — stamp ADR-0043 `Reviewing → Implemented`.** The implementation is correct,
leak-free, and fully scenario-tested. The single Minor is an `adr`-attributed doc-prose
nit ("zero disk" vs the correctly-qualified "ephemeral for the substrate") — record it for
a future doc-clarifying superseding ADR if ADR-0043 is ever revisited; it does not loop
back to the builder. F19 + F21 stay `implemented`.
