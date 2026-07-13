# ADR-0132: funcdctl dev — `--print-env` for scriptable dev S3 credentials

- **Status**: Implemented
- **Implemented**: 2026-07-12 — retroactive: the code + its test shipped this session and are green (both
  build tags, lint, `go mod verify`), and the flow is proven live — `just seed-releves` loads the creds via
  `eval "$(funcdctl dev … --print-env)"` (no manual export), seeds 13 statements, and all 13 runs Succeed with
  the gold mart spanning every month. Per the ADR-0126/0128/0130/0131 precedent the shipped test + the live run
  are this ADR's coverage; no separate adr-impl-review gate ran.
- **Date**: 2026-07-12 (**RETROACTIVE documentation ADR** — ADR-0131 precedent. Decided + shipped interactively
  while making the releve-lakehouse seed flow one command, on branch `feat/funcdctl-contract-codegen`. The
  *Implementation plan* reads forward-tense as the template wants but describes code that already exists.)
- **Deciders**: green-0-rabbit
- **Tags**: dx, tooling, funcdctl, dev, s3
- **Realizes**: [FEAT-0001/F97](../feat/0001-feat-v1.1.md) (funcdctl dev — `--print-env` scriptable S3 creds)
- **Relates to**: [ADR-0125](0125-funcdctl-dev-local-run.md) (the dev command + its `--gport`/`--s3port` banner) ·
  [ADR-0128](0128-funcdctl-dev-interpreter-config-and-seedable-writes.md) (the dev S3 relaxed-writes that make
  seeding `landing` possible) · [ADR-0085](0085-s3-gateway-request-signing-keypair.md) (the per-function SigV4
  keypair derivation this prints) · [ADR-0130](0130-funcdctl-dev-catalog-consumer-and-query-lane.md) (the pipeline
  the seed flow drives)

## Context & Need

`funcdctl dev` (ADR-0125) prints the dev S3 credentials only in its **startup banner** (stdout of the
long-running foreground daemon). To drive the S3 frontend from another shell — `aws s3 cp` a workflow's input
into `landing`, script the releve seed flow — a developer had to **copy four `export` lines out of the banner**
by hand every session. There was no machine-readable way to obtain them, so no `just` recipe could be
self-sufficient. The creds are a **fixed, well-known dev keypair** (ADR-0128 Decision 6: derived over a constant
dev master, ADR-0085), so they are fully deterministic — knowable without a running server — yet only surfaced
by booting one.

## Scenarios

- **scenario: print-env-emits-exports** — Given a dev target, When `funcdctl dev <target> --s3port <p>
  --print-env` runs, Then it prints exactly the four `export AWS_ACCESS_KEY_ID/…SECRET…/…REGION/…ENDPOINT_URL_S3`
  lines to stdout and exits 0, **booting no server**.
- **scenario: print-env-matches-banner** — Given the same target, When `--print-env` and a normal boot both run,
  Then the printed keypair equals the banner's (the fixed devS3Master derived over the resolved first function).
- **scenario: print-env-deterministic** — Given two `--print-env` invocations, Then the output is byte-identical.
- **scenario: print-env-eval** — Given a running daemon on `--s3port <p>`, When a script runs
  `eval "$(funcdctl dev <target> --s3port <p> --print-env)"`, Then the AWS env is set to working creds for that
  daemon's S3 frontend (no manual copy).

## Scope

**In**: a boolean `--print-env` flag on `funcdctl dev`; a no-boot path that resolves the target's plannedFuncs
(refactored `resolveDevPlan` / `resolveWorkflowPlan`, shared with the boot path), derives the S3 keypair
(`s3gateway.DeriveKeypair(devS3Master, "default", firstFn)`), and prints the four `export` lines to stdout with
the endpoint at `--s3port` (default 3006); and updating `just seed-releves` to auto-load the creds via
`eval "$(…--print-env)"` instead of requiring a manual export.

**Out**: printing any other env (gateway/control-plane URLs, tokens — the control token is the well-known
`DevToken`, already documented in the banner); a `--print-env` for a *running* daemon's actual random port (the
flag computes from `--s3port`, which must match the daemon — a random `--s3port 0` daemon isn't scriptable, by
design); any change to the keypair derivation or the banner.

## Constraints & Decision drivers

- **Deterministic, so compute-don't-serve** — the keypair is fixed (ADR-0128 Decision 6), so `--print-env`
  resolves + derives + prints in milliseconds without the platform boot; pairing with a fixed `--s3port` keeps
  the endpoint reproducible.
- **Reuse the boot resolution** — the first-function identity (the keypair's subject) must be exactly what a
  real boot uses, so the plan resolution is factored and shared, not re-derived.
- **stdout is only the exports** — nothing else prints on the `--print-env` path, so `eval "$(…)"` is safe. Zero
  new deps; dev-tag-only.

## Alternatives considered

| Option | Why considered | Why rejected / chosen |
|---|---|---|
| Copy the 4 lines from the banner (status quo) | No code | Manual every session; no recipe can self-serve. **Rejected.** |
| Daemon writes `.funcd-dev/dev-s3.env` at boot | Recipe sources a file | A new on-disk dev artifact + lifecycle; only exists after a boot; needs the persist dir. Heavier. **Rejected** for now. |
| `--print-env` no-boot subcommand/flag ✅ | Deterministic creds → compute + print, `eval`-able | Chosen — no new artifact, no running daemon needed to obtain creds, pairs with `--s3port`. |

## Decision

Add a `--print-env` boolean flag to `funcdctl dev`. When set, the command resolves the target to its
plannedFuncs **without booting** (via `resolveDevPlan`, which reuses `detectWorkflow` + the factored
`resolveWorkflowPlan` / `resolveDevFunctions`), derives the S3 keypair
`s3gateway.DeriveKeypair([]byte(devS3Master), devNamespace, firstFn)`, and prints exactly:

```
export AWS_ACCESS_KEY_ID=<derived>
export AWS_SECRET_ACCESS_KEY=<derived>
export AWS_REGION=<devS3Region>
export AWS_ENDPOINT_URL_S3=http://127.0.0.1:<--s3port, default 3006>
```

to stdout, then exits — no server. `just seed-releves` loads them with `eval "$("$bin" dev "$dir/workflow.yaml"
--s3port {{s3port}} --print-env)"` and takes the S3 port as a recipe arg (default 3006).

## Temporary workarounds

- None. (The `--s3port` must match the running daemon; that's a documented constraint, not a workaround — a
  random-port daemon is intentionally not scriptable.)

## Contracts

```go
// cmd/funcdctl (dev.go, //go:build dev)
type devConfig struct { /* … */ printEnv bool }
// flag: --print-env → cfg.printEnv; RunE: if cfg.printEnv { return a.printDevEnv(path, entry, cfg) }  // before boot

func resolveWorkflowPlan(op, path string, wf *v1.Workflow) ([]plannedFunc, error) // factored from startDevWorkflow
func resolveDevPlan(op, path, entryFlag string, cfg devConfig) ([]plannedFunc, error) // detect → workflow|functions, no boot
func (a *cli) printDevEnv(path, entryFlag string, cfg devConfig) error
//   pfs := resolveDevPlan(...); kp := s3gateway.DeriveKeypair([]byte(devS3Master), devNamespace, pfs[0].name)
//   port := cfg.s3port; if 0 { port = 3006 }; Fprintf(a.out, "export AWS_…\n…http://127.0.0.1:%d\n", …, port)
```

| consumes | exposes |
|---|---|
| the ADR-0125 dev target resolution; the ADR-0085/0128 fixed dev S3 keypair (`devS3Master`, `DeriveKeypair`) | `funcdctl dev --print-env`; the shared `resolveDevPlan`/`resolveWorkflowPlan`; `just seed-releves` auto-loading creds |

## Implementation plan

**Files**
- `cmd/funcdctl/dev.go` — `--print-env` flag + `devConfig.printEnv`; RunE early-return to `printDevEnv`;
  factor `resolveWorkflowPlan` out of `startDevWorkflow`; add `resolveDevPlan` + `printDevEnv`.
- `justfile` — `seed-releves` takes an `s3port` arg (default 3006) and `eval`s `--print-env` instead of
  requiring a manual export.

**Test plan** (named tests)
- `TestPrintDevEnvDerivesDeterministicKeypair` — prints exactly the four export lines, the keypair equals
  `DeriveKeypair(devS3Master, devNamespace, resolved-first-fn)`, no server booted, byte-identical across calls,
  and the endpoint defaults to 3006 (covers print-env-emits-exports / -matches-banner / -deterministic).

**Definition of done**: four Go sub-checks green both tags; the named test passes; `eval "$(funcdctl dev
workflow.yaml --s3port 3006 --print-env)"` sets working creds and `just seed-releves` runs with no manual export.

## Review checklist

- [ ] `--print-env` prints ONLY the four export lines to stdout and boots no server.
- [ ] The keypair matches a normal boot's banner (fixed devS3Master over the resolved first function).
- [ ] `resolveDevPlan`/`resolveWorkflowPlan` are the same resolution the boot path uses (no drift).
- [ ] Endpoint port is `--s3port` (default 3006); documented that it must match the running daemon.
- [ ] `just seed-releves` auto-loads via `eval "$(…--print-env)"`; no manual export needed.
- [ ] Named test present + passing; dev-tag-only; no `any`; `api/fault`; ctx-first where applicable.

## Consequences

**Positive**: the dev S3 creds are obtainable in one scriptable line — `just seed-releves` (and any user script)
runs with zero copy-paste from the banner; the boot and no-boot paths share one resolution, so the printed
identity can't drift from the real one. **Negative (accepted)**: `--print-env` needs a fixed `--s3port` matching
the daemon (a random-port daemon isn't scriptable — intentional). **Neutral**: the banner is unchanged; prod
and the thin release client are unaffected (dev-tag-only).

## Open questions

- **A `--print-env` for the running daemon's actual port** — if random-port daemons ever need scripting, the
  daemon could publish its resolved ports/creds (e.g. a `.funcd-dev/dev.env`); deferred (the fixed-port path
  covers the need today).

## References

- ADR-0125 (funcdctl dev + the banner), ADR-0128 (the fixed dev S3 keypair + relaxed writes), ADR-0085 (the
  keypair derivation), ADR-0130 (the seed flow's pipeline).
