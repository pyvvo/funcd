# ADR-0179: Unrevisioned CNI attachment IDs that cannot collide

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (judged twice by three lenses; held from publication)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, containerd, cni, network, namespace
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (function runtime behind the `runtime.Runtime` port)
- **Supersedes in part**: [ADR-0143](0143-redeploy-by-revision-switch.md) (Implemented), three clauses only: the Contracts
  table row `| CNI ID | <ns>-<name>-r<replica> | <ns>.<revision>.r<replica> |` (line 210), in its column "Without a
  revision (unchanged)", the Review-checklist clause "unrevisioned names are unchanged" (line 300), and Decision 1's
  "which keep today's behavior" for provider engines (lines 115-116), each for the unrevisioned CNI ID only. Its
  revisioned CNI ID, container IDs, instance IDs and labels stand.
- **Relates to**: [ADR-0011](0011-runtime-sandbox-port.md) (bridge + firewall CNI) ·
  [ADR-0152](0152-runtime-worker-owner-kind.md) (Proposed; engines are unrevisioned workers, CNI IDs unchanged there) ·
  [ADR-0173](0173-container-execution-runs-solo.md) (Proposed; no pool worker runs on containerd) ·
  [ADR-0167](0167-process-worker-crash-recovery.md) (Proposed; its boot sweep runs `Sweep` → `discard` on every
  `funcd-*` namespace, the main path that releases old-form leftovers on upgrade)
- **Publication**: held until the release that carries its fix ships; the PR and release notes describe the change as
  a CNI attachment naming fix, without further detail.

## Context & Need

Line numbers below were verified at commit 5dbb7fb (unchanged since origin/main 915f342). The containerd driver names each worker's CNI attachment in
`workerNames` (`internal/runtime/containerd/containerd_linux.go:381-386`): without a revision the ID is
`ns + "-" + name + "-r" + replica`; with one it is `ns + "." + revision + ".r" + replica`. `Create` attaches with
`d.cni.Setup(nctx, cniID, netnsPath)` (:318) and removes it on failure (:322); `Stop` removes it (:436); `discard`
rebuilds it from the container labels and removes it (:526-527), for `reclaim` (:367) and `Sweep` (:501).

A DNS-1123 label allows `-`, so namespace `a-b` with name `c` and namespace `a` with name `b-c` both yield
`a-b-c-r0`. Container IDs are scoped per funcd namespace by `nsCtx` (:220), but CNI attachments are node-global: the
host-local lease, the cached result and the firewall state are keyed by the attachment ID and interface. Two such
workers on one node share one attachment; the second `Setup` and either `Stop` act on the other's network state, so
one of them loses its address or its firewall rules.

The unrevisioned workers today are CatalogService engines (ADR-0152's table: `Revision` is `""` for engines and pool
workers). The purpose: no two (namespace, name) pairs share an unrevisioned CNI ID, and attachments made under the
old form are still released.

## Scenarios

- `scenario: unrevisioned-cni-ids-distinct-across-namespaces` — Given an engine `c` in namespace `a-b` and an engine
  `b-c` in namespace `a` on one containerd node, When both are created and started, Then each has its own CNI
  attachment and address, and both serve; stopping one leaves the other serving (unit: `Stop` removes only the
  stopped worker's ID; lane: the other still answers).
- `scenario: leftover-old-form-attachment-released` — Given an unrevisioned container left from before this change,
  attached under `<ns>-<name>-r<replica>`, When `reclaim` or `Sweep` discards it, Then the old-form attachment is
  removed (its host-local lease, cached result and firewall state; the lane checks lease and cache), and so is the
  new-form one.
- `scenario: other-worker-names-unchanged` — Given any worker, When it is created, Then its container ID, instance
  ID and (for a revisioned worker) CNI ID are exactly those of ADR-0143.

## Scope

In: the unrevisioned CNI attachment ID of the containerd driver, and releasing attachments left under the old form.

Out: container IDs, instance IDs, revisioned CNI IDs and labels (unchanged); the process driver (no CNI); pool
workers on containerd (none run there, ADR-0173); masquerade rules, which `discard` leaves as it does today.

## Constraints & Decision drivers

- A Namespace, Function and CatalogService name is a DNS-1123 label: `[a-z0-9-]`, no `.` and no `_`.
- libcni `ValidateContainerID` (`containernetworking/cni` v1.3.0 `pkg/utils/utils.go:38`) accepts
  `[a-zA-Z0-9][a-zA-Z0-9_.\-]*`, so `_` is a valid attachment ID character.
- ADR-0172 relies on engine container IDs carrying no `.` or `_` (ADR-0152 Contracts, lines 126-135): container IDs
  must not change.
- The ID stays readable in logs and in `/var/lib/cni` state, as today.

## Alternatives considered

| Option | Outcome |
|---|---|
| A separator a DNS-1123 label cannot contain: `<ns>_<name>-r<replica>`, plus releasing attachments left under the old form | **chosen**: unambiguous, readable, valid for libcni |
| A hash of (namespace, name) as the CNI ID | rejected: opaque in logs and CNI state; debugging needs a label lookup |
| Refuse a Create whose CNI ID another namespace already holds | rejected: treats the symptom, lets one tenant block another, misses crash leftovers |
| Keep the ID and document the collision | rejected: two tenants on one node still share one attachment |

## Decision

1. **The unrevisioned CNI attachment ID is `<ns>_<name>-r<replica>`.** The only unrevisioned containerd workers are
   engines (pools never run there, ADR-0173), but the form does not depend on that: any namespace and name are
   DNS-1123 labels without `_`, so the namespace ends at the only `_`, and two (namespace, name) pairs never share an ID. The three forms stay disjoint: the old
   form has neither `_` nor `.`, the new one has one `_`, a revisioned one has `.`.
2. **Container IDs, instance IDs and revisioned CNI IDs are unchanged.**
3. **`discard` also releases the old form.** For a container whose `funcd/revision` label is empty, after removing
   the ID `workerNames` returns it removes `<ns>-<name>-r<replica>`, built from the `funcd/namespace`, `funcd/name`
   and `funcd/replica` labels, best-effort like today; it skips the DEL when any of the three is empty. A DEL of an
   unknown attachment is tolerated (best-effort, error ignored; a plugin may return an error for it), so this is safe
   for containers created after the change. It passes netns `""`, as `discard` does today, so masquerade rules are
   left exactly as the current-form DEL leaves them. Because `reclaim` and `Sweep` both go through `discard`, no other
   path changes.

## Temporary workarounds

The old-form release in `discard` exits when no node can still hold a container created before this change; its
removal is a follow-up issue filed with the release that carries it, not this ADR.

## Contracts

| containerd name | Without a revision | With a revision (unchanged) |
|---|---|---|
| container ID (unchanged) | `<name>-r<replica>` | `<revision>.r<replica>` |
| CNI ID | `<ns>_<name>-r<replica>` (was `<ns>-<name>-r<replica>`) | `<ns>.<revision>.r<replica>` |

```go
// workerNames returns a worker's container ID and CNI attachment ID (ADR-0143, ADR-0179). The CNI ID is node-global;
// without a revision '_', which a DNS-1123 name cannot contain, ends the namespace ('.' with one: ADR-0143 line 213).
func workerNames(ns, name, revision, replica string) (ctrID, cniID string) {
	if revision == "" {
		return name + "-r" + replica, ns + "_" + name + "-r" + replica
	}
	return revision + ".r" + replica, ns + "." + revision + ".r" + replica
}
```

```go
// in discard, inside the existing `if labels, lerr := c.Labels(nctx); lerr == nil` block (:525), after the
// Remove of cniID; no second Labels call:
ns, name, rep := labels["funcd/namespace"], labels["funcd/name"], labels["funcd/replica"]
if labels["funcd/revision"] == "" && ns != "" && name != "" && rep != "" {
	_ = d.cni.Remove(nctx, ns+"-"+name+"-r"+rep, "")
}
```

No new exported name, label, reason or config key. No port or API change.

| Consumes | Exposes |
|---|---|
| Container labels `funcd/namespace`, `funcd/name`, `funcd/replica`, `funcd/revision` | One CNI attachment per (namespace, name, replica) on a node |

## Implementation plan

1. `internal/runtime/containerd/containerd_linux.go`: `workerNames` and `discard` as in Contracts.
2. Tests, one per scenario, in `internal/runtime/containerd/cniid_linux_test.go` (new), with the fake client and a
   recording CNI fake in the style of `createfail_linux_test.go`:
   - `TestScenario_UnrevisionedCNIIDsDistinctAcrossNamespaces`: `workerNames` for (`a-b`, `c`) and (`a`, `b-c`)
     differ; two `Create`s call `Setup` with two distinct IDs; `Stop` of one calls `Remove` with its ID only.
   - `TestScenario_LeftoverOldFormAttachmentReleased`: `discard` of an unrevisioned container removes both
     `a-b_c-r0` and `a-b-c-r0`; of a revisioned one, only its revisioned ID; with an empty label, no old-form DEL.
   - `TestScenario_OtherWorkerNamesUnchanged`: container IDs and revisioned CNI IDs match the ADR-0143 table.
3. Lima lane `duckdb` (engines on containerd): fixtures under `e2e/fixtures/` for engine `c` in namespace `a-b` and
   engine `b-c` in namespace `a`; in `e2e/duckdb.venom.yml`:
   - `leftover-old-form-attachment-released`: before deploying, an in-VM step plants a leftover — a container `c-r0`
     in containerd namespace `funcd-a-b` labelled `funcd/namespace=a-b`, `funcd/name=c`, `funcd/replica=0` and no
     `funcd/revision`, plus a CNI ADD of the `funcd` network under `a-b-c-r0` (lease, cached result, firewall
     state; the ADD tool, e.g. `cnitool`, is new to the lane and chosen at implementation). The ADD needs a netns
     (e.g. `ip netns add`); the step deletes it before deploying, matching `discard`'s netns `""` DEL. Deploying
     engine `c` in `a-b` reaches `Create` → `reclaim` (:367), which discards a leftover only under the container ID
     the new `Create` uses, so the planted ID must be `c-r0`. Assert no lease under `/var/lib/cni/networks/funcd`
     and no cached result under `/var/lib/cni/results` names `a-b-c-r0` (the firewall DEL is covered by the unit test).
   - `unrevisioned-cni-ids-distinct-across-namespaces`: queries both engines, stops one and queries the other.
4. Advance the F12 row's `cni ids:` status as this ADR moves (the row links it from draft).
5. At acceptance, add to ADR-0143's header the one edit an Implemented ADR allows: `- **Superseded in part by**:
   [ADR-0179](0179-unrevisioned-cni-ids-cannot-collide.md) (<date>) — unrevisioned CNI ID only`, as ADR-0003 does.

**Done when** the three scenario tests pass under `-race`, `just ci` is green and the duckdb lane passes on
containerd with both cases, the planted old-form leftover released.

## Review checklist

- [ ] `workerNames` returns `<ns>_<name>-r<replica>` without a revision; every revisioned name and every container ID match ADR-0143.
- [ ] `discard` removes the old-form ID only when `funcd/revision` is empty and the other three labels are set, after
      the current ID, ignoring errors.
- [ ] No other call site builds a CNI ID; `Stop` keeps using the stored `sb.cniID` (now the new form), so `Create`,
      `Stop`, `reclaim` and `Sweep` are otherwise unchanged.
- [ ] Each scenario has one named, passing test; the duckdb lane plants an old-form leftover and sees it released.
- [ ] The F12 row names this ADR with its status; ADR-0143 carries the back-link.

## Consequences

- Positive: two namespaces never share a CNI attachment on a node; IDs stay readable.
- Negative: `discard` sends one extra CNI DEL per unrevisioned container until the old-form release is removed.
- Risks accepted: on upgrade, the old forms of (`a-b`, `c`) and (`a`, `b-c`) are the same ID, so the first
  `discard` of either releases it. If the other's pre-upgrade task still runs, host-local may hand its address to a
  new worker until that task is discarded too (reclaim or the ADR-0167 boot sweep). That attachment was already shared
  before the upgrade; no worker of the new binary uses the old form.

## Open questions

None.

## References

- `internal/runtime/containerd/containerd_linux.go` (verified at 5dbb7fb): :220, :318, :322, :367, :381-386, :436,
  :501, :526-527
- containernetworking/cni v1.3.0 `pkg/utils/utils.go` `ValidateContainerID`; CNI spec: DEL of a missing attachment
  succeeds
- ADR-0143 lines 115-116, 210, 213, 300; ADR-0152 Contracts lines 126-135 (worker-name table, IDs never meet)
