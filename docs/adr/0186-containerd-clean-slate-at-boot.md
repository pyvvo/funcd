# ADR-0186: Containerd clean slate at boot — the private containerd drops funcd's images and namespaces

- **Status**: Accepted (2026-10-05)
- **Date**: 2026-10-05
- **Deciders**: green-0-rabbit
- **Tags**: runtime, containerd, images, crash-only
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (function runtime behind the `runtime.Runtime` port)
- **Supersedes in part** (the `Superseded in part by` back-links are added to both at acceptance):
  - [ADR-0167](0167-process-worker-crash-recovery.md) (Implemented), one clause of Decision 8 (lines 133-135): "funcd
    lists the containerd namespaces named `funcd-*` and, for each, discards every container in it", for the private
    containerd only: there the boot sweep also deletes every image in each `funcd-<ns>` namespace, then the namespace.
    On an external containerd the clause stands, and the rest of Decision 8 stands everywhere.
  - [ADR-0149](0149-runtime-availability.md) (Implemented), Decision 2's "A stored image is used as is (its `:latest`
    is not refreshed)" (lines 110-111), on the private containerd only: there a stored image lasts one funcd run
    (Decision 6). The availability rule and the rest of Decision 2 stand.
- **Refines** `blueprint.md` at acceptance: line 49 "imported into funcd's managed containerd at startup
  (**ADR-0054**" becomes "imported into funcd's managed containerd on the first `Create` of each runtime in a
  namespace that lacks its image; on the private containerd the boot sweep drops an earlier run's images (**ADR-0054**, **ADR-0186**".
- **Relates to**: refines [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md) Decision 2
  (lines 119-121): the stored image lasts one run (Decision 6) · relies on ADR-0149 Decision 3 (lines 113-123)
  unchanged · [ADR-0160](0160-worker-exit-reason.md) (boot dirs) · [ADR-0052](0052-bench-containerd-cgroup-footprint-lane.md)
  (system containerd, untouched) · [ADR-0170](0170-owner-garbage-collector.md) (unchanged).

## Context & Need

Issue [#729](https://github.com/pyvvo/funcd/issues/729), reproduced on 0.6.0 and confirmed by an independent refuter
(code lines below are at origin/main 78219297). `resolveImage` imports and unpacks the curated tar, or pulls an
override ref, into each `funcd-<ns>` containerd namespace (`internal/runtime/containerd/containerd_linux.go:995`,
Import `:1022`, Pull `:1041`). `SweepAll` (`:928-959`) and `discard` (`:725-751`) delete only containers and their own snapshots. No
non-test code deletes an image or a containerd namespace; the only namespace call is `NamespaceService().List`
(`:929`). containerd treats an image record as a GC root, so the unpacked layers stay too. The private containerd
keeps its data root across restarts (`internal/runtime/ctrmanager/manager_linux.go:41-42`, `:107-108`).

The result: a deleted Namespace keeps its images, layers and `funcd-<ns>` namespace forever, and each `imageOverride`
ref change adds one image per namespace that runs the runtime. The cost is about one unpacked rootfs per runtime per
namespace ever used (inferred, not measured).

**Purpose.** The boot sweep, called once by `cmd/funcd` before any controller starts, bounds what the private
containerd's data root holds to what one funcd run created.

## Scenarios

- **scenario: containerd-images-cleared-at-boot** (the #729 reproduction) — Given a private containerd where a worker
  ran in Namespace `x` (so `funcd-x` holds the runtime image, its unpacked layer and a boot-dir parent) and the
  Namespace was deleted, When funcd restarts, Then before the first reconcile the image, the containerd namespace
  `funcd-x` and `<bootRoot>/funcd-x` are gone.
- **scenario: override-change-leaves-one-image** — Given a private containerd whose `funcd-default` holds `<ref-v1>`
  pulled for `runtime.containerd.imageOverride`, When the operator changes the override to `<ref-v2>` and restarts
  funcd, Then `<ref-v1>` is gone and the first `Create` of that runtime pulls `<ref-v2>`.
- **scenario: curated-image-from-running-binary** — Given a private containerd whose `funcd-default` holds
  `docker.io/funcd/runtime-nodejs22:latest` imported by an earlier binary, When funcd restarts, Then that record is
  gone before the first `Create`, so the first worker runs the image embedded in the running binary.
- **scenario: external-containerd-keeps-images** — Given an external containerd (`runtime.containerd.socket` set) with
  a leftover container and an image in `funcd-x`, When funcd starts, Then the container is removed and the image and
  the namespace are kept.
- **scenario: namespace-not-empty-retried-next-boot** — Given a private containerd that refuses to delete `funcd-x`
  with FailedPrecondition (for example, a lease left by an import a crash interrupted), When funcd starts, Then startup
  goes on, a warning names the error, `<bootRoot>/funcd-x` is kept, and the next boot tries again.
- **scenario: shutdown-keeps-images** — Given a private containerd with running workers, When funcd shuts down cleanly
  (`Close`), Then the containers are removed and no image or namespace is deleted.

## Scope

- **In**: a boot-only clean slate of images, containerd namespaces and boot-dir parents on the private containerd; the
  boot hook's method in `cmd/funcd`.
- **Out**: an external containerd (no deletion; a label for funcd-imported images is deferred, see Open questions);
  release on Namespace delete (no `runtime.Runtime` port change, ADR-0011 and ADR-0170 untouched); `Close` behaviour;
  lease deletion; the process driver.

## Constraints & Decision drivers

- funcd owns the private containerd's data root (ADR-0054 Decision 3; `Config.Private`, `containerd_linux.go:82-85`);
  an external containerd may hold another owner's or a bench's state (ADR-0055), so funcd deletes nothing there.
- The sweep runs in the runtime builder before `funcd.New` (`cmd/funcd/main.go:865-871`): no store, no worker of this
  run exists. It must not need either.
- Startup never fails on a sweep error; shutdown stays bounded by `closeGrace`.
- containerd deletes a namespace only when it holds no image, blob, container or snapshot (containerd v2.3.1
  `core/metadata/namespaces.go:135`, FailedPrecondition otherwise), so the image delete must wait for GC.
- Criteria: smallest change, no port change, no new config key, leftovers bounded by one funcd run.

## Alternatives considered

| Option | Pros | Cons — why it lost |
|---|---|---|
| **A, refined: clean slate at boot, private only** (chosen) | One driver method; no store, port or config change; refreshes the curated image after an upgrade | One import or pull per (namespace, runtime) at the first cold start after each restart (not measured) |
| B: keep what is still used, at boot | No re-import after a restart | Needs the live Namespaces and wanted refs from the store before `funcd.New`, so the sweep moves or takes a new input; keeps a stale curated `:latest` after an upgrade unless it also compares digests |
| C: release on Namespace delete, plus B at boot | Space returns at once | New port method on every driver (ADR-0011), Namespace delete calls the runtime (ADR-0170), plus all of B's cost — too much for a priority/low leak |
| A on every containerd | Also bounds an external containerd | Deletes images an operator manages, such as ADR-0052's hand-loaded `funcd-default` image |
| A plus a funcd label on images (external containerd too) | Bounds an external containerd safely | Needs a label stamped via `WithImageLabels`/`WithPullLabels`; no name chosen and no reported need — deferred |
| Clean at `Close` as well | Space returns at a clean stop | Slower shutdown (one synchronous GC per image); the boot pass already covers crashes |
| Fail startup on FailedPrecondition | Surfaces a stuck namespace | A lease from an interrupted import (24 h default expiry) would block startup for a day |
| Also delete leftover leases | Empties the namespace after a crash mid-import | A third containerd service in the sweep; the lease expiry plus a retry at the next boot already bound the leak |

## Decision

1. **A boot-only clean slate.** The containerd driver gains `BootSweep`; the `bootSweeper` hook in `cmd/funcd` calls
   it instead of `SweepAll`. `Close` keeps calling the container-only `SweepAll` (`containerd_linux.go:917-918`).
2. **Containers first.** `BootSweep` runs `SweepAll`. If that fails, it returns the error and deletes no image.
3. **Private containerd only.** With `Config.Private` false, `BootSweep` stops after `SweepAll`.
4. **Images, then the namespace, then the boot dir.** For each listed namespace with the `funcd-` prefix, one at a
   time: delete every image in it with `images.SynchronousDelete()` (a NotFound counts as deleted); when every delete
   succeeded, delete the namespace; when that succeeded, remove `<bootRoot>/funcd-<ns>`.
5. **Errors are warnings.** Every error (a FailedPrecondition from the namespace delete included) is joined and
   returned; `cmd/funcd` logs it at warn and goes on, as today. What stays is retried at the next boot.
6. **After a restart.** The first `Create` per (namespace, runtime) imports the running binary's embedded tar or pulls
   the override ref, through the unchanged `resolveImage`. On the private containerd, ADR-0054 Decision 2's import and
   ADR-0149 Decision 2's "in this node's image store" therefore cover the current run only: a hand-loaded image is gone
   after a restart, and "its `:latest` is not refreshed" holds within one run. Per ADR-0149 Decision 3, an absent
   image is `RuntimeUnavailable`; a registry outage is a `Reconcile` error retried at the controller backoff, so an
   override Function does not converge until the registry answers.

## Temporary workarounds

- An external containerd keeps leaking. Workaround (from #729, untested): `ctr -a <socket> -n funcd-<ns> images rm
  <ref>`, then `ctr -a <socket> namespaces rm funcd-<ns>`. Exit criterion: an ADR that marks funcd-imported images
  with a label and deletes only those on an external containerd.

## Contracts

```go
// internal/runtime/containerd/containerd_linux.go — new method.

// BootSweep is the boot sweep (ADR-0167 Decision 8, ADR-0186): it runs SweepAll and, on the private containerd only,
// deletes every image in each funcd-<ns> namespace with a synchronous delete, then the namespace and its boot-dir
// parent. Close does not call it.
func (d *driver) BootSweep(ctx context.Context) error
```

```go
// cmd/funcd/main.go:779-782 — the hook's method changes; :867-871 calls sw.BootSweep(ctx) and keeps the warn log,
// with the message "funcd: could not clear everything an earlier run left in containerd".
type bootSweeper interface {
	BootSweep(ctx context.Context) error
}
```

containerd v2.3.1 calls used (op `runtime.containerd.BootSweep`, errors wrapped with `mapErr`):

| Call | Signature | Context |
|---|---|---|
| `d.client.NamespaceService().List` | `List(ctx) ([]string, error)` | plain `ctx` |
| `d.client.ImageService().List` | `List(ctx, filters ...string) ([]images.Image, error)` | `namespaces.WithNamespace(ctx, name)` |
| `d.client.ImageService().Delete` | `Delete(ctx, name string, opts ...images.DeleteOpt) error` with `images.SynchronousDelete()` | same |
| `d.client.NamespaceService().Delete` | `Delete(ctx, namespace string, opts ...namespaces.DeleteOpts) error` | plain `ctx` |
| `os.RemoveAll` | `filepath.Join(d.bootRoot, name)`, skipped when `d.bootRoot == ""` | — |

| Consumes | Exposes |
|---|---|
| `Config.Private` (`containerd_linux.go:82-85`, set from `Socket == ""` at `cmd/funcd/main.go:859`), `d.bootRoot` (`:120`, parent made at `:296-297`), `nsPrefix` (`:52`) | `BootSweep`; no new config key, port method, label or event |

## Implementation plan

1. **Prove first (fails on current main).** In `internal/runtime/containerd/`, add two test fakes: `recordingImages`
   (an `images.Store` whose `List`/`Get` serve a per-namespace map and whose `Delete` records the name and the
   applied `images.DeleteOptions.Synchronous`) and `recordingNamespaces` (`List` plus a recording `Delete` with an
   optional error). Add a second constructor `newBootFixture(t, imgs, nss)` that builds the `newCloseFixture` fixture
   but passes both fakes to `fakeClient` as its extra options in place of `listedNamespaces`; extras follow
   `fakeClient`'s defaults (`snapshotter_linux_test.go:100-110`), so `recordingImages` replaces `oneImage`.
   `newCloseFixture` keeps its signature. Add `BootSweep` as a pure delegate to `SweepAll`, switch `bootSweeper` and
   `main.go` to it with the new warn message, and adapt the kept ADR-0167 Decision 8 hook test
   `TestContainerdModeSweepsLeftoversAtBoot` (`cmd/funcd/crashrecovery_test.go:280-311`): `sweptRuntime` implements
   `BootSweep` instead of `SweepAll`, and its log assertion expects the new message. Write
   `TestScenarioContainerdImagesClearedAtBoot` and run it on Linux (the `golang:1.26.4` container command from #729):
   it must fail with no `images.Delete` and no `namespaces.Delete` call. Paste that output in the PR.
2. Implement `BootSweep` per Decision 2-5.
3. One test per scenario, in `bootsweep_linux_test.go`:
   - `TestScenarioContainerdImagesClearedAtBoot` — private; the image Delete is synchronous, `funcd-x` is deleted,
     `<bootRoot>/funcd-x` is gone, `other` is untouched.
   - `TestScenarioOverrideChangeLeavesOneImage` — `<ref-v1>` deleted; a following `Create` with `<ref-v2>` starts one
     pull (the `pullDriver` lease counter, `snapshotter_linux_test.go:260-270`).
   - `TestScenarioCuratedImageFromRunningBinary` — the curated record is deleted; `GetImage` in `funcd-default` then
     returns NotFound, which leads `resolveImage` to the embedded import (`containerd_linux.go:1017-1022`).
   - `TestScenarioExternalContainerdKeepsImages` — `Private: false`; container gone, no image or namespace Delete.
   - `TestScenarioNamespaceNotEmptyRetriedNextBoot` — namespace Delete returns `errdefs.ErrFailedPrecondition`;
     `BootSweep` returns an error that wraps it, the images are deleted, `<bootRoot>/funcd-x` is kept.
   - `TestScenarioShutdownKeepsImages` — private `Close`; containers gone, no image or namespace Delete.
   - Unit: `TestBootSweep_ContainerSweepErrorDeletesNoImage` — a container that `discard` cannot delete; no image
     Delete.
   `TestScenarioContainerdLeftoverRemovedAtBoot` and the `TestIssue706_*` tests stay as they are;
   `TestContainerdModeSweepsLeftoversAtBoot` stays, adapted in step 1.
4. Run the package tests with `-race` on Linux and `scripts/agent/d go test -race ./cmd/funcd/ -run
   TestContainerdModeSweepsLeftoversAtBoot`, plus `go vet` and `golangci-lint` with `GOOS=linux` on
   `./cmd/funcd/ ./internal/runtime/containerd/`; the repo-wide build runs once per PR in `scripts/agent/gate.sh`.
5. Once, on a real containerd (a Lima lane VM): deploy a Function in Namespace `x`, delete it and the Namespace,
   restart funcd, then `ctr -a <private socket> namespaces ls` shows no `funcd-x`. Record the result, the boot sweep's
   duration and the first `Create` time after the restart in the PR.

**Definition of done**: the step-1 failure is in the PR; every test above passes with `-race`; the Lima check is
recorded; the repo-wide checks run once per PR in `scripts/agent/gate.sh`.

## Review checklist

- [ ] `bootSweeper` declares `BootSweep`, and `main.go` calls it; `Close` still calls `SweepAll` only.
- [ ] `TestContainerdModeSweepsLeftoversAtBoot` is kept: `sweptRuntime` implements `BootSweep` and the log assertion
      expects the new warn message.
- [ ] `BootSweep` returns early on a `SweepAll` error and on `!d.cfg.Private`.
- [ ] Every image Delete passes `images.SynchronousDelete()`; the namespace Delete follows only when every image
      Delete succeeded; the boot-dir parent goes only after the namespace Delete succeeded.
- [ ] Only names with the `funcd-` prefix are touched.
- [ ] A sweep error never fails startup (warn log only).
- [ ] One test per scenario, named as above, plus the container-sweep-error unit test; the step-1 failure is shown.
- [ ] No new config key, port method, label or dependency; no absolute path or username in the diff.

## Consequences

- **Positive**: the private containerd's data root holds at most one funcd run's images; an old override ref is gone
  after the next restart; an upgraded binary runs its own embedded image (the out-of-scope upgrade defect in #729
  is fixed on the private containerd).
- **Negative**: each restart pays one import and unpack per (namespace, runtime) at the first cold start, and startup
  waits before `funcd.New` for one synchronous GC per deleted image, about namespaces × runtimes (neither measured;
  step 5); override Functions need their registry after a restart and keep retrying while it is down; a hand-loaded
  image in a private `funcd-<ns>` namespace is lost at restart.
- **Risks accepted**: a namespace with a leftover lease stays until the lease expires and a later boot deletes it; an
  external containerd still leaks until a follow-up ADR.

## Open questions

- Label funcd-imported images (`WithImageLabels`/`WithPullLabels`) so an external containerd can be cleaned too —
  a future ADR, when an operator on an external containerd needs it.
- The boot sweep's duration and the re-import cost at the first cold start — measured in the implementation PR
  (step 5). If the sweep is slow, only the last image delete per namespace needs to be synchronous, since containerd's
  GC is global.

## References

- Issue [#729](https://github.com/pyvvo/funcd/issues/729); parent [#735](https://github.com/pyvvo/funcd/issues/735);
  related closed #456 and #493.
- containerd v2.3.1: `core/images/image.go` (`SynchronousDelete`, `Store.Delete`), `pkg/namespaces/store.go`
  (`Store.Delete`), `core/metadata/namespaces.go:135` (namespace delete preconditions).
- ADR-0054, ADR-0055, ADR-0149, ADR-0160, ADR-0167, ADR-0170.
