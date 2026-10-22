## Verdict: changes requested — 1 blocker, 1 major, 0 minors  (issue #1170 fix, model: claude-opus-5-5)

Commit 130977e4 on `fix/stress-flakes-1` (main f7e40c89) removes the 500 ms wall-clock bound from
`TestConditionCheckDecodesEachDocumentOnce` (`internal/workflow/engine_test.go`) and keeps its 16 MiB allocation bound.

### 🔴 Blocker 1 — the schema half of the property lost its only guard  ·  attribution: model
- **The claim.** The test's comment says that each document **and schema** is decoded once per check. It also says
  that a decoding per reference multiplies the allocations by 800.
- **Why this is false for the schema.** The schema's 250 KiB padding is a `description`. `schemaNode`
  (`internal/expr/schema.go:45-53`) does not decode that field, so a schema decoded on each reference allocates little.
- **The evidence.** A mutant disables only the schema cache: `ok = false` after `cur, ok := s.decoded[root]` in
  `internal/expr/schema.go`, applied by overlay.
  - The new test passes, also under `-race` (5.15 s).
  - Main's test fails under `-race`: `input.m === 2 × 800 … allocated 3 MiB in 5.140082208s`. The 500 ms bound was
    the only check that caught this mutant. Without `-race`, main's test also passes (3 runs out of 3).
  - The fixer's evidence disabled both caches at once (1,200 MiB). In that run, the document cache hides the schema
    cache.
- **Effect.** A regression to decoding the schema on each reference would pass the test everywhere. Under `-race`,
  that regression takes 5.1 s for one reference, while the whole test takes 0.11 s on the real code. That is the cost
  that #643 bounded. Before this change, the `-race` runs (the weekly stress run and the gate's stress step) caught it.
- **The fix, which I validated by overlay.** Keep the allocation-only bound, and put the schema's padding in a field
  that `schemaNode` decodes. For example, use
  `"properties":{"pad":{"type":"string","default":"<250 KiB>"},…}` instead of the `description`. With that test:
  - the real code passes;
  - the schema-only mutant fails at 203 MiB;
  - the document-only mutant fails at 1,199 MiB;
  - the 600 ms delay injected into `evalWhen` passes.

### 🟡 Major 1 — an identical sibling keeps the same 500 ms bound  ·  attribution: model
- **The sibling.** `TestDefaultIsBoundOncePerPath` (`internal/expr/expr_test.go:631-646`) came from the same change
  (#643). It runs the same 800 references over a 250 KiB value and fails on
  `n > 16<<20 || took > 500*time.Millisecond`.
- **Its cause is the same.** A wall-clock budget under `-race` on a loaded host. Its allocation bound already catches a
  decoding per reference, because the default is a `json.RawMessage` that is copied on each decoding. So its time bound
  can go, as here.
- **Its margin.** Under `-race` on the review host, its evaluation took 22–32 ms (3 runs). It has more margin than the
  failing test (0.11 s for both references), but the budget is the same.
- **Status.** The commit neither changes nor mentions it.
- `TestStringSearchIsLinear` (`internal/expr/expr_test.go:460`, 250 ms) is a time bound by design: linearity is the
  property it checks. It is not counted here.

### ✅ Verified correct (keep it)
- **The injection proof, reproduced.** A 600 ms sleep was injected at the top of `evalWhen` by overlay:
  - main's test fails as CI did: `allocated 2 MiB in 617.469958ms`;
  - the new test passes.
- **The document half is still guarded.** With both caches disabled, the new test fails at 1,200 MiB. With only the
  document cache disabled, it fails at 1,199 MiB for both references.
- **Cause, not symptom.** The flake was the wall-clock bound, and the allocations held, which the issue measured
  (2 MiB). No timeout was raised.
- **Checks.**
  - `go test -race -count=1 ./internal/workflow/` passes (20.8 s).
  - `go vet` passes on darwin and Linux.
  - `golangci-lint` reports 0 issues on darwin and Linux.
- **Shape.** The subject is `test(workflow):` (test-only), with `Fixes #1170` and the trailer.

### Definition of Done
8 / 11. Item 1, a committed `TestIssue1170_` test, does not apply: this is a timing flake whose proof is an injection,
and I reproduced it.
- Item 4 is missed: the schema-only mutant survives.
- Item 6 is missed: the change weakens the test (Blocker 1).
- Item 12 is missed: the sibling is left (Major 1).

All three misses are attributed to the model.

### Model scorecard
Recorded: claude-opus-5-5 on issue #1170 (fix) → changes-requested, 1/1/0, 2 model-attributed, DoD 8/11.
See docs/reviews/model-scorecard.md.

### Recommendation
Back to `/fix`. Two changes are needed:
- move the schema's padding into a decoded field, so that the allocation bound covers the schema half, and show that
  the schema-only mutant fails;
- drop the time bound of `TestDefaultIsBoundOncePerPath` in the same way, or justify keeping it.
