## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0322 implementation, model: claude-opus-5-5)

ADR-0322 changes no code. It replaces ADR-0321's *Behavior rows* section, so its work is the rows of three behavior
chapters, which landed with ADR-0321's implementation (f474bb94) and were refined in b10bc4c2 and 5de2fb3c. This review,
by the same independent subagent that reviewed [ADR-0321's implementation](adr-0321-implementation-claude-opus-5-5.md),
checks those rows against the Decision. Each row says what the Decision says and cites the ADR-0321 part it tells. The
ADR is stamped `Implemented` and F237 `implemented`.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### Routed (not this work's)

- **The security chapter's room** · home: elsewhere · destination: the next ADR that adds a row to
  docs/behavior/security.md. ADR-0322's Consequences record that the chapter's written part is now about 20 bytes under
  its 16 KiB cap, so that ADR splits the chapter (option C). Nothing is due here.

### ✅ Verified correct (keep it)

- **security.md**: the `ctx.secret.get` or `list` row adds the per-Secret decision and keeps the round trip per read,
  citing ADR-0321 Decision 3 and `TestScenarioSecretBoundReads`. The new row "A Policy forbids a Function's read of a
  bound Secret" tells the failed read of any key, the failed readiness with its link to workers.md, and `Degraded` with
  the calls held, at the next read and within `runtime.supervisionPeriod`. It cites ADR-0321 Decisions 3 and 4 and
  `TestScenarioSecretForbidReadiness`.
- **services.md**: the row "A Policy forbids a Lake's read of a bound Secret" tells the hold (`Pending`,
  `BindingResolveFailed`), the engine stopped or never started with the key within `runtime.supervisionPeriod`, and the
  lift within `controller.referentPollInterval`. It cites ADR-0321 Decision 5. The `accepted` row tells the window
  between a pass's check and its Converge and cites ADR-0321's Risks accepted. The code agrees: a held Lake is `Pending`
  (`setHeld`, `internal/services/lake/reconcile.go`), and a mutant in ADR-0321's review shows that a test pins the lift.
- **workers.md**: the `A binding check fails` row names the `secret` kind and cites ADR-0321 Decision 4, with its
  re-check each `runtime.supervisionPeriod`.
- **Checks**: `just check-behavior` and `just check-hygiene` exit 0 at 5de2fb3c, through `scripts/agent/d`.
- **Tracking**: ADR-0322 was accepted and set to `Reviewing` in 5de2fb3c, with no later edit. ADR-0321 carries its
  `Superseded in part by` line, and F237 was at `reviewing`. ADR-0322 is a registry ADR, so it has no feature issue.

### Definition of Done

13 of 13 hold: the two Review-checklist items, the Implementation plan's done line (the rows land, and
`just behavior-values` and `just check-behavior` pass), and the ten generic items. The generic items hold trivially for
an ADR with no code; the code's gate is ADR-0321's.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0322 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 13/13. See
docs/reviews/model-scorecard.md.

### Recommendation

Stamped `Implemented` (2026-11-11), with F237 `implemented`. It ships in the same PR as ADR-0321.
