# Changelog

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
