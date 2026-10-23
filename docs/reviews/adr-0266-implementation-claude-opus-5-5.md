## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0266 implementation, model: claude-opus-5-5)

This report reviews branch `feat/adr-0262-function-volumes` at 40306cac, PR #1192, with ADR-0262 and ADR-0268. ADR-0266
realizes FEAT-0014/F181. The feature issue is #1187, which the PR closes.

### ✅ Verified correct (keep it)
- **The grammar follows Decision 1.** `ByteSize.Bytes` (`api/types/v1alpha1/bytesize.go`) checks the pattern
  `^[0-9]{1,15}((Ki|Mi|Gi|Ti)B?)?$` first and then calls `humanize.ParseBytes`. It refuses a count past `MaxInt64`.
  The schema tags on `VolumeEntry.Size` and `NamespaceSpec.VolumeBudget` carry the same pattern. go-humanize v1.0.1 is
  now a direct requirement.
- **`TestByteSizeGrammar` covers the Review checklist.** Cases: `1Ki` = `1KiB` = 1024;
  `999999999999999` is exact; `8388607TiB` is exact; `8388608Ti` and `999999999999999Ti` are refused past 2⁶³−1;
  `2G`, `2GB`, `1.5Gi`, `1 Gi`, `-1`, `2gib` and 16 digits are refused.
- **The one-form rules (Decisions 2 and 3).** `validateVolumes` refuses both size forms or neither, and the error names
  `spec.volumes[i]`. It refuses size `0`. `Namespace.Validate` refuses both budget forms. `functionVolumes`
  (`cmd/funcd`) refuses both `volumes.capacity` and `volumes.capacityBytes`, and a capacity outside the grammar
  (`TestVolumeCapacityForms`).
- **Every reader uses the byte count.** No code reads `VolumeEntry.SizeBytes` outside `VolumeEntry.Bytes()` and the
  validator itself. The admission, the reconciler and the backup use `Bytes()`. The budget comes from
  `NamespaceSpec.VolumeBudgetOf()`. The manager's own `meta.SizeBytes` field is a different field.
- **One test per scenario.** TestScenarioSizeWithUnits, OneSizeForm, BadSize and BudgetAndCapacityWithUnits each carry
  their `// scenario:` tag, and all pass in the targeted run (exit 0). Lint is clean (`0 issues.`).
- **The supersession links.** ADR-0262 carries the one `Superseded in part by` line. ADR-0266's substance is unchanged
  apart from its status. The F181 row links both ADRs.

### Definition of Done
8/8 (5 Review-checklist + 3 DoD items) hold.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0266 (implementation) → pass, 0/0/0, DoD 8/8. See docs/reviews/model-scorecard.md.

### Recommendation
Stamped `Implemented`. F181 stays `reviewing` until ADR-0262 passes.
