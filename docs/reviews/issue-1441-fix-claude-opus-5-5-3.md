## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #1441 batch 3, model: claude-opus-5-5)

This is round 2 of this review. Round 1 reviewed commit 9fda9408 on 3696442e and found 0 blockers, 4 majors and 3
minors. Round 2 reviews commit 36b29cda `docs(api): every field of the remaining kinds has a description`, which
replaces 9fda9408. It sits on 9721fe43 (the second batch), and the chain was rebuilt on `origin/main` 3f329326, which
merged #1448; since 31aebe90 the Spec pages write a description's `<`, `>`, `{` and `}` as entities. The commit has 38
files, 632 insertions and 509 deletions. `git range-diff 9fda9408^..9fda9408 36b29cda^..36b29cda` shows that the changes
since round 1 are seven tags, two new pins in `internal/services/backup`, and the end of the field list:
`internal/controlplane/kindref/testdata/undescribed.txt` is deleted with its exemption, its scenario test and its line in
`scripts/check-undescribed.sh`, and `.claude/rules/api-types.md` drops its sentence about the list. Between the two
trees, no reader package that round 1 checked changed (`internal/site`, `internal/edge`, `internal/route`,
`internal/dataplane`, `internal/network`, `internal/auth`, `internal/envresolve`, `internal/function`,
`internal/services` and `internal/backup`), so round 1's field-by-field evidence stands. Every line number below was
checked again on 36b29cda.

This review ran in an agent separate from the one that wrote the tags. The checks named "run" were run on 36b29cda;
the other facts were read in the code at the cited lines. Nothing was written to the worktree.

### Blockers

None, in either round.

### Majors

All four round-1 Majors are fixed, each with the text that round 1 proposed.

1. **`ConfigMapSpec.data`: a Lake does not get each key, and a Function does not get a Secret's value.** Fixed. The tag
   (`api/types/v1alpha1/configmap.go:22`) now says "Each worker of a Function that lists the ConfigMap in
   bindings.config gets each key as an environment variable when it starts, except a key that starts with FUNCD_ or
   that a bound Secret also holds. A Lake that lists it gives its engine only the keys that the Lake's bindings.config
   lets through." This matches `internal/envresolve/envresolve.go:110-112` and `internal/function/secrets.go:41-52` for
   a Function, and `internal/services/lake/reconcile.go:563-568` for a Lake, whose `bindings.config` row names those
   keys (`api/types/v1alpha1/bindings.go:48`). The text reads true in each of the 12 places it shows, the Lake page
   included.
2. **`RouteSpec.host`: the default namespace's host-less rules also answer on a host that another namespace's Route
   names.** Fixed. The tag (`api/types/v1alpha1/route.go:28`) now says "... or, in the default namespace, every host
   except another namespace's own host, <other>.<server.baseDomain> or a name under it, while that namespace uses it; a
   host that another namespace's Route names is not excluded, and the longer matching path wins there. In the default
   namespace, an explicit defaultExposure refuses an empty host (HostRequired). ..." Run: round 1's probe, a scratch
   test file passed with `go test -overlay`, gives the same answers on 36b29cda. `shop.example.com/api/v2/x` resolves to
   the `default` Function, `shop.example.com/api/x` to `shop`'s, and `team.localhost/other` to nothing. The page shows
   the placeholders as entities (`docs/guide/develop/security-and-networking/networking/spec.md:21`).
3. **`BackupScheduleSpec.encryption` and `BackupEncryption.recipients`: the manifest is not sealed.** Fixed. The tags
   (`api/types/v1alpha1/backup.go:42,74`) now say "The age recipients that seal the member files of each Backup" and
   "Every member file of each Backup is sealed to all of them, so the identity of any one opens it. The copy's
   manifest.yaml, which names the members, their files' sizes and these recipients' fingerprints, is not sealed." This
   matches `internal/backup/workload/workload.go:97-99,296-315,421-439` and
   `docs/adr/0222-workload-backup-resources-and-catalog-scope.md:107`.
4. **`BackupTarget.url` omitted that the target must accept a create-if-absent put.** Fixed. The tag
   (`api/types/v1alpha1/backup.go:68`) adds "The bucket must accept a create-if-absent put (If-None-Match: *): a bucket
   that refuses it fails each Backup with reason TargetUnconditional." This matches
   `internal/backup/workload/workload.go:408-418`, `internal/backup/backup.go:317-325` and
   `internal/services/backup/run.go:183-184`. The reason is pinned beside SameProvider
   (`internal/services/backup/doc_internal_test.go:17`).

### Minors

Minors 1 and 2 are fixed. Minor 3 is not changed, as planned: it goes to the decider. Minor 4 is new in round 2.

1. **`IdentitySpec.credentialSecretName`: a Secret that funcd issued for a deleted Identity of the same name is issued
   again.** Fixed. The tag (`api/types/v1alpha1/identity.go:33`) now says "funcd leaves alone a Secret of that name that
   no Identity of this name controls, and the Identity then stays Pending with reason SecretNotOwned; a Secret left by a
   deleted Identity of the same name is issued again." This matches
   `internal/services/identity/reconcile.go:94-100,135-143,172-180,193-201`.
2. **`BackupScope.lakes` omitted the busy-Lake failure.** Fixed. The tag (`api/types/v1alpha1/backup.go:58`) adds "A
   Lake that deletes files during each of a run's copies, as a busy one may, fails the Backup with reason LakeUnstable,
   and so does a Bucket that brings it in." This matches `internal/backup/workload/lakes.go:43-99`,
   `internal/backup/workload/workload.go:49-50`, `internal/services/backup/run.go:185-186` and
   `docs/behavior/disaster-recovery.md:44`. LakeUnstable is pinned (`internal/services/backup/doc_internal_test.go:18`);
   the sentence names no count, so `lakeAttempts` needs none.
3. **`BackupScope.resourceGroup` and `BackupScope.app`: a scope that resolves to no member gives a Backup that
   succeeds.** Not changed. The behavior is uncovered, so the tags do not state it until the decider answers. The
   evidence is in the first Routed item.
4. **New: the CI step that runs `scripts/check-undescribed.sh` still names the field list.** `.github/workflows/ci.yml:117`
   reads "Check that the list of fields without a description does not grow (ADR-0323)". Since this commit the script
   checks only the config keys' list (`scripts/check-undescribed.sh:2-7`), which ADR-0324 Decision 2 decides, so a
   reader of a failed run looks for a list that no longer exists. Proposed name: "Check that the list of config keys
   without a description does not grow (ADR-0324)". The commit does not touch `ci.yml` yet; the fix is one line there.

### The end of the field list (new in round 2)

ADR-0323's Temporary workarounds section ends: "Exit: the list is empty; the file, its exemption and the CI step are
then deleted." The commit meets it, as follows.

- **The list is empty.** At 9721fe43 the list holds 147 fields, the same 147 as round 1's parent, and this commit
  describes each of them. Run: `TestEveryFieldIsDescribed` passes on 36b29cda with no list.
- **The file goes.** `internal/controlplane/kindref/testdata/` no longer exists. Only ADR-0323, which is frozen as
  Implemented, and its implementation review still name the file or the deleted scenario test; both are history and
  stay as written.
- **Its exemption goes.** `TestEveryFieldIsDescribed` now calls `check(Undescribed(doc))`, which fails on any field
  with no description and names each one (`internal/controlplane/kindref/kindref_test.go:68-78,112-118`). The `-update`
  flag and `readList` are gone. Run: a mutant that drops the tag of `ResourceGroupSpec.description` through
  `go test -overlay` fails `TestEveryFieldIsDescribed` with "fields with no description, which need a doc tag written
  for users: ResourceGroupSpec.description". `TestScenarioUndescribedFieldRefused` (`kindref_test.go:80-86`) still names
  the new field, and it now also checks that the described field is not named.
  `TestScenarioUndescribedListOnlyShrinks` goes with the list that it tested, and no hygiene check maps an ADR's
  scenarios to tests.
- **The CI step stays, rightly.** ADR-0324 Decision 2 has the same script refuse a line added to the config keys' list
  "as it does for ADR-0323's list" (`docs/adr/0324-funcd-and-funcdctl-spec-tabs-come-from-the-config-struct-and-the-command-trees.md:90-92`),
  and ADR-0324's own exit deletes "its CI line" (line 125). So the commit removes ADR-0323's entry from `lists`
  (`scripts/check-undescribed.sh:7`) and rewrites the header for ADR-0324 (lines 2-4). `TestScenarioUndescribedListNeverGrows`
  now runs its four cases on the config list (`scripts/generated_test.go:28-51`): a list that shrinks passes, an added
  key fails and is named, a base that the script cannot read fails, and a base without the list passes. The former
  config-list case is one of these. Run: the `scripts` package passes, and `scripts/check-undescribed.sh` exits 0
  against 3f329326 and against 9721fe43. The step's name is Minor 4.
- **The rule follows.** `.claude/rules/api-types.md:16-17` drops the list and says that `TestEveryFieldIsDescribed`
  refuses a field with no tag. Both duties of ADR-0323 Decision 6 stay: write the tag with the field, and run
  `just generate`.
- **Nothing else used the list.** The `generate` recipe never ran this package with `-update` (`justfile:503-506`), so
  the removed flag breaks no recipe. No script, skill, workflow or behavior row names the deleted file.

### Routed (not this work's)

- **A scope that resolves to no member succeeds: uncovered (Minor 3).** For `resourceGroup`, funcd lists the namespace's
  stores of that group and does not check that the group exists or holds one (`internal/backup/workload/workload.go:146-160`).
  An empty copy writes only the manifest and ends Succeeded (`workload.go:286-315`, `internal/services/backup/run.go:161-200`).
  An App with no kv, buckets, lakes or sql entry and no local volume does the same, so a misspelt group gives Backups
  that succeed and hold nothing, with `rpoRisk` false. No row of `docs/behavior/disaster-recovery.md`, no clause of
  ADR-0222 Decision 2 and no clause of ADR-0228 speaks of it. It is a question for the decider: should such a run fail,
  with MemberNotFound for example?
- **TargetUnconditional still has no behavior row.** `docs/behavior/disaster-recovery.md` names LakeUnstable (line 44)
  but not this reason. Now that `BackupTarget.url` states it, the behavior-registry pass should add the row.
- **The deferred rule of two matching Routes.** `RouteSpec.host` and `RouteSpec.rules` state the current rule, which
  `docs/behavior/calls.md:44` marks deferred to an open question of ADR-0176. Both tags change if that question is
  decided another way. Nothing is to be done now.
- **A kind's own name limits show on no Spec page.** A BackupSchedule's name has at most 54 characters and a Restore's
  at most 56 (`api/types/v1alpha1/backup.go:14-19,232-234,381-383`). The Spec pages link `metadata` to
  `common-definitions.md`, which cannot show the limit of one kind. This is a question for the generator.
- **Stale Go comments on status.** `api/types/v1alpha1/site.go:127-130` and `internal/controlplane/admission/site.go:17`
  say that an apply wipes a status, while `internal/controlplane/status_test.go:21` says that status is server-owned and
  that an apply never clears it. The comments can mislead the next author of a tag.

### Verified correct (keep it)

- **Generated files, round 2.** Run: `go run ./internal/controlplane/cmd/specgen/`, with `-out` and `-guide` in a scratch
  folder that holds a copy of `docs/guide`, gives a document identical to `api/openapi/funcd.v1alpha1.yaml` (`cmp`), and
  `diff -r` against `docs/guide` is empty. The pages write `<`, `>`, `{` and `}` through `escaped`
  (`internal/controlplane/kindref/kindref.go:341-345`), so each `<other>` and `<namespace>` placeholder of these tags
  shows as text.
- **Tests and checks, round 2.** Run: `go test -count=1` passes (`ok`) for `internal/controlplane/kindref`, `scripts`,
  `api/types/v1alpha1`, `internal/auth`, `internal/auth/cedar`, `internal/services/identity`, `internal/edge/static`,
  `internal/edge/router`, `internal/dataplane`, `internal/site`, `internal/services/backup`, `internal/function` and
  `internal/reference`. `TestSpecGeneratedFromGo` in `internal/controlplane` passes, and so do
  `TestScenarioConfigSpecGenerated` in `cmd/funcd` and `TestScenarioCommandSpecGenerated` in `cmd/funcdctl`
  (`-tags dev`), each without `-update`, so the funcd and funcdctl pages are current too. `gofmt -l` prints nothing,
  `go vet` passes, and `golangci-lint run` reports 0 issues on `internal/controlplane/kindref`, `scripts`,
  `internal/services/backup` and `api/types/v1alpha1`, with its cache outside the tree. `bash -n` passes on the script.
  The tree is unchanged after every run.
- **The tags' text.** None of the 119 tags holds a backtick, an ADR number, a Go identifier, a package or file name, an
  internal component's name or a machine path. No tag repeats a pattern, an enum, a bound or a length that the schema
  shows. The ranges they state (1 to 65535 ports, at most 64 rules and 64 destinations, at most 32 ports, at least 2
  recipients) are in `Validate` only (`api/types/v1alpha1/egresspolicy.go:77-121`, `backup.go:254-266`).
- **The pins hold.** Each pin names the constant whose value the tag copies: `DefaultBackupTTL` (720h, `backup.go:22`),
  `ReasonFoundOnTarget` (`backup.go:193`), `maxEgressRules`, `maxEgressDests` and `maxEgressPorts` (64, 64 and 32,
  `egresspolicy.go:14-16`), `SiteRuntimeConfigMaxBytes` and `SiteRuntimeConfigFile` (32768 and config.json,
  `site.go:58-60`), the four action constants of `internal/auth`, all twelve built-in role names
  (`internal/auth/cedar/roles_compile.go:36-52`, pinned by iterating the map), the Identity's three Secret keys, the
  static and Site default index, the Site's `_runtime` folder, the router's reasons and `/function`, the data plane's
  `/function/` and `/health/`, the backup reasons (TargetUnconditional and LakeUnstable included) and Secret keys, and
  `reasonVolumeBudget`. The reasons that are literals where the code sets them (`BackendNotFound`, `BucketNotFound` and
  `SecretNotOwned`) have no constant to pin.
- **Config keys and commands exist under their names.** These are `site.defaultIndex` (default index.html),
  `server.baseDomain`, `server.auth.edge`, `server.network.egress`, `workloadBackup.maxCopyMembers` and `blob.target`
  (`internal/platform/config/config.go:54,78,108,216,223,437,581`), `funcdctl push --site`
  (`cmd/funcdctl/cli.go:297`) and `funcdctl hold release --namespace` (`cmd/funcdctl/hold.go:55-57`).
- **Shared types read true in each place.** The App clauses hold: an App's `sites[]` entry may not set `buckets`, and its
  Site refers to the served Bucket by ref (`api/types/v1alpha1/app.go:510-513,771-774`); a `runtimeConfig.configMap`
  that names a `configMaps` entry is set to its stored name (`app.go:770,795-808`); an empty `backupSchedules[]` scope
  becomes the App (`app.go:784-788`). An App's parts pass `site-prefix-immutable` too (`pkg/funcd/funcd.go:1051-1058`),
  so `BlobRef.prefix` holds there. An App's Route is not owned by a Site or a Function
  (`internal/route/owned/owned.go:43-47`), so the exception in `RouteSpec.enabled` does not touch it. Since Major 1's
  fix, `ConfigMapSpec.data` holds in every place too.
- **Site.** A tag resolves when the generation is unobserved or nothing is served (`internal/site/reconcile.go:122-138`),
  and status survives an apply. A bundle without its index or with a `_runtime` entry does not deploy, and the previous
  digest keeps serving (`reconcile.go:370-375,157-165`). The Bucket by name or by ref (`reconcile.go:262-301`,
  `internal/site/materialize.go:34-67`, `api/types/v1alpha1/site.go:209-254`), the prefix that cannot change
  (`internal/controlplane/admission/site.go:27-40`), the index and SPA (`materialize.go:22-30,74-105`,
  `internal/edge/static/static.go:107-124,156-180`), the rule paths (`site.go:288-345`), config.json and its 32 KiB cap
  (`internal/site/runtimeconfig.go:28-66`), and status derived from the Route (`reconcile.go:419-447`) all match.
- **Route and static backends.** Validation (`api/types/v1alpha1/route.go:158-248`), the backend checks and
  `status.host` (`internal/route/reconcile.go:155-221`), Disabled, ReservedPath, HostRequired and HostReserved
  (`internal/edge/router/aggregator.go:251-272,296-324,346-361`), lower-case host matching and segment prefixes
  (`router.go:191-222`), the rest of the path, `/` for an Exact rule (`internal/dataplane/dataplane.go:443-452`), the
  `/health/` 404 (`internal/dataplane/plugins.go:179-194`), and GET, HEAD and 405 (`static.go:73-77`) all match.
- **EgressPolicy.** Every policy of a namespace compiles, so the rules add up (`pkg/funcd/funcd.go:1661-1671`). Only
  Functions are covered (`internal/auth/cedar/egress_compile.go:128-147`). The gateway is a no-op unless egress is on,
  on Linux (`internal/network/egress/gateway.go:76-84`). A domain rule never reaches an address that is not globally
  reachable (`gateway.go:110-144,167-182`). The forwarder answers only an allowed name, keeps the answer for its TTL, and
  matches a wildcard at any depth but not the bare name (`internal/network/egress/forwarder.go:204-235,371-379,486-529`).
- **Identity, Role, RolesAssignment and Policy.** The access key id derives from the namespace and the name
  (`internal/blob/s3gateway/iam.go:87-89`). Rotation writes a new key and token and leaves no overlap, since both checks
  read the stored Secret on each call (`internal/services/identity/externalkeys.go:29-63`,
  `internal/lake/gateway/token.go:159-190`; `docs/behavior/security.md:50`). No built-in rule grants an Identity anything
  (`internal/auth/cedar/builtin_*.cedar`). A missing Role or an unknown name grants nothing
  (`roles_compile.go:74-88,97-118`). `security.md:48` marks this `differs`, since ADR-0136 asks for the names to be
  checked and the miss to show; the tag stays true once that is done, as a name that matches no role still grants
  nothing. A Namespace scope ignores its name (`roles_compile.go:184-213`). The entry defaults match `Validate`
  (`api/types/v1alpha1/rolesassignment.go:93-132`). A Policy must parse, name one known action, and name only entity
  types and ids of its own namespace (`internal/auth/cedar/schema.go:134-188`), and it applies only to a request inside
  its namespace (`internal/auth/cedar/policies.go:69-90`).
- **Namespace and ResourceGroup.** Exposure (`dataplane.go:219-232`, `aggregator.go:317-324`), the stance order
  (`dataplane.go:400-412`), the edge's 401 and 403 with Bearer or X-Api-Key (`internal/edge/authn/authn.go:53-95`), 401
  with no authenticator (`dataplane.go:330-338`, `cmd/funcd/main.go:453-455`), the volume budget at apply and at a
  worker's start (`internal/volume/volume.go:338-369`, `internal/function/volumes.go:668-678`), one form of the budget
  (`api/types/v1alpha1/namespace.go:87-111`), and a ResourceGroup description that nothing reads all match.
- **Backup, BackupSchedule and Restore.** The slots, the collapse and the overlap rule
  (`internal/services/backup/schedule.go:26-119`; `docs/behavior/disaster-recovery.md:42`), the status fields and
  `rpoRisk` with its log line (`schedule.go:165-236`), the ttl and the expiry of the record alone (`run.go:93-105,225-245`,
  `internal/controlplane/admission/backup.go:109-126`), the scope forms and `maxCopyMembers` (`workload.go:114-200`), the
  recipient and identity rules (`internal/backup/envelope/envelope.go:128-169,234-286`), the blob store's bucket refused
  and SameProvider (`admission/backup.go:32-50`, `internal/blob/gocloud/domains.go:32-62`), FoundOnTarget and
  Interrupted (`internal/services/backup/sync.go:157-197`, `run.go:72-90`), and the Restore's phases, export, hold and
  emptiness checks (`internal/services/backup/restore.go:36-297`, `admission/backup.go:156-193`,
  `internal/backup/workload/databases.go:81-83`, `lakes.go:113-153`) all match.

### Recommendation

Pass. Minor 4 is one line of `.github/workflows/ci.yml`: fix it in this commit or the next. Minor 3 and the Routed items
go to the decider.
