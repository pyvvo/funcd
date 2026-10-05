# Changelog

## [0.6.0](https://github.com/pyvvo/funcd/compare/v0.5.0...v0.6.0) (2026-10-05)


### Features

* **catalog:** keep a consumer's catalog URL across a catalog delete and a daemon restart (ADR-0162) ([#692](https://github.com/pyvvo/funcd/issues/692)) ([24e3d0a](https://github.com/pyvvo/funcd/commit/24e3d0aa95adc14efaf3aa981d4b935b68d55465))
* **config:** make the retry, requeue, supervision and drain times config keys (ADR-0163) ([#691](https://github.com/pyvvo/funcd/issues/691)) ([1b4ec5c](https://github.com/pyvvo/funcd/commit/1b4ec5c5e375f639518d5f41ea8815483fb244cb))
* **function:** keep Revisions read-only, fail closed when one is missing, and stamp every Function name (ADR-0172) ([#689](https://github.com/pyvvo/funcd/issues/689)) ([24f604f](https://github.com/pyvvo/funcd/commit/24f604f8f33bbcd7328a8b545634eba5b72ad33e))
* **function:** write every failed pass's status and hand out only listening workers (ADR-0161) ([#685](https://github.com/pyvvo/funcd/issues/685)) ([5881659](https://github.com/pyvvo/funcd/commit/5881659b06149285b9bb96c45a20cdec0e414c74))
* **invoke:** forward traceparent on fn-to-fn calls and keep them out of edge observ ([#693](https://github.com/pyvvo/funcd/issues/693)) ([b437326](https://github.com/pyvvo/funcd/commit/b4373262d0c8daebde0bc51f542c36dc4c329d40))
* **runtime:** read raw worker output through pipes and bound log records ([#694](https://github.com/pyvvo/funcd/issues/694)) ([6baf945](https://github.com/pyvvo/funcd/commit/6baf945a3660d66972f8fa9059a8eed74a06f15b))


### Bug Fixes

* **auth:** stable Cedar built-in ids and is-in scope type check ([#686](https://github.com/pyvvo/funcd/issues/686)) ([599d663](https://github.com/pyvvo/funcd/commit/599d663384a00e0ca9aa1eabb5d0959d1b42bb34))
* **function:** keep a pooled member serving through a failed gate ([#695](https://github.com/pyvvo/funcd/issues/695)) ([5644bb4](https://github.com/pyvvo/funcd/commit/5644bb42069ec21aa6b944171679466fa82addb1))
* **function:** report a pooled member redeployed to an unloadable handler ShapeInvalid ([#743](https://github.com/pyvvo/funcd/issues/743)) ([a598f84](https://github.com/pyvvo/funcd/commit/a598f84f26c93ee33a3dcb9ae0e0ab09e5127f76))
* **procreg:** wait for the test child's argv before checking ownership ([#681](https://github.com/pyvvo/funcd/issues/681)) ([2fbf623](https://github.com/pyvvo/funcd/commit/2fbf62383f7d408e9bcb69666a91d1f9e18d252f))

## [0.5.0](https://github.com/pyvvo/funcd/compare/v0.4.0...v0.5.0) (2026-10-05)


### Features

* **auth:** scope each Policy to its own namespace ([#674](https://github.com/pyvvo/funcd/issues/674)) ([6f24319](https://github.com/pyvvo/funcd/commit/6f243194a9efebfedbf2df8378f427307bbdda8a))
* **pooling:** name the pool member on every channel and pool by access ([#677](https://github.com/pyvvo/funcd/issues/677)) ([4fe2903](https://github.com/pyvvo/funcd/commit/4fe29030940cd90cdf180e8b88a7f5f9133cb6e0))
* **runtime:** reap the workers a crashed funcd left behind at boot (ADR-0167) ([#672](https://github.com/pyvvo/funcd/issues/672)) ([aa7090c](https://github.com/pyvvo/funcd/commit/aa7090c8835ef1576669861ef296ec0ac0db3b57))
* **workflow:** bind a Workflow only to the KV stores it made ([#669](https://github.com/pyvvo/funcd/issues/669)) ([8108118](https://github.com/pyvvo/funcd/commit/81081188feff14c10fcbd785d297bebd6b4aba05))


### Bug Fixes

* **function:** keep a Failed Function Failed until a new spec or a started retry (ADR-0169) ([#671](https://github.com/pyvvo/funcd/issues/671)) ([c791437](https://github.com/pyvvo/funcd/commit/c791437020c378234f39cce0304409b543e0fafa))
* **workflow:** delete the run in TestScenarioDeletedRunStops without a stale precondition ([#668](https://github.com/pyvvo/funcd/issues/668)) ([ad92960](https://github.com/pyvvo/funcd/commit/ad92960be446508d18a06106261a357c4b25a344))

## [0.4.0](https://github.com/pyvvo/funcd/compare/v0.3.0...v0.4.0) (2026-10-05)


### ⚠ BREAKING CHANGES

* **catalog:** resolve an Identity catalog token by its owner in two store reads ([#640](https://github.com/pyvvo/funcd/issues/640))
* **edge:** an external invoke whose response does not start within 60 s now gets 504 Gateway Timeout. Raise the limit per Function with `spec.timeout` (up to 1h), or for the whole daemon with `invoke.defaultTimeout`.

### Features

* atomic admission and a nested-call in-flight cap (ADR-0147) ([#637](https://github.com/pyvvo/funcd/issues/637)) ([aa55933](https://github.com/pyvvo/funcd/commit/aa55933deb5e76a993dbab8f82d89f0ead8a952f))
* **auth:** give catalog engines their own S3 keys and principal ([#655](https://github.com/pyvvo/funcd/issues/655)) ([f94ab85](https://github.com/pyvvo/funcd/commit/f94ab85a9a7ced407761d15e42701111ddf6116c))
* **auth:** static admin, developer and viewer tokens from auth.credentials (ADR-0171) ([#653](https://github.com/pyvvo/funcd/issues/653)) ([f6d6b39](https://github.com/pyvvo/funcd/commit/f6d6b3952540b16f43221479e8f959b2ffe597db))
* **blob:** one content ETag on every S3 read, and ETag conditional requests (ADR-0159) ([#645](https://github.com/pyvvo/funcd/issues/645)) ([0c88ed2](https://github.com/pyvvo/funcd/commit/0c88ed249a1efbd8699fa9b416f4c07b5b2e5263))
* **catalog:** resolve an Identity catalog token by its owner in two store reads ([#640](https://github.com/pyvvo/funcd/issues/640)) ([720f60d](https://github.com/pyvvo/funcd/commit/720f60d7135658972011478314be29865f164dba))
* **edge:** apply one claim rule set to Routes and catalog ingress ([#641](https://github.com/pyvvo/funcd/issues/641)) ([15ef7a5](https://github.com/pyvvo/funcd/commit/15ef7a552e8f57251ed450270935cf0ca8f73bce))
* **edge:** bound every external invoke with a response deadline (ADR-0151) ([#638](https://github.com/pyvvo/funcd/issues/638)) ([8487e94](https://github.com/pyvvo/funcd/commit/8487e945374be30ecc172e0a8b1b96d5a301116a))
* **edge:** rate-limit each resolved function and keep drained buckets ([#646](https://github.com/pyvvo/funcd/issues/646)) ([59cd4e0](https://github.com/pyvvo/funcd/commit/59cd4e0415575b2f523469048384d9585524a399))
* **eventing:** fire every listed blob object via a pruned, located seen list (ADR-0157) ([#661](https://github.com/pyvvo/funcd/issues/661)) ([d80f02c](https://github.com/pyvvo/funcd/commit/d80f02c1fb562cac5ca6648631e6b62ad4f10a52))
* **function:** report a runtime the node cannot serve as RuntimeUnavailable ([#642](https://github.com/pyvvo/funcd/issues/642)) ([082f3f7](https://github.com/pyvvo/funcd/commit/082f3f77ac26cb9d8963a22af85e16fe258f13a1))
* **function:** retry a worker that crashes while booting with a growing wait (ADR-0160) ([#648](https://github.com/pyvvo/funcd/issues/648)) ([54b65b3](https://github.com/pyvvo/funcd/commit/54b65b363dfa21aac59902fd18c7ca1179ae26b8))
* **gc:** collect the children of deleted owners and guard ResourceGroup deletes ([#657](https://github.com/pyvvo/funcd/issues/657)) ([46befe0](https://github.com/pyvvo/funcd/commit/46befe0682a5a53418f449b0981dbf5da776b26d))
* **httpx:** reuse connections and skip the proxy on every node-local pool (ADR-0155) ([#647](https://github.com/pyvvo/funcd/issues/647)) ([2aea7a1](https://github.com/pyvvo/funcd/commit/2aea7a1174eb6ed6c97e2c8fe66807cfc509a1c2))
* **runtime:** workers carry their owner kind, so each reconciler manages only its own workers ([#639](https://github.com/pyvvo/funcd/issues/639)) ([06359d0](https://github.com/pyvvo/funcd/commit/06359d08b5fb9a7503e1852d03689a1ae08b58b4))
* **sensor:** run every Sensor delivery on a bounded worker queue (ADR-0156) ([#651](https://github.com/pyvvo/funcd/issues/651)) ([7e864a0](https://github.com/pyvvo/funcd/commit/7e864a0541c6e8fab2f95c5c63fa6ae7dfb256ec))
* **workflow:** drive each WorkflowRun on an engine-owned goroutine with a short reconcile (ADR-0146) ([#656](https://github.com/pyvvo/funcd/issues/656)) ([cdd2103](https://github.com/pyvvo/funcd/commit/cdd2103344cbbd7d0f03402840d43ca39bdf5983))
* **workflow:** type skipped join-any branches and void inputs at reconcile (ADR-0166) ([#652](https://github.com/pyvvo/funcd/issues/652)) ([b9bd346](https://github.com/pyvvo/funcd/commit/b9bd34607271199391e9ae53529f7a1ee7602bec))


### Bug Fixes

* **catalog:** resend a query that a dead parked engine connection never sent ([#635](https://github.com/pyvvo/funcd/issues/635)) ([3cdc902](https://github.com/pyvvo/funcd/commit/3cdc90223f3b9731791ad8706b99b952bbadee9f))
* **catalog:** start a catalog consumer only on its live proxy URL ([#665](https://github.com/pyvvo/funcd/issues/665)) ([9d243b9](https://github.com/pyvvo/funcd/commit/9d243b9a89d757f1716f650c59e8fd0e713bf230))
* **containerd:** change the CNI attachment ID of unrevisioned workers ([#654](https://github.com/pyvvo/funcd/issues/654)) ([8242f10](https://github.com/pyvvo/funcd/commit/8242f10a4de95738a8df2bcb814b00c60746f7e4))
* **expr:** bound string searches and every stored step output ([#643](https://github.com/pyvvo/funcd/issues/643)) ([0cfd414](https://github.com/pyvvo/funcd/commit/0cfd414eb6ac92f5aad8aece4089bd9cc28322ca))
* **function:** report a never-booted revision Unknown, not Ready (ADR-0174) ([#644](https://github.com/pyvvo/funcd/issues/644)) ([3e3af59](https://github.com/pyvvo/funcd/commit/3e3af599b7a2853436cbeae6ccbc253dd1602653))
* **identity:** resolve Identity credentials only through the Secret the Identity controls ([#664](https://github.com/pyvvo/funcd/issues/664)) ([b6de96b](https://github.com/pyvvo/funcd/commit/b6de96b01904bd8d1e4006134bc0c8dcf40ff293))
* **runtime:** keep the private containerd running until Close when the Ensure context is cancelled ([#633](https://github.com/pyvvo/funcd/issues/633)) ([6c0aeda](https://github.com/pyvvo/funcd/commit/6c0aedaef0c9a862d6b5eb5a0594571987fd2a16))
* **runtime:** stop containerd workers on shutdown before the egress fence comes down ([#625](https://github.com/pyvvo/funcd/issues/625)) ([d0aa10d](https://github.com/pyvvo/funcd/commit/d0aa10db392737ae374918f97356566da25fc586))
* **workflow:** end a waited run's Ready=False wait when its record is mirrored ([#663](https://github.com/pyvvo/funcd/issues/663)) ([a07762c](https://github.com/pyvvo/funcd/commit/a07762c15593c5a303035fac4b4055dbeaaef972))
* **workflow:** record an inline child run as &lt;parentRun&gt;.&lt;step&gt; (ADR-0154) ([#659](https://github.com/pyvvo/funcd/issues/659)) ([86cb940](https://github.com/pyvvo/funcd/commit/86cb940b0914be0dbc4cfc79e6133e72669d5daf))

## [0.3.0](https://github.com/pyvvo/funcd/compare/v0.2.5...v0.3.0) (2026-10-05)


### Features

* **fault:** answer 413 for every size cap funcd enforces (ADR-0148) ([#631](https://github.com/pyvvo/funcd/issues/631)) ([0204582](https://github.com/pyvvo/funcd/commit/0204582d2c4747b4fc813792267cc0f036a78572))


### Bug Fixes

* **artifact:** refuse a single-file artifact title that is not a plain file name ([#626](https://github.com/pyvvo/funcd/issues/626)) ([fe49615](https://github.com/pyvvo/funcd/commit/fe49615174edc94c815e7785557e3de4165a3ea1))
* **blob:** keep bucket listings inside the caller's bound prefix ([#613](https://github.com/pyvvo/funcd/issues/613)) ([8eec6cb](https://github.com/pyvvo/funcd/commit/8eec6cb671b5d08d5263865faa3fb07005502062))
* **blob:** refuse an empty key on the file backend ([#611](https://github.com/pyvvo/funcd/issues/611)) ([915f342](https://github.com/pyvvo/funcd/commit/915f3427b8a774f58312af09a7a0dfee7637ea56))
* **bus:** stop the embedded NATS server from listening on the network ([#618](https://github.com/pyvvo/funcd/issues/618)) ([2bf921f](https://github.com/pyvvo/funcd/commit/2bf921fb04036acfe73976fadd7053fa2541c1a3))
* **catalog:** deny catalog queries from principals outside the catalog's namespace ([#620](https://github.com/pyvvo/funcd/issues/620)) ([406f84c](https://github.com/pyvvo/funcd/commit/406f84cd20ee989677972fd443f0209083449384))
* **containerd:** clean up a worker whose Create fails ([#602](https://github.com/pyvvo/funcd/issues/602)) ([14bce79](https://github.com/pyvvo/funcd/commit/14bce7965005771e161cf6e8f0ce569226f4b4f8))
* **containerd:** pull normalized image refs and honor --image overrides ([#597](https://github.com/pyvvo/funcd/issues/597)) ([1ca62fe](https://github.com/pyvvo/funcd/commit/1ca62fe54a65148453a4bc376ed0be6be564ae52))
* **dataplane:** keep upstream and worker addresses out of problem details ([#614](https://github.com/pyvvo/funcd/issues/614)) ([85a6310](https://github.com/pyvvo/funcd/commit/85a6310ed0d04a48f94861d9deabfaf5f4358635))
* **dataplane:** label edge metrics for a missing function as unresolved ([#624](https://github.com/pyvvo/funcd/issues/624)) ([d17e1c2](https://github.com/pyvvo/funcd/commit/d17e1c23b7bb54a17be65f6476d364c1242a3dc0))
* **expr:** bound expression evaluation cost and cap pass step outputs ([#632](https://github.com/pyvvo/funcd/issues/632)) ([9ecba7d](https://github.com/pyvvo/funcd/commit/9ecba7df8cca3ca2db430f9cb5044f46548357ec))
* **funcdctl:** escape control characters when printing log records ([#615](https://github.com/pyvvo/funcd/issues/615)) ([ca96fc4](https://github.com/pyvvo/funcd/commit/ca96fc4286bbd29cc4d6879272eb3f88b648bee9))
* **funcdctl:** start dev again on a fresh S3 port when the picked one is taken ([#629](https://github.com/pyvvo/funcd/issues/629)) ([dc3974b](https://github.com/pyvvo/funcd/commit/dc3974bdec87c6e62381d4926b5cb825c6d8c611))
* **funcd:** refuse worker pooling combined with container execution ([#628](https://github.com/pyvvo/funcd/issues/628)) ([8c560ba](https://github.com/pyvvo/funcd/commit/8c560badd44b1435177489b4b575b46033697fd2))
* **funclog:** bound the replica part of a trace segment key ([#608](https://github.com/pyvvo/funcd/issues/608)) ([5dbb7fb](https://github.com/pyvvo/funcd/commit/5dbb7fb18fa5899011c56576ce77da7e68377811))
* **funclog:** user log attrs no longer override a line's inv and source ([#623](https://github.com/pyvvo/funcd/issues/623)) ([762d2fa](https://github.com/pyvvo/funcd/commit/762d2fa37e501d4b44631baaa53a372cf9693d3b))
* **function:** back off before re-creating a failed pool host ([#603](https://github.com/pyvvo/funcd/issues/603)) ([0a29b5a](https://github.com/pyvvo/funcd/commit/0a29b5ab7a386723aced48a74c5a5f5732de37f5))
* **function:** give each namespace's pool worker its own manifest file ([#617](https://github.com/pyvvo/funcd/issues/617)) ([464f08a](https://github.com/pyvvo/funcd/commit/464f08aa1fa0fb2f904afee45c802ea7947af5b6))
* **function:** name each worker's spans after its Function ([#605](https://github.com/pyvvo/funcd/issues/605)) ([f93325d](https://github.com/pyvvo/funcd/commit/f93325d27d8673cd7f620cf2d9489ad61e081af9))
* report over-cap site objects and page S3 listings under 4 MiB ([#596](https://github.com/pyvvo/funcd/issues/596)) ([2543622](https://github.com/pyvvo/funcd/commit/2543622aadce18622f54be17413413b4404dc045))
* **route:** a static Route prefix must end with "/" so it never serves sibling Bucket prefixes ([#621](https://github.com/pyvvo/funcd/issues/621)) ([8a2d708](https://github.com/pyvvo/funcd/commit/8a2d7081bf73933b2281258b749bff9ef3df2dab))
* **runtime:** keep the daemon's environment out of process-driver workers ([#616](https://github.com/pyvvo/funcd/issues/616)) ([1dc8646](https://github.com/pyvvo/funcd/commit/1dc864655ca9e08efc78fe70b4ee25021664b7be))
* **s3gateway:** bind a multipart upload id to its namespace, bucket and key ([#619](https://github.com/pyvvo/funcd/issues/619)) ([74cd07c](https://github.com/pyvvo/funcd/commit/74cd07c87c1f958e933a67b706429624c1f10160))
* **s3gateway:** honor date preconditions and create-only puts ([#600](https://github.com/pyvvo/funcd/issues/600)) ([c4dab54](https://github.com/pyvvo/funcd/commit/c4dab54942945998fb6530ca577ea67b3f095a28))
* **scripts:** give each lane run its own checkout ([#599](https://github.com/pyvvo/funcd/issues/599)) ([eb7adfe](https://github.com/pyvvo/funcd/commit/eb7adfe8e6a25561b7233a1aa526f0310542dbc0))
* **sdk:** use the server's path for WorkerNode ([#607](https://github.com/pyvvo/funcd/issues/607)) ([436e8b4](https://github.com/pyvvo/funcd/commit/436e8b47088115e5273e104b3a418eb27d7fe807))
* **tls:** refuse TLS with no storage dir instead of keeping keys in the shared temp dir ([#622](https://github.com/pyvvo/funcd/issues/622)) ([d106b9c](https://github.com/pyvvo/funcd/commit/d106b9c638acbd8ddb8f48a08bf1ea788940e18a))
* **workflow:** drain a step's response body so retries reuse the connection ([#598](https://github.com/pyvvo/funcd/issues/598)) ([0b8335c](https://github.com/pyvvo/funcd/commit/0b8335c55d96fdd9919dc7f7ec6535f21c13262d))
* **workflow:** reject a void onFailure handler and params on a void step at reconcile ([#606](https://github.com/pyvvo/funcd/issues/606)) ([f3ccaa5](https://github.com/pyvvo/funcd/commit/f3ccaa5458986bb3f2b8e992db0bda63b15fd27e))

## [0.2.5](https://github.com/pyvvo/funcd/compare/v0.2.4...v0.2.5) (2026-10-03)


### Bug Fixes

* **artifact:** give oras-go a transport of its own ([#585](https://github.com/pyvvo/funcd/issues/585)) ([113aa5e](https://github.com/pyvvo/funcd/commit/113aa5e942a9af227b066624f38a1c76d3fa3d96))
* **artifact:** resolve a cached single-file bundle to its handler, never a directory ([#584](https://github.com/pyvvo/funcd/issues/584)) ([13b723c](https://github.com/pyvvo/funcd/commit/13b723c8586fb2a64b19b2cea6759efccf94042d))
* connect NATS in process, share test helpers, drop sleeps ([#559](https://github.com/pyvvo/funcd/issues/559)) ([#578](https://github.com/pyvvo/funcd/issues/578)) ([e6b7974](https://github.com/pyvvo/funcd/commit/e6b7974fafea26a3b771952d558cb3942758d99a))
* **function:** stabilize revision drain and s3gateway shutdown flakes ([#586](https://github.com/pyvvo/funcd/issues/586)) ([addb4bc](https://github.com/pyvvo/funcd/commit/addb4bc3c8f44dfacb8213357679e916e13f93e8))
* **platform:** give every HTTP caller its own transport ([#570](https://github.com/pyvvo/funcd/issues/570)) ([da24df4](https://github.com/pyvvo/funcd/commit/da24df4670cd9440f50d1931ed436fd37f64cec0))
* **workernode:** log a failed local API Serve and drop its dead socket ([#559](https://github.com/pyvvo/funcd/issues/559)) ([#579](https://github.com/pyvvo/funcd/issues/579)) ([458c565](https://github.com/pyvvo/funcd/commit/458c5658e14a683841175e1dda50dcc811b71532))

## [0.2.4](https://github.com/pyvvo/funcd/compare/v0.2.3...v0.2.4) (2026-10-03)


### Bug Fixes

* **catalog:** keep the catalog proxy's engine calls off http.DefaultTransport ([#537](https://github.com/pyvvo/funcd/issues/537)) ([9466b23](https://github.com/pyvvo/funcd/commit/9466b23fb7697e79bb13221a949462554f94962e))
* **deps:** pin funcd-typescript v0.4.3 and funcd-python v0.3.4 ([#541](https://github.com/pyvvo/funcd/issues/541)) ([3efb66e](https://github.com/pyvvo/funcd/commit/3efb66ed3cbab90477cbfef60f10cad360921e81))
* **deps:** pin funcd-typescript v0.4.4 and funcd-python v0.3.5 ([#543](https://github.com/pyvvo/funcd/issues/543)) ([03cc357](https://github.com/pyvvo/funcd/commit/03cc357123297b45669d880fd90a382fc33c649a))
* **edge:** send Vary: Accept-Encoding on 304 and 206 responses under edge compression ([#528](https://github.com/pyvvo/funcd/issues/528)) ([090f969](https://github.com/pyvvo/funcd/commit/090f9692dd67985438849d536ab25bf621357eab))
* **funcdctl:** fix dev reload, restart and startup gaps and SDK nullable types ([#520](https://github.com/pyvvo/funcd/issues/520)) ([#535](https://github.com/pyvvo/funcd/issues/535)) ([a2d51d3](https://github.com/pyvvo/funcd/commit/a2d51d3800037a1821091a4118675b7b2929980f))
* **funcd:** shut down on setup errors, keep socket, validate header ([#519](https://github.com/pyvvo/funcd/issues/519)) ([#530](https://github.com/pyvvo/funcd/issues/530)) ([3d4ca0b](https://github.com/pyvvo/funcd/commit/3d4ca0bf4d68b8e9b77189d79cc7f5c485a60965))
* **function:** harden shim test helper and share real-shim setup ([#525](https://github.com/pyvvo/funcd/issues/525)) ([#538](https://github.com/pyvvo/funcd/issues/538)) ([0447ae8](https://github.com/pyvvo/funcd/commit/0447ae8c014d4924a7a644809134f7bb91964b72))
* **observability:** shut down exporters, route logs via the configured logger ([#526](https://github.com/pyvvo/funcd/issues/526)) ([#539](https://github.com/pyvvo/funcd/issues/539)) ([5d4603d](https://github.com/pyvvo/funcd/commit/5d4603d1969c2126b896397e025767ea093d983a))
* **runtime:** clean up failed Create logs and reuse imported image ([#521](https://github.com/pyvvo/funcd/issues/521)) ([#533](https://github.com/pyvvo/funcd/issues/533)) ([0f04485](https://github.com/pyvvo/funcd/commit/0f044853014d8c12cbc46d049394d42da2438377))
* **services:** S3 gateway ModTime and bind failure, provider doc, test helper ([#523](https://github.com/pyvvo/funcd/issues/523)) ([#536](https://github.com/pyvvo/funcd/issues/536)) ([58ee7af](https://github.com/pyvvo/funcd/commit/58ee7af1673cee92cec356dce396ac04a2496ca4))
* **skills:** keep fix-batch regression test, drop stale lint-lock note ([#524](https://github.com/pyvvo/funcd/issues/524)) ([#529](https://github.com/pyvvo/funcd/issues/529)) ([485e53a](https://github.com/pyvvo/funcd/commit/485e53af4ef4b23476cdb98bcb903a04e6787063))
* **workflow:** keep input defaults, reject non-null void input, fix docs ([#522](https://github.com/pyvvo/funcd/issues/522)) ([#532](https://github.com/pyvvo/funcd/issues/532)) ([3777821](https://github.com/pyvvo/funcd/commit/3777821d0908ff7941b71eae3a1aa749be73d893))

## [0.2.3](https://github.com/pyvvo/funcd/compare/v0.2.2...v0.2.3) (2026-10-03)


### Bug Fixes

* **control-plane:** close races, leaks and config gaps from wave 3 ([#471](https://github.com/pyvvo/funcd/issues/471)) ([#481](https://github.com/pyvvo/funcd/issues/481)) ([86f4b87](https://github.com/pyvvo/funcd/commit/86f4b871a291e37f0e8d5bcfdef0536c3c16f579))
* **dataplane:** edge headers, panic log, gzip ETag, slog upstream errors ([#466](https://github.com/pyvvo/funcd/issues/466)) ([#484](https://github.com/pyvvo/funcd/issues/484)) ([611c9f4](https://github.com/pyvvo/funcd/commit/611c9f4dc57680f439a392a44317c4f77e54758a))
* **deps:** pin funcd-typescript v0.4.2 and funcd-python v0.3.3 ([#483](https://github.com/pyvvo/funcd/issues/483)) ([4dc2935](https://github.com/pyvvo/funcd/commit/4dc2935c1e74ac509e45303482e35e6c77924272))
* **docs:** TLS dir hint and revert-check guidance ([#472](https://github.com/pyvvo/funcd/issues/472)) ([#478](https://github.com/pyvvo/funcd/issues/478)) ([912c5cd](https://github.com/pyvvo/funcd/commit/912c5cdbbbfa960830e880b9582554a15441e53b))
* **funcdctl:** harden dev hot-reload, boot and S3 startup ([#465](https://github.com/pyvvo/funcd/issues/465)) ([#480](https://github.com/pyvvo/funcd/issues/480)) ([42c2247](https://github.com/pyvvo/funcd/commit/42c2247566f371fa34cccde3c6dce7b5ea5245c3))
* **funcd:** drain logs on shutdown, route http server errors to slog ([#473](https://github.com/pyvvo/funcd/issues/473)) ([#482](https://github.com/pyvvo/funcd/issues/482)) ([21460ca](https://github.com/pyvvo/funcd/commit/21460ca88df8682d85538bbbb5d186b5a4c86757))
* **function:** pool worker replacement, error wording, key order, index path ([#468](https://github.com/pyvvo/funcd/issues/468)) ([#476](https://github.com/pyvvo/funcd/issues/476)) ([6d99f6d](https://github.com/pyvvo/funcd/commit/6d99f6dce70aed3b8f3e41ce90244efbc382f352))
* **lint:** let golangci-lint runs from parallel checkouts on one host succeed ([#475](https://github.com/pyvvo/funcd/issues/475)) ([8910bad](https://github.com/pyvvo/funcd/commit/8910bad02b0197ddcd9b1033e4cbda89a3443a88))
* **runtime:** clean replaced worker logs, unpack present images ([#469](https://github.com/pyvvo/funcd/issues/469)) ([#477](https://github.com/pyvvo/funcd/issues/477)) ([93508d8](https://github.com/pyvvo/funcd/commit/93508d8c30988e4035fb71a3c9414d7a0beb6b18))
* **services:** s3gateway, blob, kv and probe fixes from wave 3 ([#470](https://github.com/pyvvo/funcd/issues/470)) ([#485](https://github.com/pyvvo/funcd/issues/485)) ([8257711](https://github.com/pyvvo/funcd/commit/825771131da1d57f0b1537389e96dbe6463afe13))
* **workflow:** seven eventing and workflow wave-3 findings ([#467](https://github.com/pyvvo/funcd/issues/467)) ([#486](https://github.com/pyvvo/funcd/issues/486)) ([45a130c](https://github.com/pyvvo/funcd/commit/45a130ce6d5467df64583325fb26ce1e9c2f6e07))

## [0.2.2](https://github.com/pyvvo/funcd/compare/v0.2.1...v0.2.2) (2026-10-03)


### Bug Fixes

* **ci:** lint dev-tag packages, close test platform, block-style e2e YAML ([#383](https://github.com/pyvvo/funcd/issues/383)) ([#397](https://github.com/pyvvo/funcd/issues/397)) ([4d64fbf](https://github.com/pyvvo/funcd/commit/4d64fbf2ad4a753b186b652bd935dee10dae6ea7))
* **controlplane:** close idle conns, sync Site status, validate config ([#385](https://github.com/pyvvo/funcd/issues/385)) ([#409](https://github.com/pyvvo/funcd/issues/409)) ([a5db9d5](https://github.com/pyvvo/funcd/commit/a5db9d5c7067033b7f7cc893e1347bab40533d77))
* **deps:** pin funcd-typescript v0.4.1 and funcd-python v0.3.2 ([#413](https://github.com/pyvvo/funcd/issues/413)) ([3346c65](https://github.com/pyvvo/funcd/commit/3346c65185022321b86c8dd005e6cf5e362999cb))
* **docs:** document every config key and the built secret injection ([#390](https://github.com/pyvvo/funcd/issues/390)) ([#403](https://github.com/pyvvo/funcd/issues/403)) ([3c4e232](https://github.com/pyvvo/funcd/commit/3c4e232e037546c2b18421ce822943d37ecc977d))
* **edge:** gzip, panic and problem-JSON data-plane fixes ([#386](https://github.com/pyvvo/funcd/issues/386)) ([#399](https://github.com/pyvvo/funcd/issues/399)) ([d9599dd](https://github.com/pyvvo/funcd/commit/d9599dde4d7c76d4615677c53a8474dff9decdbc))
* **funcdctl:** fix manifest decoding and dev/apply/inspect defects ([#384](https://github.com/pyvvo/funcd/issues/384)) ([#405](https://github.com/pyvvo/funcd/issues/405)) ([e4a198b](https://github.com/pyvvo/funcd/commit/e4a198b5de56a5c1bb40d65df07403b71d286767))
* **funcdctl:** retry a dev re-apply that loses its update to a controller ([#408](https://github.com/pyvvo/funcd/issues/408)) ([aa70d03](https://github.com/pyvvo/funcd/commit/aa70d037cae43e859bbc07706ec20480c75e0b22))
* **function:** repair readiness, pool, socket and artifact packing faults ([#387](https://github.com/pyvvo/funcd/issues/387)) ([#410](https://github.com/pyvvo/funcd/issues/410)) ([65dc526](https://github.com/pyvvo/funcd/commit/65dc5266457d97839031a909e97264b450c81403))
* **observ:** count handler panics and stop Pump read-error spin ([#388](https://github.com/pyvvo/funcd/issues/388)) ([#400](https://github.com/pyvvo/funcd/issues/400)) ([ecef0fb](https://github.com/pyvvo/funcd/commit/ecef0fb428035e4abd4daee5600f4b4d3bdaf133))
* **runtime:** close temp-file, DNS forwarder and snapshotter leaks ([#391](https://github.com/pyvvo/funcd/issues/391)) ([#406](https://github.com/pyvvo/funcd/issues/406)) ([681fb82](https://github.com/pyvvo/funcd/commit/681fb8247224a4fba0dbdcab3dbcdd85c7267f2e))
* **services:** bound, log and harden catalog, blob, kv and provider paths ([#389](https://github.com/pyvvo/funcd/issues/389)) ([#402](https://github.com/pyvvo/funcd/issues/402)) ([eb39f8b](https://github.com/pyvvo/funcd/commit/eb39f8b3b1c02d3b7239ffba99475c96ecf30027))
* **test:** stop s3, lint-fixture and artifact seam test flakes ([#411](https://github.com/pyvvo/funcd/issues/411)) ([9e319ce](https://github.com/pyvvo/funcd/commit/9e319ce0b6eea1053b19ac9e512f0ce0da90a1f3))
* **workflow:** fix the eventing fix-wave findings ([#382](https://github.com/pyvvo/funcd/issues/382)) ([#407](https://github.com/pyvvo/funcd/issues/407)) ([c407273](https://github.com/pyvvo/funcd/commit/c40727325c6d0dca6807fe66fa309838e79bccca))
* **workflow:** ignore a cancel on a run that is already terminal ([#401](https://github.com/pyvvo/funcd/issues/401)) ([6fe883a](https://github.com/pyvvo/funcd/commit/6fe883abb0db90539ea74c3694fd63a360b2a68a))

## [0.2.1](https://github.com/pyvvo/funcd/compare/v0.2.0...v0.2.1) (2026-10-02)


### Bug Fixes

* **activator:** stop idle reclaim from killing busy functions ([#208](https://github.com/pyvvo/funcd/issues/208)) ([#260](https://github.com/pyvvo/funcd/issues/260)) ([969a179](https://github.com/pyvvo/funcd/commit/969a1790010a6f92e9b8b70dafb1f057a9d74c30))
* **artifact:** fix four artifact push, pull and platform defects ([#195](https://github.com/pyvvo/funcd/issues/195)) ([#256](https://github.com/pyvvo/funcd/issues/256)) ([0bce5f9](https://github.com/pyvvo/funcd/commit/0bce5f99b897fca43e89ad941e6dd8965921e4f0))
* **artifact:** keep a manifest's digest when identical content is re-pushed ([#250](https://github.com/pyvvo/funcd/issues/250)) ([2f9ab54](https://github.com/pyvvo/funcd/commit/2f9ab5454e22417b29389363f25f537cdbb0e40a))
* **badger:** split KV and DLQ writes that overflow one txn ([#200](https://github.com/pyvvo/funcd/issues/200)) ([#241](https://github.com/pyvvo/funcd/issues/241)) ([f7afd53](https://github.com/pyvvo/funcd/commit/f7afd534f13e4cd121c5a085bece3de9dcbadcc6))
* **blob:** enforce maxObjectBytes and reject unstorable file keys ([#227](https://github.com/pyvvo/funcd/issues/227)) ([#271](https://github.com/pyvvo/funcd/issues/271)) ([5a4a4ff](https://github.com/pyvvo/funcd/commit/5a4a4ff301a01cec4fbd2f4429f7fb3e4ac848a3))
* **catalog:** relaunch crashed dev engine, start Created engines ([#225](https://github.com/pyvvo/funcd/issues/225)) ([#274](https://github.com/pyvvo/funcd/issues/274)) ([6eef614](https://github.com/pyvvo/funcd/commit/6eef614d20b99c24d647cb2bc4185f3ad0aa1b35))
* **ci:** stop commit-msg-lint failing on long commit messages ([#251](https://github.com/pyvvo/funcd/issues/251)) ([0238784](https://github.com/pyvvo/funcd/commit/0238784408ac3268fae0491501d7a5898781cb5c))
* **config:** gate Function start on ConfigMap/Secret validity ([#215](https://github.com/pyvvo/funcd/issues/215)) ([#262](https://github.com/pyvvo/funcd/issues/262)) ([94d346e](https://github.com/pyvvo/funcd/commit/94d346ed3412c77729f988399209aa3491784de4))
* **controller:** keep one slow replica or run from stalling the worker ([#197](https://github.com/pyvvo/funcd/issues/197)) ([#284](https://github.com/pyvvo/funcd/issues/284)) ([069fcd8](https://github.com/pyvvo/funcd/commit/069fcd85cb742e7d120577fc0b1d86eb9ff8ffe1))
* **controller:** re-watch a kind after the store drops its watch ([#240](https://github.com/pyvvo/funcd/issues/240)) ([0f86f3e](https://github.com/pyvvo/funcd/commit/0f86f3e305117562ff9d98ad9dd0dbc1c4acc479))
* **controlplane:** handle delete and re-create of Function, Sensor, Bucket, KV ([#211](https://github.com/pyvvo/funcd/issues/211)) ([#254](https://github.com/pyvvo/funcd/issues/254)) ([2dc9d26](https://github.com/pyvvo/funcd/commit/2dc9d264193336c35868884ed1c8704376ebdf4f))
* **controlplane:** reject namespace mismatch, 409 on dup name, serve spec wire shape ([#212](https://github.com/pyvvo/funcd/issues/212)) ([#285](https://github.com/pyvvo/funcd/issues/285)) ([3cc30ce](https://github.com/pyvvo/funcd/commit/3cc30ce46ba0e93b444f2b41b3a577a4353b8828))
* **daemon:** bound the uploads and bodies the daemon holds in memory ([#199](https://github.com/pyvvo/funcd/issues/199)) ([#243](https://github.com/pyvvo/funcd/issues/243)) ([0bdc94f](https://github.com/pyvvo/funcd/commit/0bdc94f083eeac511f0f167ec5e1d3384b2f2fc5))
* **daemon:** make graceful shutdown keep logs and retries and stop on time ([#202](https://github.com/pyvvo/funcd/issues/202)) ([#247](https://github.com/pyvvo/funcd/issues/247)) ([4ab4456](https://github.com/pyvvo/funcd/commit/4ab445626083cfb283aded20e29ae43335e9c610))
* **edge:** emit the edge SERVER span the injected traceparent names ([#218](https://github.com/pyvvo/funcd/issues/218)) ([#265](https://github.com/pyvvo/funcd/issues/265)) ([c77c907](https://github.com/pyvvo/funcd/commit/c77c9075d6a02d7671277d2f0336c7743b66a292))
* **edge:** fix ingress limit bypass, key memory and negative limits ([#219](https://github.com/pyvvo/funcd/issues/219)) ([#255](https://github.com/pyvvo/funcd/issues/255)) ([825804f](https://github.com/pyvvo/funcd/commit/825804fb159b4d88d2dc0e056fb55e8a62b945e6))
* **edge:** static asset path, ETag and gzip status bugs ([#226](https://github.com/pyvvo/funcd/issues/226)) ([#264](https://github.com/pyvvo/funcd/issues/264)) ([09b3a17](https://github.com/pyvvo/funcd/commit/09b3a1749507b2f8683ec8ec004539d924d16272))
* **egress:** serve DNS over TCP and evict stale correlations ([#231](https://github.com/pyvvo/funcd/issues/231)) ([#281](https://github.com/pyvvo/funcd/issues/281)) ([556e684](https://github.com/pyvvo/funcd/commit/556e68435c242a075578104199656357e8efb45a))
* **error-reporting:** report load, stream, 422 and cap errors correctly ([#216](https://github.com/pyvvo/funcd/issues/216)) ([#282](https://github.com/pyvvo/funcd/issues/282)) ([e4bbf19](https://github.com/pyvvo/funcd/commit/e4bbf19eb176113007307f210fb4404ece86ccd0))
* **eventing:** fix timer intervals, Sensor action input and DLQ replay ([#204](https://github.com/pyvvo/funcd/issues/204)) ([#244](https://github.com/pyvvo/funcd/issues/244)) ([c801b71](https://github.com/pyvvo/funcd/commit/c801b71a81e44cebe58a48f9451839eeaa6223f0))
* **eventing:** stop the EventSource reconciler rewriting the spec and keeping a stale condition ([#292](https://github.com/pyvvo/funcd/issues/292)) ([9380edf](https://github.com/pyvvo/funcd/commit/9380edf629bae32d1159245ed19be9a8c1f4b5fd))
* **funcd:** bound data-plane reads and reap idle local API conns ([#220](https://github.com/pyvvo/funcd/issues/220)) ([#270](https://github.com/pyvvo/funcd/issues/270)) ([0b8ea3b](https://github.com/pyvvo/funcd/commit/0b8ea3b6639299c10f624aeb054ef87152f8bf02))
* **funcdctl:** hot-reload dev handlers, reject bad -o and write redirects ([#230](https://github.com/pyvvo/funcd/issues/230)) ([#276](https://github.com/pyvvo/funcd/issues/276)) ([78874ef](https://github.com/pyvvo/funcd/commit/78874efbedce51977980771ac0af257227c5deb7))
* **funcd:** harden startup, shutdown and persistence config ([#221](https://github.com/pyvvo/funcd/issues/221)) ([#261](https://github.com/pyvvo/funcd/issues/261)) ([4f37b21](https://github.com/pyvvo/funcd/commit/4f37b21b756dcf9be769984a0605acd3dca68c2d))
* **funclog:** fix compaction, read and flush data loss, add config block ([#217](https://github.com/pyvvo/funcd/issues/217)) ([#257](https://github.com/pyvvo/funcd/issues/257)) ([25f1310](https://github.com/pyvvo/funcd/commit/25f1310409e4a0e37914e39ec36a3050d5d6dd8f))
* **funclog:** skip a log line over 1 MiB instead of ending capture ([#201](https://github.com/pyvvo/funcd/issues/201)) ([#239](https://github.com/pyvvo/funcd/issues/239)) ([60c9771](https://github.com/pyvvo/funcd/commit/60c9771a2799b33a7a961dcf5f1dbea480bfc469))
* **function:** fail a Function whose handler never becomes ready ([#214](https://github.com/pyvvo/funcd/issues/214)) ([#266](https://github.com/pyvvo/funcd/issues/266)) ([eb07b17](https://github.com/pyvvo/funcd/commit/eb07b1748bb2b9e44fbebc698b1c8a809ea65ed8))
* **function:** finish a switch after the serving Revision is deleted ([#210](https://github.com/pyvvo/funcd/issues/210)) ([#277](https://github.com/pyvvo/funcd/issues/277)) ([7eaa884](https://github.com/pyvvo/funcd/commit/7eaa884a0fe6dfcc40be34d7a8fb51f32a84eeef))
* **function:** give the readiness probe a transport of its own ([#290](https://github.com/pyvvo/funcd/issues/290)) ([bd64598](https://github.com/pyvvo/funcd/commit/bd645986e6ece89d51e91cd5b8c2845dbc385fa9))
* **function:** reuse the keep-alive connection across readiness probes ([#268](https://github.com/pyvvo/funcd/issues/268)) ([e36b0c9](https://github.com/pyvvo/funcd/commit/e36b0c9c5fd95b2a277de109385fa8b2ae842f5b))
* **function:** serve pooled Functions and supervise their pools ([#205](https://github.com/pyvvo/funcd/issues/205)) ([#252](https://github.com/pyvvo/funcd/issues/252)) ([23490bc](https://github.com/pyvvo/funcd/commit/23490bccfb82aa3931ae2a50a527b6d214fe2acf))
* **function:** stop a broken redeploy from spinning the reconciler ([#235](https://github.com/pyvvo/funcd/issues/235)) ([b5f28fc](https://github.com/pyvvo/funcd/commit/b5f28fc07adbd15722206e18f0d7b20439439375))
* **function:** write a Failed status when a worker cannot start ([#278](https://github.com/pyvvo/funcd/issues/278)) ([aadd934](https://github.com/pyvvo/funcd/commit/aadd9345940117224a09d5a247b02698cac01157))
* **kv:** keep KVStore bindings and owner refs current ([#209](https://github.com/pyvvo/funcd/issues/209)) ([#275](https://github.com/pyvvo/funcd/issues/275)) ([58c0296](https://github.com/pyvvo/funcd/commit/58c0296bb8b8f721f25a41c175bb7915c856a149))
* **kv:** store path-like keys verbatim, cap and evict KV caches ([#223](https://github.com/pyvvo/funcd/issues/223)) ([#258](https://github.com/pyvvo/funcd/issues/258)) ([af9cd7f](https://github.com/pyvvo/funcd/commit/af9cd7fc8270c483ce11fc78954433fdb5ded434))
* **kvstore:** record DropPrefix in CDC and keep tailer after errors ([#222](https://github.com/pyvvo/funcd/issues/222)) ([#245](https://github.com/pyvvo/funcd/issues/245)) ([5b7f3d4](https://github.com/pyvvo/funcd/commit/5b7f3d46e57ec1dce33f7bf6f8b7a2d46a5f9299))
* **route:** keep every Route's status in step with the edge table and its backends ([#273](https://github.com/pyvvo/funcd/issues/273)) ([2e37b61](https://github.com/pyvvo/funcd/commit/2e37b6196940836cec02d76ec7a532d3c9f636c9))
* **runtime/process:** reclaim worker subprocesses and temp files ([#207](https://github.com/pyvvo/funcd/issues/207)) ([#269](https://github.com/pyvvo/funcd/issues/269)) ([2b18dfe](https://github.com/pyvvo/funcd/commit/2b18dfe0121bf1becf0ff2a509f843b94aa9b527))
* **runtime:** start Functions again after a daemon restart ([#206](https://github.com/pyvvo/funcd/issues/206)) ([#242](https://github.com/pyvvo/funcd/issues/242)) ([6470172](https://github.com/pyvvo/funcd/commit/6470172a83036ab9f392aaee66525631c9fdb12d))
* **s3gateway:** honor HEAD, ranges, bindings, parts and listing params ([#228](https://github.com/pyvvo/funcd/issues/228)) ([#279](https://github.com/pyvvo/funcd/issues/279)) ([8f4c5c1](https://github.com/pyvvo/funcd/commit/8f4c5c1dc58f462fe91643433fc7e1668032bd28))
* **sdk:** apply every manifest document and decode keys strictly ([#213](https://github.com/pyvvo/funcd/issues/213)) ([#259](https://github.com/pyvvo/funcd/issues/259)) ([21af41f](https://github.com/pyvvo/funcd/commit/21af41f9313bf802c29c51f241285eba67db3a78))
* **status:** report NotReady when a dependency is gone or failed ([#224](https://github.com/pyvvo/funcd/issues/224)) ([#263](https://github.com/pyvvo/funcd/issues/263)) ([868c20e](https://github.com/pyvvo/funcd/commit/868c20ebfb49decabd32cdd4e9f216142a59a5b0))
* **store:** stamp creationTimestamp on create and ignore the client's value ([#267](https://github.com/pyvvo/funcd/issues/267)) ([b649571](https://github.com/pyvvo/funcd/commit/b64957117a173a2fa292b201c7a58084cf0bd8b3))
* **workernode:** give every function a local API socket that fits the Unix path limit ([#272](https://github.com/pyvvo/funcd/issues/272)) ([8985c32](https://github.com/pyvvo/funcd/commit/8985c325f66b5890d13a9ae5338fd783146c4634))
* **workernode:** wrap fn-to-fn input in an envelope; gate python on shim load ([#229](https://github.com/pyvvo/funcd/issues/229)) ([#253](https://github.com/pyvvo/funcd/issues/253)) ([317087f](https://github.com/pyvvo/funcd/commit/317087feaf9363c95def263d13e9bf255818c55f))
* **workflow:** fix run lifecycle, onFailure and fan-out defects ([#198](https://github.com/pyvvo/funcd/issues/198)) ([#283](https://github.com/pyvvo/funcd/issues/283)) ([af880a5](https://github.com/pyvvo/funcd/commit/af880a5c0b502fb20e1741f886df6a6d04413d42))
* **workflow:** keep an owned KVStore's status when a Workflow is re-materialized ([#294](https://github.com/pyvvo/funcd/issues/294)) ([f6d8cc2](https://github.com/pyvvo/funcd/commit/f6d8cc2638d9318e9b1b1e358a5893d5b4d9af40))
* **workflow:** sweep closed WorkflowRuns at retention, keep run counts ([#196](https://github.com/pyvvo/funcd/issues/196)) ([#280](https://github.com/pyvvo/funcd/issues/280)) ([293282c](https://github.com/pyvvo/funcd/commit/293282cb411bdb2c1ebdaf5b3ee2029c42dc9c21))

## [0.2.0](https://github.com/pyvvo/funcd/compare/v0.1.4...v0.2.0) (2026-10-02)


### Features

* bundle in the language toolchains and ship multi-arch function bundles (ADR-0144, ADR-0145) ([#22](https://github.com/pyvvo/funcd/issues/22)) ([171adac](https://github.com/pyvvo/funcd/commit/171adac2a3b0dc8f40268fe2835e1c08a5dc0da9))

## [0.1.4](https://github.com/pyvvo/funcd/compare/v0.1.3...v0.1.4) (2026-10-02)


### Bug Fixes

* **controlplane:** keep the stored status when a resource is applied ([#16](https://github.com/pyvvo/funcd/issues/16)) ([bc2ce8e](https://github.com/pyvvo/funcd/commit/bc2ce8e86821ac4dca1f22d9991ef3bb89a2c1e8))
* **function:** switch a redeployed Function to its new revision (ADR-0143) ([#21](https://github.com/pyvvo/funcd/issues/21)) ([1248f8f](https://github.com/pyvvo/funcd/commit/1248f8fad7d8dfaa03858d796cb4e54e292e2ea6))

## [0.1.3](https://github.com/pyvvo/funcd/compare/v0.1.2...v0.1.3) (2026-09-30)


### Bug Fixes

* **supervision:** replace a crashed worker or engine without a write (ADR-0142) ([#10](https://github.com/pyvvo/funcd/issues/10)) ([0fdb86e](https://github.com/pyvvo/funcd/commit/0fdb86e4cc0c59346e316c8d030f6386a5e293b3))

## [0.1.2](https://github.com/pyvvo/funcd/compare/v0.1.1...v0.1.2) (2026-09-29)


### Bug Fixes

* **catalog:** keep the proxy URL stable when the engine moves (ADR-0137) ([#7](https://github.com/pyvvo/funcd/issues/7)) ([6dbcfed](https://github.com/pyvvo/funcd/commit/6dbcfedfbff5fb68a7714d72c04fcb8ba5d1a08c))

## [0.1.1](https://github.com/pyvvo/funcd/compare/v0.1.0...v0.1.1) (2026-09-28)


### Bug Fixes

* **function:** inject a catalog only once it is Ready (ADR-0137) ([#4](https://github.com/pyvvo/funcd/issues/4)) ([2ef9672](https://github.com/pyvvo/funcd/commit/2ef9672e5388a5827e0648ef5616ac20137c9d56))
