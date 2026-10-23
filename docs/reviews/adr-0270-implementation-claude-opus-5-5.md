## Verdict: pass — 0 blockers, 0 majors, 0 minors open  (ADR-0270 implementation, model: claude-opus-5-5)

This report reviews the private advisory branch `fix/ghsa-vcf2-key-rotation` (GHSA-vcf2-hc48-w9jp, rotation of the
derived S3 key pairs), merged with the advisory's other fixes in 08eeea98. The review ran privately, in six rounds,
before publication; this record follows it. The ADR was judged twice: round 1's Major (pools re-derived pairs per pass,
so every pool would rebuild at each window) became Decision 5, "a pair is issued with its worker".

### Rounds
1. **Changes requested:** the forced renewal cut calls in flight; one replica per pass; the Lake's `MaxAge` untested.
2. **Changes requested:** a draining worker was stopped as a boot crash (`listening` served both the boot judgement and
   the hand-out); a stale renewal mark retired a fresh worker; the pool drain untested.
3. **Changes requested:** after #1196 a Lake gaining its first blob binding kept a pairless engine; the issued keys,
   not their values, now count in the engine's fingerprint.
4. A real-platform experiment (8 s period, calls every 50 ms) then found four gaps the rounds could not see on a fake
   runtime: a rolling gap, a misleading Degraded status, held revisions left out, no logs. All fixed.
5. **Changes requested:** an unbounded rolling hold let one broken replica expire the healthy ones' pairs. Now an early
   renewal waits for all replicas to listen, and a renewal at its age waits only for a booting renewal replacement,
   until halfway to the end of its pair's validity.
6. **Pass**, with a minor folded: startup refuses a period of at most 20 × (supervisionPeriod + drainGrace +
   2 × drainPollInterval + 10 s), so a forced renewal lands inside the pair's validity.

### Verified correct (keep)
- **The deriver:** a four-part key with a canonical non-negative window, accepted only for the current and previous
  window; three-part keys only in the fixed mode; the salt is random per start and kept in memory.
- **Mutants that each fail a test:** accept-any-window; pairs in the pool signature; no rolling hold; no cap on the wait;
  a broken replica counted as booting; a renewal reason ahead of a crash or start failure; no held renewal or its poll;
  the drain-start log removed; the config bound weakened.
- **Two guards kept as defense in depth, claiming no test:** forgetting an ended renewal boot, and clearing marks in
  `forgetWorker`.

### Checks
- `go test -race` on every touched package; vet and golangci-lint, with and without the e2e tag: clean.
- `TestScenarioKeyRenewalUnderLoad` (e2e, a smoke test): 0 failed calls, the rolling case never Degraded.
- `just lima-example-all` (13 lanes, rotation on by default in every S3 lane) and `scripts/agent/gate.sh`: PASS on
  776dd134, 33f829b3 and 517bc69c, and on the combined advisory tree before the merge.
