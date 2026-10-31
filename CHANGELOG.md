# Changelog

## [0.36.0](https://github.com/pyvvo/funcd/compare/v0.35.0...v0.36.0) (2026-10-31)


### ⚠ BREAKING CHANGES

* **runtime:** the shims read no contract, and the python314 image ships no fastjsonschema (ADR-0304)

### Features

* **contract:** funcd defines each contract rule once (ADR-0301) ([d66ebc3](https://github.com/pyvvo/funcd/commit/d66ebc3aa5ad0fc7d5c86635515e852de56c8cb6))
* **gateway:** every call to a worker runs one chain of invokePlugins (ADR-0302) ([d66ebc3](https://github.com/pyvvo/funcd/commit/d66ebc3aa5ad0fc7d5c86635515e852de56c8cb6))
* **gateway:** funcd checks every call against its contract, and the shims stop checking (ADR-0303) ([d66ebc3](https://github.com/pyvvo/funcd/commit/d66ebc3aa5ad0fc7d5c86635515e852de56c8cb6))
* **runtime:** the shims read no contract, and the python314 image ships no fastjsonschema (ADR-0304) ([3a54c9f](https://github.com/pyvvo/funcd/commit/3a54c9fca0d6489378a60b19e02d6c66f7c61a86))


### Bug Fixes

* **deps:** pin funcd-python v0.18.1, so warm Python calls no longer wait about 40 ms ([#1341](https://github.com/pyvvo/funcd/issues/1341)) ([5f21f5d](https://github.com/pyvvo/funcd/commit/5f21f5df9cf39035fb002a8123bdad792f951f90))

## [0.35.0](https://github.com/pyvvo/funcd/compare/v0.34.0...v0.35.0) (2026-10-29)


### ⚠ BREAKING CHANGES

* **eventing:** the event store's directory and config key are named eventstore (ADR-0300)

### Features

* **eventing:** the event store's directory and config key are named eventstore (ADR-0300) ([fef70f2](https://github.com/pyvvo/funcd/commit/fef70f24af9635b8d0650ce25714072a87c755d3))

## [0.34.0](https://github.com/pyvvo/funcd/compare/v0.33.0...v0.34.0) (2026-10-29)


### ⚠ BREAKING CHANGES

* **runtime:** a worker's root filesystem is read-only, its /tmp counts in its memory, and a Lake engine writes into a scratch of fixed size; lake.catalogMaxSize defaults to 256MiB (ADR-0294)

### Features

* **platform:** a start on a full data filesystem waits for space instead of exiting (ADR-0297) ([7b45172](https://github.com/pyvvo/funcd/commit/7b45172800affd0473df5d4eedd379815104b955))
* **runtime:** a worker's root filesystem is read-only, its /tmp counts in its memory, and a Lake engine writes into a scratch of fixed size; lake.catalogMaxSize defaults to 256MiB (ADR-0294) ([7b45172](https://github.com/pyvvo/funcd/commit/7b45172800affd0473df5d4eedd379815104b955))
* **storage:** workload writes stop at a free-space floor of the data filesystem, storage.minFreeSize (ADR-0296) ([7b45172](https://github.com/pyvvo/funcd/commit/7b45172800affd0473df5d4eedd379815104b955))
* **volume:** a volume's quota limits its inodes in proportion to its size (ADR-0295) ([7b45172](https://github.com/pyvvo/funcd/commit/7b45172800affd0473df5d4eedd379815104b955))


### Bug Fixes

* **iam:** a writer role writes only the data kinds its actions name ([7b45172](https://github.com/pyvvo/funcd/commit/7b45172800affd0473df5d4eedd379815104b955))
* **volumes:** loop devices read and write their image with direct I/O ([7b45172](https://github.com/pyvvo/funcd/commit/7b45172800affd0473df5d4eedd379815104b955))
* **volumes:** the volume image keeps the blocks preallocated for it ([7b45172](https://github.com/pyvvo/funcd/commit/7b45172800affd0473df5d4eedd379815104b955))

## [0.33.0](https://github.com/pyvvo/funcd/compare/v0.32.0...v0.33.0) (2026-10-28)


### ⚠ BREAKING CHANGES

* **storage:** a store caps what it holds in spec.quota, and the per-item caps move into it (ADR-0292)
* **function:** a Function's workers get CPU and memory limits, and an unsized solo Function gets 1 CPU and 512Mi (ADR-0291)

### Features

* **function:** a Function's workers get CPU and memory limits, and an unsized solo Function gets 1 CPU and 512Mi (ADR-0291) ([a6fa161](https://github.com/pyvvo/funcd/commit/a6fa16158614061aea3e8eabaabe57beb46a9e63))
* **lake:** a Lake's catalog lives in a platform catalog store that only its engine reaches (ADR-0293) ([a6fa161](https://github.com/pyvvo/funcd/commit/a6fa16158614061aea3e8eabaabe57beb46a9e63))
* **storage:** a store caps what it holds in spec.quota, and the per-item caps move into it (ADR-0292) ([a6fa161](https://github.com/pyvvo/funcd/commit/a6fa16158614061aea3e8eabaabe57beb46a9e63))

## [0.32.0](https://github.com/pyvvo/funcd/compare/v0.31.0...v0.32.0) (2026-10-27)


### Features

* **s3gateway:** a namespace held for its data refuses writes through the S3 gateway (ADR-0290, amended) ([17ed221](https://github.com/pyvvo/funcd/commit/17ed221aa7da1bbc0f685e0d17b3085aeb0bfc78))

## [0.31.0](https://github.com/pyvvo/funcd/compare/v0.30.1...v0.31.0) (2026-10-27)


### Features

* **dataplane:** a namespace held for its data refuses calls until its owner's release (ADR-0290) ([ef03630](https://github.com/pyvvo/funcd/commit/ef0363015caae2359c7ae81293dad8d4d967ac8f))

## [0.30.1](https://github.com/pyvvo/funcd/compare/v0.30.0...v0.30.1) (2026-10-25)


### Bug Fixes

* **backup:** warn of rpoRisk while a call to the backup target waits ([#1294](https://github.com/pyvvo/funcd/issues/1294)) ([bccade5](https://github.com/pyvvo/funcd/commit/bccade5854c3d558cc36be1fa3110f3d5d953746))
* **ci:** the lanes' simulated host loss removes crun's state under /run ([#1296](https://github.com/pyvvo/funcd/issues/1296)) ([7fb7c74](https://github.com/pyvvo/funcd/commit/7fb7c74ad2e1dcedce47a75054b22511a41683a1))

## [0.30.0](https://github.com/pyvvo/funcd/compare/v0.29.0...v0.30.0) (2026-10-25)


### ⚠ BREAKING CHANGES

* **funcdctl:** backup plan prints an example and edits no file (ADR-0286)

### Features

* **backup:** a platform restore finds the Backups its schedules made after the restored point (ADR-0287) ([9fac108](https://github.com/pyvvo/funcd/commit/9fac108838fd653671ae354c1a9abfbc7f011217))
* **funcdctl:** backup plan prints an example and edits no file (ADR-0286) ([9fac108](https://github.com/pyvvo/funcd/commit/9fac108838fd653671ae354c1a9abfbc7f011217))
* **lake:** a Lake retries a failed catalog save until one succeeds (ADR-0289) ([9fac108](https://github.com/pyvvo/funcd/commit/9fac108838fd653671ae354c1a9abfbc7f011217))
* **storage:** a boot reclaim quarantines the data no object names instead of deleting it (ADR-0288) ([9fac108](https://github.com/pyvvo/funcd/commit/9fac108838fd653671ae354c1a9abfbc7f011217))

## [0.29.0](https://github.com/pyvvo/funcd/compare/v0.28.0...v0.29.0) (2026-10-24)


### Features

* **blob:** a file blob Put returns once its object is on disk, and a read refuses one that lost its bytes (ADR-0278) ([#1258](https://github.com/pyvvo/funcd/issues/1258)) ([25dc0e3](https://github.com/pyvvo/funcd/commit/25dc0e3fea06c7d96d43e133fd99fa8f4605055e))
* **lake:** a Lake answers a commit once its catalog save covers it (ADR-0279) ([#1263](https://github.com/pyvvo/funcd/issues/1263)) ([87acfb7](https://github.com/pyvvo/funcd/commit/87acfb7678d4262212cca64b0c681c15a5fbe8d5))


### Bug Fixes

* **artifact:** a pulled artifact reaches the disk before its cache dir is marked pulled ([647381d](https://github.com/pyvvo/funcd/commit/647381d6e84cb1ce277fca0ce0dd8ba43c7ef54a))
* **artifact:** the cached platforms of an artifact survive a power loss ([647381d](https://github.com/pyvvo/funcd/commit/647381d6e84cb1ce277fca0ce0dd8ba43c7ef54a))
* **containerd:** the CNI conflist survives a power loss after funcd's first start ([d9a78a5](https://github.com/pyvvo/funcd/commit/d9a78a55588dd6f23f8c2190f8e6bce70d390220))
* **install:** funcd install writes its systemd unit durably ([d9a78a5](https://github.com/pyvvo/funcd/commit/d9a78a55588dd6f23f8c2190f8e6bce70d390220))
* **lake:** a Lake stop lets the clients of the queries it cancelled read their interrupt error (ADR-0273) ([#1265](https://github.com/pyvvo/funcd/issues/1265)) ([1aac5f6](https://github.com/pyvvo/funcd/commit/1aac5f68c7d5488586bc5d9e09a928969cd10c2d))
* **provision:** funcd install writes each runtime binary durably ([647381d](https://github.com/pyvvo/funcd/commit/647381d6e84cb1ce277fca0ce0dd8ba43c7ef54a))
* **s3gateway:** the node master secret survives a power loss after it is written or restored ([647381d](https://github.com/pyvvo/funcd/commit/647381d6e84cb1ce277fca0ce0dd8ba43c7ef54a))

## [0.28.0](https://github.com/pyvvo/funcd/compare/v0.27.0...v0.28.0) (2026-10-24)


### ⚠ BREAKING CHANGES

* **api:** a Bucket prefix's owner names its kind (ADR-0275) ([#1237](https://github.com/pyvvo/funcd/issues/1237))

### Features

* **api:** a Bucket prefix's owner names its kind (ADR-0275) ([#1237](https://github.com/pyvvo/funcd/issues/1237)) ([5606318](https://github.com/pyvvo/funcd/commit/5606318f6a2c1a09f7e0c82205b88a98539a9333))
* **lake:** a Lake shows when its catalog saves fail (ADR-0276) ([#1238](https://github.com/pyvvo/funcd/issues/1238)) ([bf26ced](https://github.com/pyvvo/funcd/commit/bf26cedff70c7fc85494cf576a09756da2fbad40))
* **runtime:** a containerd worker gets one stop grace, set in funcdconfig.yaml (ADR-0277) ([#1239](https://github.com/pyvvo/funcd/issues/1239)) ([f3c7dfb](https://github.com/pyvvo/funcd/commit/f3c7dfbbabf0054155a4169cd0dde25e36dee2b2))

## [0.27.0](https://github.com/pyvvo/funcd/compare/v0.26.2...v0.27.0) (2026-10-24)


### Features

* **lake:** lake.saveInterval sets every Lake engine's catalog save interval, and funcdctl backup plan lake derives it from the RPO (ADR-0274) ([5cb19bf](https://github.com/pyvvo/funcd/commit/5cb19bfd4ee7e23a40e39cfa0466e43284ff98f6))


### Bug Fixes

* **funcd:** a graceful daemon stop keeps the S3 gateway, egress and DNS until the workers have stopped ([#1236](https://github.com/pyvvo/funcd/issues/1236)) ([6aec601](https://github.com/pyvvo/funcd/commit/6aec601c98226758129e8ee0d13802996a3442f8))
* **runtime:** a containerd stop keeps the worker's network until its task exits ([#1230](https://github.com/pyvvo/funcd/issues/1230)) ([cbc3f2b](https://github.com/pyvvo/funcd/commit/cbc3f2ba9b5423fd4516e9a7f96d19d30a10f6f1))

## [0.26.2](https://github.com/pyvvo/funcd/compare/v0.26.1...v0.26.2) (2026-10-24)


### Bug Fixes

* **lake:** a Lake stop drains its queries while the catalog keeps saving, then cancels the queries that outlive the wait (ADR-0273) ([155fe20](https://github.com/pyvvo/funcd/commit/155fe20a0ab48b5ef389abdfabd3d331041d2852))

## [0.26.1](https://github.com/pyvvo/funcd/compare/v0.26.0...v0.26.1) (2026-10-23)


### Bug Fixes

* **backup:** a Restore refused as not empty leaves its target as it was ([d21f7b4](https://github.com/pyvvo/funcd/commit/d21f7b4cb654081bc3fa0ffbf045654070435614))
* **function:** a damaged volume meta no longer stops funcd from starting, nor gives its quota to another volume ([d21f7b4](https://github.com/pyvvo/funcd/commit/d21f7b4cb654081bc3fa0ffbf045654070435614))
* **function:** a dry-run apply books no volume capacity ([d21f7b4](https://github.com/pyvvo/funcd/commit/d21f7b4cb654081bc3fa0ffbf045654070435614))
* **function:** a local volume added to a Ready Function is not taken for lost data ([d21f7b4](https://github.com/pyvvo/funcd/commit/d21f7b4cb654081bc3fa0ffbf045654070435614))
* **function:** a local volume refuses scaling.minReplicas above 1 ([d21f7b4](https://github.com/pyvvo/funcd/commit/d21f7b4cb654081bc3fa0ffbf045654070435614))
* **function:** a lost local volume can be restored after its Function wrote to it, with no worker running during the load ([d21f7b4](https://github.com/pyvvo/funcd/commit/d21f7b4cb654081bc3fa0ffbf045654070435614))
* **function:** a replaced worker's emptyDir space goes to the worker that replaces it, also in a revision switch ([d21f7b4](https://github.com/pyvvo/funcd/commit/d21f7b4cb654081bc3fa0ffbf045654070435614))
* **function:** a volume holding a FIFO or a socket backs up and restores, and an earlier backup holding one restores ([d21f7b4](https://github.com/pyvvo/funcd/commit/d21f7b4cb654081bc3fa0ffbf045654070435614))
* **function:** a volume keeps its booking while the Function or App part that declares it waits at an earlier gate ([d21f7b4](https://github.com/pyvvo/funcd/commit/d21f7b4cb654081bc3fa0ffbf045654070435614))
* **function:** the emptyDirs a held boot keeps are released once the hold lifts ([d21f7b4](https://github.com/pyvvo/funcd/commit/d21f7b4cb654081bc3fa0ffbf045654070435614))
* **function:** volume bookings fit the filesystem's free space, not only the image's size ([d21f7b4](https://github.com/pyvvo/funcd/commit/d21f7b4cb654081bc3fa0ffbf045654070435614))

## [0.26.0](https://github.com/pyvvo/funcd/compare/v0.25.0...v0.26.0) (2026-10-23)


### ⚠ BREAKING CHANGES

* **lake:** a Lake keeps its DuckLake catalog in a DuckDB file, and a stop keeps every acknowledged commit (ADR-0271)

### Bug Fixes

* **backup:** an App backup copies a Lake's catalog without opening it (ADR-0271) ([7dd5f7e](https://github.com/pyvvo/funcd/commit/7dd5f7e853b3447fc63a84f27bafc1012802887e))
* **lake:** a Lake keeps its DuckLake catalog in a DuckDB file, and a stop keeps every acknowledged commit (ADR-0271) ([7dd5f7e](https://github.com/pyvvo/funcd/commit/7dd5f7e853b3447fc63a84f27bafc1012802887e))

## [0.25.0](https://github.com/pyvvo/funcd/compare/v0.24.0...v0.25.0) (2026-10-23)


### Features

* **s3gateway:** derived S3 key pairs rotate every period and at every start (ADR-0270) ([fa8cdce](https://github.com/pyvvo/funcd/commit/fa8cdce226e090b009e9469560594515a2115ac7))


### Bug Fixes

* **lake:** a Lake or SQL engine reaches only its one internal endpoint (ADR-0269) ([fa8cdce](https://github.com/pyvvo/funcd/commit/fa8cdce226e090b009e9469560594515a2115ac7))
* **lake:** the Lake engine's S3 key pair stays out of queries, and bound data cannot redirect the engine ([fa8cdce](https://github.com/pyvvo/funcd/commit/fa8cdce226e090b009e9469560594515a2115ac7))
* **provider:** an engine created from another spec is replaced, not adopted ([#1196](https://github.com/pyvvo/funcd/issues/1196)) ([380d0a5](https://github.com/pyvvo/funcd/commit/380d0a5f0217645fdaaca6f11e916fb14c37a398))
* **runtime:** a containerd worker runs under containerd's default seccomp profile (ADR-0267) ([fa8cdce](https://github.com/pyvvo/funcd/commit/fa8cdce226e090b009e9469560594515a2115ac7))

## [0.24.0](https://github.com/pyvvo/funcd/compare/v0.23.0...v0.24.0) (2026-10-23)


### Features

* **dev:** the dev Lake engine serves on a port it chooses and reports it from its listen_url row (ADR-0263) ([621f7f2](https://github.com/pyvvo/funcd/commit/621f7f2501c246ed12be25ea0e80567f323bc5b0))
* **function:** Function-owned volumes, sizes with units and the project-id filter (ADR-0262, ADR-0266, ADR-0268) ([#1192](https://github.com/pyvvo/funcd/issues/1192)) ([50439ed](https://github.com/pyvvo/funcd/commit/50439ed67a6c4d350f64af1a9b3d140019fd5c94))
* **lake:** the Lake engine runs DuckDB 2.0.0, and new Lake catalogs are DuckLake 1.1, which a funcd older than this release cannot open (ADR-0263) ([621f7f2](https://github.com/pyvvo/funcd/commit/621f7f2501c246ed12be25ea0e80567f323bc5b0))
* **lake:** the Lake engine takes its tuning keys from an allow-list (ADR-0265) ([#1193](https://github.com/pyvvo/funcd/issues/1193)) ([33e044c](https://github.com/pyvvo/funcd/commit/33e044cd32ef5e873e7f5781cc624d937c9eddeb))
* **lake:** the Lake engine's sandbox allows only s3:// paths and its temp directory (ADR-0263) ([621f7f2](https://github.com/pyvvo/funcd/commit/621f7f2501c246ed12be25ea0e80567f323bc5b0))
* **runtime:** a containerd worker sees its own cgroup (ADR-0264) ([#1194](https://github.com/pyvvo/funcd/issues/1194)) ([8b7c434](https://github.com/pyvvo/funcd/commit/8b7c4347a62eb042b66cd9e03558b5e76246f9da))

## [0.23.0](https://github.com/pyvvo/funcd/compare/v0.22.1...v0.23.0) (2026-10-22)


### ⚠ BREAKING CHANGES

* **api:** `CatalogService` is now `Lake`, with the renamed fields, environment, Cedar names, roles, access-key code, default path and daemon config section of ADR-0261. Nothing stored is converted; an Identity issued before this release gets its `lakeToken` at its next rotation.

### Features

* **api:** CatalogService is renamed Lake (ADR-0261) ([#1181](https://github.com/pyvvo/funcd/issues/1181)) ([bd64119](https://github.com/pyvvo/funcd/commit/bd64119d6fc9adbd5ea604e39b41e8153fa0d6fe))

## [0.22.1](https://github.com/pyvvo/funcd/compare/v0.22.0...v0.22.1) (2026-10-22)


### Bug Fixes

* **dev:** a dev SQL or catalog engine counts as ready only on its own output, and an SQL engine whose port was taken starts again ([8b1ba12](https://github.com/pyvvo/funcd/commit/8b1ba1279275ba67d88ef6780ad9d8bdedcf3f24))
* **dev:** funcdctl dev waits for a Site's build again when it vanishes before dev's start pushes it ([6cafcc3](https://github.com/pyvvo/funcd/commit/6cafcc3879b858206aa3fb2f89482268ac48b44f))
* **sql:** on loopback the change feed binds its own port, so two platforms in one process no longer share 7075 ([8b1ba12](https://github.com/pyvvo/funcd/commit/8b1ba1279275ba67d88ef6780ad9d8bdedcf3f24))

## [0.22.0](https://github.com/pyvvo/funcd/compare/v0.21.0...v0.22.0) (2026-10-22)


### Features

* **function:** a late health answer keeps a worker's last readiness for runtime.readinessTimeout (ADR-0259) ([d6814ef](https://github.com/pyvvo/funcd/commit/d6814efb041e2701ebe469b3389c1bfe7306d731))

## [0.21.0](https://github.com/pyvvo/funcd/compare/v0.20.0...v0.21.0) (2026-10-21)


### Features

* **gate:** stress-run the tests a branch adds or changes ([#1147](https://github.com/pyvvo/funcd/issues/1147)) ([f8d8f5e](https://github.com/pyvvo/funcd/commit/f8d8f5e738e12ebd4c820582722736ddaae16f0e))


### Bug Fixes

* **e2e:** TestIssue700_DevHangupStopsWorkers signals dev once it serves; a stop during dev's start ends it cleanly ([#1146](https://github.com/pyvvo/funcd/issues/1146)) ([60b3e5a](https://github.com/pyvvo/funcd/commit/60b3e5a822fe4edf989e54ed6dc1f21b7dd8e2f6))

## [0.20.0](https://github.com/pyvvo/funcd/compare/v0.19.0...v0.20.0) (2026-10-21)


### Features

* **deps:** funcd-typescript and funcd-functions v0.20.0, with the @funcd-dev/drizzle package (ADR-0258) ([242a9e0](https://github.com/pyvvo/funcd/commit/242a9e035264ebd176da08bdd3fac6818513e777))

## [0.19.0](https://github.com/pyvvo/funcd/compare/v0.18.0...v0.19.0) (2026-10-21)


### Features

* **funcdctl:** funcdctl dev keeps a Site's last build while its build cannot be pushed (ADR-0257) ([8f966e6](https://github.com/pyvvo/funcd/commit/8f966e65d579b2948426c529c083bea5d3f67901))


### Bug Fixes

* **funcdctl:** funcdctl dev prints every log line's time in the host's time zone ([#1135](https://github.com/pyvvo/funcd/issues/1135)) ([980a849](https://github.com/pyvvo/funcd/commit/980a849c0344d05dc3f2fe184aabb952c3fc7db9))
* **funcdctl:** funcdctl dev starts live reload when the file names no watch command ([8f966e6](https://github.com/pyvvo/funcd/commit/8f966e65d579b2948426c529c083bea5d3f67901))

## [0.18.0](https://github.com/pyvvo/funcd/compare/v0.17.0...v0.18.0) (2026-10-21)


### Features

* **funcdctl:** funcdctl dev prints a watch command's output that led to no build (ADR-0256) ([e3c858d](https://github.com/pyvvo/funcd/commit/e3c858dba3ffcbbec59537180b4bf5bf2d0a628e))


### Bug Fixes

* **ci:** a PR that changes only docs/ skips the fast lane ([2830300](https://github.com/pyvvo/funcd/commit/2830300895072a8933b413ba8b4ca8cdc8a44958))

## [0.17.0](https://github.com/pyvvo/funcd/compare/v0.16.0...v0.17.0) (2026-10-20)


### Features

* **funcdctl:** funcdctl dev runs an App project's watch builds and reloads its Site pages (ADR-0255) ([#1124](https://github.com/pyvvo/funcd/issues/1124)) ([c03e165](https://github.com/pyvvo/funcd/commit/c03e1650cb64083ca7308a73be5b3826a12657ed))


### Bug Fixes

* **e2e:** app-hung-worker-restarted holds the replacement until the App's Degraded write is seen ([#1123](https://github.com/pyvvo/funcd/issues/1123)) ([1381575](https://github.com/pyvvo/funcd/commit/1381575cb188c2cf13451400425cd3d7d585204a))
* **funcdctl:** dev reloads a build only once its tool stops writing it ([#1122](https://github.com/pyvvo/funcd/issues/1122)) ([e34dada](https://github.com/pyvvo/funcd/commit/e34dadada266302b327519e2c2968e082cc30176))
* **funcd:** pooled-rig tests give their local pool host a 500 ms probe budget ([#1129](https://github.com/pyvvo/funcd/issues/1129)) ([0513539](https://github.com/pyvvo/funcd/commit/05135390fdd26a0e06df0b2f53d38b677085bb3f))

## [0.16.0](https://github.com/pyvvo/funcd/compare/v0.15.0...v0.16.0) (2026-10-20)


### Features

* **funcdctl:** funcdctl dev runs a whole App from its template (ADR-0253) ([#1112](https://github.com/pyvvo/funcd/issues/1112)) ([46038aa](https://github.com/pyvvo/funcd/commit/46038aae808127218fd4ac425e45c3e22a45f1b2))
* **funcdctl:** funcdctl.config.yaml holds what funcdctl dev is run with (ADR-0254) ([#1113](https://github.com/pyvvo/funcd/issues/1113)) ([5a3e321](https://github.com/pyvvo/funcd/commit/5a3e3215fb42f71fa30aba8277b231517eaf91d3))

## [0.15.0](https://github.com/pyvvo/funcd/compare/v0.14.1...v0.15.0) (2026-10-20)


### ⚠ BREAKING CHANGES

* round 2 of the resource-shape refactor — ADR-0247, ADR-0249 step 3, ADR-0250, ADR-0251 ([#1099](https://github.com/pyvvo/funcd/issues/1099))
* **api:** ADR-0246 bindings under spec.bindings + ADR-0248 stores at the root (FEAT-0001/F159) ([#1096](https://github.com/pyvvo/funcd/issues/1096))

### Features

* **api:** ADR-0246 bindings under spec.bindings + ADR-0248 stores at the root (FEAT-0001/F159) ([#1096](https://github.com/pyvvo/funcd/issues/1096)) ([fda4363](https://github.com/pyvvo/funcd/commit/fda4363c428050154065ae818811a9420f4fc85f))
* **function:** set FUNCD_CONFIG_KEYS and FUNCD_SECRET_KEYS on Function workers (ADR-0245) ([#1075](https://github.com/pyvvo/funcd/issues/1075)) ([3a056f6](https://github.com/pyvvo/funcd/commit/3a056f65a137aed6e9b3bad9d10526a93f910325))
* **local:** GET /secret and GET /secret/{key} read a Function's bound Secrets from funcd (ADR-0249) ([#1095](https://github.com/pyvvo/funcd/issues/1095)) ([f1adb74](https://github.com/pyvvo/funcd/commit/f1adb7434b9b5bac1ce2cc84854af3ef5dc0b29c))
* round 2 of the resource-shape refactor — ADR-0247, ADR-0249 step 3, ADR-0250, ADR-0251 ([#1099](https://github.com/pyvvo/funcd/issues/1099)) ([cd91442](https://github.com/pyvvo/funcd/commit/cd914428fb1ae8a19b9638e7c51f8c1aeb9df5c3))


### Bug Fixes

* **function:** refuse a cloudevent Function whose OCI artifact carries no I/O contract ([#1089](https://github.com/pyvvo/funcd/issues/1089)) ([0fbf791](https://github.com/pyvvo/funcd/commit/0fbf7918312e7bdb44345037454033256421464a))
* **test:** point tests at a port nothing listens on, not a closed server's ([#1101](https://github.com/pyvvo/funcd/issues/1101)) ([adb2a50](https://github.com/pyvvo/funcd/commit/adb2a507ce8a3e4c30249bfccc7ea8ca7532043e))

## [0.14.1](https://github.com/pyvvo/funcd/compare/v0.14.0...v0.14.1) (2026-10-18)


### Bug Fixes

* **funcdctl:** serve a function isolated in its own bundle again after a --persist restart ([#1054](https://github.com/pyvvo/funcd/issues/1054)) ([e6a71ee](https://github.com/pyvvo/funcd/commit/e6a71eece157cc8107da691d937d4101e9ec86ab))

## [0.14.0](https://github.com/pyvvo/funcd/compare/v0.13.0...v0.14.0) (2026-10-17)


### ⚠ BREAKING CHANGES

* **edge:** implement ADR-0241 — one exposure shape for every kind (FEAT-0006/F154) ([#1047](https://github.com/pyvvo/funcd/issues/1047))
* **edge:** implement ADR-0240 — namespace hosts (FEAT-0006/F153) ([#1042](https://github.com/pyvvo/funcd/issues/1042))

### Features

* **edge:** implement ADR-0240 — namespace hosts (FEAT-0006/F153) ([#1042](https://github.com/pyvvo/funcd/issues/1042)) ([6db4801](https://github.com/pyvvo/funcd/commit/6db4801393b2b802dcd9c06aca995a5c1446c060))
* **edge:** implement ADR-0241 — one exposure shape for every kind (FEAT-0006/F154) ([#1047](https://github.com/pyvvo/funcd/issues/1047)) ([c47fc1a](https://github.com/pyvvo/funcd/commit/c47fc1a61de72d7ed98484530c512c84b431016e))
* **function:** implement ADR-0243 — Functions that serve HTTP (FEAT-0006/F156) ([#1050](https://github.com/pyvvo/funcd/issues/1050)) ([5d716f9](https://github.com/pyvvo/funcd/commit/5d716f958ad68466b73f7dd81e7b6c56b7aa5497))
* **route:** implement ADR-0242 — a Route backend that serves a Site (FEAT-0003/F155) ([#1048](https://github.com/pyvvo/funcd/issues/1048)) ([292df47](https://github.com/pyvvo/funcd/commit/292df472375c1e117465527e63953b099de8c676))
* runtime configuration of a Site, ADR-0239 (FEAT-0003/F152) ([#1035](https://github.com/pyvvo/funcd/issues/1035)) ([427d707](https://github.com/pyvvo/funcd/commit/427d70794064595ea82ff703be2d049b580a774a))


### Bug Fixes

* **app:** a pass that read a deleted App writes back no part ([#1037](https://github.com/pyvvo/funcd/issues/1037)) ([027fef9](https://github.com/pyvvo/funcd/commit/027fef919264fd14939ffe2741c63aad0d7152fb))
* **funcdctl:** take a bundle push's entry from its funcdctl.yaml when --entry is not given ([#1051](https://github.com/pyvvo/funcd/issues/1051)) ([13996dd](https://github.com/pyvvo/funcd/commit/13996dd231c9308f7b71e263de3ec0b63b2b141f))

## [0.13.0](https://github.com/pyvvo/funcd/compare/v0.12.1...v0.13.0) (2026-10-13)


### Features

* the container bridge address, ADR-0238 (FEAT-0011/F136) ([#1005](https://github.com/pyvvo/funcd/issues/1005)) ([ceb2d49](https://github.com/pyvvo/funcd/commit/ceb2d49d0592a2b75fb5c8ba1ebb17a7cf761826))
* the SQL epoch, SQL databases on rqlite (FEAT-0011, ADR-0231 to ADR-0237) ([#1003](https://github.com/pyvvo/funcd/issues/1003)) ([c804656](https://github.com/pyvvo/funcd/commit/c804656bcbad9954b2cd2022c776b72ec065f5fb))


### Bug Fixes

* **sql:** align the SQL epoch's documents and code with its ADRs ([#1011](https://github.com/pyvvo/funcd/issues/1011)) ([6f7bc5d](https://github.com/pyvvo/funcd/commit/6f7bc5d74b4e16eb9e3e6ce2c1e7d1b895454196))
* **sql:** clear DataMissing for a Restore created in the same millisecond ([#1007](https://github.com/pyvvo/funcd/issues/1007)) ([9bd7c62](https://github.com/pyvvo/funcd/commit/9bd7c6299f6256dc9d95d6c9856249dc7af0156e))

## [0.12.1](https://github.com/pyvvo/funcd/compare/v0.12.0...v0.12.1) (2026-10-12)


### Bug Fixes

* **backup:** a catalog without a checkpoint backs up as an empty member ([#996](https://github.com/pyvvo/funcd/issues/996)) ([dd04061](https://github.com/pyvvo/funcd/commit/dd04061b2ceb9b42ee960aafab911dec7341319e))
* **catalog:** serve a catalog restored under another prefix ([#997](https://github.com/pyvvo/funcd/issues/997)) ([1babef5](https://github.com/pyvvo/funcd/commit/1babef5eb5fa1f32ad1443b90268cee6f9ad68b8))
* **funcd:** close the in-memory bus that tests and WithBus left running ([#983](https://github.com/pyvvo/funcd/issues/983)) ([f5b8899](https://github.com/pyvvo/funcd/commit/f5b889968196c194fba643e11b0ace5491afb6d1))
* **runtime:** the duckdb engine checkpoints its catalog after each commit, not only on SIGTERM ([#1000](https://github.com/pyvvo/funcd/issues/1000)) ([6b607f2](https://github.com/pyvvo/funcd/commit/6b607f2ba7aff7b8b0ef48c7aa0c85c10d41891a))

## [0.12.0](https://github.com/pyvvo/funcd/compare/v0.11.0...v0.12.0) (2026-10-11)


### Features

* **lanes:** implement ADR-0230, the full lane run shares one Lima VM (FEAT-0001/F46) ([#962](https://github.com/pyvvo/funcd/issues/962)) ([d295adc](https://github.com/pyvvo/funcd/commit/d295adc32cb5c15cb25bdec52045a1eedc32ec31))


### Bug Fixes

* **dr:** a restore hands over what it creates, and four smaller DR fixes ([#964](https://github.com/pyvvo/funcd/issues/964)) ([d6b3c81](https://github.com/pyvvo/funcd/commit/d6b3c813786c36887b129c29005354c9adc3a622))
* **runtime:** fail Close fast when the private containerd has exited ([#961](https://github.com/pyvvo/funcd/issues/961)) ([9178d36](https://github.com/pyvvo/funcd/commit/9178d36d9a9094707ff41103091708ee0f6905de))

## [0.11.0](https://github.com/pyvvo/funcd/compare/v0.10.0...v0.11.0) (2026-10-11)


### ⚠ BREAKING CHANGES

* **backup:** implement ADR-0222 — workload backup resources (F111) ([#949](https://github.com/pyvvo/funcd/issues/949))

### Features

* **backup:** implement ADR-0222 — workload backup resources (F111) ([#949](https://github.com/pyvvo/funcd/issues/949)) ([3eccee2](https://github.com/pyvvo/funcd/commit/3eccee2f5c00dc1a009732ebe16cb55da1c12b55))
* implement ADR-0223 — funcdctl backup plan (FEAT-0009/F112) ([#954](https://github.com/pyvvo/funcd/issues/954)) ([ae8869a](https://github.com/pyvvo/funcd/commit/ae8869a9cba4c0ce468e78e2f2833afcb54deba4))
* implement ADR-0229, an undeclared step store is refused at apply (FEAT-0010/F126) ([#959](https://github.com/pyvvo/funcd/issues/959)) ([5652957](https://github.com/pyvvo/funcd/commit/5652957802ec37184c657ad70135292d70ee685d))

## [0.10.0](https://github.com/pyvvo/funcd/compare/v0.9.0...v0.10.0) (2026-10-11)


### ⚠ BREAKING CHANGES

* **kvstore:** implement ADR-0209 — KV backup on the common format ([#943](https://github.com/pyvvo/funcd/issues/943))
* **funcd:** implement ADR-0208 — blob store backend and backup target ([#941](https://github.com/pyvvo/funcd/issues/941))
* **funcd:** implement ADR-0207 — pre-upgrade snapshot and safe mode ([#920](https://github.com/pyvvo/funcd/issues/920))
* **restore:** implement ADR-0206 — Restore and held boot ([#910](https://github.com/pyvvo/funcd/issues/910))
* **backup:** implement ADR-0205 — backup runs and verification ([#909](https://github.com/pyvvo/funcd/issues/909))
* **backup:** a config that sets `backup.target` with neither `backup.encryption.recipients` nor `backup.encryption.none: true` no longer starts, nor does `backup.encryption.none: true` without `secrets.encryptionKeyFile`.
* **backup:** implement ADR-0203 — Backup format, targets, fencing (F109) ([#888](https://github.com/pyvvo/funcd/issues/888))
* **eventing:** implement ADR-0201 event store (F110) ([#887](https://github.com/pyvvo/funcd/issues/887))
* **api:** a PUT whose body carries a `metadata.resourceVersion` other than the stored one now answers 409 instead of overwriting the object, and a malformed or disagreeing `If-Match` answers 400. Requests that carry no version behave as before.
* **store:** implement ADR-0202 — Platform store snapshot and timeline (F109) ([#881](https://github.com/pyvvo/funcd/issues/881))

### Features

* **api:** replace and delete honour the client's resourceVersion (ADR-0210) ([#878](https://github.com/pyvvo/funcd/issues/878)) ([318231e](https://github.com/pyvvo/funcd/commit/318231ebdd9972880a7808e812a2dd6d1ce9b7c6))
* **app:** App configMaps and secrets declarations (ADR-0213) ([#886](https://github.com/pyvvo/funcd/issues/886)) ([aa6ae31](https://github.com/pyvvo/funcd/commit/aa6ae31e1a3a07d3875b8074b7b551eead5d20a0))
* **app:** App lifecycle hooks (ADR-0214) ([#898](https://github.com/pyvvo/funcd/issues/898)) ([290222c](https://github.com/pyvvo/funcd/commit/290222c80212f0065af5f8e71c63a86d7e187e04))
* **app:** App templates with values and client-side rendering (ADR-0217) ([#894](https://github.com/pyvvo/funcd/issues/894)) ([f2db5f9](https://github.com/pyvvo/funcd/commit/f2db5f9979b6342500ea202bd877ad93f3bab04b))
* **app:** hold an App revision until its required Apps are met (ADR-0219) ([#911](https://github.com/pyvvo/funcd/issues/911)) ([9bdbaf1](https://github.com/pyvvo/funcd/commit/9bdbaf1f4e55bf4a086f21b79060dde36780833e))
* **app:** pin template images in app.lock, funcdctl app lock and push --template (ADR-0218) ([#906](https://github.com/pyvvo/funcd/issues/906)) ([7f58743](https://github.com/pyvvo/funcd/commit/7f587433cf3d59d29a40856fdc50e996ea3145b8))
* **backup:** implement ADR-0203 — Backup format, targets, fencing (F109) ([#888](https://github.com/pyvvo/funcd/issues/888)) ([390a698](https://github.com/pyvvo/funcd/commit/390a69811a8eab4cf27f234a9d2a25d1d92369c7))
* **backup:** implement ADR-0204 — backup encryption and key escrow ([#891](https://github.com/pyvvo/funcd/issues/891)) ([36aae2e](https://github.com/pyvvo/funcd/commit/36aae2e86958863e78992a70c5b699dd4f75db74))
* **backup:** implement ADR-0205 — backup runs and verification ([#909](https://github.com/pyvvo/funcd/issues/909)) ([a7e77c5](https://github.com/pyvvo/funcd/commit/a7e77c55b1479733809deb8d19db485814adc85a))
* **dryrun:** the dry-run engine, --dry-run on apply and app commands (ADR-0220) ([#915](https://github.com/pyvvo/funcd/issues/915)) ([ff8965b](https://github.com/pyvvo/funcd/commit/ff8965b94bc9aa446d8b14c16eb7afcd8754293e))
* **eventing:** cron schedules for timer events (ADR-0211) ([#882](https://github.com/pyvvo/funcd/issues/882)) ([0ba028d](https://github.com/pyvvo/funcd/commit/0ba028df520481db3a46e5d20d420b035ede5c5a))
* **eventing:** implement ADR-0201 event store (F110) ([#887](https://github.com/pyvvo/funcd/issues/887)) ([1d563ae](https://github.com/pyvvo/funcd/commit/1d563ae5d48d64d4304bbcaf27fb3e1b8cb9109e))
* **funcd:** implement ADR-0207 — pre-upgrade snapshot and safe mode ([#920](https://github.com/pyvvo/funcd/issues/920)) ([8c18934](https://github.com/pyvvo/funcd/commit/8c1893495d5d5a43cca1467dcef8f48fccb5ddb4))
* **funcd:** implement ADR-0208 — blob store backend and backup target ([#941](https://github.com/pyvvo/funcd/issues/941)) ([f9a374c](https://github.com/pyvvo/funcd/commit/f9a374cb5917f8afacd967c913393c29830e6d2c))
* **function:** implement ADR-0225 and ADR-0224 — pool boot crash loop and pool switch on member ready ([#912](https://github.com/pyvvo/funcd/issues/912)) ([78ab5e3](https://github.com/pyvvo/funcd/commit/78ab5e3ed0e8e879a05a4762937b3783a4b703b8))
* **health:** built-in liveness, dependency readiness and storage probes (ADR-0215) ([#895](https://github.com/pyvvo/funcd/issues/895)) ([7b24a77](https://github.com/pyvvo/funcd/commit/7b24a7760a9b158b8e94673dc5a0932fcd418252))
* implement ADR-0226, stuck calls end (FEAT-0010/F125) ([#948](https://github.com/pyvvo/funcd/issues/948)) ([e04edbb](https://github.com/pyvvo/funcd/commit/e04edbb915c812c86f983cb3f898507fc908ee2d))
* **kvstore:** implement ADR-0209 — KV backup on the common format ([#943](https://github.com/pyvvo/funcd/issues/943)) ([74f8e60](https://github.com/pyvvo/funcd/commit/74f8e60481a547ffc903395dbe19963863d19bb7))
* **restore:** implement ADR-0206 — Restore and held boot ([#910](https://github.com/pyvvo/funcd/issues/910)) ([2025947](https://github.com/pyvvo/funcd/commit/20259476e0109397cf604bdf082d067c8e3011fd))
* **store:** implement ADR-0202 — Platform store snapshot and timeline (F109) ([#881](https://github.com/pyvvo/funcd/issues/881)) ([9164e11](https://github.com/pyvvo/funcd/commit/9164e114a50ada4b964bce46b62e7f2ef372341b))


### Bug Fixes

* **app:** refuse an App whose two parts would write one object ([#925](https://github.com/pyvvo/funcd/issues/925)) ([39cf3dc](https://github.com/pyvvo/funcd/commit/39cf3dc99e4bce036eb442459b5ead268478c4bc))
* **controlplane:** a write without a resourceVersion no longer fails with 409 on a concurrent status write ([#905](https://github.com/pyvvo/funcd/issues/905)) ([e920858](https://github.com/pyvvo/funcd/commit/e920858a317ee62043182966814920e435c5c27c))
* **controlplane:** keep a typed fault's problem type when an input resolver refuses a request ([#885](https://github.com/pyvvo/funcd/issues/885)) ([6d025a7](https://github.com/pyvvo/funcd/commit/6d025a7548967c6e5a9d8700606d4b49bc673d3f))
* **funcd:** keep the node master secret under storage.dataDir with the S3 gateway off ([#889](https://github.com/pyvvo/funcd/issues/889)) ([73e1cec](https://github.com/pyvvo/funcd/commit/73e1cec813435956c8f3b1411ffe5675bc042ace))
* **function:** a Degraded Function serves again while a gate or a pass fails (ADR-0221) ([#879](https://github.com/pyvvo/funcd/issues/879)) ([9ecdfcb](https://github.com/pyvvo/funcd/commit/9ecdfcb4bef286a309ed78a700b450053b429bfa))
* **function:** name a digest-pinned image's digest once in error messages ([#947](https://github.com/pyvvo/funcd/issues/947)) ([90532c1](https://github.com/pyvvo/funcd/commit/90532c1faf849830d18bbbe8da014073f990f13f))
* **test:** stop four merge-queue flakes and release the lock on Close ([#929](https://github.com/pyvvo/funcd/issues/929)) ([#937](https://github.com/pyvvo/funcd/issues/937)) ([5ae9f94](https://github.com/pyvvo/funcd/commit/5ae9f942b841afc8e22f2b0772f19fda7b9c3dcd))
* **workflow:** refuse a Workflow that names one kv store twice ([#736](https://github.com/pyvvo/funcd/issues/736)) ([#936](https://github.com/pyvvo/funcd/issues/936)) ([0e6ff53](https://github.com/pyvvo/funcd/commit/0e6ff53ecbce5dd2b151fcd967ae33b8e564cee3))
* **workflow:** refuse at apply what a Workflow's stores and step Functions refuse ([#942](https://github.com/pyvvo/funcd/issues/942)) ([19f02ea](https://github.com/pyvvo/funcd/commit/19f02eabfaebf513f5bec9e31248afeba2ef3fcc))

## [0.9.0](https://github.com/pyvvo/funcd/compare/v0.8.0...v0.9.0) (2026-10-10)


### ⚠ BREAKING CHANGES

* **api:** the `since` query parameter of the function and workflow-run logs routes refuses, with 400, a Go duration outside ADR-0194's grammar (`500us`, `1.5h`, `-15m`) and a timestamp not in ADR-0196's form (an offset other than `Z`, or a precision other than exactly three fractional digits, such as `2026-10-07T22:00:00Z`). `funcdctl` still accepts any RFC3339 time and converts it.

### Features

* **api:** the logs since parameter takes only the duration and timestamp forms ([#875](https://github.com/pyvvo/funcd/issues/875)) ([d85d26b](https://github.com/pyvvo/funcd/commit/d85d26b0c78d64393610ad541bbbdc75e2fc9caa))
* **app:** implement ADR-0199 and ADR-0200, the App resource and App revisions (FEAT-0010 F113, F114) ([#851](https://github.com/pyvvo/funcd/issues/851)) ([c35bdf5](https://github.com/pyvvo/funcd/commit/c35bdf5e54207edc79b28b90cb3460210692d7d1))
* **app:** self-heal record and pause for an App (ADR-0212) ([#876](https://github.com/pyvvo/funcd/issues/876)) ([390497d](https://github.com/pyvvo/funcd/commit/390497d83ce9db92968f3120b521c98186994035))


### Bug Fixes

* **agents:** keep TMPDIR when scripts/agent/d runs inside another d call ([#861](https://github.com/pyvvo/funcd/issues/861)) ([6ea5259](https://github.com/pyvvo/funcd/commit/6ea5259dc0d046ed2d5f832aedb535daa2258a89))
* **agents:** report PRs of every branch in the PR watcher ([#860](https://github.com/pyvvo/funcd/issues/860)) ([441331d](https://github.com/pyvvo/funcd/commit/441331d978ca154d96de0b1b2461809039f84994))
* **agents:** scripts/agent/d sources the whole dev shell under bash 3.2 and leaves no temp directory ([#864](https://github.com/pyvvo/funcd/issues/864)) ([6c6d5bf](https://github.com/pyvvo/funcd/commit/6c6d5bf0055498706f82e7a40f0e80bf718ac39b))
* **eventing:** skip a timer event with no positive interval instead of panicking ([#873](https://github.com/pyvvo/funcd/issues/873)) ([2a03fc7](https://github.com/pyvvo/funcd/commit/2a03fc7fef76fa103c195e6addb1e1049ed3759f))
* **function:** keep a redeployed pooled member Ready while its old pool worker serves it ([#869](https://github.com/pyvvo/funcd/issues/869)) ([b589760](https://github.com/pyvvo/funcd/commit/b5897603951d42ea5ad6000e0bf1d4b9d1fc9871))
* **function:** wait for a released call to end before the pool drain tests check its worker ([#862](https://github.com/pyvvo/funcd/issues/862)) ([a5c916f](https://github.com/pyvvo/funcd/commit/a5c916fc0f10374b2f11753c4fcdde72971c5081))

## [0.8.0](https://github.com/pyvvo/funcd/compare/v0.7.3...v0.8.0) (2026-10-08)


### ⚠ BREAKING CHANGES

* **observability:** the log keys `timeout` (boot backoff) and `bound` (sensor retry drain) become `timeout_ms` and `bound_ms`. Each is now a number of milliseconds; before, it was nanoseconds in JSON and a Go duration string in text. `funcdctl dev` warnings go to funcdctl's output instead of stderr.
* **api:** WorkflowRun `status.steps[].startedAt` and `endedAt` are timestamp strings instead of unix nanoseconds. Every timestamp in a request body, a manifest, a stored object, a DLQ record or a CloudEvent payload must have the form `YYYY-MM-DDTHH:MM:SS.sssZ`. Any other precision, or an offset, is refused with 422 (or `fault.Invalid` outside the API). A data dir that an earlier version wrote holds instants in another form, so it no longer loads. The KV backup manifest is the one exception.
* **blob:** a presign request now answers 400 instead of signing a URL when any of these holds:
    - its `expiry` is outside the ADR-0194 grammar or the whole-second
    `1s`–`168h` range (for example `1.5s`, `500ms`, `0s`, `10`, or the
    `60.0s` that the old Python shim sent);
    - its `expiry` is empty;
    - its `method` is anything other than exactly `GET`, `PUT` or `DELETE`
    (for example `put` or `POST`).
* **api:** integer durations are refused everywhere: in manifests, request bodies and the platform config (a bare `0`, `1.5s` and `500us` are refused; write `0s`, `1s500ms`). A data dir written by an earlier version that holds a duration no longer loads.

### Features

* **api:** API durations are duration strings (ADR-0194) ([#832](https://github.com/pyvvo/funcd/issues/832)) ([77ab7f9](https://github.com/pyvvo/funcd/commit/77ab7f9cfe70017f4bfb2a96dbd76cd3a68df7ce))
* **api:** write every instant as an RFC3339 UTC timestamp with milliseconds (ADR-0196) ([#840](https://github.com/pyvvo/funcd/issues/840)) ([b5aa622](https://github.com/pyvvo/funcd/commit/b5aa622a90d243b088df43e5cc86f40426f182d7))
* **blob:** refuse a bad presign expiry or method with 400 (ADR-0198) ([#839](https://github.com/pyvvo/funcd/issues/839)) ([fae4100](https://github.com/pyvvo/funcd/commit/fae4100969a062a02d2be325a204fdf3f4085631))
* **observability:** log every duration as a &lt;key&gt;_ms number of milliseconds (ADR-0197) ([#842](https://github.com/pyvvo/funcd/issues/842)) ([022e6f3](https://github.com/pyvvo/funcd/commit/022e6f3fda3ecf74c2e72b224f3c831d5b710498))


### Bug Fixes

* **function:** keep a gated pooled member Ready when its pool host misses one health probe ([#845](https://github.com/pyvvo/funcd/issues/845)) ([628eebb](https://github.com/pyvvo/funcd/commit/628eebbe76a1a353b56505b1560597ed844ff583))
* **kvstore:** keep every gateway delete in the KV backup chain (ADR-0195) ([#837](https://github.com/pyvvo/funcd/issues/837)) ([be53220](https://github.com/pyvvo/funcd/commit/be532206aab0c0ce4c9c530b664544698af2cd71))
* **observability:** route runtime, local API, proxy and KV migration logs to the platform logger ([#817](https://github.com/pyvvo/funcd/issues/817)) ([#833](https://github.com/pyvvo/funcd/issues/833)) ([f52f341](https://github.com/pyvvo/funcd/commit/f52f34125651d727035f5306d827cff18b76ee9e))
* **workflow:** a cancelled run keeps its recorded steps when its goroutine exits mid-pass ([#847](https://github.com/pyvvo/funcd/issues/847)) ([297f8ba](https://github.com/pyvvo/funcd/commit/297f8baf7eee788b458748df0be90b7bc580bd7f))

## [0.7.3](https://github.com/pyvvo/funcd/compare/v0.7.2...v0.7.3) (2026-10-06)


### Bug Fixes

* **kvstore:** re-baseline the KV backup on schedule and after backup-off runs ([#737](https://github.com/pyvvo/funcd/issues/737)) ([#812](https://github.com/pyvvo/funcd/issues/812)) ([3d5d3ec](https://github.com/pyvvo/funcd/commit/3d5d3ec0b8010cbec2b4aadda806e9302387ed50))

## [0.7.2](https://github.com/pyvvo/funcd/compare/v0.7.1...v0.7.2) (2026-10-06)


### Bug Fixes

* **kvstore:** make the KV backup export lose no write and use one producer ([#737](https://github.com/pyvvo/funcd/issues/737)) ([#809](https://github.com/pyvvo/funcd/issues/809)) ([85487e4](https://github.com/pyvvo/funcd/commit/85487e4d08892583165efc7fbe77efd5e5becce3))

## [0.7.1](https://github.com/pyvvo/funcd/compare/v0.7.0...v0.7.1) (2026-10-05)


### Bug Fixes

* **funcd:** give each TLS server its own config ([#801](https://github.com/pyvvo/funcd/issues/801)) ([96ead37](https://github.com/pyvvo/funcd/commit/96ead37d5a2d7c381189338fd9ee32ea34235f22))
* **function:** let a pooled scale-to-zero member sleep through a failing gate ([#802](https://github.com/pyvvo/funcd/issues/802)) ([5e2b989](https://github.com/pyvvo/funcd/commit/5e2b989553ba9f528e8c65f3ff73396e5da2051f))

## [0.7.0](https://github.com/pyvvo/funcd/compare/v0.6.2...v0.7.0) (2026-10-05)


### Features

* **blob:** seek S3 listing pages from the marker; file:// keeps all keys ([#782](https://github.com/pyvvo/funcd/issues/782)) ([7f3163b](https://github.com/pyvvo/funcd/commit/7f3163b13794276b744839c78fd65701e8939de9))
* **dataplane:** forward a bodiless upgrade untouched and close an idle tunnel (ADR-0181) ([#780](https://github.com/pyvvo/funcd/issues/780)) ([2a327dd](https://github.com/pyvvo/funcd/commit/2a327dd27e061608658304400e07aa3b3c462f91))
* **edge:** end a data-plane response whose client accepts no data for 60 s ([#795](https://github.com/pyvvo/funcd/issues/795)) ([5b661b9](https://github.com/pyvvo/funcd/commit/5b661b9d9b4639209ad224f7a3f00f9a6df76c87))
* **s3gateway:** bound buffered multipart parts and copies by one daemon-wide budget ([#786](https://github.com/pyvvo/funcd/issues/786)) ([8505f47](https://github.com/pyvvo/funcd/commit/8505f47b76276f61d3f2ed190d22e782d0124607))
* **workflow:** a run pins its child workflow tree at start (ADR-0189) ([#784](https://github.com/pyvvo/funcd/issues/784)) ([282b6f3](https://github.com/pyvvo/funcd/commit/282b6f3ccbfbd8324fb2e379fa08a4bb83e101b4))
* **workflow:** a run stays bound to the revision it started with (ADR-0190) ([#799](https://github.com/pyvvo/funcd/issues/799)) ([a9865ed](https://github.com/pyvvo/funcd/commit/a9865edf2b79ef58781d9cfeb23b40291c9d26bf))


### Bug Fixes

* **activator:** leave a gate-held Pending Function out of idle reclaim ([#783](https://github.com/pyvvo/funcd/issues/783)) ([85ebe6c](https://github.com/pyvvo/funcd/commit/85ebe6c1037ad6b039257656904ea6e1fd83b055))
* **eventing:** keep timer schedules across daemon restarts ([#781](https://github.com/pyvvo/funcd/issues/781)) ([71d490a](https://github.com/pyvvo/funcd/commit/71d490aff16a2c898c516078f74f635505e935d9))
* **function:** count the boot timeout and crash waits from the last successful Start ([#788](https://github.com/pyvvo/funcd/issues/788)) ([53c7b6f](https://github.com/pyvvo/funcd/commit/53c7b6f53ea9a9f97af5f34a1ca67d4b742a871a))
* **function:** stop an asleep Function's worker when a gate fails and keep it asleep until a call ([#792](https://github.com/pyvvo/funcd/issues/792)) ([b9c5682](https://github.com/pyvvo/funcd/commit/b9c56823de1a345940f3c527c5fe9ccc20862da4))
* **kvstore:** stop an idle KV backup from shipping its own cursor every tick ([#793](https://github.com/pyvvo/funcd/issues/793)) ([633881f](https://github.com/pyvvo/funcd/commit/633881f0ead857b0cf5b70f41e8cf5fcefc20a5f))
* **procreg:** reap a crashed run's worker that rewrote its own argv ([#787](https://github.com/pyvvo/funcd/issues/787)) ([8679ca7](https://github.com/pyvvo/funcd/commit/8679ca701a9f3f2c3b216caef63ebddb0b27fb6d))
* **runtime:** clear an earlier run's containerd images and namespaces at boot ([#785](https://github.com/pyvvo/funcd/issues/785)) ([fa3e8b1](https://github.com/pyvvo/funcd/commit/fa3e8b172b1b3bc9ec402e9eaa60802704e3b6b6))

## [0.6.2](https://github.com/pyvvo/funcd/compare/v0.6.1...v0.6.2) (2026-10-05)


### Bug Fixes

* **activator:** join a pooled member's path and the request remainder as a rooted path ([#774](https://github.com/pyvvo/funcd/issues/774)) ([7374fe8](https://github.com/pyvvo/funcd/commit/7374fe865ac45864ed131760880c01e9badb118b))
* **artifact:** commit a pulled artifact to the cache only after the pull completes ([#772](https://github.com/pyvvo/funcd/issues/772)) ([9fe5682](https://github.com/pyvvo/funcd/commit/9fe56827cb7f8c5a08f638b4862b189c8ca8e7bd))
* **edge:** match Route hosts in one canonical spelling ([#773](https://github.com/pyvvo/funcd/issues/773)) ([b597659](https://github.com/pyvvo/funcd/commit/b59765993f36dfb9064d19e48a9aa6d8fe70cac2))
* **funcdctl:** escape function-supplied text in workflow describe, dlq list and printed errors ([#775](https://github.com/pyvvo/funcd/issues/775)) ([8fc9e29](https://github.com/pyvvo/funcd/commit/8fc9e29a5a1dc37bc15f94cdb6a5173b779c6294))
* **runtime:** bound the containerd log socket's live connections per worker ([#771](https://github.com/pyvvo/funcd/issues/771)) ([3dfaa71](https://github.com/pyvvo/funcd/commit/3dfaa719288c7a0a7c8952da98f5c81c0f8efd87))

## [0.6.1](https://github.com/pyvvo/funcd/compare/v0.6.0...v0.6.1) (2026-10-05)


### Bug Fixes

* **controlplane:** five control-plane service fixes ([#734](https://github.com/pyvvo/funcd/issues/734)) ([#746](https://github.com/pyvvo/funcd/issues/746)) ([f5542ad](https://github.com/pyvvo/funcd/commit/f5542ad707ded039e17c2c36f3b72f0e3e609415))
* **edge:** match trailing-slash Prefix routes and keep CORS headers single ([#739](https://github.com/pyvvo/funcd/issues/739)) ([#749](https://github.com/pyvvo/funcd/issues/749)) ([2e58f64](https://github.com/pyvvo/funcd/commit/2e58f64eebb6ab97563240834cd0ae28d646406f))
* **eventing:** keep the KV change feed and hold dead-letter replays ([#740](https://github.com/pyvvo/funcd/issues/740)) ([#752](https://github.com/pyvvo/funcd/issues/752)) ([6e77084](https://github.com/pyvvo/funcd/commit/6e77084d72cff3df8046d339f1f3459bac5c6f84))
* **function:** bound registry stalls, retry unresolved artifacts ([#733](https://github.com/pyvvo/funcd/issues/733)) ([#754](https://github.com/pyvvo/funcd/issues/754)) ([203f4ad](https://github.com/pyvvo/funcd/commit/203f4ade76cbc9a6dbe080b011cd4e4e9964f958))
* **observability:** final edge status, funclog Put retry, pinned log step ([#741](https://github.com/pyvvo/funcd/issues/741)) ([#753](https://github.com/pyvvo/funcd/issues/753)) ([5aaded5](https://github.com/pyvvo/funcd/commit/5aaded5cfdb3cdc252ad6e94a575776b2feca2e0))
* **runtime:** fix containerd CNI DEL, readiness wait and reap kill ([#735](https://github.com/pyvvo/funcd/issues/735)) ([#748](https://github.com/pyvvo/funcd/issues/748)) ([720f8ef](https://github.com/pyvvo/funcd/commit/720f8efba1a7dccae3914a76e6a596f5e290f337))
* **sdk:** validate names, keep numeric strings, stop dev on hangup ([#738](https://github.com/pyvvo/funcd/issues/738)) ([#750](https://github.com/pyvvo/funcd/issues/750)) ([e41cfed](https://github.com/pyvvo/funcd/commit/e41cfed85218cf5be8ac25b6ccd6c3978a6659a8))
* **storage:** reclaim deleted KVStore data, keep S3 folder markers apart ([#737](https://github.com/pyvvo/funcd/issues/737)) ([#747](https://github.com/pyvvo/funcd/issues/747)) ([978c581](https://github.com/pyvvo/funcd/commit/978c581c113e832eeaca92c3e0fd3d9bb4648d8f))
* **workflow:** harden run admission, sweep records and image step names ([#736](https://github.com/pyvvo/funcd/issues/736)) ([#751](https://github.com/pyvvo/funcd/issues/751)) ([02a27ce](https://github.com/pyvvo/funcd/commit/02a27ce53de7137d3c59c28e281d871b68af54c7))

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
