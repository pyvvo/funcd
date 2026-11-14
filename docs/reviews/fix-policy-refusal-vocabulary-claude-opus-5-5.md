## Verdict: pass — 0 blockers, 0 majors, 3 minors  (fix-policy-refusal-vocabulary, model: claude-opus-5-5)

This report reviews branch `fix/policy-refusal-vocabulary`: one commit, c24fb4d on 3f179b5,
`fix(auth): a Policy's refusal of an unknown action or entity type lists every one funcd knows`. It changes
`internal/auth/cedar/capability.go`, `internal/auth/cedar/schema.go` and `internal/auth/cedar/schema_test.go`. The fix has
no issue, at the decider's request, so the commit body states the defect, and the `TestIssue<N>` name and the `Fixes #N`
line do not apply.

The defect: `validateCedar` refused an unknown action with the hand-written list "(curated: kv::read, kv::write,
link::invoke, s3::read, s3::write)" and an unknown entity type with a hand-written list of six types. `KnownAction` and
`KnownEntityType` accept the vocabulary that is assembled from every registered capability: ten actions and eleven types
on the base. A Policy author who mistyped `lake::query` was told that `lake::query` does not exist.

### Minor
- **The entity-type assertion cannot tell `Identity` from `S3Identity`** · attribution: model · evidence: mutant M3
  replaces `strings.Join(defaultRegistry.EntityTypes(), ", ")` with a hand-written list of the ten other types, without
  `Identity`. The tests of `internal/auth/cedar` and `internal/controlplane/admission` stay green, because
  `require.ErrorContains(t, typeErr, "Identity")` matches inside `S3Identity`. A hand-written list that drifts is the
  defect this fix removes. Fix: split the text after `(curated: ` on `", "` and assert each type as an element of that
  slice.
- **`Registry.Actions()` returns `[]string`** · attribution: model · evidence: `internal/auth/cedar/capability.go:135`.
  `Capability.Actions` and `KnownAction` use the typed `auth.Action`. ADR-0002 §4 keeps typed enums in signatures and
  accepts "some conversion at glue points", and `v1.AllKinds() []Kind` is the house precedent. The deviation is
  cosmetic, because the method only feeds a message. Fix: return `[]auth.Action` and convert the elements at the join.
- **Comments on the same path keep the old hand-written vocabulary** · attribution: model · evidence:
  `api/types/v1alpha1/policy.go:24-25` (`PolicySpec.Cedar`), `internal/controlplane/admission/policy.go:16-17`
  (`NewPolicyValidityAdmission`) and `pkg/funcd/funcd.go:1059-1060` each describe the schema as "kv::read/kv::write;
  Function/KVStore/KVTable". `api/types/v1alpha1/role.go:17-18` lists five of the Role actions. In the touched
  `schema.go`, the comment on `defaultRegistry` still names "the three migrated capabilities (kv, invoke, s3)", but the
  registry holds seven. These are comments, so no Policy author reads them. The user-facing text (the OpenAPI `doc:` tag
  and the security guide) is generic and correct ("an action funcd knows, such as kv::read"), and no other code path lists
  the vocabulary: the PDP's own refusal in `cedar.go:70` names only the action. For these reasons this is a Minor, not the
  Major of an unfixed sibling code path. Fix: point each comment at the assembled vocabulary (ADR-0116) in this commit, or
  name them in the PR.

### Routed (not this work's)
None.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the defect's reason.** With main's `capability.go` and `schema.go` as
  `go test -overlay` replacements (neither file changed on main since the base), the test fails with `Error "…unknown
  action \"s3::delete\" (curated: kv::read, kv::write, link::invoke, s3::read, s3::write)" does not contain
  "egress::connect"`. With only the entity-type refusal reverted, it fails with `does not contain "NetDestination"`. The
  same overlay on a scratch merge with origin/main 1d1a2d28 fails in the same way.
- **The test passes with the fix**, not skipped, under `-race`: `--- PASS: TestUnknownActionAndTypeRefusalsListTheVocabulary`.
  The gate's stress step ran it 20 times with 0 failures.
- **Mutants.** M1 (`EntityTypes` leaves out the supporting entity types) fails on `KVStore`. M2 (`Actions` keeps only
  the first action of each capability) fails on `kv::write`. M3 survives (see the first Minor).
- **The user-visible refusal is fixed.** A probe through `NewPolicyValidityAdmission` on the merged tree, with the action
  `lake::querry`, returns `invalid admission.policy-validity: Policy "p" has invalid Cedar: … unknown action
  "lake::querry" (curated: egress::connect, kv::read, kv::write, lake::query, link::invoke, s3::read, s3::write,
  secret::read, secret::write, sql::read, sql::write)`. The entity-type refusal lists all eleven types.
- **The fix removes the cause, not the symptom.** Both lists are now built from the registry that the check reads
  (`defaultRegistry`), in the way ADR-0116 builds `KnownAction` and `KnownEntityType`. The proof is that v0.44.0's
  `secret::write` appears in the refusal on the merged tree with no edit. The lists are built only when a Policy is
  refused, so the `Authorize` hot path, which calls `KnownAction`, is unchanged.
- **Scope.** All three files serve the defect. No test was changed or removed, and
  `TestScenarioPolicyValidityCuratedSchema` and `TestIssue676_IsInScopeUnknownEntityTypeRejected` still pass.
- **Design check: `bug`.** ADR-0074 and ADR-0116 were read whole. ADR-0074 Decision 6 sets a curated schema that the
  consumer ADRs extend. ADR-0116's Decision assembles the vocabulary from the registered capabilities, replacing the
  hand-listed maps, and its Constraints say that "no capability-specific branch survives in shared code". ADR-0177
  Decision 3 (its namespace refusal) and ADR-0080's implementation plan (map entries only) were also checked. No ADR
  decides the listed text. An issue search, open and closed, for "curated", "unknown action", "unknown entity type",
  "ValidateCedar" and "cedar policy references unknown" finds only #676, which is closed and is about the `is … in`
  check; it wants the unknown type named, and the refusal still names it. No board card covers it, no test pinned the old
  text, and `docs/behavior/` has no row for it. A refusal that tells an author that an accepted action does not exist
  contradicts ADR-0116's assembled vocabulary. No ADR file was edited.
- **Siblings.** No other message lists the vocabulary. The `namespacedEntityType` switch is ADR-0177 Decision 3's
  classification of namespaced types, not a listing. The local checkouts of funcd-typescript, funcd-python and
  funcd-functions hold no hand-written list of it.
- **Reuse.** Before the fix, the registry had predicates only, and no helper listed the vocabulary. The new methods follow
  the loop over `reg.caps` that `KnownAction` and `Builtins` use, and `slices.Sort` and `slices.Compact` come from the
  standard library. Rewriting `KnownAction` on top of `Actions()` would allocate on every authorization, so the separate
  loop is right. The gate's bloat audit reports 0 hard flags, no clones and no lint delta.
- **Conventions**, apart from the second Minor: `fault.Invalidf`, one short why-comment per helper, `t.Parallel()`,
  `require`, and the package's capability constructors in the test.
- **Checks.** On the branch: `go test -race` of `internal/auth/cedar` and `internal/controlplane/admission` is ok,
  `go vet` exits 0, and golangci-lint reports `0 issues`. The gate on c24fb4d passed ci-full with e2e (`pkg/funcd` ok,
  407 s), the Linux build, vet and lint (`0 issues`), stress, the audit and the unchanged-tree check. Its Linux tests were
  skipped because their VM was stopped.
- **Commit shape.** One commit with a `fix(auth):` subject. The body states the cause, the fix and the regression test,
  and it ends with the attribution trailer.

### Against the current main
origin/main has moved to 1d1a2d28 (v0.44.0, which adds `secret::write`). A scratch merge (`git merge --no-commit`) is
clean, because the three files did not change on main. On the merged tree, the regression test and both packages pass
under `-race`, vet and lint are clean, and the refusal lists `secret::write`. The branch needs no rebase before its PR,
since the merge queue tests it on main. If it is rebased anyway, the body's "ten actions" becomes eleven.

### Definition of Done
9 / 12 items hold. This is the fix checklist with items 1 and 11 adapted to a fix that has no issue; item 13 does not
apply, because no design spec models Policy validity. Misses: item 4 (M3 survives), item 9 (the typed return) and
item 12 (the stale comments). All three are Minor and model-attributed.

### Model scorecard
No ledger row is recorded here: the batch records its rows in one later commit. The row for this review:
claude-opus-5-5 on fix-policy-refusal-vocabulary (fix) → pass, 0/0/3, 3 model-attributed, DoD 9/12.

### Recommendation
Ready for its PR. Before the PR, optionally: assert the entity types as list elements (this kills M3), return
`[]auth.Action` from `Actions()`, and update the stale comments or name them in the PR.
