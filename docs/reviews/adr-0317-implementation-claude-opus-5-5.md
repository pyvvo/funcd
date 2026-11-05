# ADR-0317 implementation review — claude-opus-5-5

## Verdict: pass — 0 blockers, 0 majors, 1 minor (ADR-0317 implementation, model: claude-opus-5-5)

This reviews the commit `feat(egress): a domain rule reaches globally reachable addresses only`: `notGlobal`, `restricted`, the tests, the
documents and the line on ADR-0315. The table holds against ADR-0317 Decision 1 and against each row of IANA's two registries, and each scenario
has its named test, which passes under `-race`. The one Minor needs no rework before the merge. ADR-0317 is stamped `Implemented` and F231
`implemented`.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

1. **Minor · model — no test samples a reachable row nested in an entry.** `docs/behavior/security.md:61` says that a registry row marked globally
   reachable inside an entry of the table, "such as `192.0.0.9` or `2001:3::/32`", counts as its entry does, and it cites
   `TestScenarioDomainRuleGloballyReachableOnly`. That test samples the first and the last address of each prefix, so it names no such row. Two
   overlay mutants of `restricted`, one that answers false for `192.0.0.9` and one that answers false inside `2001:3::/32`, pass all six tests of
   this rule. The code is right: a scratch test with one sample in each of the nine nested rows passes on this code and fails on the second mutant.
   **Fix**: add those nine samples to the scenario test, in one loop.

### Routed (not this work's)

- **The comparison of the table with the registries has no owner** · home: elsewhere · destination: the session that drives this work, as a board
  card or a line in a sweep like #1324. The ADR's Consequences state what a new registry row costs. `driver.py list` shows no card for the comparison,
  and no script or workflow names the registries. The judge routed the same item.
- **ADR-0317's IPv6 costs have no row** · attribution: `adr` · home: elsewhere · destination: `docs/behavior/security.md`, a living document. The second
  bullet of Consequences says that IPv6 space outside these rows that IANA's address-space registry reserves counts as globally reachable, and that the
  IPv6 rows take effect once IPv6 worker egress lands. The ADR's *Behavior rows* do not list this, and the work follows that section, so nothing goes back to
  the builder. No chapter of `docs/behavior/` mentions IPv4 or IPv6, and the `adr` skill gives an accepted cost a row. One `accepted` row would close this
  with no amendment.
- **Two lines of ADR-0315's tests** · home: elsewhere · destination: the next change to those tests. This ADR's plan moves only the node addresses.
  - `TestScenarioCIDRRuleNamesTheAddress` ends by asserting that a connection with no name needs no read of the node's addresses
    (`internal/network/egress/egress_test.go:344-347`). It does so at `10.0.0.5`, which `restricted` answers before any read. A mutant that reads at every
    connection therefore passes, on this code and on the parent commit's code and tests, so this work did not bring it. A scratch test at `203.0.114.9` fails
    that mutant.
  - `TestScenarioRuleFiresThroughACNAME` samples `203.0.113.7` (`internal/network/egress/forwarder_test.go:336`), now an address of the table, while ADR-0315's
    scenario says "at a public address". It stops at `DomainsFor`, so it stays true.
- **A mapped or zoned destination at the PDP** · home: above · destination: the decision on IPv6 worker egress (`internal/network/nftables_linux.go:215`).
  For those forms `decideConnect` ends in an error from the PDP, with nothing allowed and no audit record (a scratch test ran four). V1's gateway builds only IPv4
  (`internal/network/egress/gateway_linux.go:134`), so no connection has either form today.

### Not run here

`just ci`, `just ci-full`, `scripts/agent/gate.sh`, `just test-linux`, the lanes and the e2e suite did not run in this review. The session that drives this work
runs one gate over the branch afterwards. None of this is a finding.

### ✅ Verified correct (keep it)

**One named test per Scenario, each with `t.Parallel()`, un-skipped and passing under `-race`:**

| Scenario | Test |
|---|---|
| `domain-rule-globally-reachable-only` | `TestScenarioDomainRuleGloballyReachableOnly` |
| `globally-reachable-block-allowed` | `TestScenarioGloballyReachableBlockAllowed` |
| `cidr-rule-names-a-table-address` | `TestScenarioCIDRRuleNamesATableAddress` |

The plan's `TestNotGlobalIsTheDecidedTable` passes too, and so do the two tests of ADR-0315 whose node addresses moved, `TestScenarioDomainRulePublicOnly` and
`TestScenarioCIDRRuleNamesTheAddress`. Those six run 130 subtests with no skip, each test in at most 0.01 s. All three scenario tests use the real compiler and PDP.

**Commands, each through `scripts/agent/d`:**

| Command | Exit |
|---|---|
| `go build ./...` | 0 |
| `go test -race -count=1 ./internal/network/... ./internal/auth/cedar/ ./api/types/...` | 0 |
| `go test -race -count=1 -v` of the six tests above in `./internal/network/egress/` | 0 |
| `go vet ./internal/network/... ./api/...`, and `GOOS=linux go vet ./internal/network/...` | 0, 0 |
| `golangci-lint run ./internal/network/... ./api/...` | 0 (0 issues) |
| `just check-behavior`, `just check-hygiene`, before and after the stamp | 0, 0 |

**The table against its sources.** IANA's two CSV files, fetched 2026-11-05, hold 50 rows (25 and 25) and 51 blocks, as one row holds two.

| Registry rows | Count | Result |
|---|---|---|
| Marked False | 32 rows, 33 blocks | Each lies inside a prefix, as the checklist asks |
| With no True or False value: `192.88.99.0/24`, `2001::/32`, `2001:10::/28`, `2002::/16` | 4 | Each lies inside a prefix |
| Marked True, outside the table: `192.31.196.0/24`, `192.52.193.0/24`, `192.175.48.0/24`, `2620:4f:8000::/48` | 4 | Allowed |
| Marked True, nested: `192.0.0.9/32`, `192.0.0.10/32`, `2001:1::1/128`, `2001:1::2/128`, `2001:1::3/128`, `2001:3::/32`, `2001:4:112::/48`, `2001:20::/28`, `2001:30::/28` | 9 | Restricted, as Decision 1 says of an entry that counts whole |
| Marked True, named in Others: `64:ff9b::/96` | 1 | Restricted |

- `notGlobal`, the ADR's Decision 1 and the test's `adr0317Table` hold the same 32 prefixes (13 + 12 + 7), each masked, and none lies inside another. 28 are registry rows.
  The other four are the ones Decision 1 names from outside the registries: `224.0.0.0/4`, `ff00::/8`, `fec0::/10` and `168.63.129.16/32`.
- A scratch test added by an overlay ran the first and the last address of every row, and a third in a wider block, through `restricted` and, but for the mapped row, through
  `decideConnect` with the real compiler and PDP. Every answer is the one in the table above, with no `domain` in a refused record.
- `restricted(::ffff:8.8.8.8)` is false and `restricted(::ffff:0.0.0.0)` is true, so no address reaches `::ffff:0:0/96`, as Decision 1 says.
- A `cidrs` rule that names an address reached it by address and with a name attested, and was refused on another port, for 73 samples: both ends of each of the 31 sampled
  prefixes and 11 others, among them samples of the nested rows. `0.0.0.0/0` on 443 reaches `168.63.129.16`, as Consequences says.
- `4000::1`, `fe00::1`, `1::1`, `::a00:5` and `2a00::5efe:a00:5` count by their own prefix, as Scope and Consequences say.

**Overlay mutants.** 141 mutants of `internal/network/egress/gateway.go` ran against the six tests; the unchanged file passes all six.

| Mutant | Tests that fail |
|---|---|
| No `Unmap` | `TestScenarioDomainRuleGloballyReachableOnly`, at `::ffff:93.184.216.34` |
| No `WithZone("")` | `TestScenarioDomainRuleGloballyReachableOnly` |
| No `IsValid` check | `TestScenarioDomainRuleGloballyReachableOnly` |
| No table | 4: both domain tests and both CIDR tests |
| The node comparison skipped | `TestScenarioDomainRulePublicOnly`, as the checklist asks, and no other |
| A failed read of the node's addresses counts as no error | `TestScenarioDomainRulePublicOnly` |
| `decideConnect` keeps the domains at a restricted address | 4: both domain tests and both CIDR tests |
| Each of the 32 prefixes removed | `TestNotGlobalIsTheDecidedTable` on all; the scenario test on all but `::ffff:0:0/96`, as the plan says |
| Each prefix narrowed to either half, and widened by one bit (90) | `TestNotGlobalIsTheDecidedTable` on all; the scenario test on all but 11: those of `::ffff:0:0/96` and the widenings that only join neighbors in the table, such as `100::/64` to `100::/63` |
| `192.31.196.0/24`, `192.52.193.0/24`, `192.175.48.0/24` or `2620:4f:8000::/48` added | `TestScenarioGloballyReachableBlockAllowed` and the table test |
| `192.0.0.0/23` or `::/96` added | The scenario test and the table test |
| `2000::/3` added | 4: both scenario tests above, the table test and `TestScenarioDomainRulePublicOnly` |
| `10.0.0.1/8` in place of `10.0.0.0/8` | The table test |
| `192.0.0.9` or `2001:3::/32` exempted | None (Minor 1) |
| The node's addresses read before the table | None: the order shows only when that read fails at an address of the table, and no scenario speaks of that |
| The node's addresses read at every connection | None (routed) |

**Review checklist and Contracts.**
- `notGlobal` is a `[]netip.Prefix` built once (`internal/network/egress/gateway.go:113-126`), with the house `gochecknoglobals` line. `restricted` keeps its signature and reads the
  table, then the node's addresses (`gateway.go:130-144`). No config key, port, dependency or Cedar text changes.
- `decideConnect` drops the attested set at a restricted address before the PDP (`gateway.go:174-182`), so a `cidrs` rule stays the only thing that permits it.
- The node addresses in the existing tests moved to `203.0.114.9` and `2a00:1450:1::9`, both outside the table. The other samples of `TestScenarioDomainRulePublicOnly` are unchanged and
  still refused or allowed as before; the rest of that test's diff is a move to a shared rig, and no assertion is weakened.
- `restricted` takes 84 to 136 ns with no allocation, on one benchmark run with a fake read of the node's addresses, which fits the *(neutral)* line of Consequences.
- *Design model* reads `None`, and no file under `docs/design/` changed.

**The judge's two Majors landed, in the ADR and in the work.**
- The row no address reaches: Decision 1 says that `::ffff:0:0/96` is listed to match its registry row and that the mapping comes off first, and Scope reads "an IPv6 form other than the
  mapping". The plan and the checklist leave that row out of the samples, and the test skips it (`egress_test.go:383`). `TestNotGlobalIsTheDecidedTable` holds it, and the mapped form, a
  zoned form and the zero `Addr` run on `restricted` directly.
- The behavior rows: *Behavior rows* lists `An operator's Cedar Policy by domain` and a new registry row, and both are in the chapter. The judge's Minors landed too, but for the feature
  issue, which the decider waived.

**Tree and tracking.**
- The commit touches the plan's files and two more that the process asks for: the F231 row and the line on ADR-0315. `git diff` of ADR-0315 against the branch's base is that one line, below the
  Status line, in the form of ADR-0272 Decision 7, and it names the parts the ADR's header names.
- `docs/behavior/security.md` holds what *Behavior rows* says: the row `A domain rule at an address that is not globally reachable`, the changed `An operator's Cedar Policy by domain`, the
  note's sentence, and the rows for a nested row and a new registry row. Each test a row cites exists.
- `api/types/v1alpha1/egresspolicy.go`, `blueprint.md` and `docs/PROJECT-SUMMARY.md` state the rule as Decision 1 does. No generated file holds the type's comment. `state.py --check` reads
  `IN SYNC`.
- The ADR enters the tree in this one commit with its status at `Reviewing`, so no diff separates its accepted text. It carries each part ADR-0272 Decision 7 asks of an amendment, *Behavior
  rows* included.
- The branch's `egress` lane ended `final status: PASS` on this code, with its wildcard domain case and `cidr-rule-reaches-the-bridge-address`, a `cidrs` rule at an address of the table. Its log was
  read, not rerun. The one later commit touches `internal/network/fence_linux_test.go` alone, and the table is a pure read.

### Definition of Done

12 of 12 hold: the two lines of the ADR's Review checklist, the `egress` lane clause of its plan, ADR-0313's Behavior rows, and eight generic items (scenario tests, real behavior, Contracts
honored, tree, conventions, dependencies, scope, tracking). `just ci` and the gate are not counted, because they did not run in this review. No contract suite applies, because no port changed.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0317 (implementation) → pass, 0 blockers, 0 majors, 1 minor, 1 model-attributed, DoD 12/12. See docs/reviews/model-scorecard.md.

### Recommendation

ADR-0317 is `Implemented` and F231 `implemented`, and the branch merges after its one gate. Minor 1 goes back to the builder as one small loop in a test and does not hold the stamp. The ADR is now
frozen, so a fault the gate finds in this work is fixed in the code. The PR closes no issue: F231 has no feature issue, at the decider's request.
