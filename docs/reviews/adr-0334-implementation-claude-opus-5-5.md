## Verdict: changes requested — 0 blockers, 1 major, 5 minors  (ADR-0334 implementation, model: claude-opus-5-5)

Reviewed at `7d7ddeaa` of `feat/addons`: the implementation `b8758e10`, its merge `fe1845cb` (dev's runtime embeds the
backup package's `workload.AddonFiles`), the nerdctl volume mount `bede185b`, and the status bump `9e1c0a79`. The
drivers work: every Docker-backed test passes on colima's Docker, and the nerdctl driver works on a real nerdctl v2.3.1.
One gap is placed here: two scenarios have no test under `funcdctl dev`, which the ADR's test plan asks for.

### 🟡 Major 1 — `dev-addon-deletion` and `dev-addon-backup` have no test under dev  ·  attribution: model

The Implementation plan says: "Tests: each Scenario named after it, the add-on ones in `cmd/funcdctl` against Docker
when a socket answers". Seven of the nine scenarios meet it: six in `cmd/funcdctl/dev_addon_test.go`, and
`dev-admin-token-loopback-only` in `pkg/funcd` and `cmd/funcd`. These two do not:

- `TestScenarioDevAddonDeletion` and `TestScenarioDevAddonBackup` live in `internal/provider/docker/docker_test.go:355`
  and `:413`, and their comments call them "the runtime's half". They call `Reclaim`, `Quarantine().Move`, `Newest`,
  `Put` and `Clear` on the driver directly. Nothing in funcd runs the reconciler's deletion path, the backup member or
  the in-place Restore through dev's wiring: `pkg/funcd/addon_provider.go:99-100` takes the runtime's
  `workload.AddonFiles` and `provider.Volumes` with silent type assertions, so a wrapper that hid those methods would
  turn off reclaim, quarantine and backups under dev, and no funcd test would fail.
- The full flow runs in pyvvo/funcd-addons' `e2e/run.py`. Its last run, at this commit, passed 31 of 31 checks (its
  `work/results.json`), the F259 Backup and in-place Restore of rauthy under dev on Docker among them. That covers
  `dev-addon-backup` in another repository: funcd's suite, its gate and its CI never run it. For deletion it covers
  nothing: the only deletions are in a loop that runs when `SAMPLES` is above 1 (the default is 1), and that loop checks
  no volume.
- The tests were cheap to write. A review overlay test (not committed) ran `dev-addon-deletion` under dev on Docker in
  5.1 s: `echo` with `deletion: delete` and `--persist` left the folder, the reload's prune deleted it, its container
  and volume were gone after 604 ms, the re-added `echo` started empty, and with `retain` the volume moved into a
  quarantine entry of its own after 1.0 s. So the behavior holds; only its test is missing.

Fix (builder): add `TestScenarioDevAddonDeletion` and `TestScenarioDevAddonBackup` in `cmd/funcdctl`, against Docker,
skipped with the reason without it, as the other four Docker scenarios are. The deletion test can follow
`TestScenarioDevAddonData` (remove `echo.yaml`, then re-add it, for `delete` and `retain`). The backup test can take the
e2e's shape: a Bucket on dev's S3 gateway, a BackupSchedule naming the object, a Backup, then an in-place Restore, with
the snapshot placed by `Put`.

### Minor

- **A retain move and the quarantine sweep can interleave, and the entry then leaves the quarantine** · model ·
  `internal/provider/docker/docker.go:883-903` creates the labelled entry volume, then runs a helper that writes `state:
  moving`. Between the two, the entry has neither data nor state, so the sweep's `Expire` removes it (ADR-0288 Decision
  2). The helper's mount then makes Docker create the volume again, without labels; the copy goes into it, the object's
  volume is removed, and `Move` returns `Kept` with no error. A review overlay test forced that order on colima's
  Docker, with the sweep's `Expire` run from a wrapper Engine right after the entry's `CreateVolume`: `Move` returned no
  error, `Quarantine().List` returned nothing, the key had no volume left, and the data sat in a volume whose `Labels`
  are `null`, which `List`, the reap and the sweep never find. On the server the move is one rename, so no such gap
  exists there, and quarantine.fizz models moves only at a start, when the sweep never runs. Under dev, ADR-0333 moves
  at runtime, beside a sweep that runs 5 min after start and then hourly (`pkg/funcd/quarantine.go:28-29`). The window
  is short, so this is rare. Fix: serialize `Move` and `Expire` in the Runtime, which one session alone holds (Decision
  6).
- **The fake nerdctl and the Engine contract still claim a check that nerdctl no longer makes** · model · `bede185b`
  mounts an engine's volume as a bind of its directory. nerdctl does not count a bind as a use of the volume, so a real
  `nerdctl volume rm` now succeeds under a running engine: on nerdctl v2.3.1, the review's run removed the volume with
  no error while the engine ran. `internal/testkit/fakenerdctl/main.go:227-228` still maps the bind back to the volume,
  `:280` refuses it as "in use", and the `Engine` doc (`docker.go:122`) still says "Conflict while a container uses it".
  No path depends on it today, since `Reclaim`, `Quarantine().Move` and the reap check or remove the container first.
  But a mutant that drops `Reclaim`'s container check survives both `TestEngineThroughNerdctl` (with the fake) and
  `TestScenarioDevAddonDeletion` (on Docker, which answers 409), while on nerdctl that mutant deletes a running engine's
  data. Fix: make the fake answer as nerdctl does, and say in the `Engine` doc that nerdctl does not refuse.
- **The containerd follow-through of the test plan is not all done** · model · The plan asks for `dev-addon-docker`,
  `dev-addon-data` and `dev-addon-backup` on colima's containerd runtime, run by hand and recorded. `bede185b`'s message
  records the first and the stop half of the second; it records no Backup or in-place Restore on containerd, and no
  `--persist` start. The Open question's follow-up issue for an automated containerd lane is not filed: an issue search
  for "nerdctl" finds only #1500. The review ran the runtime's half on nerdctl v2.3.1 (an overlay test, not committed):
  the engine was Ready after 3.2 s, the volume root was `10001:10001:700`, `Put`, `Newest` and `Clear` worked through
  the helper (`restore` 700, the copy 600, owned `10001:10001`), and `Reclaim` and the quarantine move and expiry also
  worked. Fix: run the two missing cases by hand under dev, record them for the PR, and file the lane issue under the
  epoch's tracker.
- **The driver repeats the API's file-name rule** · model · `docker.go:100-101` defines `fileName =
  ^[A-Za-z0-9][A-Za-z0-9._-]*$` with the comment of `v1.IsAddonFile` (`api/types/v1alpha1/addon.go:323-328`), which the
  server's `EngineFiles` calls. Fix: call `v1.IsAddonFile`.
- **A changed security row cites tests that never use the dev admin token** · model · `docs/behavior/security.md`, the
  row "`funcd.New` with `DevToken` or `DevAdminToken` off loopback", cites `TestScenarioLibraryFollowsTheRule` and
  `TestScenarioEdgeAuthFollowsTheRule`. Neither test uses `DevAdminToken`; `TestScenarioDevAdminTokenLoopbackOnly`
  (`pkg/funcd/credentials_test.go`) proves the added half. Fix: cite it there.

### Routed (not this work's)

- **Decision 3 names nerdctl's `volume-nocopy`, which no nerdctl release accepts** · `adr` · home: elsewhere,
  destination: the decider. nerdctl v2.3.1's `ProcessFlagMount` (`pkg/mountutil/mountutil_linux.go`) has no such key:
  the bare form fails "invalid field 'volume-nocopy' must be a key=value pair", which is the error `TestLastWords`
  records, and `volume-nocopy=true` fails as an unexpected key. nerdctl's main branch has it, and so does the command
  reference the ADR read, but no release up to v2.3.1, the one colima installs. The work keeps the Decision's rule:
  `bede185b` binds the directory that `nerdctl volume inspect` names, and a bind never copies the image's files up. On
  v2.3.1 the root stayed `10001:10001:700`. Nothing changes here; the decider can record the mechanism in the ADR of the
  containerd lane, or in a partial supersession of the parenthesis.
- **The Design model's reason missed the sweep** · `adr` · home: elsewhere, destination: the decider. "One session alone
  holds its key, so no two actors share a container or a volume" holds between sessions, but within one session the
  reconciler's retain move and the quarantine sweep are two actors on the same entry volume (the race above). If the
  Runtime serializes them, no new decision is needed.

### ✅ Verified correct (keep it)

- **Item 2, `RuntimeUnavailable` with no retry**: conforming. The reconciler holds the object `Pending` with the
  runtime's error and `RequeueAfter` 0 (`internal/services/addon/reconcile.go:92-95`). This is ADR-0149 Decision 5's
  rule for availability fixed at start ("those gates set no requeue"): dev chooses its runtime once, and its message
  says "start one, then restart funcdctl dev". Only `devAddonsUnavailable` returns that reason with an error, so no
  server path changes. `TestRuntimeUnavailableNamesWhy` pins the reason, the text and the zero requeue, and
  `TestScenarioDevAddonRuntimeChoice` shows the message under dev naming `/var/run/docker.sock` and `colima nerdctl
  install`.
- **Item 4, the dev admin token and the partial supersession**: conforming. ADR-0171 and ADR-0314 carry `Superseded in
  part by: ADR-0334` lines (added at acceptance, `955563f3`). Each superseded refusal covers the new token: ADR-0171
  Decision 3 in `cmd/funcd/credentials.go` (a token file), ADR-0171 Decision 7 in `checkCredential`, ADR-0314 Decision 2
  as an `auth.token` refusal on any address (ADR-0334 Decision 5's own rule), and ADR-0314 Decisions 5 and 6 in
  `checkDevTokenBind`, for the control plane and, with `WithEdgeAuth`, the edge. The wrap comes after every option, so
  "whichever option built the store" holds. The compare is constant-time, nothing type-asserts the store, and no error
  holds a token. Mutant M1, which drops `DevAdminToken` from `checkDevTokenBind`, fails `refused/control-plane` and
  `refused/edge`.
- **Decisions 1, 2, 6 and 7 on real Docker**: `TestSocketOrder` (the six places in order, the context's endpoint, a
  `tcp://` host named, the first that answers wins); `TestEngineContainer` (restart policy `no`, `SIGTERM`,
  `StopTimeout` 10, no `User`, `127.0.0.1`, `NoCopy`, root `10001:10001:700`, replacement on a new spec hash, SIGTERM in
  the engine's kill events); `TestReap` (helpers reaped, `--persist` keeps the volume, another key's engine untouched).
  Mutant M3, the reap removing volumes under `--persist`, fails `TestReap`.
- **The six dev-level scenario tests** pass on colima's Docker, none skipped: `TestScenarioDevAddonRuntimeChoice` 1.2 s,
  `TestScenarioDevAddonRegister` 2.6 s (App mode included; 403 `may not act on cluster-scoped kind` with the developer
  token), `TestScenarioDevAddonDocker` 12.2 s (rauthy Ready, `/auth/v1/ping` 200 through the edge),
  `TestScenarioDevAddonData` 7.4 s, `TestScenarioDevAddonCrashReaped` 12.6 s (both modes),
  `TestScenarioDevAddonTwoProjects` 3.6 s; so does `TestDevPruneDeletesObjectsBeforeTheirAddon`.
  `TestScenarioDevAdminTokenLoopbackOnly` passes in `pkg/funcd` and `cmd/funcd` with no Docker.
- **The nerdctl driver**: the engine's environment on standard input (`--env-file /dev/stdin`), `--restart no`,
  `--stop-signal SIGTERM`, `--stop-timeout 10`, `127.0.0.1::<port>`, `--pull missing` with the developer's own login,
  and the helper's `tar`, `stat` and `rm` (with the fake, and on nerdctl v2.3.1 as above).
- **Decision 5's dev wiring**: Addons first with the admin client (`applyOrdered`); the folder reader keeps an Addon
  with no namespace or group but still tags it; both prunes delete add-on objects before their Addon with the admin
  client; any registry, still pinned by digest (`TestAddonRulesAnyRegistry`); the runtime is built after `process.Open`,
  and `StopAll` runs in the cleanups after the platform's `Run` returned.
- **Tree, contracts and conventions**: the files are the ones the plan names, plus the fake nerdctl it implies and the
  three small changes the Decisions need (`AnyRegistry`, the reconciler's hold, the behavior chapters). The signatures
  match the Contracts. There is no new dependency, `go.mod` is unchanged, and the work adds no stub, `panic` or log
  package other than slog. Errors are `fault` errors. The ADR changed only in its status line (`9e1c0a79`), and the F260
  row is at `reviewing`.
- **Checks**, all exit 0, through `scripts/agent/d`: `go build` and `go vet` of the touched packages (and of
  `cmd/funcdctl` with `-tags dev`); `go tool golangci-lint run` on them, 0 issues (`--build-tags dev` for
  `cmd/funcdctl`); `go test -race -count=1` of `cmd/funcd` (84 s), `pkg/funcd` (146 s), `cmd/funcdctl` with `-tags dev`
  (250 s), and `internal/controlplane`, `internal/provider/docker`, `internal/provider/nerdctl`,
  `internal/services/addon` and `scripts/behavior`; `just check-behavior`; `just check-specs` (21 specs, 0 failing);
  `just check-hygiene`. No container or volume was left behind on either engine.

### Definition of Done

16 / 19 items hold: the six Review-checklist items, `just check-behavior`, the Scenarios on colima's Docker, `just ci`
(pending the final gate; every touched package is green), and the generic items (real behavior, Contracts, tree,
conventions, dependencies, scope, tracking). Misses, all model: each Scenario's test where the plan puts it (Major 1);
the containerd runs recorded; the containerd lane's follow-up issue filed (Minor 3).

### Model scorecard

Recorded by the integrator: claude-opus-5-5 on ADR-0334 (implementation) → changes-requested, 0/1/5, 6 model-attributed,
DoD 16/19. See docs/reviews/model-scorecard.md.

### Recommendation

The builder adds the two dev-level scenario tests in `cmd/funcdctl`, serializes the quarantine's `Move` and `Expire`,
makes the fake nerdctl answer as nerdctl does, runs the two missing containerd cases by hand and files the lane issue;
the rest are one-line fixes. The two routed `adr` notes go to the decider and block nothing. Stamp nothing until the
rework passes; the PR then closes #1500.
