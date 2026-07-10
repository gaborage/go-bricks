# Changelog

## [0.49.0](https://github.com/gaborage/go-bricks/compare/v0.48.0...v0.49.0) (2026-07-10)


### ⚠ BREAKING CHANGES

* **migration:** a static config with a non-empty DB password < 8 bytes now fails config validation at startup; a per-tenant migration fails with ErrDatabasePasswordTooShort. Use >=8-byte passwords (or empty for trust/IAM auth).

### Added

* **database:** add database.manager.* config parity (maxsize/idlettl/cleanupinterval) ([#666](https://github.com/gaborage/go-bricks/issues/666)) ([d3c864c](https://github.com/gaborage/go-bricks/commit/d3c864c838fdbb1f9d7d455601099e9a03206e00))
* **messaging:** harden publisher lifecycle defaults ([#660](https://github.com/gaborage/go-bricks/issues/660)) ([b66e253](https://github.com/gaborage/go-bricks/commit/b66e253ba9e1ea26392c916e102beeb3cbb1d0ae))
* **server:** log registered routes at startup behind server.logroutes ([#680](https://github.com/gaborage/go-bricks/issues/680)) ([b165c0e](https://github.com/gaborage/go-bricks/commit/b165c0e42cafad45840789e0417366413ebce27b)), closes [#678](https://github.com/gaborage/go-bricks/issues/678)


### Fixed

* **config:** apply messaging defaults in all deployment modes ([#661](https://github.com/gaborage/go-bricks/issues/661)) ([453d084](https://github.com/gaborage/go-bricks/commit/453d0843cb6c34da8ea4f50d72c00efbcaab1b8a))
* **config:** reject unit-less numeric durations at decode time ([#670](https://github.com/gaborage/go-bricks/issues/670)) ([5db2b3b](https://github.com/gaborage/go-bricks/commit/5db2b3bfae332f70b1cc24dfb9d19d34ca754361))
* **deps:** update aws-sdk-go-v2 monorepo ([#646](https://github.com/gaborage/go-bricks/issues/646)) ([7278bf1](https://github.com/gaborage/go-bricks/commit/7278bf1744f9685655ccc42f6e33ad9f630446a7))
* **deps:** update module github.com/aws/aws-sdk-go-v2/config to v1.32.29 ([#652](https://github.com/gaborage/go-bricks/issues/652)) ([4e5d40a](https://github.com/gaborage/go-bricks/commit/4e5d40a3705723aba43e1fac1002e9b10b563463))
* **deps:** update module github.com/gaborage/go-bricks to v0.48.0 ([#647](https://github.com/gaborage/go-bricks/issues/647)) ([e612fa4](https://github.com/gaborage/go-bricks/commit/e612fa462e8c388e7dca198e62d3f19dff88edf0))
* **deps:** update module github.com/go-co-op/gocron/v2 to v2.22.0 ([#667](https://github.com/gaborage/go-bricks/issues/667)) ([d432325](https://github.com/gaborage/go-bricks/commit/d432325408950e8bb3d96289456932d13cd70232))
* **messaging:** promote eviction/idle-cleanup logs, add counters ([#657](https://github.com/gaborage/go-bricks/issues/657)) ([d40dec6](https://github.com/gaborage/go-bricks/commit/d40dec67c0872ecf99708ef019e198a6ab594c06))
* **messaging:** wait for cold client readiness before publish ([#656](https://github.com/gaborage/go-bricks/issues/656)) ([82de6ba](https://github.com/gaborage/go-bricks/commit/82de6ba39617094e2a373bd133a998f694ffdb17))
* **messaging:** wire reconnect delay keys into client; make cache maxsize mode-aware ([#669](https://github.com/gaborage/go-bricks/issues/669)) ([9da8483](https://github.com/gaborage/go-bricks/commit/9da848355096acd46a5fb4c08d628f980e65bd1d))
* **migration:** harden redactPassword against escaped-form leaks and JSON-token collisions ([#677](https://github.com/gaborage/go-bricks/issues/677)) ([d32975f](https://github.com/gaborage/go-bricks/commit/d32975f8713fbef36ffdf8bca1d6eb42bf76399a))
* **migration:** reject database passwords shorter than 8 bytes ([bb40410](https://github.com/gaborage/go-bricks/commit/bb404106f27d2f3795e15511320783aedb9e24c5)), closes [#675](https://github.com/gaborage/go-bricks/issues/675)
* **migration:** surface unparseable Flyway output and failure envelopes as errors ([#674](https://github.com/gaborage/go-bricks/issues/674)) ([4bcd7f5](https://github.com/gaborage/go-bricks/commit/4bcd7f5321123c1365268c39ce0837931e031905))

## [0.48.0](https://github.com/gaborage/go-bricks/compare/v0.47.0...v0.48.0) (2026-07-06)


### Added

* **server:** add GlobalMiddlewareRegisterer for module-contributed global middleware ([#643](https://github.com/gaborage/go-bricks/issues/643)) ([534d106](https://github.com/gaborage/go-bricks/commit/534d10637b3a6ac9d455f09350691637e42b264c))


### Fixed

* **deps:** update module github.com/gaborage/go-bricks to v0.47.0 ([#641](https://github.com/gaborage/go-bricks/issues/641)) ([25d9b6f](https://github.com/gaborage/go-bricks/commit/25d9b6fb8ef515d565c66117102bbbd3a159afba))

## [0.47.0](https://github.com/gaborage/go-bricks/compare/v0.46.0...v0.47.0) (2026-07-04)


### Added

* **server:** add WithRouteTemplate test option for HandlerContext ([#640](https://github.com/gaborage/go-bricks/issues/640)) ([53d4701](https://github.com/gaborage/go-bricks/commit/53d47010e2b0ad5f2cb3f09a4444e9d807889d78))
* **server:** emit RouteDescriptor for raw RouteRegistrar.Add routes ([#638](https://github.com/gaborage/go-bricks/issues/638)) ([2884188](https://github.com/gaborage/go-bricks/commit/2884188e643710a165166c6ea5ebdb0416275354))


### Fixed

* **deps:** update module github.com/gaborage/go-bricks to v0.46.0 ([#636](https://github.com/gaborage/go-bricks/issues/636)) ([408b038](https://github.com/gaborage/go-bricks/commit/408b038e806f133189beb91cc178e37aacb058ab))

## [0.46.0](https://github.com/gaborage/go-bricks/compare/v0.45.0...v0.46.0) (2026-07-03)


### Added

* **server:** restore route-template and path-param access on HandlerContext ([#635](https://github.com/gaborage/go-bricks/issues/635)) ([6b89824](https://github.com/gaborage/go-bricks/commit/6b898241c94960102fc7600193b3a5ab9f735961))


### Fixed

* **deps:** update module github.com/gaborage/go-bricks to v0.45.0 ([#630](https://github.com/gaborage/go-bricks/issues/630)) ([dddc824](https://github.com/gaborage/go-bricks/commit/dddc824beebadb080317a725cbbe8c5c5ad8543e))

## [0.45.0](https://github.com/gaborage/go-bricks/compare/v0.44.0...v0.45.0) (2026-07-01)


### ⚠ BREAKING CHANGES

* hide echo.* types behind go-bricks boundary abstractions ([#627](https://github.com/gaborage/go-bricks/issues/627))
* advance outbox retry_count on every failure; bound AMQP publish retries ([#626](https://github.com/gaborage/go-bricks/issues/626))

### Added

* hide echo.* types behind go-bricks boundary abstractions ([#627](https://github.com/gaborage/go-bricks/issues/627)) ([b0ef71d](https://github.com/gaborage/go-bricks/commit/b0ef71d1285b9a05eef93abc115f2a621786ba03))


### Fixed

* advance outbox retry_count on every failure; bound AMQP publish retries ([#626](https://github.com/gaborage/go-bricks/issues/626)) ([771493e](https://github.com/gaborage/go-bricks/commit/771493e12b72ce1c48781f5795404eaf7b1e1f9a))
* **deps:** update aws-sdk-go-v2 monorepo ([#621](https://github.com/gaborage/go-bricks/issues/621)) ([aac8687](https://github.com/gaborage/go-bricks/commit/aac86872fb4786b04b4858b4ce09bf8313a4132c))
* **deps:** update module github.com/gaborage/go-bricks to v0.44.0 ([#618](https://github.com/gaborage/go-bricks/issues/618)) ([0deceb6](https://github.com/gaborage/go-bricks/commit/0deceb65a1308eb47355ea7650edd6f8ab282478))
* update deps ([#628](https://github.com/gaborage/go-bricks/issues/628)) ([85fd79a](https://github.com/gaborage/go-bricks/commit/85fd79a9f45a286db82beed88bdef5f1f0281e62))

## [0.44.0](https://github.com/gaborage/go-bricks/compare/v0.43.0...v0.44.0) (2026-06-25)


### ⚠ BREAKING CHANGES

* **deps:** update actions/checkout action to v7 ([#609](https://github.com/gaborage/go-bricks/issues/609))

* **deps:** update actions/checkout action to v7 ([#609](https://github.com/gaborage/go-bricks/issues/609)) ([4023b73](https://github.com/gaborage/go-bricks/commit/4023b73e57900137125606bc9e27ef55967a3c04))


### Fixed

* **deps:** update amqp091-go to v1.12.0 and go-redis to v9.21.0 ([#616](https://github.com/gaborage/go-bricks/issues/616)) ([38ebe32](https://github.com/gaborage/go-bricks/commit/38ebe3211b35706de05db1432bb48289c428549d))
* **deps:** update module github.com/gaborage/go-bricks to v0.43.0 ([#611](https://github.com/gaborage/go-bricks/issues/611)) ([6b3fade](https://github.com/gaborage/go-bricks/commit/6b3fadece0189e495a47c0d0a5cdc5b828c79ff9))
* **deps:** update module github.com/labstack/echo/v5 to v5.2.1 ([#598](https://github.com/gaborage/go-bricks/issues/598)) ([ca838b1](https://github.com/gaborage/go-bricks/commit/ca838b1cf299b4a4e2208c6ee3e0844ad5eeea11))
* **deps:** update testcontainers-go monorepo to v0.43.0 ([#612](https://github.com/gaborage/go-bricks/issues/612)) ([9fa97df](https://github.com/gaborage/go-bricks/commit/9fa97df648014d3dad06692170cb40c1cce32d65))

## [0.43.0](https://github.com/gaborage/go-bricks/compare/v0.42.0...v0.43.0) (2026-06-17)


### ⚠ BREAKING CHANGES

* lease/refcount per-tenant resource handles to close the eviction-while-in-use race ([#606](https://github.com/gaborage/go-bricks/issues/606)) (#607)
* **database:** validate direct-string identifier arguments in the query builder (close M9 SQLi) ([#604](https://github.com/gaborage/go-bricks/issues/604))
* **config:** harden env ingestion, honor explicit keep-alive disable, fail-fast tenant cache ([#601](https://github.com/gaborage/go-bricks/issues/601))

### Fixed

* **app:** consume validated startup-budget and manager-tuning config keys ([#600](https://github.com/gaborage/go-bricks/issues/600)) ([b2acd0e](https://github.com/gaborage/go-bricks/commit/b2acd0e2beca31cd4f99550ffac37f7c57832bac))
* close evicted resource handles outside the manager lock; warn on under-provisioned pools ([#605](https://github.com/gaborage/go-bricks/issues/605)) ([4668189](https://github.com/gaborage/go-bricks/commit/46681897a7eb37ca903876cf5f8463caea60cd66))
* **config:** harden env ingestion, honor explicit keep-alive disable, fail-fast tenant cache ([#601](https://github.com/gaborage/go-bricks/issues/601)) ([489759c](https://github.com/gaborage/go-bricks/commit/489759ce5dc2868af772013c5e80a42c42edb134))
* **database:** correct Oracle identifier quoting in the query builder ([#603](https://github.com/gaborage/go-bricks/issues/603)) ([e8b2949](https://github.com/gaborage/go-bricks/commit/e8b29497ddf370da23a672b0c5ceaeeed03249e0))
* **database:** validate direct-string identifier arguments in the query builder (close M9 SQLi) ([#604](https://github.com/gaborage/go-bricks/issues/604)) ([d86e864](https://github.com/gaborage/go-bricks/commit/d86e864a1899fbf255520b68ce8c8e5e6b25c662))
* **deps:** update aws-sdk-go-v2 monorepo to v1.32.25 ([#584](https://github.com/gaborage/go-bricks/issues/584)) ([8dd6894](https://github.com/gaborage/go-bricks/commit/8dd6894e1900ef61f26196adc32cbe3651c44e4a))
* **deps:** update module github.com/gaborage/go-bricks to v0.42.0 ([#591](https://github.com/gaborage/go-bricks/issues/591)) ([9844b18](https://github.com/gaborage/go-bricks/commit/9844b18b6f79753038ddc4b81a91c4e715eae886))
* **deps:** update module github.com/labstack/echo/v5 to v5.2.0 ([#597](https://github.com/gaborage/go-bricks/issues/597)) ([c55d9de](https://github.com/gaborage/go-bricks/commit/c55d9debaffb7debd60cad47973144c7cb1b87c9))
* **inbox:** support the default (empty) tenant on Oracle ([#593](https://github.com/gaborage/go-bricks/issues/593)) ([57c92f1](https://github.com/gaborage/go-bricks/commit/57c92f1375c7d4e828ccc6bab77f04c39822260d))
* lease/refcount per-tenant resource handles to close the eviction-while-in-use race ([#606](https://github.com/gaborage/go-bricks/issues/606)) ([#607](https://github.com/gaborage/go-bricks/issues/607)) ([e578ffc](https://github.com/gaborage/go-bricks/commit/e578ffcd15100de1e56773a01ea702203cca196d))
* **lint:** enable correctness linters and fix surfaced defects ([#596](https://github.com/gaborage/go-bricks/issues/596)) ([28c027b](https://github.com/gaborage/go-bricks/commit/28c027bf68cb9e2d5798e755c061a46f347afe1c))

## [0.42.0](https://github.com/gaborage/go-bricks/compare/v0.41.0...v0.42.0) (2026-06-11)


### ⚠ BREAKING CHANGES

* **database:** bind PostgreSQL upsert update values (Oracle MERGE parity) ([#583](https://github.com/gaborage/go-bricks/issues/583))
* **database:** honor database.tls.cert/key/ca (fail closed on Oracle) ([#582](https://github.com/gaborage/go-bricks/issues/582))

### Fixed

* **app:** require trusted proxy for debug-endpoint IP allowlist (block XFF spoofing) ([#576](https://github.com/gaborage/go-bricks/issues/576)) ([43b7230](https://github.com/gaborage/go-bricks/commit/43b723090292c98580b37c0f919b67ab9ba522ab))
* **app:** stop inbound work before module teardown on shutdown ([#585](https://github.com/gaborage/go-bricks/issues/585)) ([1d94162](https://github.com/gaborage/go-bricks/commit/1d94162f8db34f778c780c17a909f76c14c627ed))
* **config:** select config.&lt;env&gt;.yaml overlay from APP_ENV ([#578](https://github.com/gaborage/go-bricks/issues/578)) ([35c7291](https://github.com/gaborage/go-bricks/commit/35c72915af04de484e0bb33b9c298ed26ad69525))
* **database:** bind PostgreSQL upsert update values (Oracle MERGE parity) ([#583](https://github.com/gaborage/go-bricks/issues/583)) ([88ecabb](https://github.com/gaborage/go-bricks/commit/88ecabb7a2ca8f21a3e337df4c8e0a61f02aca6c))
* **database:** honor database.tls.cert/key/ca (fail closed on Oracle) ([#582](https://github.com/gaborage/go-bricks/issues/582)) ([37da1eb](https://github.com/gaborage/go-bricks/commit/37da1eb4e305a6289c81a25d26e6f60379ce6d92))
* **database:** number subquery filter placeholders contiguously ([#579](https://github.com/gaborage/go-bricks/issues/579)) ([691dfcd](https://github.com/gaborage/go-bricks/commit/691dfcd63a8649c94270c70335ca820a58251a10))
* **deps:** update aws-sdk-go-v2 monorepo ([#572](https://github.com/gaborage/go-bricks/issues/572)) ([2077d55](https://github.com/gaborage/go-bricks/commit/2077d55f9ca617cdc96b1db09c6c0f6dfdc735ae))
* **deps:** update module github.com/gaborage/go-bricks to v0.41.0 ([#567](https://github.com/gaborage/go-bricks/issues/567)) ([984580f](https://github.com/gaborage/go-bricks/commit/984580f96a1e89b5d9759024f65153dee97549a9))
* **deps:** update module golang.org/x/sync to v0.21.0 ([#568](https://github.com/gaborage/go-bricks/issues/568)) ([9add39a](https://github.com/gaborage/go-bricks/commit/9add39ab742dda3a77d34e9ee02cb1cddbd7444d))
* **deps:** update module golang.org/x/term to v0.44.0 ([#570](https://github.com/gaborage/go-bricks/issues/570)) ([332cf9e](https://github.com/gaborage/go-bricks/commit/332cf9eb94a1fb66aebcc10944d9afc1be817d1d))
* **httpclient:** redact credentials and secrets from logged request URLs ([#575](https://github.com/gaborage/go-bricks/issues/575)) ([c7fed56](https://github.com/gaborage/go-bricks/commit/c7fed56f715ff007f24d143083ca3776c8a58b8c))
* **messaging:** apply reconnect.connectiontimeout to the AMQP client (+ repo-wide doc-drift cleanup) ([#571](https://github.com/gaborage/go-bricks/issues/571)) ([9e0c6c4](https://github.com/gaborage/go-bricks/commit/9e0c6c4a7c0eae69851f0b16c58a39d1a3307772))
* **messaging:** detach lazily-started consumers from the caller/request context ([#577](https://github.com/gaborage/go-bricks/issues/577)) ([8a4197f](https://github.com/gaborage/go-bricks/commit/8a4197fbaea160cb63c6ed3d3af71399f03bf720))
* **migration:** honor Config.DryRun by running validate instead of migrate ([#580](https://github.com/gaborage/go-bricks/issues/580)) ([a4ca6cb](https://github.com/gaborage/go-bricks/commit/a4ca6cb3394671d7c1665725be1274a8e9f158f5))
* **outbox:** resolve tenants in the relay & cleanup jobs (multi-tenant delivery) ([#581](https://github.com/gaborage/go-bricks/issues/581)) ([6ce8bfe](https://github.com/gaborage/go-bricks/commit/6ce8bfe9eda0b395b09da49f27586a65793544f0))
* **outbox:** support the default (empty) exchange on Oracle ([#589](https://github.com/gaborage/go-bricks/issues/589)) ([642c40a](https://github.com/gaborage/go-bricks/commit/642c40ab8b19e1b50999f47ab248adc673b2c441))

## [0.41.0](https://github.com/gaborage/go-bricks/compare/v0.40.1...v0.41.0) (2026-06-07)


### ⚠ BREAKING CHANGES

* **server:** X-Response-Time is no longer emitted by default; set server.responsetime.enabled=true (SERVER_RESPONSETIME_ENABLED=true) to restore it. The exported server.CORS() helper gains a leading exposeResponseTime bool. Part of ADR-026 (perf iteration 2).
* zero-overhead request path when observability and logging are disabled ([#559](https://github.com/gaborage/go-bricks/issues/559))

### Added

* **server:** make X-Response-Time header opt-in (default off) ([#563](https://github.com/gaborage/go-bricks/issues/563)) ([4199c22](https://github.com/gaborage/go-bricks/commit/4199c2239ab770aa014cb32009741f3309bc5ca6))
* zero-overhead request path when observability and logging are disabled ([#559](https://github.com/gaborage/go-bricks/issues/559)) ([a656339](https://github.com/gaborage/go-bricks/commit/a656339d147f36c40274474cd54b5ce4f5aaa7a0))


### Fixed

* **ci:** gate the coverage run on a single "code" signal so SonarCloud always gets a complete report ([#557](https://github.com/gaborage/go-bricks/issues/557)) ([23d9a56](https://github.com/gaborage/go-bricks/commit/23d9a56b5dfbe5cc850e7ee54e8f3e213c868810))
* **database:** default pool idle connections to track max (ADR-025) ([#558](https://github.com/gaborage/go-bricks/issues/558)) ([d365539](https://github.com/gaborage/go-bricks/commit/d365539e7b8e568f0d8e71fbe5f51a2a37cb3616))
* **deps:** update module github.com/gaborage/go-bricks to v0.40.1 ([#553](https://github.com/gaborage/go-bricks/issues/553)) ([32381dc](https://github.com/gaborage/go-bricks/commit/32381dcd7fa1e1e25e32c009f37eddf1670ec7ab))
* **observability:** flat-smush underscored mapstructure config keys ([#554](https://github.com/gaborage/go-bricks/issues/554)) ([#556](https://github.com/gaborage/go-bricks/issues/556)) ([e74c14e](https://github.com/gaborage/go-bricks/commit/e74c14e2ef907b7149efaa5b6bbadd35fdd04674))


### Changed

* **database:** hoist per-vendor statement builders to package init ([#560](https://github.com/gaborage/go-bricks/issues/560)) ([7a62cf0](https://github.com/gaborage/go-bricks/commit/7a62cf0f0796d6802b90cbb70f736124c60e40a7))
* **database:** short-circuit DB-tracking debug log fields when level disabled ([#562](https://github.com/gaborage/go-bricks/issues/562)) ([fa9e819](https://github.com/gaborage/go-bricks/commit/fa9e8199a6ec2a865cee7ed1566be4fac0f96f56))
* **logger:** reuse LogEventAdapter across chained setters (drop per-field wrapEvent alloc) ([#565](https://github.com/gaborage/go-bricks/issues/565)) ([dc85775](https://github.com/gaborage/go-bricks/commit/dc857750b607bafa5da1912c2d5b7aca2e0bb069))
* **server:** typed internal envelope for the default meta ([#564](https://github.com/gaborage/go-bricks/issues/564)) ([69721e6](https://github.com/gaborage/go-bricks/commit/69721e6c4d7fce812851ec92a1117c3a36bf98c5))

## [0.40.1](https://github.com/gaborage/go-bricks/compare/v0.40.0...v0.40.1) (2026-06-05)


### Fixed

* **config:** rename underscored config keys to flat-smushed convention ([#549](https://github.com/gaborage/go-bricks/issues/549)) ([7192f25](https://github.com/gaborage/go-bricks/commit/7192f2558793f789a64bb9e76273af4b381f83f6))
* **deps:** update aws-sdk-go-v2 monorepo ([#541](https://github.com/gaborage/go-bricks/issues/541)) ([f255187](https://github.com/gaborage/go-bricks/commit/f25518768b48a3d22f711d53eb564373266b5e78))
* **deps:** update module github.com/gaborage/go-bricks to v0.40.0 ([#552](https://github.com/gaborage/go-bricks/issues/552)) ([66cb6eb](https://github.com/gaborage/go-bricks/commit/66cb6eb2cc401024fe8dd1df93c48ce770aa5d78))
* **docs:** correct server-path env vars and .env.example orphans ([#551](https://github.com/gaborage/go-bricks/issues/551)) ([38ef705](https://github.com/gaborage/go-bricks/commit/38ef7050b79bcdc47586325d22c9c79bd82e0eb0))

## [0.40.0](https://github.com/gaborage/go-bricks/compare/v0.39.1...v0.40.0) (2026-06-05)


### Added

* **database:** add vendor-aware unique/FK/not-found error classifiers ([#542](https://github.com/gaborage/go-bricks/issues/542)) ([ddc5ca4](https://github.com/gaborage/go-bricks/commit/ddc5ca46f08c554437ff9d3b00ea019b45391ee8))
* **database:** add WithTx/WithTxOptions transaction helpers ([#543](https://github.com/gaborage/go-bricks/issues/543)) ([b64e660](https://github.com/gaborage/go-bricks/commit/b64e6606543c273e9831032cda033f813ca4b327))
* **inbox:** add durable consumer-side idempotency ledger (ProcessOnce) ([#545](https://github.com/gaborage/go-bricks/issues/545)) ([cc2f1c8](https://github.com/gaborage/go-bricks/commit/cc2f1c8d301e966aa897594cb8f7f0924d456a0a))
* **outbox:** export x-outbox-event-id header name and EventIDFromHeaders getter ([#544](https://github.com/gaborage/go-bricks/issues/544)) ([b500dc0](https://github.com/gaborage/go-bricks/commit/b500dc0fa7c0ad2a929a3ae9db6212318abdb96b))


### Fixed

* **config:** split comma-separated env vars into []string fields ([#548](https://github.com/gaborage/go-bricks/issues/548)) ([19e2363](https://github.com/gaborage/go-bricks/commit/19e23633b2af89a8d2c3d3404db65397b1b1cad7))
* **deps:** update module github.com/gaborage/go-bricks to v0.39.1 ([#537](https://github.com/gaborage/go-bricks/issues/537)) ([e9a2691](https://github.com/gaborage/go-bricks/commit/e9a26917565ab51d5e56555f447cf8f7be2db4e0))
* **deps:** update module github.com/jackc/pgx/v5 to v5.10.0 ([#530](https://github.com/gaborage/go-bricks/issues/530)) ([27a59e1](https://github.com/gaborage/go-bricks/commit/27a59e1f933f4e11b927ed7d90a61cc0379f303a))
* **outbox:** derive index names from the table's last segment for schema-qualified names ([#547](https://github.com/gaborage/go-bricks/issues/547)) ([6c1da09](https://github.com/gaborage/go-bricks/commit/6c1da09c197a4f694cd5803050775f9abe4f2da9))


### Changed

* **database:** extract shared SQL table-name validator to internal/sqlid ([#540](https://github.com/gaborage/go-bricks/issues/540)) ([317ebb4](https://github.com/gaborage/go-bricks/commit/317ebb4036ed917856e0597595187fb616dc2274))

## [0.39.1](https://github.com/gaborage/go-bricks/compare/v0.39.0...v0.39.1) (2026-06-03)


### Fixed

* **deps:** update aws-sdk-go-v2 monorepo ([#528](https://github.com/gaborage/go-bricks/issues/528)) ([c293812](https://github.com/gaborage/go-bricks/commit/c293812deebb84dba379a2fdaec2a0eefbfb6f1e))
* **deps:** update module github.com/gaborage/go-bricks to v0.39.0 ([#522](https://github.com/gaborage/go-bricks/issues/522)) ([a583722](https://github.com/gaborage/go-bricks/commit/a58372246e06f1d2530b4039c9d2296425a12740))

## [0.39.0](https://github.com/gaborage/go-bricks/compare/v0.38.0...v0.39.0) (2026-06-02)


### Added

* **migrate:** add --applied-by/--git-sha/--pipeline-run-id audit flags to the CLI ([#525](https://github.com/gaborage/go-bricks/issues/525)) ([8d7a9bf](https://github.com/gaborage/go-bricks/commit/8d7a9bf16503d5cce7b473b537f78d6a964ab6ff))
* **migrate:** add quiesce set|clear|status subcommand to the CLI ([#526](https://github.com/gaborage/go-bricks/issues/526)) ([4ac40a4](https://github.com/gaborage/go-bricks/commit/4ac40a47f07893a5efaf26c933e47702963be634))
* **migration:** deployment quiesce flag with PostgreSQL control plane ([#524](https://github.com/gaborage/go-bricks/issues/524)) ([b0db7fa](https://github.com/gaborage/go-bricks/commit/b0db7fa830fbe4933463ea458b1ab25e4397dbd9))
* **migration:** emit state.transitioned audit events from the provisioning state machine ([#523](https://github.com/gaborage/go-bricks/issues/523)) ([7961fb3](https://github.com/gaborage/go-bricks/commit/7961fb3501cc486c8dca4096d3b1915ccefd1e6c))
* **scheduler:** configurable timezone for scheduled jobs (scheduler.timezone) ([#527](https://github.com/gaborage/go-bricks/issues/527)) ([6bc53dd](https://github.com/gaborage/go-bricks/commit/6bc53ddd4f49685e6ff834549e5956625cc4effb))


### Fixed

* **deps:** update module github.com/alicebob/miniredis/v2 to v2.38.0 ([#519](https://github.com/gaborage/go-bricks/issues/519)) ([8f6bacc](https://github.com/gaborage/go-bricks/commit/8f6bacce523cc3beafdab399fc0d96424701cfed))

## [0.38.0](https://github.com/gaborage/go-bricks/compare/v0.37.0...v0.38.0) (2026-06-02)


### ⚠ BREAKING CHANGES

* **database:** rename connection pool metrics to OTEL semconv names ([#516](https://github.com/gaborage/go-bricks/issues/516))

### Added

* **database:** add repository.method attribute to operation duration metric ([#517](https://github.com/gaborage/go-bricks/issues/517)) ([504a0bc](https://github.com/gaborage/go-bricks/commit/504a0bc80bc71d4dad82b9fd25b1c9137fce7def))
* **openapi:** CLI document-metadata flags + UX hardening (PR12) ([#500](https://github.com/gaborage/go-bricks/issues/500)) ([839da1b](https://github.com/gaborage/go-bricks/commit/839da1b0d40c213f6df3b9a8802d9cf02dff7112))
* **openapi:** conformance — servers, security, qualified operationIds, schema gating (PR10) ([#495](https://github.com/gaborage/go-bricks/issues/495)) ([f1d8bf1](https://github.com/gaborage/go-bricks/commit/f1d8bf17cbbecf8e27774ad0c7ff005a0aedd03c))
* **openapi:** cross-package resolution, named-type underlying kind, collision qualification (PR9) ([#494](https://github.com/gaborage/go-bricks/issues/494)) ([0709403](https://github.com/gaborage/go-bricks/commit/07094030e97816c601f009e8efadbfe10efaa71d))
* **openapi:** deepen validator-constraint coverage (PR11) ([#498](https://github.com/gaborage/go-bricks/issues/498)) ([aaebe60](https://github.com/gaborage/go-bricks/commit/aaebe604c4f6f0bd82b176b40264040629d518f1))
* **openapi:** handler-receiver resolution + Result[R] unwrapping (PR3) ([#488](https://github.com/gaborage/go-bricks/issues/488)) ([11267c4](https://github.com/gaborage/go-bricks/commit/11267c45c204b8b80f27e5f031946ee2945a991c))
* **openapi:** promote embedded/anonymous struct fields (PR8) ([#493](https://github.com/gaborage/go-bricks/issues/493)) ([1513f0b](https://github.com/gaborage/go-bricks/commit/1513f0b7fc10fc110dd347a36aa3f8043269c321))
* **openapi:** recursive schema registry with $ref emission (PR5) ([#490](https://github.com/gaborage/go-bricks/issues/490)) ([1f35840](https://github.com/gaborage/go-bricks/commit/1f35840b1d96665657c6e5a2c40ae7497658ae00))
* **openapi:** registration-walk route discovery (PR4) ([#489](https://github.com/gaborage/go-bricks/issues/489)) ([b9f154e](https://github.com/gaborage/go-bricks/commit/b9f154e23d83db7ebd02359b8b331fde04b6c978))
* **openapi:** round-trip golden harness + OpenAPI path templating (PR1) ([#484](https://github.com/gaborage/go-bricks/issues/484)) ([e734329](https://github.com/gaborage/go-bricks/commit/e7343296fd2ea842f9c8fd55c3d8b22f33a0ba65))
* **openapi:** testable Run() seam lifts cmd/main.go off 0% coverage (PR14) ([#502](https://github.com/gaborage/go-bricks/issues/502)) ([5a8862f](https://github.com/gaborage/go-bricks/commit/5a8862f3a13b4d908e4741bba8bc2356c20efa3a))
* **openapi:** typed response envelope + constructor-derived status codes (PR6) ([#491](https://github.com/gaborage/go-bricks/issues/491)) ([b955942](https://github.com/gaborage/go-bricks/commit/b9559423327431c3dc66d9572f071dc7c0093618))
* **openapi:** well-known type formats, map additionalProperties, uint minimum (PR7) ([#492](https://github.com/gaborage/go-bricks/issues/492)) ([65324ac](https://github.com/gaborage/go-bricks/commit/65324ac099060dc706dfdccc2ac3dc4629acba7e))
* **release:** scripted signed-tag release flow + release-please calculator mode ([#512](https://github.com/gaborage/go-bricks/issues/512)) ([795e5c0](https://github.com/gaborage/go-bricks/commit/795e5c02497980e776ba3dff715d9dbc7c4d039d))


### Fixed

* **database:** rename connection pool metrics to OTEL semconv names ([#516](https://github.com/gaborage/go-bricks/issues/516)) ([c28f907](https://github.com/gaborage/go-bricks/commit/c28f9075f5b37d6b35d57a9e932e3ae1fc002e20))
* **deps:** update aws-sdk-go-v2 monorepo ([#479](https://github.com/gaborage/go-bricks/issues/479)) ([f8c93f3](https://github.com/gaborage/go-bricks/commit/f8c93f3627d1aae6229b59ab936863126c1bacd0))
* **deps:** update aws-sdk-go-v2 monorepo ([#486](https://github.com/gaborage/go-bricks/issues/486)) ([6899fc6](https://github.com/gaborage/go-bricks/commit/6899fc694935abad2bb3070a3430e8d05b65fe7a))
* **deps:** update module github.com/gaborage/go-bricks to v0.37.0 ([#474](https://github.com/gaborage/go-bricks/issues/474)) ([a5276ec](https://github.com/gaborage/go-bricks/commit/a5276ec47fb3f7652ae161588d185c31d6cb9b83))
* **deps:** update module github.com/go-playground/validator/v10 to v10.30.3 ([#487](https://github.com/gaborage/go-bricks/issues/487)) ([871c1a1](https://github.com/gaborage/go-bricks/commit/871c1a1a8895c294878ea2e9a2a40e4b167b12ca))
* **deps:** update module github.com/knadh/koanf/v2 to v2.3.5 ([#499](https://github.com/gaborage/go-bricks/issues/499)) ([f7b1626](https://github.com/gaborage/go-bricks/commit/f7b16260ee4e428a4b40da97eb6fd96a30e5cc92))
* **deps:** update module github.com/redis/go-redis/v9 to v9.20.0 ([#477](https://github.com/gaborage/go-bricks/issues/477)) ([602e122](https://github.com/gaborage/go-bricks/commit/602e12244e388fec30c3683f7df9c76ebe8599bd))
* **deps:** update module go.opentelemetry.io/contrib/instrumentation/runtime to v0.69.0 ([#478](https://github.com/gaborage/go-bricks/issues/478)) ([c29de11](https://github.com/gaborage/go-bricks/commit/c29de1188a44e1f30f92f8b34bec8883d9403412))
* **jose,keystore,outbox:** resolve SonarCloud CRITICAL smells + cover nil-map branches ([#483](https://github.com/gaborage/go-bricks/issues/483)) ([5377c88](https://github.com/gaborage/go-bricks/commit/5377c8839b51c05d021e6f004a41c15c95b7c925))
* **messaging:** auto-resubscribe consumers after AMQP reconnect ([#480](https://github.com/gaborage/go-bricks/issues/480)) ([3666d2e](https://github.com/gaborage/go-bricks/commit/3666d2e1684efd7b9c661fbb7d1a22da34238734))
* **messaging:** stop reconnect goroutine on Close for never-ready clients (fixes flaky -race) ([#481](https://github.com/gaborage/go-bricks/issues/481)) ([99bd7da](https://github.com/gaborage/go-bricks/commit/99bd7dafa39546e8859a4379b0d8833d63d230d0))
* **openapi:** cut analyzer cognitive complexity below S3776 ceiling ([#503](https://github.com/gaborage/go-bricks/issues/503)) ([bdc8be1](https://github.com/gaborage/go-bricks/commit/bdc8be1a5859e65c6d24b90d30e854f9e371e2c6))
* **openapi:** strict doctor diagnostics + version-floor reconciliation (PR13) ([#501](https://github.com/gaborage/go-bricks/issues/501)) ([5ef40bd](https://github.com/gaborage/go-bricks/commit/5ef40bd6490cd2720ad03fb0511d785c3d734627))
* **outbox:** propagate trace context HTTP→outbox→consumer ([#482](https://github.com/gaborage/go-bricks/issues/482)) ([70f6163](https://github.com/gaborage/go-bricks/commit/70f61639a77dd4c9c9182daeace75e2d570bb324))
* **server:** extend default OTEL metric attributes instead of replacing them ([#515](https://github.com/gaborage/go-bricks/issues/515)) ([c67d4e7](https://github.com/gaborage/go-bricks/commit/c67d4e73efc4fa525c6511caf5a6553103122299))


### Changed

* **openapi:** single yaml.Marshal struct-graph render (PR2) ([#485](https://github.com/gaborage/go-bricks/issues/485)) ([bbbc6e2](https://github.com/gaborage/go-bricks/commit/bbbc6e244e38b35d52b2b892e8cedeeb4bc7fbb0))
* **wiki:** rename all wiki docs to snake_case + update references ([#506](https://github.com/gaborage/go-bricks/issues/506)) ([55ee62b](https://github.com/gaborage/go-bricks/commit/55ee62b4d32fe1fbaa1c384346a8fe1a282db22c))
