## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0269 implementation, model: claude-opus-5-5)

This report reviews the private advisory branch `fix/ghsa-vcf2-engine-egress` (GHSA-vcf2-hc48-w9jp, route 3), merged
with the advisory's other fixes in 08eeea98. The review ran privately before publication; this record follows it.
The ADR was judged twice: round 1's two Majors (a gateway endpoint that is a hostname; the SQL engine left out of the
decider's choice) were folded before acceptance.

### Verified correct (keep)
- **Fail-closed ordering:** the containerd driver's `Create` pins the netns, runs CNI setup, then `network.FenceNetns`
  when `EgressOnly` is non-nil, all before `Start`; a fence failure takes the CNI failure's cleanup path.
- **The fence:** an `inet funcd_fence` table whose `output` chain has policy drop and accepts loopback (ifindex 1),
  `ct state established,related` and IPv4 TCP to each endpoint; IPv6 and UDP fall to the policy, and a non-IPv4
  endpoint is refused at write time. The engine has no CAP_NET_ADMIN, and the table dies with the netns.
- **The engines:** `provider.EgressTo` fences the Lake to its S3 gateway and the SQL engine to its change feed; a
  hostname gateway holds the Lake not Ready (`GatewayEndpointNotAnAddress`), so no engine starts unfenced. Nothing on
  this path reads `server.network.egress`.
- Mutants: dropping the hostname refusal fails `TestScenarioLakeSetsEgressOnly`; a nil SQL fence fails
  `TestEngineFencedToChangeFeed`.
- **Non-vacuous lane step:** with the fence call disabled the duckdb lane's `engine-fenced` probe logged HITS=1 and
  failed; with it, HITS=0.

### Checks
- `go test -race` on `internal/provider`, `internal/services/lake`, `internal/services/sql`; vet (Linux too) and
  golangci-lint: clean. `just test-linux -run=Fence`: ok.
- `just lima-example-all` (13 lanes) and `scripts/agent/gate.sh`: PASS on the branch and on the combined advisory tree.
- Found on the way, fixed separately: a Lake re-created within one reconcile pass kept its old engine (#1195, #1196).
