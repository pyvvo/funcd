## Verdict: changes requested — 0 blockers, 1 major (ADR-0237 implementation, model: claude-opus-5-5)

The work is complete and behaves as the ADR decides. One formatting defect makes the gate fail once the change is
committed.

### 🟡 Major 1 — `cmd/funcdctl/dev.go` is not gofmt-clean · attribution: model

`gofmt -l cmd/funcdctl/dev.go` lists the file. The two new imports `sqldevengine` and `sqlembedengine` sit between
`internal/catalog/devengine` and `internal/catalog/embedengine` instead of in sorted order, and the comment on the new
`databases` map in `synthesizeResources` is misaligned by one space. `dev.go` is a `-tags dev` file, but `go fmt ./...`
formats ignored files too (`go list -f '{{.IgnoredGoFiles}}' ./cmd/funcdctl` lists it), so `just ci` rewrites it and
then fails its `git diff -- '*.go'` check. golangci-lint with `--build-tags dev` reports 0 issues, so only the fmt step
catches it. Fix: `just fmt` (or `gofmt -w cmd/funcdctl/dev.go`) before the commit.

### Minor

- **The ADR names a `dev-run` recipe that does not exist** · attribution: adr. The engine build went into
  `dev-example`, which builds `funcdctl -tags dev` for a project and now builds the engine once when a
  `funcdctl.yaml` declares `sql:`. That is the right place; the ADR's name is wrong.
- **linux/amd64 engine not built during implementation** (Implementation plan 3) · attribution: env. The Docker
  builder stage crashed under qemu emulation on the arm64 host ("found pointer to free object" inside Go). linux/arm64
  built through the same stage, so the stage itself is sound; the release workflow's matrix covers linux/amd64.

### ✅ Verified correct (keep it)

- **Scenarios**, `go test -tags dev -race -count=1` on `./cmd/funcdctl/` with the real darwin/arm64 engine embedded:
  `TestScenarioDevFunctionBindsDatabase` PASS (4.1 s; the database is Ready, owned by the function, and a write plus a
  read work), `TestScenarioDevDataPersistsWithPersist` PASS (16.7 s; four dev starts), `TestScenarioDevCrashedEngineReaped`
  PASS (6.0 s), `TestScenarioDevWorkflowStepBindsDatabase` PASS (4.6 s; step `a` writes as a granted writer, step `b`
  as owner reads). `TestScenarioDevEngineUnavailable` skips with a real engine and PASSES with a placeholder swapped in
  (the error is `fault.Unavailable` and names `just build-sql-engine`); the real engine was restored byte-for-byte
  (sha256 compared). The darwin/arm64 run of a bound function's query that the Definition of done asks for is this run.
- The whole package once: `go test -tags dev -race -count=1 ./cmd/funcdctl/` → `ok` (63 s), exit 0.
- `./internal/sql/...` and `./internal/catalog/...` with `-tags dev -race`: all `ok`; the catalog dev tests pass
  unchanged. `TestPlaceholderExtractsAndIsNotBundled` passes on the placeholder.
- `go vet -tags dev` exit 0; `golangci-lint run --build-tags dev` on the touched packages: 0 issues;
  `just check-hygiene`: clean, exit 0.
- **Driver** (`internal/sql/devengine/devengine.go`): HTTP and Raft addresses on `127.0.0.1` only; arguments and auth
  file from `internal/services/sql/engineargs`, the builder `cmd/funcd-rqlite` uses; auth file at 0600 in a per-start
  temporary directory; its own process group, SIGTERM then SIGKILL after a grace; `procreg` registry with a token in
  the engine directory's name, reaped at `New` (ADR-0167); fresh ports each start, which the implementer's manual
  restart check supports; the storage spec's uid/gid ignored; temporary data root removed by `StopAll`.
- **Unit tests with a fake engine** (the test binary serving `/readyz`): lifecycle, restart of an exited engine,
  removal of the temporary root, and the reap of a killed run's engine with its data kept.
- **Binding and synthesis** (`cmd/funcdctl/dev.go`): `Bindings.SQL` in `pkg/sdk/manifest.go`; one `SQLDatabase` per
  named database owned by the last binder, a `SQL Data Writer` RolesAssignment scoped per database for every other
  binder; `fn.Spec.SQL` set; `KindSQLDatabase` in `pruneStale`'s kinds; `<persist-to>/sql` under `--persist`; the
  start check before the platform boots (Decision 4).
- **Binary and build**: `embedengine` under `-tags dev` only, so the thin client embeds nothing; the placeholder
  threshold of 4 KiB matches the `check-hygiene` staged-size check; `scripts/build-sql-engine.sh` reads
  `rqlite.pin`, checks the commit, builds linux through the new `engine` Docker stage and darwin natively with
  `clang -arch`; `build-sql-engine`, `sql-engine-pin`/`unpin` recipes; `_dev-packages` extended so `just ci` lints and
  tests the new packages with the tag; the release workflow's `build-sql-engine` job on `macos-latest`, mapped to the
  host by `.actrc`, with actions pinned by SHA.
- **Tracking**: the ADR's only change is the `Accepted → Reviewing` status line; the F133 row reads `reviewing`.

### Definition of Done

10 / 11 hold (Review checklist 5/5; ADR Definition of done 5/6). Miss: "the gate passes" — `just ci` would fail on
the fmt check (Major 1, model).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0237 (implementation) → changes-requested, 0/1/2, 1 model-attributed, DoD 10/11.
See docs/reviews/model-scorecard.md.

### Recommendation

Run `just fmt` on `cmd/funcdctl/dev.go` and re-review; nothing else blocks sign-off. A later ADR touching dev
recipes can correct the `dev-run` name.
