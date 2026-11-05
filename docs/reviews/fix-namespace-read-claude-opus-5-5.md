## Verdict: changes-requested — 0 blockers, 1 major, 1 minor (namespace-read, model: claude-opus-5-5)

This report reviews `056355ac` on `claude/optimistic-jang-6b6af2` against `origin/main` `5b27e74a`: one commit and two files.

A failed read of a Namespace now refuses the call with a 503 before anything is served, and only an absent Namespace reads as unset. The Route,
static and by-name paths all reach one function, which holds the data plane's only read of a Namespace. The new tests fail on the old code and under
18 of my 20 mutants, and the fix adds no read and no measurable cost. One check on a real daemon found a case this change should still cover, so the fix goes
back to `/fix`. The gate, the e2e suite, `go test ./...` and the lanes run on the PR's tip, and did not run here.

### 🔴 Blockers

None.

### 🟡 Majors

- **One check on a real daemon** · attribution: issue. A measurement on a real daemon found one case that this fix should still change. It is not a regression:
  `origin/main` does the same. The commit does what its brief rules, so the finding is recorded and not scored against the model. The change it needs
  is small. The details are with the fixer, and the re-review reads them.

### Minors

- **A phrase in the commit message** · attribution: model · `056355ac`. It says "The stance and the exposure share one read". They share one function.
  A by-name call with no stance of its own reads the Namespace twice, as it does on `origin/main` (the count is below). Fix: "share one function", in
  the PR body too.

### ✅ Verified correct (keep)

- **Checks on `056355ac`, through `scripts/agent/d`:**
  - `go build` of `internal/dataplane`, `pkg/funcd` and `cmd/funcd`, on the host and with `GOOS=linux`, and of `cmd/funcdctl` on the host: exit 0.
  - `go vet` of `internal/dataplane` with no tag, with `GOOS=linux`, and with the tags `e2e`, `integration` and `dev`: exit 0.
  - `golangci-lint` on `internal/dataplane`, on the host and with `GOOS=linux`: 0 issues.
  - `go test -race -count=1 ./internal/dataplane/`: ok. `TestNamespaceRead_*` under `-race -count=5`: 105 passes, no failure and no skip.
  - `just check-hygiene`: clean.
- **Not run, as agreed:** the gate, the e2e suite, `go test ./...` and any lane.
- **The revert check.** With `origin/main`'s `dataplane.go` overlaid, `TestNamespaceRead_FailedReadRefusesTheCall` and
  `TestNamespaceRead_RefusalNamesOnlyAnUnavailableEdge` fail in all 17 of their cases, each for the reason the fix names: it expects the 503 and gets the
  answer that the unset defaults give. `TestNamespaceRead_AbsentNamespaceIsUnset` and `TestNamespaceRead_OwnStanceNeedsNoRead` pass there, as they pin what stays.
- **The design check.** The brief has no issue, so I made this check myself. Verdict: `bug`.
  - **Absent stays unset.** ADR-0110 Decisions 2 and 6.3, ADR-0113 Decision 3 and ADR-0241 Decisions 2, 4 and 8 rule on an absent or unset value. None of
    them rules on a failed read, and `TestNamespaceRead_AbsentNamespaceIsUnset` shows that their clauses still hold.
  - **The clauses this restores.** Under ADR-0113 Decision 2, an `authenticated` stance fails closed. Under ADR-0120 Decision 4, the PEP runs with the
    resolved stance before any byte is read. ADR-0176 Decision 2 already refuses on a failed read of the same object, for the edge aggregator.
  - **Nothing accepted the fallback.** The old comment's "(or a read error)" came with the first edge commit, `9c7661fe`. No ADR, test, issue or board
    card carries it. ADR-0241's Risks accepted names one store read per call before the PEP, which this fix leaves as it was.
  - **Other ADRs on the path.** ADR-0302 Decision 3's by-name order is unchanged, and its Decision 6 holds, as the refusal is an RFC 9457 problem. It comes
    before the chain, so no worker runs and nothing wakes. ADR-0290 Decision 4 leaves `serveStatic` unchecked, and this fix does too. ADR-0164's token
    rule and ADR-0151's deadline are untouched.
- **No added read.** I counted Namespace and Function reads per path on both builds, with an overlaid probe: 15 paths, in an implicit, open namespace and
  an explicit, authenticated one, with and without a token. The 60 rows of status and read counts are identical on `origin/main` and this tip. A Route
  rule with its own stance reads no Namespace, and a by-name call reads it once or twice.
- **No measurable cost.** An in-process bench against a stub worker, four alternating rounds, against `origin/main`: by name −1.0 % on 1 CPU and −0.8 % on
  8; static −2.2 % and −1.8 %. Each is within the spread of the rounds. Allocations per call are unchanged, apart from one on 8 CPUs by name (278 and 279).
- **Mutants.**
  - 18 of my 20 fail a `TestNamespaceRead_*` test: any error reading as unset, an absent Namespace refusing, a wrong kind reading as unset, the detail naming
    the cause or the namespace, a 500 for the 503, the log line gone or missing either field, each of the Route, static and by-name paths ignoring the
    error, a stance or an exposure falling back alone, the own stance used when the Function is not served, the stance read before the own stance, and an
    internal call reading the Namespace.
  - The 19th ignores the own stance when the Function is served. `TestExposure_ByNameStance` and `TestScenarioTheByNameFormKeepsItsStance` fail on it.
  - The 20th moves the exposure read before the address check. It adds a read and changes no answer, so no test sees it. The count above shows that this
    tip has no such read.
- **The refusal.** With a store that fails the read, each path that reads a Namespace answers the same `application/problem+json` 503, of type
  `urn:funcd:problem:unavailable`. Its detail holds neither the cause nor the namespace, and the log holds both. It reaches no worker. It takes no rate token:
  with a burst of one, five refused calls were followed by a 200 and then a 429. In a held namespace it names no hold, as ADR-0290's `auth-before-hold`
  scenario wants.
- **What stays.** An absent Namespace reads as implicit and open on every path. A Route rule with its own stance and an internal call read no Namespace and
  are served through a failing read. By-name calls with odd `X-Funcd-Namespace` values, such as a slash, `..` or 70,000 bytes, answer as on `origin/main`,
  on a real Badger store.
- **Siblings.** The other reads of a Namespace are the aggregator's `ModeFunc`, whose error fails its `Set`, and the volume budget, whose error goes up. Nothing
  else reads `EdgeDefaults` or `DefaultExposure`, and `Ingress.AuthMode` has this one caller. A Site's Route compiles to static rows
  (`internal/route/reconcile.go`, `siteRules`), so it takes the static path. `serveUpstream` reads no Namespace. A failed read in `s3BucketFor` ends in a 404, and no
  stance is involved.
- **A measurement on a real daemon** passed for the case this fix was written for. The case reproduces on `origin/main` and not on this tip.
- **Scope, reuse and conventions.**
  - Two files, and each hunk serves the topic. No ADR file is edited, `go.mod` is unchanged, and no test is weakened or deleted.
  - `callOp`, `fault.Unavailablef`, `seedFnIngress`, `seedNSAuth`, `readyEndpoints`, `noScaler` and `creds` are reused.
  - `namespaceReads` is the package's first store that fails a read, and `internal/testkit` holds none. `authDoorUpstream` and `edgeDoor` build their own stores and
    cannot take it. `authReq` posts, which a static Route refuses, and `do` takes no token, so `callTeam` has a reason to exist.
  - The change keeps to `api/fault`, slog and the surrounding shape of `authStance`. No design spec models this path, so item 13 of the checklist does not apply.
- **Shape.** One `fix(dataplane):` commit with the attribution trailer. The commit message, the code comments and the test names describe behavior only.

### Fix checklist

10 of 12. Item 12 misses for the Major. Item 8 stays open until the gate and the lanes run on the PR's tip. The tests are named for their behavior,
and the commit carries no `Fixes #N`, because the fix has no public issue.

### Model scorecard

Not recorded by this report: the row goes into the ledger with the batch. It reads: claude-opus-5-5 on namespace-read (fix) → changes-requested, 0/1/1, 1 model-attributed, DoD 10/12.

### Recommendation

Back to `/fix` for the Major and the phrase in the message. Nothing under ✅ needs rework. If the decider takes the Major elsewhere, this
commit holds on its own terms, and a re-review can close it.
