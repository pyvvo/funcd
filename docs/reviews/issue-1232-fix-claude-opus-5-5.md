## Verdict: pass — 0 blockers, 0 majors, 0 minors  (pyvvo/funcd issue #1232 fix, model: claude-opus-5-5)

Branch `fix/1232-daemon-stop-order`, commit e965f989 `fix(funcd): a graceful daemon stop keeps the S3 gateway, egress
and DNS until the workers have stopped`, on `origin/main` cbc3f2ba (`git diff origin/main...HEAD`: `pkg/funcd/funcd.go`,
`pkg/funcd/workerservices.go`, `pkg/funcd/funcd_test.go`, `e2e/duckdb.venom.yml`). Issue: "A graceful daemon stop
closes the S3 gateway and egress before the workers stop". The review ran in the session that made the fix, as the
decider asked on 2026-10-11 (no review agents); every claim below comes from a command that ran.

### Routed (not this work's)
- **A daemon stop gives an engine 3 s, while a Lake stop drains for up to 5 s** · destination: ADR-0277 (#1234) ·
  evidence: `closeGrace` (`internal/runtime/containerd/containerd_linux.go:54`) and `closeTimeout`
  (`pkg/funcd/funcd.go:118`) are unchanged by this fix, as the ADR's author asked. With the fix, a kill during the
  drain loses at most one save interval (ADR-0273).
- **A failed catalog save leaves no trace** · destination: ADR-0276 (#1233) · evidence: the lane shows the effect of a
  save, not the save itself; the engine's output stays the worker's tail (ADR-0168).

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** `origin/main`'s `pkg/funcd/funcd.go`
  overlaid: `TestIssue1232_DaemonStopKeepsWorkerServicesUntilTheWorkersStop` fails at "the S3 gateway stopped serving
  before the runtime stopped the workers" (`connect: connection refused` while the runtime closes). With the fix it
  passes under `-race`.
- **Mutants.** Six overlays, each fails the test: the three services back on Run's context (the S3 assertion); only
  the DNS forwarder back on it ("the DNS forwarder stopped before the runtime stopped the workers"); only the egress
  gateway back on it ("the egress gateway stopped …"); `s3gw.Close` back before the runtime (the S3 assertion); the
  egress gateway's `Close` before the services' stop ("Shutdown closed the egress gateway before its Serve
  returned"); and a stop that does not wait (the same assertion, 5 of 5 runs).
- **The behavior is fixed on the real platform.** The duckdb lane passes 21 of 21 cases. Its new case,
  `issue1232-daemon-stop-keeps-commits`, starts the daemon with `lake.saveInterval: 1h` (the file gets its interval
  back once that daemon has read it), commits three tables through lake-reader, stops the daemon with
  `systemctl stop funcd-demo`, starts it again and reads `[[3]]`, in 12.0 s. On unfixed `origin/main` cbc3f2ba, with
  only the suite copied in, the same case fails: all 31 read-backs answer "Table with name issue1232_a does not
  exist", while the tables of the #1227 case, saved by the watcher, are still there; the other 20 cases pass.
- **The fix addresses the cause.** `Run` started the three services on its own context, which SIGTERM ends before the
  data-plane drain and before `Shutdown` closes the runtime. They now run on a context that only `Shutdown` ends,
  after the runtime has stopped the workers; `Shutdown` waits for them within its own
  bound, then closes the egress gateway and the S3 gateway, still before the blob store.
- **Every case of the issue.** The Lake case has the Go test and the lane case. The in-flight invocation case shares
  the cause: the services' context ends only in `Shutdown`, after the drain and the runtime's close, so the
  assertions made during the runtime's close also hold through the drain.
- **Siblings.** Nothing else starts these three services. The SQL change feed and the Lake PEP proxy stop at SIGTERM
  by design (the stop-path search: at-least-once delivery, consumer queries cut); the local API sockets and the KV
  store close in `Shutdown` after the drain, and no engine uses them.
- **Reuse.** `logRoutes` (`pkg/funcd/logroutes.go`) tracks capture Routes over an `io.ReadCloser`, and `errgroup`
  cancels on the first error with an unbounded `Wait`: neither fits a context that `Shutdown` ends. The new tracker
  keeps a `Shutdown` that races `Run`'s start from adding to a `WaitGroup` already being waited on. A lifecycle
  context in a field is the codebase's idiom (`internal/activator/activator.go`, `internal/blob/s3gateway/backend.go`).
- **ADRs.** ADR-0117's `Serve` runs "until ctx is cancelled or Close is called", and `Shutdown` cancels before it
  closes, so a closed listener is never logged as a failure. ADR-0033's drain now has its platform services. ADR-0028:
  `TestIssue489_RunShutsDownOnSetupError` passes, so a setup error still shuts everything down. ADR-0080/0085: the
  gateway still closes before the blob store. No ADR file was edited.
- **Conventions.** Why-comments that name #1232; ctx-first signatures; block-style YAML; the lane's mutations in the
  VM, `retry` as the wait, and its restart step copied from `owner-kind-across-daemon-restart`. Subject
  `fix(funcd):`, `Fixes #1232`, the attribution trailer, one commit.
- **Checks.** `go build ./...`; `go vet ./pkg/funcd/` on darwin and linux; `go tool golangci-lint run ./pkg/funcd/`:
  0 issues; `go test -race ./pkg/funcd/` ok; `go test -run Lane ./cmd/funcd/ ./internal/testkit/langmod/` ok.
  `scripts/agent/gate.sh` on e965f989: PASS (audit with 0 hard flags, ci-full in 680 s, the Linux build, vet and lint,
  the Linux tests, stress with the new test 20 times and 0 failed, a clean tree).

### Definition of Done
12 / 12 items hold. Item 13 does not apply: no design spec models the daemon's stop order (`docs/design` holds
function, lake and volume models).

### Model scorecard
Recorded: claude-opus-5-5 on #1232 (fix) → pass, 0/0/0, 0 model-attributed, DoD 12/12.

### Recommendation
Ready for the PR. The two routed findings are ADR-0277 and ADR-0276, which the "Disaster recovery platform
components" session drives.
