## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #1441 batch 2, model: claude-opus-5-5)

This is round 4 of this review, a short one, and the verdict above is round 4's. Round 4 reviews commit 5f60db63, on
branch `docs/describe-every-field-and-key`. Its parent is batch 1's 2bb9b604, and 2bb9b604 sits on main b12ccdf6, which
merged #1449. I only read that checkout: I exported 5f60db63 with `git archive` into a scratch folder and ran every
check there.
- **Diff.** The literal `git diff b12ccdf6 5f60db63` holds batch 1 too, because 5f60db63's parent is 2bb9b604. So I
  compared the batch-2 commit's own diff, `git diff 2bb9b604 5f60db63`, with round 3's `git diff 31aebe90 9721fe43` on
  `api/types`, `internal/services` and `internal/activator`. Apart from blob ids, they differ only in the `KVTable.owner`
  hunk. Its removed line is now #1449's text and its added line the merged text, and the hunk's context holds the
  comment that #1449 reworded. The `KVStoreSpec.tables` hunk is identical, so that tag keeps batch 2's text.
  `undescribed.txt` equals round 3's.
- **The merged `KVTable.owner`** reads: "The Function that owns the table, in the store's namespace. The owner writes the
  table, and so does a principal that a RolesAssignment grants kv::write on the table, its store or its namespace, such
  as the KV Data Writer role. In a store that a Function declares, the owner defaults to that Function; in one that a
  Workflow declares, it names a step, and the Function that funcd creates for that step is the owner; in a KVStore
  applied on its own, a table with no owner is written only through such a grant. Every Function, the owner included,
  reaches the table through an entry of its spec.bindings.kv." Every fact holds at 5f60db63:
  - A table's writers are its owner, as a Function, and the principals that `KVWriters` returns
    (`internal/auth/cedar/capabilities.go:163-177`). `KVWriters` keeps a grant that holds `kv::write` at namespace
    scope, or at KVStore scope that names the store or the store and the table
    (`internal/services/roles/writers.go:53-67`). So a table with no owner is written only through such a grant.
  - `kv::write` is `auth.ActionKVWrite` (`internal/auth/authorizer.go:66`), and the built-in role "KV Data Writer" holds
    it (`internal/auth/cedar/roles_compile.go:40`).
  - The owner defaults to the declaring Function (`internal/controlplane/admission/bindingdeclared.go:348`).
  - In a store that a Workflow declares, a table owner must name a step (`api/types/v1alpha1/workflow.go:525`) and
    becomes that step's Function (`internal/workflow/reconcile_workflow.go:560`).
- **Consistency with `KVStoreSpec.tables`.** The merged text reads consistently with that tag ("only its owner and the
  writers that a RolesAssignment names may write"). The owner tag says the same thing more exactly, as the
  `kv::write` grant and its scope. The KV Spec page shows both tags as committed.
- **Checks.**
  - specgen on the export gives an OpenAPI document identical to `git show 5f60db63:api/openapi/funcd.v1alpha1.yaml`
    and 16 pages identical to the commit's pages.
  - Against 2bb9b604, `undescribed.txt` lost 135 lines and gained none.
  - None of the 56 tags holds a backtick, an ADR number, an issue number or a path.
  - `go test -count=1` passes (`ok`) on the six touched packages, and `go vet` is clean.
- **The `kv::write` pin.** `kv::write` in `KVTable.owner` came from #1449 and is unpinned at b12ccdf6 and 5f60db63.
  Batch 3's 088acfe4 pins it (`internal/auth/doc_test.go`), so it is routed below and is not this batch's.

No finding of round 3 changes. The one left is Minor 1, which waits for the decider.

This is round 3 of this review. Round 3 reviews commit 9721fe43, which is the batch rebuilt on batch 1's 31aebe90. 31aebe90
sits on origin/main 3f329326, which merged #1448, on branch `docs/describe-every-field-and-key`. That checkout was only
read: its tree at 9721fe43 was exported with `git archive` into a scratch folder, and every check ran there.
`git diff 31aebe90 9721fe43` equals round 2's `git diff 087f39cf 91bd1f86` on `api/types`, `internal/services` and
`internal/activator` except in one line, the `LakeSpec.configMaps` tag, which now takes Minor 8's fix. Between 91bd1f86
and 9721fe43, those paths and `undescribed.txt` differ only in that line. The Spec pages now write a description's `<`,
`>`, `{` and `}` as entities (batch 1's change). Round 3's verdict was the same as round 4's. Round 2 found 0 blockers,
0 majors and 2 minors; round 1 found 0 blockers, 5 majors and 7 minors.

Round 1 reviewed commit 3696442e on 8ebae558. Round 2 reviewed commit 91bd1f86 `docs(api): every field of the service
kinds has a description`, which replaced 3696442e, on 087f39cf (batch 1 after its own review fixes). The commit has 25
files, 467 insertions and 411 deletions, and 9721fe43 has the same counts. `git range-diff 8ebae558..3696442e
087f39cf..91bd1f86` showed that the changes after round 1 were only the tags listed below and the new lines of
`internal/services/lake/doc_internal_test.go`. In every round, the commit adds 54 `doc` tags to the fields of Bucket,
SQLDatabase, Lake, Secret, EventSource and Sensor and of the types they share. It also rewords two KVStore tags, adds
three `TestDocNamesTheConstant` pins, removes 135 lines from `internal/controlplane/kindref/testdata/undescribed.txt`, and
regenerates `api/openapi/funcd.v1alpha1.yaml` and 11 Spec pages. In each round, every changed tag was checked again
against the code that reads its field, and the generation, the list and the tests were checked again on that round's
commit.

This review ran in an agent separate from the one that wrote the tags. Every check below was run, not read.

### Blockers

None, in either round.

### Majors

All five round-1 Majors are fixed.

1. **`BucketPrefix.owner`: a Site serves prefixes that have an owner.** Fixed. The tag now says "The prefix that a Site
   serves its bundle from must have no owner, since the Site writes its builds there; the prefixes of a Site's ingress
   rules may have one." This matches the code: `internal/site/reconcile.go:282-293` checks only the owner of
   `s.Spec.ServedPrefix()`. `internal/site/materialize.go:77-86` mounts each `spec.ingress.rules[]` prefix and checks no
   owner, and `pkg/funcd/site_e2e_test.go:247-250` serves `gold`, which Function `etl` owns.
2. **`BucketSpec.quota` and `BucketPrefix.quota`: a size cap also refuses a presigned PUT.** Fixed. `BucketSpec.quota`
   adds "While the Bucket has a size cap, funcd refuses to sign a presigned PUT URL for it, with 403." `BucketPrefix.quota`
   adds "and while it is set funcd refuses to sign a presigned PUT URL into the prefix, with 403." This matches the code:
   `internal/blob/quoted.go:109-113` refuses for the Bucket's cap (`capped("")`) and for the cap of the key's prefix.
   `pkg/funcd/funcd.go:1839-1840` wraps every Bucket view in that check, and the refusal's `fault.Forbidden` answers 403
   (`api/fault/problem.go:31`).
3. **`LakeBindings.secret`: a missing Secret holds a Lake applied on its own with `SecretNotFound`.** Fixed.
   `LakeBindings.secret` now says "a forbidden Secret holds the Lake in Pending, with the reason BindingResolveFailed, as
   does a missing one in a lake that another resource declares". `LakeSpec.secrets` says "the Lake waits in Pending, with
   SecretNotFound or SecretKeyMissing, while one is missing or lacks a key". Both match
   `internal/services/lake/reconcile.go:82-89,104-114`, `internal/services/lake/rootentries.go:55-62` and
   `TestScenarioLakeSecretCheck` (`pkg/funcd/lake_rootentries_internal_test.go:154-170`). The two reasons are pinned at
   `internal/services/lake/doc_internal_test.go:20`.
4. **`LakeBindings.secret`: `secret::read` is a copied constant with no pin.** Fixed:
   `internal/services/lake/doc_internal_test.go:23` pins `string(auth.ActionSecretRead)`. The pin is in the package that
   reads the constant, not in `internal/auth`, which owns it. Batch 1 already pins `ownedstore`'s reasons from
   `internal/function` (`internal/function/doc_internal_test.go:22`), and a changed constant still fails `go test ./...`.
5. **`SecretSpec.data`: the tag omitted what a key and a value must be.** Fixed. The tag adds "a key that starts with
   FUNCD_ never reaches a workload" and "Each value must be UTF-8 text with no NUL byte: a Function or a Lake that binds a
   Secret holding another value does not start." Correction to round 1: my proposed sentence "A workload gets each value
   as an environment variable" was wrong for a Function. Its handler reads Secrets through `ctx.secret`, and no Secret
   value reaches its worker (`internal/function/workerenv.go:147-151`, ADR-0249 Decision 3). Only a Lake's engine gets
   them as environment variables. The commit rightly leaves that sentence out. Both kept facts hold on the Function's path
   too:
   - `ctx.secret` drops the `FUNCD_` keys (`internal/services/secret/secret.go:87`), as the Lake's environment does
     (`internal/envresolve/envresolve.go:107`).
   - A Function's start check runs `secrets.Resolver.ResolveEnv` (`internal/function/workerenv.go:155-165`). For a value
     that is not UTF-8 or holds a NUL byte, that check fails with `SecretResolveFailed` and no worker
     (`internal/secrets/secrets.go:83-85`, `docs/behavior/security.md:54`). A Lake is held with `BindingResolveFailed`.

### Minors

Minors 2 to 7 were fixed in round 2. Minor 8, found in round 2, is fixed in round 3. Minor 1 is unchanged and goes to
the decider; it is the one finding left.

1. **`BlobEvent.on` and `SQLEvent.on` name the values of named constants with no pin.** Not changed: it goes to the
   decider with the Routed item on how far the pin rule reaches. The values are `Created` (`BlobCreated`,
   `api/types/v1alpha1/eventsource.go:92`) and `Insert`, `Update` and `Delete` (`SQLInsert`, `SQLUpdate` and
   `SQLDelete`, `eventsource.go:50-54`). Batch 1 names enum values the same way (`FunctionSpec.protocol`'s
   `cloudevent` and `http`).
2. **`LakeSpec.buckets` and `LakeSpec.configMaps` omitted `StoreNotOwned`.** Fixed, and both are pinned through
   `reasonStoreNotOwned` (`internal/services/lake/doc_internal_test.go:18-19`). The `buckets` clause holds: a Bucket is
   written under its own name (`internal/services/lake/rootentries.go:35-39,63-68`). For the `configMaps` clause, see
   Minor 8.
3. **`Action.input`: `InvalidInput` stops every action of the Sensor.** Fixed. The tag ends "... and then none of its
   actions runs", which matches `internal/sensor/sensor.go:193-197`.
4. **`Action.function`: each event for an http target ends as a dead letter.** Fixed. The tag now says "A Function whose
   protocol is http is never called: the Sensor is not Ready, with reason TargetServesHTTP, and each event for this
   action ends as a dead letter." This matches `internal/sensor/invoker.go:53`, `sensor.go:221-227` and
   `docs/behavior/eventing.md:41`.
5. **`EventSourceSpec.sql`: three of the listed fields can be absent.** Fixed. The tag now says "op, table, rowId and
   index, and before, after and commitTimestamp when the change carries them", which matches
   `internal/services/sql/changefeed.go:284-293`. The `256 KiB` pin still holds.
6. **`SensorSpec.do`: a delivery past the Sensor's queue bound gets no attempt.** Fixed. The tag adds "while
   eventing.maxQueuedPerSensor deliveries wait, a new one becomes a dead letter with no attempt". The key exists
   (`internal/platform/config/config.go:392`), and the rule matches `internal/sensor/retry.go:141` and
   `docs/behavior/eventing.md:44`.
7. **`BlobEvent.prefix`: the objects already there fire only to the Sensors subscribed at that moment.** Fixed. The tag
   adds "to the Sensors subscribed then: an object that no Sensor got counts as seen", which matches
   `internal/eventing/fanout.go:28-44`, `internal/eventing/blobwatch.go:413-418` and `docs/behavior/eventing.md:38`.
8. **`LakeSpec.configMaps` said "a ConfigMap of that name", but the collision is on the stored name.** Fixed in round
   3: the tag now says "by name the Lake defines one, and a ConfigMap of its stored name that is not the Lake's holds it
   in Pending with StoreNotOwned". That matches `internal/services/lake/rootentries.go:41-46,63-68`, and the
   `reasonStoreNotOwned` pin still holds. The round-2 finding follows. This clause came from my round-1 proposal. A ConfigMap that a Lake declares by name is written as `<name>-<hash of data>`
   (`internal/services/lake/rootentries.go:41-46`, `v1.AppConfigMapName`), and `StoreNotOwned` comes only when a
   ConfigMap of that stored name is not the Lake's (`rootentries.go:63-68`). An existing ConfigMap named exactly
   `<name>` holds nothing. The entry's own `name` row shows the stored form on the same page, so a reader can work it
   out, and nothing breaks, which keeps this Minor. Proposed: "... by name the Lake defines one, and a ConfigMap of its
   stored name that is not the Lake's holds it in Pending with StoreNotOwned; by ref it binds an existing one. ...", or
   drop the clause, as `FunctionSpec.configMaps` does, together with the `ConfigMaps` pin line.

### Routed (not this work's)

- **A Workflow's database owner.** Not changed; it goes to the decider. This commit's `SQLDatabaseSpec.owner` says
  "in a Workflow's sql it names a step". The first batch's `WorkflowSpec.sql` says "its owner naming a step or a declared
  lake". `api/types/v1alpha1/workflow.go:518-519` and ADR-0250 Decision 1 accept both, and
  `docs/behavior/services.md:65` marks the lake-owned case as deferred to open issue #1235.
- **Placeholders on the Spec pages.** Resolved by batch 1. Round 2 noted that 087f39cf's `asCode` showed them as code;
  in round 3, batch 1 writes them as entities instead. At 9721fe43, `escaped`
  (`internal/controlplane/kindref/kindref.go:341-345`) writes a description's `<`, `>`, `{` and `}` as `&lt;`, `&gt;`,
  `&#123;` and `&#125;`, as #1448's config page does. The Lake page writes `FUNCD_LAKE_&lt;ALIAS&gt;_URL` and
  `&lt;name&gt;-duckdb`, and the Sensor's `input` row writes `$&#123;&#123; &#125;&#125;`, so each placeholder and
  expression of this batch shows as written.
- **How far the pin rule reaches.** Not changed; it goes to the decider with Minor 1. ADR-0323 Decision 4 says
  `TestScenarioDocNamesTheConstant` "covers the others", but neither batch pins a phase name (`Pending`, `Ready`) or an
  enum value that a tag names.
- **The `kv::write` pin (round 4).** `KVTable.owner` names `kv::write`, the value of `auth.ActionKVWrite`. That text came
  from #1449 on main, with no pin. Batch 3's commit 088acfe4 adds
  `doctag.Names(t, reflect.TypeFor[v1.KVTable](), "Owner", string(ActionKVWrite))` in `internal/auth/doc_test.go`.
- **`LakeSpec.configMaps[].data`** still has no description on the Lake page. It is `ConfigMapSpec.data`, which is listed
  in `undescribed.txt` and belongs to the ConfigMap batch.

### Verified correct (keep it)

- **Generated files and the list, round 3.** In the export of 9721fe43, `go run ./internal/controlplane/cmd/specgen/`,
  with its outputs in a separate scratch folder, gives a document identical to `git show
  9721fe43:api/openapi/funcd.v1alpha1.yaml`, and 16 pages, each identical to the commit's page at its path. The two other
  `spec.md` pages under `docs/guide/develop` (`running-funcd` and `tools/funcdctl`) come from #1448's generator, and this
  commit does not touch them. Against 31aebe90, `undescribed.txt` lost 135 lines and gained none. None of the 56 tags
  holds a backtick, an ADR number, an issue number, a repository path or a machine path. Round 2 got the same results on
  91bd1f86 against 087f39cf.
- **Tests and vet, round 3.** In the export of 9721fe43, `go test -count=1` passes (`ok`) for `api/types/v1alpha1`,
  `internal/controlplane/kindref`, `internal/controlplane/cmd/specgen`, `internal/services/sql`, `internal/services/lake`
  and `internal/activator`, including `TestEveryFieldIsDescribed` and the lake pin. `go vet` is clean on the four
  packages with new tags or tests. Round 2 got the same results on 91bd1f86.
- **The pins hold.** `defaultMemory` (256Mi, `internal/services/sql/reconcile.go:33`), `maxChangeData` (256 KiB,
  `internal/services/sql/changefeed.go:35`), `reasonPrefixNotOwned` (`internal/services/lake/lake.go:63`),
  `reasonStoreNotOwned` (`internal/services/lake/rootentries.go:17`), `quackTokenEnvKey`
  (`internal/services/lake/reconcile.go:29`), `ownedstore.ReasonSecretNotFound` and `ReasonSecretKeyMissing`
  (`internal/ownedstore/ownedstore.go:120-121`), `auth.ActionSecretRead` (`internal/auth/authorizer.go:93`) and
  `ReasonTargetServesHTTP` (`internal/activator/protocol.go:11`) each match the tag that names them. The other reasons
  the tags name (`BucketNotFound`, `DatabaseNotFound`, `InvalidInput`, `BindingResolveFailed`) are literals where the
  code sets them, so there is no constant to pin.
- **Config keys and commands exist under their names.** These are `s3gateway.maxUploadBytes`, `workflow.payloadLimit`,
  `eventing.deliveryAttempts`, `eventing.maxQueuedPerSensor` and `eventing.blobPollInterval`
  (`internal/platform/config/config.go:333,368,389,392,404`, `examples/funcdconfig.yaml`), and the command `funcdctl
  eventing dlq list` (`cmd/funcdctl/eventing.go:38-60`).
- **Bucket.**
  - Prefix names are unique, and an owner names a Function or a Lake (`api/types/v1alpha1/bucket.go:85-115`).
  - A missing Bucket or prefix holds a Function (`internal/function/references.go:45-50`) and a Lake
    (`internal/services/lake/reconcile.go:599-616`) with `BucketNotFound`.
  - The first segment of a key selects its prefix (`internal/blob/quoted.go:36-40`).
  - A prefix's writers are its owner and a RolesAssignment's writers (`internal/auth/cedar/capabilities.go:316-333`).
  - An omitted owner becomes the Function or the Lake that declares the Bucket by name
    (`internal/controlplane/admission/bindingdeclared.go:335-357,375-399`).
  - In a Workflow, a Function owner names a step and a Lake owner a declared lake (`api/types/v1alpha1/workflow.go:530-536`,
    `internal/workflow/reconcile_workflow.go:576-592`).
  - A write must fit the prefix's cap, then the Bucket's (`quoted.go:69-107`).
  - `maxObjectBytes` gives 413, or EntityTooLarge over S3, and refuses a presigned PUT with 403
    (`internal/blob/capped.go:30-42`, `internal/blob/s3gateway/backend.go:187-188`).
- **KVStore.** Round 4 checks the merged `KVTable.owner` in the round-4 paragraph above. Rounds 1 to 3 checked these
  facts:
  - A table's writers are its owner and a RolesAssignment's writers (`capabilities.go:163-177`).
  - The owner defaults to the Function that declares the store by name (`bindingdeclared.go:341-350`).
  - In a store that a Workflow declares, a step owner becomes `<workflow>-<step>` (`reconcile_workflow.go:555-562`,
    `workflow.go:521-526`).
- **SQLDatabase.**
  - An empty memory means 256Mi, and an empty cpu sets no limit (`internal/services/sql/reconcile.go:343-346`,
    `internal/runtime/runtime.go:195-204`).
  - A change to resources replaces the engine, and a change to the quota does not
    (`internal/provider/runtime.go:162-187`).
  - The quota counts the pages in use (`internal/services/sql/facade.go:167`). At the cap, an Execute whose statements
    are each a DELETE or a DROP still runs (`internal/services/sql/deletes.go:9-37`, `facade.go:131-138`).
  - `status.bindings` counts the Functions of the namespace that bind the database (`reconcile.go:309-323`).
- **Lake.**
  - The engine's environment: the key filter, a Secret's value winning over a ConfigMap's, and the Secret read asked as
    the Lake (`internal/services/lake/reconcile.go:544-575`, `internal/envresolve/envresolve.go:80-109`,
    `internal/secrets/secrets.go:93-104`).
  - A changed spec or bound value replaces the engine (`docs/behavior/services.md:49`).
  - The location must be one of `bindings.blob` and owned by the Lake (`api/types/v1alpha1/lake.go:144-154`,
    `reconcile.go:617-624`).
  - The catalog is kept in a store per namespace, keyed by the location (`lake.go:71-79`,
    `internal/blob/s3gateway/catalog.go:33-58`).
  - `spec.ingress` (`api/types/v1alpha1/ingress.go:41-50`, `lake.go:168-170`, `reconcile.go:448-494`).
  - `status.function`, `status.endpoint` and `status.proxyPort` (`internal/services/lake/lake.go:255`,
    `reconcile.go:61-78,174-223`, `internal/function/lake.go:55-68`, `internal/lake/gateway/manager.go:156-185`).
- **EventSource.**
  - Exactly one kind, with at least one event and unique event names (`api/types/v1alpha1/eventsource.go:127-256`).
  - A timer's data and a blob firing's data (`internal/eventing/cloudevent.go:43-69`).
  - The blob poll, and the seen-list reset for a new source or prefix (`internal/eventing/blobwatch.go:264-269,385-431`).
  - The SQL source: the table match and the truncation (`internal/services/sql/changefeed.go:346-353,424-523`).
  - The reasons when a source is not Ready (`internal/eventing/eventing.go:194-240`).
- **Sensor.**
  - `Validate`'s rules (`api/types/v1alpha1/sensor.go:61-119`).
  - The subscriptions and the attempts (`internal/sensor/sensor.go:261-279,333-389`, `internal/sensor/retry.go:454`).
  - The WorkflowRun a Sensor starts, and the gate on its input when it starts (`sensor.go:498-513,683-694`,
    `internal/workflow/engine.go:297-305`).
  - The input projection (`sensor.go:590-667`, `internal/sensor/resolver.go:17-33`, `internal/expr/expr.go:101-105`).
- **Shared types and wording.** Each tag reads true in every place that it shows. The Lake's root entries say "a Lake
  applied on its own", and the event and Sensor tags say "this namespace" and "the Sensor's namespace". No tag repeats
  what the schema shows. "funcd does not read it" (`SecretSpec.type`) and "funcd makes no object of that name and does
  not read it" (`LakeStatus.function`) are exact.

### Recommendation

Pass. Nothing of this batch is left to fix. Minor 1, the reach of the pin rule and the #1235 point wait for the decider.
