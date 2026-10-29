# ADR-0299: `funcdctl scan` matches what runs against its SBOMs, in an extension built on Grype

- **Status**: Accepted (2026-10-29; judged twice by the author under the adr-judge skill, no Blocker; the decider
  confirmed the seven choices the draft made: exit 2 for anything not scanned, any finding failing without
  `--fail-on`, serving and draining Revisions as what runs, the ref mode, the `funcdctl-scan/` module, the database
  updated unless `--no-update`, and several SBOMs read as their union)
- **Date**: 2026-10-29
- **Deciders**: green-0-rabbit
- **Tags**: supply-chain, sbom, scanning, funcdctl, extension
- **Realizes**: [FEAT-0017/F215](../feat/0017-feat-artifact-supply-chain.md) (a vulnerability scan of what runs)
- **Answers**: the decider's refinement of 2026-10-29: the scan runs outside the daemon, with Grype embedded in an
  extension beside funcdctl rather than in funcdctl itself, so funcd's license, funcdctl and the root `go.mod` stay as
  they are; MPL-2.0 dependencies are approved in the extension binary only
- **Relates to**: [ADR-0298](0298-a-function-artifact-carries-an-sbom.md) (the SBOMs it reads) ·
  [ADR-0280](0280-the-model-engine-on-bifrosts-go-library.md) (a large library in its own nested module) ·
  [ADR-0035](0035-artifact-digest-resolution-at-revision.md) (the digest a Revision pins) ·
  [ADR-0145](0145-multi-arch-function-bundles-and-arch-aware-placement.md) (an index per function) ·
  [ADR-0031](0031-oci-artifact-distribution-oras.md) (registry credentials, `funcdctl login`)

## Context & Need

With ADR-0298 each platform manifest carries a CycloneDX SBOM, but nothing in funcd reads it, and the scanners an
operator would reach for fail on funcd artifacts: in experiment 0015
([pyvvo/experiment#27](https://github.com/pyvvo/experiment/pull/27)), `trivy image` and zot report a multi-arch
function's index clean, with exit 0 and no error. From the SBOMs alone, Trivy, Grype and OSV-Scanner found every
vulnerable package.

Experiment 0015 also linked Grype v0.113.0 into funcdctl at funcd `e5f7c7b5`. It builds with `CGO_ENABLED=0` and finds
the same 33 matches as the Grype CLI; its database opens in 0.65 s, then each SBOM matches in 15 ms, at 72 MiB of
resident memory. It costs funcdctl 60.7 → 139.2 MB and 129 → 345 linked modules, and the root `go.mod`, which the
daemon shares, gains 168 requires and moves 3 others. Of the 216 new modules, 8 are MPL-2.0 (among them `hashicorp/go-getter`, `hcl/v2`,
`go-multierror`); the rest are Apache-2.0, MIT, BSD, ISC, CC0 or public domain (`spdx/tools-golang` is "Apache-2.0 OR
GPL-2.0-or-later", taken as Apache-2.0). Grype's database is a 187 MiB download that unpacks to 3.1 GiB, rebuilt daily.

**Purpose**: an operator or a CI lists the known vulnerabilities of what runs (each Function's serving and draining
Revision) or of given refs, from their SBOMs, and every artifact it could not scan is named, never counted clean.

## Scenarios

- **scenario: scan-what-runs**: Given `fetch` deployed from a multi-arch ref whose platform manifests carry ADR-0298's
  SBOMs, When `funcdctl scan` runs, Then it lists each advisory of requests 2.25.1, urllib3 1.26.4, Jinja2 3.1.2,
  cryptography 41.0.0 and idna 2.10, per Function, Revision and platform, and exits 1.
- **scenario: scan-a-ref**: Given a pushed ref, When `funcdctl scan <ref>` runs, Then it scans that ref's platform
  manifests the same way, with no API call.
- **scenario: no-sbom-is-named**: Given a running Revision whose artifact has no SBOM, When `funcdctl scan` runs, Then
  the report names that Revision as not scanned, with the reason, and the command exits 2.
- **scenario: index-never-reads-clean**: Given a multi-arch ref where one platform manifest has no SBOM, When it is
  scanned, Then the other platform's findings are listed, the bare one is named as not scanned, and the command exits 2.
- **scenario: offline-database**: Given `--db <dir>` holding a Grype database and `--no-update`, When `funcdctl scan`
  runs with no route to Grype's distribution, Then it matches against that database and reports its build date.
- **scenario: extension-missing**: Given funcdctl without `funcdctl-scan`, When `funcdctl scan` runs, Then it exits 2,
  since nothing was scanned, with an error naming the missing binary and where to get it.

## Scope

**In**: the `funcdctl-scan` binary in the nested module `funcdctl-scan/` and its release; the `funcdctl scan` command
that runs it; the list of running targets; reading platform manifests and SBOM referrers; matching with Grype; the
database cache; the report and the exit codes; the third-party notice.
**Out**: making SBOMs (ADR-0298); checking signatures or provenance (F216); scanning in the daemon, or a Revision
condition fed by scans; the runtime images and funcd itself, which image scanners and `govulncheck` read; crawling
every tag of a registry.

## Constraints & Decision drivers

- funcd stays Apache-2.0, the root `go.mod` takes none of Grype's modules, as ADR-0280 decided for Bifrost, and
  funcdctl and the daemon link no MPL-2.0 code (the decider, 2026-10-29).
- `blueprint.md:239` keeps a large third-party library that parses outside input out of the trusted core: Grype
  parses SBOMs from registries and a database from the internet.
- Never count an artifact clean that was not matched: experiment 0015's index false negative. A gate reads exit 2 as
  "not scanned", never as "no finding" or "findings".
- Open the database once per run: opening costs 0.65 s, a match 15 ms.
- Offline use: a CI or an air-gapped operator gives a database directory and turns updates off.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| A. Grype inside funcdctl | One binary | 78.5 MB more for every funcdctl user; MPL-2.0 code in funcdctl; 168 more requires in the root `go.mod` | Rejected (decider) |
| **B. An extension binary `funcdctl-scan` on Grype, run by `funcdctl scan`** | funcdctl and the root module unchanged; MPL-2.0 only in the extension; same speed | One more binary to build and release | **Chosen** |
| C. `funcdctl scan` runs an installed `grype` or `trivy` | No new dependency | The user installs and pins a scanner; funcdctl parses its output across versions | Rejected (decider: embedded) |
| D. A scan loop in the daemon | Continuous, a status condition | `blueprint.md:239`; the daemon downloads the database | Rejected |
| E. Trivy's Go packages | One vendor for images and SBOMs | Not measured; the decider asked for Grype, measured in experiment 0015 | Not pursued |

## Decision

1. **The command.** `funcdctl scan` runs `funcdctl-scan`, found beside the running funcdctl binary
   (`os.Executable`) first, then on `PATH`, with its arguments unparsed (cobra's `DisableFlagParsing`) and the same
   environment, and exits with its exit code.
   Without it, `funcdctl scan` exits 2 with `funcdctl-scan is not installed: get it from the funcd release beside
   funcdctl`.
2. **The module.** `funcdctl-scan/go.mod` declares `module github.com/pyvvo/funcd/funcdctl-scan`, requires
   `github.com/anchore/grype v0.113.0` and `github.com/pyvvo/funcd` (replaced by `..` in the repository), and builds
   its binary from `funcdctl-scan/main.go` with `CGO_ENABLED=0` and `-trimpath`. The directory is not called
   `extensions/`: funcdctl already extracts DuckDB extensions (`cmd/funcdctl/dev.go:1124`). The release workflow
   builds it per OS and arch beside funcdctl and ships `THIRD_PARTY_NOTICES`, which lists each MPL-2.0 module it links
   with its version and source URL (MPL-2.0 §3.2). Grype is pre-1.0, so its calls stay in one file of the module.
3. **What runs.** It reaches the API as funcdctl does: `--server` (default `$FUNCD_SERVER`, else
   `http://localhost:8080`) and `--token` (default `$FUNCD_TOKEN`) give `sdk.New` its URL and token
   (`cmd/funcdctl/cli.go:56-79`). Without refs, it lists the Namespaces, or takes only `-n`'s, then each Namespace's Functions; it
   takes each Function's `status.servingRevision` and `status.drainingRevision`, reads each Revision's `spec.image` and
   `spec.imageDigest` (ADR-0035), and scans each digest once. With refs, it scans those refs instead, with no API call.
4. **Per digest.** `artifact.PlatformManifests` (NEW) resolves the digest with funcdctl's registry credentials: an
   index stands for its platform manifests, each scanned on its own (ADR-0145), and never counts as scanned itself;
   a single manifest stands for itself. `artifact.SBOMs` (ADR-0298) reads a manifest's SBOMs, and several are read as
   the union of their packages. A manifest with none, an SBOM that fails `artifact.CheckSBOM`, or a registry error is
   **not scanned**, with that reason.
5. **Matching.** Grype's library matches each SBOM: `grype.LoadVulnerabilityDB` opens the database once per run, with
   its checksum checked and its age limited as Grype's CLI does; `pkg.Provide("sbom:…")` reads the SBOM; and
   `grype.VulnerabilityMatcher` with `matcher.NewDefaultMatchers` matches it. The database lives in `--db <dir>`, by
   default `funcd/grype-db` under `os.UserCacheDir`, and is updated from Grype's distribution unless `--no-update` is
   given, which also lifts the age limit; the report states its build date. A missing or unreadable database ends the
   run with exit 2.
6. **Report and exit.** A table per finding (namespace, function, revision, platform, package, version, advisory,
   severity, fixed in), then each artifact not scanned with its reason; `-o json` gives the same as JSON. The exit
   code is 2 when anything was not scanned, else 1 when a finding is at or above `--fail-on` (negligible, low, medium,
   high or critical; without it, any finding; a finding of unknown severity counts at every threshold), else 0.

## Design model

None: one process reads, matches and reports; nothing runs concurrently with another actor.

## Temporary workarounds

None.

## Contracts

```text
funcdctl scan [<ref>...] [-n <namespace>] [-o table|json] [--fail-on <severity>] [--db <dir>] [--no-update]
              [--server <url>] [--token <token>]
```

```go
// Package artifact (internal/artifact), NEW: the platform manifests a digest stands for.
type PlatformManifest struct {
	Platform v1.OCIPlatform // "" for a manifest that records none
	Digest   string
}

// PlatformManifests returns an index's platform manifests, or the manifest itself when digest names one.
func PlatformManifests(ctx context.Context, ref, digest string) ([]PlatformManifest, error)
```

```go
// Package scan (funcdctl-scan/scan), NEW.

// Target is one artifact to scan: a running Revision's, or a ref from the command line.
type Target struct {
	Namespace, Function, Revision string // empty for a ref
	Image, Digest                 string
}

type Finding struct {
	Target
	Platform, Package, Version, Advisory, Severity, FixedIn string
}

// Skipped is an artifact or one of its platform manifests that was not matched, and why.
type Skipped struct {
	Target
	Platform, Reason string
}

type Report struct {
	DBBuilt  time.Time
	Findings []Finding
	Skipped  []Skipped
}

type Match struct{ Package, Version, Advisory, Severity, FixedIn string }

// Matcher matches one CycloneDX SBOM; NewGrype opens Grype's database once.
type Matcher interface {
	Match(ctx context.Context, sbom []byte) ([]Match, error)
	Built() time.Time
}

func NewGrype(dbDir string, update bool) (Matcher, error)
func Running(ctx context.Context, c *sdk.Client, namespace string) ([]Target, error) // serving and draining
func Scan(ctx context.Context, targets []Target, m Matcher) Report
func ExitCode(r Report, failOn string) int // 2 when anything was skipped, else 1 at or above failOn, else 0
```

| Consumes | Exposes |
|---|---|
| The funcd API (Namespaces, Functions, Revisions); manifests and referrers, with `funcdctl login` credentials; Grype's database distribution, unless `--no-update` | The report (table or JSON); exit 0, 1 or 2; the database directory |

## Implementation plan

- `internal/artifact`: `PlatformManifests`, next to `Platforms` (`platform.go:135`), with a test on an index and on a
  single manifest in an OCI layout.
- `funcdctl-scan/`: `go.mod`, `main.go` (cobra, the flags) and package `scan` (`Running`, `Scan`, `ExitCode`, the
  Grype `Matcher` in one file). Tests with a fake `Matcher` on an OCI layout, one per scenario:
  `TestScenarioScanWhatRuns`, `…ScanARef`, `…NoSBOMIsNamed`, `…IndexNeverReadsClean`, `…OfflineDatabase`; and
  `TestGrypeMatcher`, which matches experiment 0015's `fetch` SBOM when `FUNCD_SCAN_DB` names a database and skips
  otherwise (a database is 3.1 GiB).
- `cmd/funcdctl`: the `scan` command; `TestScenarioExtensionMissing`, and a fake `funcdctl-scan` beside the binary
  that receives the arguments.
- `.github/workflows/release.yml`: build `funcdctl-scan` per OS and arch; `scripts/third-party-notices.sh` (NEW)
  writes `THIRD_PARTY_NOTICES` from `go version -m` and each module's license.
- `just ci` and the gate build, vet, lint and test `funcdctl-scan` (`go -C funcdctl-scan …`), since `./...` in the
  root does not reach a nested module.
- Docs: `examples/artifact-sboms.md` (ADR-0298) gains the scan; `docs/install.md` gains the extension.
- **Done when**: the tests pass with `-race`; build, vet and lint are clean in both modules; `scripts/agent/gate.sh`
  passes; the root `go.mod` and `go.sum` are unchanged.

## Review checklist

- [ ] The root `go.mod` and `go.sum` do not change; `go version -m` lists no Grype module in funcd or funcdctl.
- [ ] No target is reported without each of its platform manifests matched or named; an index never counts as scanned.
- [ ] The exit codes follow Decisions 1, 5 and 6; the database opens once per run.
- [ ] The release ships `THIRD_PARTY_NOTICES` naming every MPL-2.0 module of `funcdctl-scan`.
- [ ] Each scenario has its named test.

## Consequences

- **Positive**: one command lists what runs and its known vulnerabilities, 15 ms per SBOM once the database is open;
  a cron job and a CI gate use the same command; funcd's license, funcdctl and the root module stay as they are.
- **Negative (accepted)**: a second binary to install; each machine that scans keeps a 3.1 GiB database and downloads
  187 MiB per update; the extension's release carries MPL-2.0 code and its notice; Grype's upgrades are the
  extension's own and may change its calls.

## Open questions

- Revisions that running WorkflowRuns still pin (ADR-0190): scanned with each Function's serving and draining ones?
  Answered in the implementation PR; until then, they are not listed.
- Trusting only a signed SBOM: F216's ADR.

## References

- Experiment 0015, [pyvvo/experiment#27](https://github.com/pyvvo/experiment/pull/27): the scanners on funcd
  artifacts, and Grype linked into funcdctl (sizes, modules, licenses, timings).
- The decider's refinement of 2026-10-29; FEAT-0017.
- `blueprint.md:239`; ADR-0280's Constraints (the root `go.mod` takes none of Bifrost's modules, decided
  2026-10-24).
- MPL-2.0 §3.2 and §3.3; the `spdx/tools-golang` README (dual license).
- Grype v0.113.0: `grype/load_vulnerability_db.go:13`, `grype/vulnerability_matcher.go:39,92`,
  `grype/pkg/provider.go:22`, `cmd/grype/cli/commands/root.go:178-236`.
- `api/types/v1alpha1/function.go:201-207` (`currentRevision`, `servingRevision`, `drainingRevision`),
  `api/types/v1alpha1/revision.go:21`, `pkg/sdk/kinds.go:48` (`Namespace`), `cmd/funcdctl/dev.go:1124`.
