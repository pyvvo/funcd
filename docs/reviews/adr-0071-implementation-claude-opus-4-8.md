# ADR-0071 implementation review — curated python314 image runs contract-validated functions

- **ADR**: [0071](../adr/0071-python-runtime-ships-fastjsonschema.md) · **phase**: implementation · **model**: claude-opus-4-8
- **Verdict**: **pass** (DoD met, no Blockers/Majors) · 2026-06-22

## Verification (evidence)

| check | result |
|---|---|
| `just build-runtime-images` (rebuild `python314.tar`, bookworm base) | OK |
| `docker run --entrypoint python3.14 … -c "import fastjsonschema, ssl, sqlite3, zlib, lzma; import funcd_shim; from funcd_shim.kv import KVClient"` | **OK** (deterministic, no containerd) |
| `just lima-example-kv` — **both** functions on real containerd | **PASS** — `counter` (nodejs22) **and** `pycounter` (python314, contract-validated) reach Ready; each serves `context.kv` 1→2 |
| new Go deps | **none** (image-level Python dep only) |

## Scenarios → tests (named, passing)

- `interpreter-and-stdlib-load` + `validator-imports-in-image` — the `docker run` import probe (glibc loads;
  the C-ext lib closure + fastjsonschema present).
- `contract-python-reaches-ready` — `just lima-example-kv`: `pycounter` reconciles to **Ready** and serves
  1→2 (previously `NoReplicas`); the Lima probe gates `limactl start` on **both** counters Ready.
- `shim-stays-stdlib-only` — the final distroless stage runs no pip/venv; additions are fastjsonschema +
  the stdlib C-ext shared-lib closure only.

## ✅ Verified correct (keep)

- **All three repairs are present + necessary** — (1) bookworm base pin (glibc 2.36 == distroless), (2)
  pinned fastjsonschema==2.21.2 on `PYTHONPATH`, (3) the dynamic `ldd`-closure copy onto `LD_LIBRARY_PATH`.
  Each was independently proven required (GLIBC error → libz error → shim-runs).
- **Dynamic closure** — the lib set is resolved from `ldd` at build (minus distroless-provided core libs),
  so it stays correct across base updates rather than a brittle hand-list. Excluding glibc/libstdc++/libgcc
  avoids clobbering the distroless base's own libs.
- **Scope broadening was decider-directed** and recorded in the ADR header + Context; ADR-0054 reconciled
  here (newer authority), its frozen body untouched.
- **JS untouched** — `nodejs22` is self-contained (AJV inlined); only `python314` changed.
- **Hygiene** — no Go dep; the only additions are one pure-Python BSD-3 dep + the stdlib's own C-ext libs;
  no identity/path leak.

## Findings
None (Blocker/Major/Minor). The version-tag (not digest) base pin is noted as an Open question
(hermetic-by-digest), not a defect.

## DoD
ADR Review-checklist: 7/7 satisfied (bookworm pin; pinned fastjsonschema on PYTHONPATH no pip/venv; ldd
closure on LD_LIBRARY_PATH + import probe; contract-Python Ready+1→2 in Lima; additions limited to the
validator dep + C-ext closure with nodejs22 untouched; ADR-0054 reconciled here not in the frozen ADR; no Go
dep / no leak).

## Recommendation
**pass** — `Reviewing → Implemented`. The python314 runtime is repaired; contract-validated Python runs in
containerd.
