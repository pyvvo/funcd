# ADR-0124: Multi-function funcdctl.yaml — per-function-named manifests

- **Status**: Implemented (2026-07-11 — stem-first `resolveManifest` + 3 tests; fn-to-fn + workflow migrated to 7
  per-function `<name>.funcdctl.yaml`; `Manifest` unchanged. `s3-lakehouse` deferred — a skeleton example with no
  handler/contract source to migrate. Review: docs/reviews/adr-0124-implementation-claude-opus-4-8.md.)
- **Date**: 2026-07-11 (**Accepted 2026-07-11** — self-accepted via `/adr-batch`; a small refinement of ADR-0122's
  resolver, reusing its `Manifest` unchanged.)
- **Deciders**: green-0-rabbit
- **Tags**: dx, tooling, funcdctl
- **Realizes**: FEAT-0001/F89
- **Relates to**: [ADR-0122](0122-funcdctl-yaml-manifest-native-contract-codegen.md) (the `funcdctl.yaml` push/dev config
  this extends — `Manifest` shape unchanged) · [ADR-0064](0064-fn-to-fn-rpc-links.md)/[ADR-0094](0094-workflow-engine-core.md)
  (the multi-function examples that need this: fn-to-fn, workflow)

## Context & Need

ADR-0122's `funcdctl.yaml` is one config per function, resolved by a fixed filename in the push target's directory. But
several funcd examples put **multiple single-file functions in one directory** — `fn-to-fn/` has `front.mjs` +
`greeter.mjs` (each its own `front.yaml`/`greeter.yaml` CRD + `front.schema.json`/`greeter.schema.json`); `workflow/`
has five (`hi`/`ingest`/`lo`/`report`/`score`). One fixed `funcdctl.yaml` per directory cannot carry more than one
function's contract, so `funcdctl push front.mjs …` and `funcdctl push greeter.mjs …` would collide on the same file.

**Purpose.** Let one directory hold **one `funcdctl.yaml` per function**, selected by the push target — so a
multi-function example migrates to the ADR-0122 config without restructuring. Who calls it: an author running
`funcdctl push <handler-file> <ref>` / `funcdctl types` in a multi-function directory.

## Scenarios

- **scenario: per-function-manifest-resolved** — Given `fn-to-fn/` with `front.funcdctl.yaml` and
  `greeter.funcdctl.yaml`, When `funcdctl push front.mjs registry:front`, Then funcdctl resolves **`front.funcdctl.yaml`**
  (matched by the `front` stem) and bakes *its* contract — not `greeter`'s.
- **scenario: generic-fallback** — Given a single-function directory with only `funcdctl.yaml` (no `<stem>.funcdctl.yaml`),
  When `funcdctl push handler.py <ref>`, Then funcdctl falls back to `funcdctl.yaml` — the ADR-0122 single-function
  behavior is **unchanged**.
- **scenario: types-named-manifest** — Given `front.funcdctl.yaml`, When `funcdctl types -f front.funcdctl.yaml`, Then
  the `.d.ts`/`.pyi` are generated from it (the `-f` path is honored verbatim).

## Scope

**In**: the CLI **resolver** learns to try `<stem>.funcdctl.yaml` (the push target's basename minus extension) before
the generic `funcdctl.yaml`; the per-function-named manifest convention; migrating the multi-function examples
(fn-to-fn, workflow, s3-lakehouse) to it.

**Out**: any change to the `Manifest` **shape** (ADR-0122's `{runtime, handler, bindings, contract}` is reused
verbatim); a `functions:` list/map (see Alternatives); bundle multi-function (a bundle **is** one function — its dir
keeps the single `funcdctl.yaml`).

## Constraints & Decision drivers

- **Reuse ADR-0122's `Manifest`** — no schema change, no new fields; this is a *resolution* refinement only.
- **Consistent with funcd's existing per-function file convention** — examples already name per function
  (`front.yaml`, `front.schema.json`, `front.mjs`); `front.funcdctl.yaml` is the natural sibling.
- **Backward-compatible** — a directory with only `funcdctl.yaml` behaves exactly as under ADR-0122.

## Alternatives considered

- **A `functions:` list/map in one `funcdctl.yaml` (keyed by function).** *Pros*: one file lists every function in the
  dir. *Cons*: fights funcd's per-function-file convention (`front.yaml`/`front.schema.json` are already per-function);
  needs a `--function` selector or a per-entry `entry:` field to disambiguate which function a `push <file>` targets;
  changes the `Manifest` shape. **Rejected** — more surface, less consistent, reopens the just-settled shape.
- **Per-function subdirectories** (`fn-to-fn/front/funcdctl.yaml`). *Pros*: fixed filename per dir. *Cons*: restructures
  the examples' layout and every lane's push/apply paths. **Rejected** — heavier, no gain over stem-named files.
- **Per-function-named `<stem>.funcdctl.yaml` (chosen).** Reuses the `Manifest`, matches the existing per-function naming,
  a ~one-function resolver change, fully backward-compatible.

## Decision

1. **Per-function-named manifests.** In a directory, each function may have `<stem>.funcdctl.yaml` (e.g.
   `front.funcdctl.yaml` for `front.mjs`), holding the ADR-0122 `Manifest` **unchanged**.
2. **Stem-first resolution.** For a **single-file** push `funcdctl push <file> <ref>`, `resolveManifest` looks for
   `<dir>/<stem>.funcdctl.yaml` **first** (stem = the file's basename without extension), then falls back to
   `<dir>/funcdctl.yaml`. For a **bundle** (directory) push it resolves `<dir>/funcdctl.yaml` as before (a bundle is one
   function). `funcdctl types -f <path>` honors an explicit path verbatim.
3. **No shape change.** The `Manifest` and everything downstream (contract bake, `types`) are ADR-0122's, untouched.

## Temporary workarounds

None.

## Contracts

**`resolveManifest(path string) (*sdk.Manifest, string, error)`** (in `cmd/funcdctl`) — signature unchanged; behavior:
for a non-directory `path`, try `filepath.Join(dir, stem+"."+manifestFileName)` (stem = `strings.TrimSuffix(base,
filepath.Ext(base))`) before `filepath.Join(dir, manifestFileName)`; the first that exists is loaded (`sdk.LoadManifest`).
Absent ⇒ `(nil, "", nil)` (the legacy push path runs — unchanged). No new flags; `manifestFileName` (`funcdctl.yaml`)
stays the fallback/default.

No `pkg/sdk` change; no new deps.

## Implementation plan

**Files**: `cmd/funcdctl/manifest.go` (extend `resolveManifest` with the stem lookup) + its test. Migrate the
multi-function examples: `examples/js/fn-to-fn/{front,greeter}.funcdctl.yaml`,
`examples/js/workflow/{hi,ingest,lo,report,score}.funcdctl.yaml`, and the `examples/python/s3-lakehouse/` functions
(`ingest`/`transform`/`report`) — each a block-style per-function manifest whose contract is that function's
`<name>.schema.json` (or its established contract source), sitting beside the existing per-function CRD/handler.

**Test plan** — `per-function-manifest-resolved` (a stem file wins over the generic + over a sibling function's file),
`generic-fallback` (only `funcdctl.yaml` → ADR-0122 behavior), `types-named-manifest` (`-f <stem>.funcdctl.yaml`), all
passing; verify each migrated manifest with `funcdctl types -f <it>`.

**Definition of done**: the four sub-checks green; `funcdctl push front.mjs …` in `fn-to-fn/` resolves
`front.funcdctl.yaml`; every migrated multi-function manifest parses + generates types; FEAT-0001/F89 linked.

## Review checklist

- [ ] `resolveManifest` tries `<stem>.funcdctl.yaml` before `funcdctl.yaml` for single-file pushes; bundle + fallback
      paths unchanged; no `Manifest` shape change.
- [ ] The 3 multi-function examples carry per-function `<name>.funcdctl.yaml` (block-style), each verified by
      `funcdctl types`.
- [ ] Backward-compatible: single-function examples (only `funcdctl.yaml`) still resolve.
- [ ] FEAT-0001/F89 linked; no identity/path leak.

## Consequences

- (+) Multi-function directories migrate to `funcdctl.yaml` with **no shape change** and a ~one-function resolver delta.
- (+) Consistent with funcd's per-function file naming (`front.yaml`/`front.schema.json`/`front.funcdctl.yaml`).
- (−) Two manifest-naming forms coexist (`funcdctl.yaml` and `<stem>.funcdctl.yaml`) — mitigated: stem-first-then-generic
  is a single, obvious rule, and single-function dirs never need the stem form.

## Open questions

- Whether `funcdctl dev` (future) selects a function the same stem way or takes an explicit name. *(Answered at: the
  `funcdctl dev` ADR.)*

## References

- [ADR-0122](0122-funcdctl-yaml-manifest-native-contract-codegen.md) (the config + `Manifest` this reuses),
  [ADR-0064](0064-fn-to-fn-rpc-links.md), [ADR-0094](0094-workflow-engine-core.md) (the multi-function examples).
