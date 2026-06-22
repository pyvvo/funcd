# ADR-0071: the curated Python runtime image runs contract-validated functions — fastjsonschema + base/lib-closure repair

- **Status**: Implemented (2026-06-22)
- **Date**: 2026-06-22 (judged 2026-06-22 — resolves the ADR-0060⟷ADR-0054 contradiction; newest/load-bearing
  decision wins, reconciled here not by editing frozen ADR-0054; license BSD-3 + pinned + pure-Python.
  **Scope broadened at the decider's direction** during impl: verifying the rebuild surfaced two further
  pre-existing breakages in the curated image — a glibc base drift and a missing system-lib closure — that
  prevented *any* Python function from running; both are folded in here as one repair of "make the python314
  image run contract-validated functions". No Blockers/Majors.)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, python, contract, image, glibc, lib-closure, bugfix
- **Realizes**: [FEAT-0001/F40](../feat/0001-feat-v1.1.md) (contract-validated Python runs in containerd)
- **Relates to**: [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md) (the curated
  `python314` image — **amends** its dependency note), [ADR-0060](0060-contract-validator-generation.md)
  (Python validators via fastjsonschema — this makes that runnable in the curated image),
  [ADR-0058](0058-contract-codegen-from-code-types.md) (contracts), [ADR-0050](0050-python-worker-pooling-subinterpreters.md)
  (the subinterpreter pool the validator must also run in)

## Context & Need

ADR-0060 generates each Python function's I/O validator at build time with **fastjsonschema** and bakes it
into the artifact; the baked code imports `fastjsonschema` at runtime (`from fastjsonschema import
JsonSchemaValueException`, plus `compile_to_code`'s generated body). But the curated `python314` image
(ADR-0054) was built **stdlib-only** — it copies *only* `funcd_shim`, "no pip, no third-party deps". So a
**contract-validated Python function cannot import its own validator** and the replica dies on startup. This
went unnoticed because the only Python example (`examples/python/hello-world`) is unit-tested under `uv`
(where fastjsonschema is present) and never deployed to the containerd lane; the first contract-validated
Python function actually run there (`examples/python/kv-counter`) crashed with `Ready=False / NoReplicas`.

This is a direct contradiction between two Accepted decisions — **ADR-0060** (validators need fastjsonschema
at runtime) and **ADR-0054** (the image is stdlib-only). ADR-0060 is the newer, load-bearing decision (the
contract gate is a platform guarantee); the image note is what must give. The JS path has no equivalent gap
— esbuild inlines AJV into a self-contained `.mjs`.

**Two further pre-existing breakages surfaced when rebuilding the image to verify the fastjsonschema fix**
(neither caused by it — fastjsonschema is pure-Python), both rooted in the image's **unpinned base** and an
incomplete original lib closure (ADR-0054's deferred open question), and both meaning *no* Python function —
contract or not — currently starts in the containerd lane:
1. **glibc drift** — `python:3.14-slim` moved to Debian trixie (glibc 2.41); the `distroless/cc-debian12`
   final stage is glibc 2.36, so the copied `libpython3.14` fails to load (`GLIBC_2.x not found`).
2. **missing system-lib closure** — the image copies python's lib tree but **not** the system shared
   libraries its stdlib C-extensions link (zlib, openssl, ffi, sqlite, lzma, …); the first such import
   (`zlib`→`binascii`) dies on `libz.so.1`.

Per the decider, all three are folded into this one repair (rather than a separate ADR) — they are one
topic at one altitude: *the curated python314 image runs contract-validated functions*.

## Scenarios

- **scenario: interpreter-and-stdlib-load** — Given the curated `python314` image, When `python3.14` starts
  and imports stdlib C-extensions (`ssl`, `sqlite3`, `zlib`, `lzma`, …), Then they load — the interpreter's
  glibc matches the base and the system-lib closure is present (previously: `GLIBC_2.x not found` /
  `libz.so.1: cannot open`).
- **scenario: validator-imports-in-image** — Given the image, When `python3.14 -c "import fastjsonschema"`
  runs, Then the import succeeds (the baked contract validator's dep is present).
- **scenario: contract-python-reaches-ready** — Given a contract-validated Python function deployed in the
  containerd lane, When it reconciles, Then it reaches **Ready** and serves — previously it stuck at
  `NoReplicas`.
- **scenario: shim-stays-stdlib-only** — Given the change, When the image is inspected, Then there is no
  pip/venv at runtime; the only additions over the stdlib are the one validator dep (fastjsonschema) and the
  stdlib C-extensions' own shared-lib closure.

## Scope

**In** — make the curated `python314` image run contract-validated functions, via three changes to its
build:
1. **ship fastjsonschema** (pure-Python, BSD-3, pinned) on `PYTHONPATH` beside `funcd_shim` — the validator's
   runtime dep;
2. **pin the build base** to `python:3.14-slim-bookworm` (glibc 2.36) so the interpreter links the same
   glibc the distroless final stage ships;
3. **copy the system-lib closure** — the resolved `ldd` set of `libpython` + `lib-dynload/*.so` (zlib,
   openssl, ffi, sqlite, lzma, …), minus the core libs distroless already provides — into the final stage's
   `LD_LIBRARY_PATH`.
Rebuild the embedded image tar; record the ADR-0054 reconciliation here (newest authority).

**Out**: a general pip/venv surface in the image (still none — one vendored validator dep + the C-ext lib
closure, no package manager at runtime); `nodejs22` (already self-contained — AJV is inlined at build);
reworking how validators are generated (ADR-0060 stands); the subinterpreter-pool execution mechanics
(ADR-0050 unchanged — fastjsonschema is pure-Python and import-safe per subinterpreter); pinning bases by
**digest** (version-tag pinning now; digest-hermetic builds tracked in Open questions).

## Constraints & Decision drivers

- **Correctness** — a contract-validated Python function must run in the curated image; today it can't.
- **Minimal surface** — add exactly one pure-Python dependency (the validator's), not a general pip runtime.
- **License** — fastjsonschema is **BSD-3-Clause** (Apache-2.0/MIT-compatible). ✓
- **Pure-Python** — no C-extension, so it loads in distroless and in the ADR-0050 subinterpreter pool.
- **Pinned** — pin the version (`==`) for a reproducible image, matching the shim's declared floor.

## Alternatives considered

| Option | Why considered | Why rejected / chosen |
|---|---|---|
| **Vendor fastjsonschema into the image** (pure-Python, on PYTHONPATH beside funcd_shim) ✅ | One small pure-Python dep; makes ADR-0060 runnable; no C-ext | The validator already targets fastjsonschema (ADR-0060); shipping it is the honest fix. **Chosen** |
| **Emit stdlib-only validators** (strip the fastjsonschema import; hand-roll/transpile checks) | Keeps the image literally stdlib-only | Re-opens ADR-0060 (a different validator strategy); large, error-prone, loses the precompiled-fastjsonschema integrity invariant. Rejected |
| **Bundle the validator's deps into the artifact** (like esbuild does for JS) | Self-contained artifact, mirrors JS | No Python bundler in the toolchain; would reinvent packaging; the runtime is curated by us anyway — put the dep there. Rejected |
| **Drop Python contract validation** | Trivial | Violates ADR-0058/0060 (contracts are a platform guarantee); the user explicitly requires it. Rejected |

## Decision

Repair the curated `python314` image so contract-validated Python functions run, with three Dockerfile
changes; rebuild the embedded tar.

1. **Base pin** — build stage `FROM python:3.14-slim-bookworm` (glibc 2.36, matching `distroless/cc-debian12`);
   the default `:3.14-slim` drifted to trixie (glibc 2.41), whose `libpython` won't load on the final stage.
2. **fastjsonschema** — `pip install --no-compile --no-deps --target /opt/funcd fastjsonschema==2.21.2`
   (pure-Python, BSD-3) in the build stage; the distroless final stage already `COPY`s `/opt/funcd` and sets
   `PYTHONPATH=/opt/funcd`, so the validator dep ships with no pip/venv in the runtime image.
3. **Lib closure** — in the build stage, resolve the `ldd` closure of `libpython3.14` + every
   `lib-dynload/*.so`, drop the core libs distroless already provides (glibc/libstdc++/libgcc/…), and stage
   the rest; the final stage `COPY`s that set into `/usr/local/lib` (on `LD_LIBRARY_PATH`). This is the
   closure ADR-0054 deferred — enumerated dynamically so it stays correct across base updates.

The funcd shim itself stays stdlib-only; the only additions are the one pure-Python validator dep and the
C-extension lib closure the stdlib already needs. **ADR-0054's "stdlib-only" note is reconciled here** (this
is the newer authority) — the image is "stdlib + the contract-validator dep + the stdlib C-ext lib closure",
not "stdlib-only" — without editing the frozen ADR-0054.

## Temporary workarounds

None.

## Contracts

No Go/wire contract change. The image's build contract:

| consumes | exposes |
|---|---|
| `python:3.14-slim-bookworm` + pip + `ldd` (build stage); fastjsonschema==2.21.2 (BSD-3, pure-Python) | a `python314` runtime image where the interpreter, the stdlib C-extensions, and baked validators all load + run |
| `shim/python/src/funcd_shim` (unchanged) | `PYTHONPATH=/opt/funcd` carrying `funcd_shim` **and** `fastjsonschema`; `LD_LIBRARY_PATH=/usr/local/lib` carrying the C-ext shared-lib closure |

## Implementation plan

**Files**: `images/runtime/python314/Dockerfile` (bookworm base pin; the pinned `pip install … --target
/opt/funcd fastjsonschema==2.21.2`; the `ldd`-closure gather + final-stage `COPY`); rebuild
`internal/runtime/embedimg/python314.tar` via `just build-runtime-images`. The ADR-0054 reconciliation is
recorded **here** (this ADR is the newer authority), not by editing the frozen ADR-0054 body.

**go.mod / deps**: none (image-level Python dep, not a Go dep).

**Test plan**
- `interpreter-and-stdlib-load` + `validator-imports-in-image` — after rebuild, `docker run --rm
  --entrypoint /usr/local/bin/python3.14 funcd/runtime-python314 -c "import fastjsonschema, ssl, sqlite3,
  zlib, lzma; import funcd_shim"` exits 0 (deterministic, no containerd needed).
- `contract-python-reaches-ready` — `just lima-example-kv`: the contract-validated `examples/python/kv-counter`
  reaches **Ready** and serves `1→2` on real containerd alongside the JS function (the e2e proof; per the
  roadmap's test-sequencing the containerd/e2e check runs in the Lima lane, not CI).
- `shim-stays-stdlib-only` — the final distroless stage runs no pip/venv; the only additions over the stdlib
  are fastjsonschema and the stdlib C-ext shared-lib closure.

**Definition of done**: the rebuilt image starts python3.14 and imports the stdlib C-extensions +
fastjsonschema + funcd_shim; a contract-validated Python function reaches Ready and serves in the containerd
lane; the only additions are the one pure-Python validator dep (BSD-3) + the C-ext lib closure (no pip/venv
at runtime); this ADR records the ADR-0054 reconciliation; no Go dep; no identity/path leak.

## Review checklist

- [ ] Build stage pinned to `python:3.14-slim-bookworm` (glibc 2.36, matching the distroless final stage).
- [ ] The **pinned** fastjsonschema is installed into `/opt/funcd`; the final distroless stage carries it on
      `PYTHONPATH` with no pip/venv.
- [ ] The `ldd` closure of `libpython` + `lib-dynload/*.so` (minus distroless-provided core libs) is copied
      into the final stage's `LD_LIBRARY_PATH`; `python3.14 -c "import ssl, sqlite3, zlib, lzma, fastjsonschema"`
      succeeds on the rebuilt image.
- [ ] A contract-validated Python function reaches **Ready** + serves in the Lima/containerd lane (1→2).
- [ ] The only additions are fastjsonschema (pure-Python, BSD-3, recorded) + the stdlib C-ext lib closure;
      `nodejs22` untouched.
- [ ] The ADR-0054 reconciliation is recorded here, not by editing the frozen ADR.
- [ ] No Go dependency; no identity/path leak.

## Consequences

**Positive**: Python functions actually run in the containerd lane again (the image was broken for *all*
Python by base drift); contract validation works (it never did there); `examples/python/kv-counter` and any
future contract-validated Python function run; the dynamic `ldd`-closure copy makes the image robust to base
updates (the closure ADR-0054 deferred is now resolved); ADR-0060's integrity invariant holds end-to-end.
**Negative (accepted)**: the `python314` image grows by one pure-Python dep + the stdlib C-ext shared libs
(~a few MB), softening "stdlib-only" to "stdlib + validator dep + C-ext closure"; the base is pinned by
version-tag (not digest) — reproducible enough, hermetic-by-digest tracked as an Open question.
**Neutral**: JS is unaffected (self-contained); the metastore/KV engines are unaffected; ADR-0050's
subinterpreter pool is unaffected (all additions are pure-Python or the stdlib's own C-ext libs).

## Open questions

- **Vendoring vs pip-at-build** — pinned `pip install --target` is used now; a fully-vendored copy (committed
  source) is a possible hardening if hermetic image builds are later required (tracked, not blocking).

## References

- [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md) — the curated images (note amended here).
- [ADR-0060](0060-contract-validator-generation.md) — Python validators via fastjsonschema (the runtime dep).
- fastjsonschema — <https://github.com/horejsek/python-fastjsonschema> (BSD-3-Clause, pure-Python).
- [FEAT-0001](../feat/0001-feat-v1.1.md) — F40.
