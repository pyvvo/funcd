## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0328 implementation, model: claude-opus-5-5)

ADR-0328 names the TypeScript Secret client's delete `del`. It changes no funcd code. Its work is in three places:

- pyvvo/funcd-typescript, which carries the rename with ADR-0327's `put` (`205ef11`, released as v0.28.0);
- the TypeScript handler of an e2e scenario;
- the title of a secrets.md row.

The subagent that reviewed [ADR-0327's implementation](adr-0327-implementation-claude-opus-5-5.md) reviewed this one
with it, in the same two rounds.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### Routed (not this work's)

- **F240's issue** · home: elsewhere · destination: tracker #1430. Resolved: the tracker's table records F240 under #1432
  ("#1432, with F236"), so the PR's `Fixes #1432` covers it.

### ✅ Verified correct (keep it)

- **The TypeScript client.**
  - funcd-typescript v0.28.0 (from the module cache) matches `205ef11` apart from the version and `put`'s doc comment.
  - In `shim/src/secret.ts`, `SecretClient` has `put(binding, key, value): Promise<void>` and
    `del(binding, key): Promise<void>`, and no `delete`.
  - `shim.mjs` and `pool.mjs` carry both calls, and `api.json` lists `put` and `del`.
  - The shim test "scenario secret-owner-writes" sends `PUT` and then `DELETE /secret/oauth/TOKEN`, each with the member
    header. A refused `del` rejects with `context.secret.del failed: 403`.
- **The Python client.** funcd-python v0.23.0 keeps `put` and `delete`, and each raises a failure as its KV client does
  (`test_secret_owner_writes`, `test_a_refused_write_raises`).
- **The changed scenario.** The TypeScript handler of `TestScenarioSecretOwnerWrites` deletes with `ctx.secret.del`. It
  passes on the pinned v0.28.0 (e2e, 0.55 s).
- **The behavior row.** The secrets.md row is titled "`ctx.secret.put`, `del` or `delete`" and cites ADR-0328.
- **Tracking.**
  - After acceptance, the ADR's only edit is its status line.
  - ADR-0327 carries the `Superseded in part by` line.
  - The blueprint names Python's `delete` (`blueprint.md:121`).
  - F240 was at `reviewing`.

### Definition of Done

7 of 7 items hold:

- the Review-checklist item;
- the Implementation plan's two items: the rename ships in the same funcd-typescript change and release as ADR-0327's,
  and it moves with ADR-0327;
- the changed scenario;
- the row's title;
- 2 generic items: the Contract (`SecretClient.del`, in place of `delete`) and tracking.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0328 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 7/7. See
docs/reviews/model-scorecard.md.

### Recommendation

Stamped `Implemented` (2026-11-13) with ADR-0327, and F240 `implemented`. It ships in ADR-0327's PR.
