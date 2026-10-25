## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0288 implementation, model: claude-opus-5-5)

This review covers b1c5649a on `feat/adr-0288-reclaim-quarantine` (d2f34632, 9e693a27, 45b5d540 and b1c5649a on top of
the docs commit 8409a372) against ADR-0288's Decisions, Contracts, Scenarios, Review checklist and done-when line. The
checks below ran in a worktree detached at b1c5649a. The gate, the e2e suite and the lanes run once on the batch branch
and did not run here.

### Checks run
- `go build ./...`: ok.
- `go vet` and `GOOS=linux go vet` on `cmd/funcd`, `cmd/funcdctl`, `internal/blob/...`, `internal/controlplane`,
  `internal/function`, `internal/platform/{config,hold,quarantine}`, `internal/provider`, `internal/services/{kv,sql}`,
  `internal/testkit/s3stub`, `internal/volume`, `pkg/funcd` and `pkg/sdk`: ok; `go vet -tags dev` on `cmd/funcdctl` and
  `internal/sql/devengine`: ok.
- `golangci-lint run` on the same packages, plain and with `GOOS=linux`, and with `--build-tags dev` on the two dev
  packages: 0 issues each.
- `go test -race -count=1` on the same packages: 16 ok; `-race -tags dev` on `cmd/funcdctl` and
  `internal/sql/devengine`: ok.
- `scripts/specs.py check docs/design/storage/quarantine.fizz`: pass, 2,028 states; the seven mutants fail as the ADR
  names them; the diagram is current.
- Two mutants, run with `go test -overlay`: a sweep without its hold check (`pkg/funcd/quarantine.go`,
  `sweepQuarantine`) fails `TestScenarioQuarantineExpires`; an `Entry.Expired` without its `Kept` guard
  (`internal/platform/quarantine/quarantine.go:67`) fails `TestExpiredAndUntil`, the KV store's `TestQuarantineExpire`
  and `TestVolumeQuarantineExpire` (no scenario test sees it; the unit tests do).

### Minor
- **A SQL entry's since comes from the system clock** (`model`): `ReclaimOrphans` dates its move with `time.Now()`
  (`internal/services/sql/reconcile.go:449`), while the start passes the KV and Bucket moves the quarantine's clock
  (`now := p.cfg.quarantineNow()`, `pkg/funcd/funcd.go`, the unheld branch of `Run`) and the volume move uses the
  function reconciler's clock (`internal/function/quarantine.go:173`). In production all are the system clock, so the
  behavior holds. But the clock seam does not reach SQL entries: a SQL expiry test on the manual clock cannot be
  written, which is why `TestScenarioQuarantineExpires` covers KV and Buckets only. Pass `now` into
  `ReclaimOrphans`, as `ReclaimDeleted(ctx, now)` takes it.

### Routed (not this work's)
- **The ADR's `Store.Move` and `Returner.Return` take a `v1.ObjectRef`, which cannot name a volume** · home: here, as
  an `adr` gap · destination: none needed now. The code's `quarantine.Ref` (an `ObjectRef` plus `Volume`) is the
  smallest type that implements the ADR's own volume entries. The deviation is the ADR's, not the builder's, and it is
  not scored.

### ✅ Verified correct (keep)
- **Decision 1:** the unheld start calls the four reclaims, each moving instead of deleting
  (`reclaimDeletedBuckets` → `bucketQuarantine.Move`, `ReclaimDeleted`, `ReclaimOrphans`, `ReclaimVolumes`); the held
  branch calls none of them but `ReclaimVolumes`' booking, which returns before moving anything while held. The old
  purge path (`bucketPurger` at start) is gone. EmptyDirs are still released (`id.Worker != ""` is refused by the
  volume move and released by the reclaim).
- **Decision 2's layouts and hiding:** KV under `.quarantine/<ns>/<store>/<since>/`, Buckets under
  `quarantine/bucket/<ns>/<bucket>/<since>/` outside `s3Root`, SQL and volumes under `<root>/.quarantine/…`; `List`
  reads a missing state as `kept` (`bucketQuarantine.state`), and the ms stamp is `quarantine.Stamp`.
- **Decision 3, a cut move:** each copy move writes `moving` before its first copy and `kept` last; a start resumes a
  `moving` entry of the orphan's name (`Move` reuses its since); `settle` writes `kept` each `moving` entry whose source
  is empty or whose object exists again. `TestScenarioCrashDuringMove` refuses the second copy and shows the next start
  resuming the same entry, with every object kept.
- **The builder's departures hold:**
  - writing `moving`/`kept` around the file store's rename too gives both backends one path; with `settle`, a crash
    after the rename and before `kept` still ends `kept`, so the behavior is Decision 2's;
  - the guards split as Decision 6 needs them: the object's existence in `ReturnQuarantined`, the emptiness in each
    Returner, which knows its store;
  - `fileMovePrefix` replaces an empty destination (a re-created Bucket's) and refuses one with files (Conflict); it
    renames, then syncs both parents;
  - the S3 stub's copy of an object's latest version without a `versionId` matches S3.
- **Decision 5, the sweep:** first run five minutes after start, then hourly, in its own goroutine after the reclaims;
  it returns while held, and `Expired` deletes only `kept` entries past their cutoff.
- **Decision 6, the way back:** the route `POST /apis/funcd.io/v1alpha1/quarantine/return` authorizes update on
  WorkerNode, as `hold release` does (`internal/controlplane/hold.go:385-388`); a missing object is Conflict, a missing
  entry NotFound (`quarantine.Pick`), a full destination Conflict; a `returning` entry resumes. The volume return
  reserves with the reconciler's own deletion default (`internal/function/quarantine.go:128`, as `volumes.go:176`),
  stops the workers, then loads the entry with `Untar`. `TestScenarioQuarantineReturned` and
  `TestScenarioQuarantineReturnedVolume` cover the guards.
- **Decision 4, the report and logs:** `hold status` lists every store's entries with since, until and state; each
  move logs Warn `moved the data of a <kind> with no object into the quarantine` with namespace, name and until, and the
  sweep logs Info `deleted quarantined data`.
- **Contracts and config:** `storage.quarantineRetention` (default 168h, at least 1h), `blob.PrefixMover` on the
  gocloud driver for file and S3, the `quarantine` package's `Entry`, `State`, `Stamp`, `Store` and `Returner`.
- **Tracking:** the ADR's substance is unchanged since its acceptance commit (only the status line moved); F203 is at
  `reviewing`; the runbook names the release step and the way back.
- **Tests:** the seven scenario tests pass and carry the oracle markers of the five assertions; the per-driver unit
  tests cover `Move`, `List`, `Expire` and `Return`.

### Definition of done
7 of 7: the six Review checklist items and the done-when line (the touched packages' tests under `-race`, vet, lint
with `-tags dev` for `cmd/funcdctl`, and `just check-specs`).

### Recommendation
Stamp ADR-0288 `Implemented` and F203 `implemented`. The Minor is a one-line change that the stamp need not wait for.
