## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0324 implementation, model: claude-opus-5-5)

Reviewed `git diff origin/main...HEAD` on branch `docs/adr-0324-funcd-funcdctl-spec`, rebased on origin/main after #1445
merged. The repo-wide gate (`just ci`, the e2e suite, `scripts/agent/gate.sh`) was not run here: it runs once on the
final branch after this review (CLAUDE.md, "Each check runs once, in one place").

### Checks run

| Command (through `scripts/agent/d`) | Exit |
| --- | --- |
| `go test -race -count=1` on `internal/reference`, `internal/testkit/funcdenv`, `internal/platform/config`, `cmd/funcd`, `cmd/funcdctl`, `scripts`, `scripts/behavior` | 0 |
| `go test -race -count=1 -tags dev ./cmd/funcdctl/` (201 s) | 0 |
| `go vet` on the same packages; `go vet -tags dev ./cmd/funcdctl/` | 0; 0 |
| `go tool golangci-lint run` on the same packages; `--build-tags dev ./cmd/funcdctl/` | 0 (0 issues); 0 (0 issues) |
| `just generate`, then `just generate` again | 0, 0; no tracked file changed either time |
| `just generate` with `FUNCD_SERVER`, `FUNCD_TOKEN`, `FUNCD_DATA_PLANE`, `FUNCD_LISTEN_ADDR` and `FUNCD_STORAGE_MODE` set | 0; no tracked file changed; neither page holds any of the values |
| `scripts/check-generated.sh` | 0 |
| `scripts/check-undescribed.sh origin/main` | 0 |
| `just check-behavior` | 0 |
| `funcd --help` and `funcdctl-dev --help` (built from the branch) against the pages' `##` sections | every listed command has a section; the funcdctl top-level lists are identical |

### Minor

- **The sentinel variables cover each test's own CLI, not both** · attribution: model · Decision 3 says the test sets
  "every `FUNCD_*` variable that either CLI or `config.Config`'s `env` tags name". `reference.Variables(".")` in
  `cmd/funcd/spec_page_test.go:38` and `cmd/funcdctl/spec_page_test.go:37` scans only the test's own directory. A
  variable that only the other CLI names, or that a helper package reads, gets no sentinel. Today no tree reads one
  (the funcdctl page is identical with the four #1445 variables set). Fix: pass both CLI directories,
  `reference.Variables("../funcd", "../funcdctl")`.
- **Surface beyond the Contracts and the plan** · attribution: model · `reference.DevAnnotation` and
  `reference.Variables` (`internal/reference/commands.go`) are exported but not in the Contracts, and the package
  `internal/testkit/funcdenv` is named in neither the Contracts nor the Implementation plan. Each serves Decision 3
  (the annotation key, the variable list, clearing and seeding the environment), and the `internal/reference` skip in
  `scripts/behavior/main.go` keeps a docs-only package out of the behavior scan. No change asked; noted so the
  surface is known.

### Routed (not this work's)

None.

### ✅ Verified correct (keep it)

- **Tracking**: the only ADR edit since acceptance is the `Accepted → Reviewing` status line; the F02 entry read
  `reviewing`. The substance is unchanged.
- **Contracts**: `Marker`, `ConfigKey`, `ConfigKeys`, `Command`, `Flag`, `Commands`, `ConfigTable`, `CommandTables`,
  `Page` and `Stale` match the ADR's signatures. `Commands` adds cobra's `help` and `completion` commands, skips
  hidden commands, lists the root's persistent flags once (through `LocalFlags`), and refuses an ADR number in a text
  or a usage, a set `FUNCD_*` value in a default, and a default part equal to `runtime.GOOS` or `runtime.GOARCH`.
- **Scenarios**, each a named, passing test: `config-spec-generated` (`TestScenarioConfigSpecGenerated`,
  `server.listenAddr` with type, default `127.0.0.1:8080`, `FUNCD_LISTEN_ADDR` and its description);
  `command-spec-generated` (`TestScenarioCommandSpecGenerated`, `funcdctl apply`, `funcdctl dev` marked
  "funcdctl-dev only"); `page-same-on-every-host` (`TestScenarioPageSameOnEveryHost` in both main packages);
  `host-default-refused`, `undescribed-config-key-refused`, `stale-command-spec-fails` in
  `internal/reference/reference_test.go`; plus unit tests of the limit mapping, a `path[].key` row, a map row and an
  ADR number in a help text, and the config-list case of `TestScenarioUndescribedListNeverGrows`.
- **Host independence**: both page tests clear `FUNCD_*` before rendering, so a value in the developer's shell
  neither reaches a page nor trips the walk. `config.Defaults()` reads nothing from the host.
- **The config table**: 141 undescribed keys listed, the 34 `server` keys described in plain user words with no ADR
  number; the descriptions checked against the code (the loopback rule in `cmd/funcd/credentials.go`, the egress
  gateway port in `internal/network/network.go`, the forwarder-port-0 rule in `cmd/funcd/main.go`) hold. Lists read
  `auth.credentials[].role`; maps show their `k1=v1,k2=v2` variable form.
- **The command tables**: every help text that named an ADR is rewritten (22 references on 21 lines); the four #1445 defaults are empty on the
  page with the rule in the usage; the `build: dev` annotation is a literal in `cmd/funcdctl/dev.go`, so neither
  binary imports `internal/reference` and neither gains a command or a dependency (`go.mod` untouched).
- **Generation and checks**: `just generate` runs the two `-update` tests after specgen; `just ci` already depends on
  `generate`, and `check-generated.sh`'s glob covers both pages; `check-undescribed.sh` guards both lists.
- **Pages**: both carry `reference.Marker`, sit beside a hand-written `index.md` in folders that `meta.json` lists;
  MDX-sensitive characters are escaped in table cells and none lands inside a code span. The Usage page's YAML
  example is held to `config.Parse` by `TestUsagePageConfigParses`.
- **Agent rule**: `.claude/rules/config-and-cli.md`, scoped to the three paths of Decision 5.

### Definition of Done

11 / 11 items hold (the ADR's 7 Review-checklist items and 4 Definition-of-done items). The `just ci` item is covered
here by its sub-checks on the touched packages and is run once by the final gate on the branch.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0324 (implementation) → pass, 0/0/2, 2 model-attributed, DoD 11/11. See
docs/reviews/model-scorecard.md.

### Recommendation

Pass: the ADR moves to `Implemented` and the F02 entry to `implemented`. The two Minors can be folded before the
final gate or left; neither blocks. ADR-0324 has no feature issue, so the PR closes none.
