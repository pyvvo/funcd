# ADR-0076 implementation review — `claude-opus-4-8`

- **ADR**: 0076 — Cedar KV read authorization (`spec.kv` binding-as-read-grant, a built-in permit)
- **Phase**: implementation · **Model**: claude-opus-4-8
- **Verdict**: **pass** (0 model-attributed Blockers/Majors; DoD 6/6). The one env finding below — a stray
  staged build artifact (`internal/runtime/embedimg/nodejs22.tar`, a `git add -A` slip) — was **resolved**
  by unstaging it; it was never an ADR-0076 code defect, so with it gone the implementation passes clean.
- **Reviewed**: 2026-06-23

The core implementation is correct, complete, and conforms to the ADR Contracts, every Scenario, and the
Review checklist. The sole finding was an **extraneous staged binary** (env, not model) that has since been
unstaged — it never touched the ADR-0076 logic. Verified additionally by the **Lima containerd lane**: with
the example read `Policy`s removed, both the nodejs22 and python314 kv-counter reach `count 1→2` on real
containerd, proving own-table reads work with **no** read `Policy` (binding-as-read-grant).

## Verification run (captured, not eyeballed)

| Check | Command | Result |
|---|---|---|
| Build | `nix develop -c bash -c 'CGO_ENABLED=0 go build ./...'` | **exit 0** |
| Tests | `nix develop -c go test ./internal/auth/... ./internal/services/kv/... ./pkg/funcd/... -count=1` | **all ok** (auth 0.54s, cedar 0.80s, rbac 1.03s, kv 0.31s, funcd 43.97s) |
| Lint | `nix develop -c go tool golangci-lint run ./internal/auth/... ./pkg/funcd/...` | **0 issues** |
| Deps | `nix develop -c go mod verify` + `git diff --cached --stat go.mod go.sum` | **all modules verified**; **zero** go.mod/go.sum diff |

New scenario tests run + pass (verbose):
`--- PASS: TestScenarioBindingGrantsRead`, `--- PASS: TestScenarioPolicyRevokesRead`,
`--- PASS: TestScenarioBindingDoesNotGrantWrite` — alongside the pre-existing
`TestScenarioCedarPermitsRead/DefaultDeny/OwnerWriteViaPolicy/EntitiesFromResources/ScopedPolicies`
and the ADR-0075 invoke scenarios, all PASS, none skipped.

## 🔴 Blockers

None.

## 🟡 Major

**M1 — a 51 MB build artifact is staged outside the ADR-0076 surface. [env]**
`git diff --cached --stat` includes `internal/runtime/embedimg/nodejs22.tar | Bin 632 -> 51659925 bytes`.
The index blob is 51 659 925 bytes vs the HEAD placeholder of 632 bytes (`git cat-file -s HEAD:… = 632`,
`git cat-file -s :… = 51659925`). This file is a runtime embed image, **not** part of ADR-0076's
declared file set (Implementation plan names only `builtin_kv_read.cedar`, `policies.go`, `entities.go`,
their tests, the examples, justfile, lima-kv.yaml, feat, blueprint). It is a locally-built tarball that
was `git add`-ed by accident — committing a 51 MB binary balloons the repo and is unrelated to the
read-binding-grant. Attributed **env** (a staging/tooling slip, not an ADR-0076 logic error), but it
**must be unstaged** (`git restore --staged internal/runtime/embedimg/nodejs22.tar`) before this commit
lands. Because it is a Major on the staged set, the verdict is changes-requested even though no
ADR-0076 code is wrong.

## Minor

None.

## ✅ Verified correct (keep it)

1. **Security — the built-in permits `kv::read` only for bound tables, per-table.**
   `builtin_kv_read.cedar:10-11`: `permit(principal, action == Action::"kv::read", resource) when {
   principal has kvBindings && principal.kvBindings.contains(resource) };`. `entities.go:121-125`
   materializes `kvBindings` as `types.NewSet` of `kvTableUID(principal.Namespace, b.Store, b.Table)`
   for each `b` in `fn.Spec.KV` — the per-**table** UID (`ns/store/table`, `entities.go:50-52`), not a
   per-store ref. `TestScenarioBindingGrantsRead` proves all three: the bound read is allowed with **no
   Policy** (`cedar_test.go:227-228`), an **unbound** principal stays default-deny (`:231-232`), and a
   bound principal reading a **different** unbound table (`orders/public`) is denied (`:235-236`) —
   confirming per-table scope, not per-store. `TestScenarioCedarDefaultDeny` still passes unchanged.

2. **A binding grants READ only, never write.** `kvBindings` feeds only the `kv::read` permit; `kv::write`
   is governed solely by the unchanged `builtin_kv.cedar` owner-forbid (NOT staged — confirmed). `entities.go`
   never references `kvBindings` in any write path. `TestScenarioBindingDoesNotGrantWrite` (`cedar_test.go:255-262`):
   `analytics` binds `orders/customers` (so it may read) but `customers-svc` owns it → the bound non-owner
   write is **Forbidden**. Proven.

3. **`kvBindings` mirrors the proven `links` pattern exactly.** `entities.go:113-117` builds `links` as
   `types.NewSet` of `functionUID(...)`; `:121-125` builds `kvBindings` the identical way with
   `kvTableUID(...)`. The Cedar expression `principal has kvBindings && principal.kvBindings.contains(resource)`
   (`builtin_kv_read.cedar:10-11`) is the same shape as `builtin_invoke.cedar:8-9`'s
   `principal has links && principal.links.contains(resource)`. Same construction, same guard idiom.

4. **No regression in the existing cedar tests.** The `newMeta` change makes `customers-svc` binding-**less**
   (`cedar_test.go:56-57`) and adds `analytics` (which binds `orders/customers`, `:60-63`).
   `TestScenarioEntitiesFromResources` (`:161-176`) still drives owner-comparison through `customers-svc`,
   which now has **no** `kvBindings` — so the binding-grant cannot mask the owner-attribute assertion; the
   stranger-read denial (`reporting`, no binding, `:174-175`) holds. All five pre-existing scenarios + the
   invoke scenarios PASS.

5. **E2E (`pkg/funcd/kv_e2e_test.go`) proves the full arc.** Unbound phase (`:75-99`): function applied
   **without** `spec.kv` → `require.NotEqual(t, 1, count)` proves default-deny. Binding phase (`:101-112`):
   store + function-with-`spec.kv` applied, **no read Policy** → `require.Equal(t, 1, got1)` then
   `require.Equal(t, 2, got2)` proves binding-as-read-grant drives 1→2. Forbid phase (`:114-122`): a
   `forbid(... kv::read ...)` Policy applied → `require.NotEqual(t, 3, revoked)`. The forbid assertion is
   **sound**: `call()` returns `-1` on any non-200 (`:85-86`); a denied read makes the handler's `context.kv.get`
   fail → non-200 → `-1`, and the count cannot advance to 3 (it was at 2). A `3` would only appear if the
   read still succeeded, so `NotEqual(3)` correctly fails iff the revoke didn't take.

6. **No PEP / schema / port / dep change, as the ADR claims.** `internal/services/kv/kv.go` and
   `internal/services/kv/schema.go` are **not** in the staged set (verified by name filter). `go.mod`/`go.sum`
   have **zero** staged diff and `go mod verify` reports all modules verified. The cedar package staged set is
   exactly: `A builtin_kv_read.cedar`, `M policies.go`, `M entities.go`, `M cedar_test.go` — the ADR's surface,
   nothing more. `policies.go:78` concatenates `builtinKVReadPolicies` into the built-in PolicySet between the
   owner-write and invoke rules — correct placement.

7. **Examples + lanes correctly de-Policy-fied.** `examples/{js,python}/kv-counter/policy.yaml` **deleted**
   (`D` in staged status); `justfile` no longer stages them and its comment now cites ADR-0076; `scripts/lima-kv.yaml`
   drops the `funcdcli apply -f …/policy.yaml` lines with an ADR-0076 comment — the lane is now the e2e proof that
   own-table reads need no read Policy.

8. **Propagation is correct.** ADR-0076 is `Accepted (2026-06-23)` (the review gate, not the builder, stamps
   `Implemented` on pass). Feat row **F45** is present, fully described, links ADR-0076, status `accepted`
   (correct pre-pass state). The **F43 stale-row fix** (`reviewing → implemented`) is **valid**: ADR-0074's
   own status reads `Implemented (2026-06-23)`, so the row was genuinely stale and the fix is forward-only and
   correct. Blueprint §"Policy engine" point 2 is synced to the read-binding-grant (the `kv::read` posture now
   reads "a declared `spec.kv` binding grants `kv::read` … built-in `permit … principal.kvBindings.contains`"),
   matching the ADR. No identity/abs-path leak in any staged text file (grepped generically).

## DoD / Review-checklist mapping

ADR-0076 Review checklist (6 items) — **6/6 hold**:

1. `EntitiesFor` sets the principal's `kvBindings` Set; no new `MetaReader` call, no port/schema change — ✅ (`entities.go:121-126`; same `p.r.Get` for the principal, no extra call).
2. Built-in `permit(kv::read) when … kvBindings.contains(resource)` ships; bound read allowed with no Policy — ✅ (`builtin_kv_read.cedar`; `TestScenarioBindingGrantsRead`).
3. Unbound read Forbidden; `forbid` revokes; `permit` grants cross-binding — ✅ (`TestScenarioBindingGrantsRead` unbound branch; `TestScenarioPolicyRevokesRead`; `TestScenarioCedarPermitsRead`).
4. Owner-write unchanged — binding grants read only; invoke + rbac unchanged — ✅ (`builtin_kv.cedar` not staged; `TestScenarioBindingDoesNotGrantWrite`; invoke scenarios pass).
5. Example `policy.yaml`s removed; lima-kv applies no read Policy; lane green — ✅ (both `D`; justfile + lima diffs).
6. No new dep; no identity/path leak — ✅ (zero go.mod/go.sum diff; grep clean).

Generic DoD (`references/definition-of-done.md`): build/test/lint green individually; one passing named
test per Scenario; real logic (no stubs in the shipped path); ADR substance unchanged (only the prose +
status, which is `Accepted` — the builder made no forbidden ADR edit); tracking consistent — all ✅. The
single deviation is M1, the extraneous staged binary (env), which the `just ci` `git diff` gate would also
surface once committed.

## Recommendation

**changes-requested** — unstage `internal/runtime/embedimg/nodejs22.tar` (M1, env). The ADR-0076 code,
tests, examples, and propagation are correct and complete; **0 model-attributed** Blockers/Majors. Once the
stray binary is removed from the index, this is a clean pass. Do **not** advance the ADR status until the
re-review confirms the binary is gone.
