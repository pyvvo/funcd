## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0267 implementation, model: claude-opus-5-5)

This report reviews the private advisory branch `fix/ghsa-vcf2-default-seccomp` (GHSA-vcf2-hc48-w9jp, item 6), merged
with the advisory's other fixes in 08eeea98. The review ran privately before publication; this record follows it.

### Finding
- **Minor (process):** ADR-0268 needed its `Superseded in part by ADR-0267` back-link; it was added at acceptance.
- Nits, folded: the Constraints line now names the libseccomp rule exactly (an unconditional rule for a system call
  overrides its conditional rules), and Consequences note the cost of the `ioctl` allow rules.

### Verified correct (keep)
- **The exact cover:** `ioctlAllowRules` yields 69 pairwise disjoint masked rules on the command's low 32 bits; together they
  match 2^32 − 3 commands, each by exactly one rule, and none of ADR-0268's three. The upper 32 bits are ignored.
- containerd v2.4.2's `DefaultProfile` names `ioctl` once, in its unconditional allow list; `withSeccompProfile` removes
  it there only, and the profile is computed last in `ociOpts`, so it sees the final capabilities.
- **The measured failure it fixes:** with the profile's unconditional `ioctl` allow kept, the volumes lane's V3 probe set a
  project id; Docker reproduced it across six rule orders and shapes.
- Mutant: keeping the unconditional `ioctl` allow fails `TestScenarioDefaultProfileRefuses` ("refused command
  0x401c5820 matches 1 allow rules, want 0").

### Checks
- `just test-linux` (the Linux packages): ok. The volumes lane, with `unshare EPERM` added to V3: PASS.
- `just lima-example-all` (12 lanes at the time) and `scripts/agent/gate.sh`: PASS on the branch, and again on the
  combined advisory tree (13 lanes, gate PASS) before the merge.
