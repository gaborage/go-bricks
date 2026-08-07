# Changelog

## [0.57.0](https://github.com/gaborage/go-bricks/compare/v0.56.0...v0.57.0) (2026-08-07)


### ⚠ BREAKING CHANGES

* **httpclient:** validate JOSE policies at Build time ([#908](https://github.com/gaborage/go-bricks/issues/908))
* **app:** refuse to register debug endpoints with no access control ([#905](https://github.com/gaborage/go-bricks/issues/905))
* **config:** a delivered-but-empty database identity field fails startup ([#897](https://github.com/gaborage/go-bricks/issues/897))
* **config:** infer database.type from the connectionstring scheme ([#896](https://github.com/gaborage/go-bricks/issues/896))
* **outbox:** fail startup without a usable outbox/inbox database ([#892](https://github.com/gaborage/go-bricks/issues/892))

### Added

* **messaging:** expose delivery metadata to typed consumers ([72da338](https://github.com/gaborage/go-bricks/commit/72da33888e8858a9ff59694390db237657ff3716))
* **messaging:** expose delivery metadata to typed consumers ([#899](https://github.com/gaborage/go-bricks/issues/899)) ([72da338](https://github.com/gaborage/go-bricks/commit/72da33888e8858a9ff59694390db237657ff3716))


### Fixed

* **app:** drop db_stats.connections from /ready's 200 body ([#889](https://github.com/gaborage/go-bricks/issues/889)) ([0854123](https://github.com/gaborage/go-bricks/commit/0854123a0f93925a4716786297dded48c435e11c))
* **app:** fail startup when consumer bootstrap fails for declared consumers ([#907](https://github.com/gaborage/go-bricks/issues/907)) ([34b97cf](https://github.com/gaborage/go-bricks/commit/34b97cfe5666b6d6475c1964702239ed17fc9d8a))
* **app:** refuse to register debug endpoints with no access control ([#905](https://github.com/gaborage/go-bricks/issues/905)) ([ab24463](https://github.com/gaborage/go-bricks/commit/ab24463e51ab6be2e9852f0106b2e2dd5019273f))
* **app:** sanitize every critical probe's /ready error by default ([#888](https://github.com/gaborage/go-bricks/issues/888)) ([79d20cb](https://github.com/gaborage/go-bricks/commit/79d20cb4fed5d0ebf2d78028b93677cce3681175))
* **app:** serve a fixed public error for the database readiness probe ([#887](https://github.com/gaborage/go-bricks/issues/887)) ([fe17325](https://github.com/gaborage/go-bricks/commit/fe17325607a4c8b5fb52a54199ab3f7b1e3ebdc4))
* **app:** warn for named databases with parameter logging enabled ([#909](https://github.com/gaborage/go-bricks/issues/909)) ([f5dbbb7](https://github.com/gaborage/go-bricks/commit/f5dbbb72a4a13854a1ffa57204972c4728907954))
* **config:** a delivered-but-empty database identity field fails startup ([#897](https://github.com/gaborage/go-bricks/issues/897)) ([590ee62](https://github.com/gaborage/go-bricks/commit/590ee6269f41e94d244743ed72d00da65ab996cb)), closes [#880](https://github.com/gaborage/go-bricks/issues/880) [#880](https://github.com/gaborage/go-bricks/issues/880)
* **config:** infer database.type from the connectionstring scheme ([#896](https://github.com/gaborage/go-bricks/issues/896)) ([a790e43](https://github.com/gaborage/go-bricks/commit/a790e43fc84a64d8821778708adfe887b0f2934a))
* **config:** ship parameter logging off in the example config ([#906](https://github.com/gaborage/go-bricks/issues/906)) ([bd304ee](https://github.com/gaborage/go-bricks/commit/bd304ee41dca4294b0bec25971d512d4a4284e00))
* **deps:** update module github.com/gaborage/go-bricks to v0.56.0 ([#884](https://github.com/gaborage/go-bricks/issues/884)) ([ac6b5a8](https://github.com/gaborage/go-bricks/commit/ac6b5a8c45c0bfc0f1da8b14302a9471bc83d427))
* **deps:** update module go.opentelemetry.io/contrib/instrumentation/runtime to v0.70.0 ([#875](https://github.com/gaborage/go-bricks/issues/875)) ([bdcdcf5](https://github.com/gaborage/go-bricks/commit/bdcdcf5570c04d4289db50c89ad4e2ad71e49369))
* **httpclient:** validate JOSE policies at Build time ([#908](https://github.com/gaborage/go-bricks/issues/908)) ([dc5e057](https://github.com/gaborage/go-bricks/commit/dc5e05762b42060ce0c4d6d2cb4662572d882125))
* **outbox:** fail startup without a usable outbox/inbox database ([#892](https://github.com/gaborage/go-bricks/issues/892)) ([90ef35d](https://github.com/gaborage/go-bricks/commit/90ef35d917f6576345e4352abf607625771f231b))

## [0.56.0](https://github.com/gaborage/go-bricks/compare/v0.55.0...v0.56.0) (2026-08-05)


### ⚠ BREAKING CHANGES

* **config:** report an unconfigured database as not_configured ([#881](https://github.com/gaborage/go-bricks/issues/881))
* **app:** report cache health on /ready, strict by default ([#870](https://github.com/gaborage/go-bricks/issues/870))
* **cache:** remove the dead and drifted Manager interface ([#865](https://github.com/gaborage/go-bricks/issues/865))
* **httpclient:** Build returns an error on unsafe transport composition ([#845](https://github.com/gaborage/go-bricks/issues/845))

### Added

* **app:** let modules declare a required database via DatabaseRequirer ([#878](https://github.com/gaborage/go-bricks/issues/878)) ([36f916f](https://github.com/gaborage/go-bricks/commit/36f916fc98a5802def7a28a16033c15702fcf741))
* **cmd:** seal-payload CLI generating compact JWE-of-JWS for curl testing ([#807](https://github.com/gaborage/go-bricks/issues/807)) ([282f495](https://github.com/gaborage/go-bricks/commit/282f495a4a0ff29e23da9228c7fecacff161f98d))
* **jose:** add CheckJTIReplay for cache-backed jti replay detection ([#811](https://github.com/gaborage/go-bricks/issues/811)) ([1b6de4e](https://github.com/gaborage/go-bricks/commit/1b6de4ebdc9a698e1089e813a61e890cd628c5f6))
* **logger:** mask PCI card-data fields by default ([#827](https://github.com/gaborage/go-bricks/issues/827)) ([8a2bf60](https://github.com/gaborage/go-bricks/commit/8a2bf608e56f952d90c289d078559d545ad30b2e))
* **messaging:** typed consumer payload binding ([#849](https://github.com/gaborage/go-bricks/issues/849)) ([20abee0](https://github.com/gaborage/go-bricks/commit/20abee03667b4a3679c635a773988ddb2c6efb0a))
* **mutatediff:** bound the CPU make mutate consumes ([#866](https://github.com/gaborage/go-bricks/issues/866)) ([6c69b3d](https://github.com/gaborage/go-bricks/commit/6c69b3d1dce58eecd45ce87c0666eb2d450bf96e))
* **server,config:** ALB forwarded-client-cert identity middleware ([#801](https://github.com/gaborage/go-bricks/issues/801)) ([07e33c4](https://github.com/gaborage/go-bricks/commit/07e33c490eff313f5e371550711dad521c73ca1c))


### Fixed

* **app:** report cache health on /ready, strict by default ([#870](https://github.com/gaborage/go-bricks/issues/870)) ([639d853](https://github.com/gaborage/go-bricks/commit/639d8538a3085e47f765db3795c21e4fa613679a))
* **cache:** dispatch CAS vs NX on an explicit mode flag ([#830](https://github.com/gaborage/go-bricks/issues/830)) ([460bb0a](https://github.com/gaborage/go-bricks/commit/460bb0aa1ba22e7f284dda0b481ffc0ef5482f11))
* **cache:** fail closed on zero-value CacheManager instead of panicking ([#859](https://github.com/gaborage/go-bricks/issues/859)) ([1760d5f](https://github.com/gaborage/go-bricks/commit/1760d5f6344ccd588c847571334dd0a5265ee332))
* **cache:** remove the dead and drifted Manager interface ([#865](https://github.com/gaborage/go-bricks/issues/865)) ([27ad0e0](https://github.com/gaborage/go-bricks/commit/27ad0e06ba15298ca2e9fb61b378d932ede27763))
* **config:** report an unconfigured database as not_configured ([#881](https://github.com/gaborage/go-bricks/issues/881)) ([917fa18](https://github.com/gaborage/go-bricks/commit/917fa186bc38d5f470bf7fe9e284748eb3f8d4f7))
* **deps:** port to the OTel v1.45.0 attribute value model ([#871](https://github.com/gaborage/go-bricks/issues/871)) ([c77bec3](https://github.com/gaborage/go-bricks/commit/c77bec304dd8a0fdf194cf318337b57fd6ccbfa4))
* **deps:** update aws-sdk-go-v2 monorepo ([#814](https://github.com/gaborage/go-bricks/issues/814)) ([1598492](https://github.com/gaborage/go-bricks/commit/15984926150597e01c6fb23c12057534f510047b))
* **deps:** update aws-sdk-go-v2 monorepo ([#821](https://github.com/gaborage/go-bricks/issues/821)) ([c4880f2](https://github.com/gaborage/go-bricks/commit/c4880f21ccc398601450da5e640d82fb8274c07c))
* **deps:** update aws-sdk-go-v2 monorepo ([#836](https://github.com/gaborage/go-bricks/issues/836)) ([af75184](https://github.com/gaborage/go-bricks/commit/af75184e829b829af7a8a962dc650698b315a4b8))
* **deps:** update module github.com/gaborage/go-bricks to v0.55.0 ([#805](https://github.com/gaborage/go-bricks/issues/805)) ([b685255](https://github.com/gaborage/go-bricks/commit/b68525540c1dad605ca644d17d9c259ff3a83182))
* **deps:** update module github.com/knadh/koanf/parsers/yaml to v1.1.1 ([#863](https://github.com/gaborage/go-bricks/issues/863)) ([2b67a85](https://github.com/gaborage/go-bricks/commit/2b67a852e2e40929cca3690cd4c322e3dde2b498))
* **deps:** update module github.com/knadh/koanf/providers/rawbytes to v1.0.1 ([#868](https://github.com/gaborage/go-bricks/issues/868)) ([bb37fba](https://github.com/gaborage/go-bricks/commit/bb37fba8cfbb462871985a3c5eae601cce10ee46))
* **deps:** update module github.com/redis/go-redis/v9 to v9.22.0 ([#853](https://github.com/gaborage/go-bricks/issues/853)) ([72bf22c](https://github.com/gaborage/go-bricks/commit/72bf22cabce057f3daebb4aa99aa2ea3e22a9817))
* **deps:** update module google.golang.org/grpc to v1.83.0 ([#834](https://github.com/gaborage/go-bricks/issues/834)) ([0cf8c2f](https://github.com/gaborage/go-bricks/commit/0cf8c2faa01a77f17c6933e09139c5185d12a961))
* **deps:** update tools/migration module dependencies ([#874](https://github.com/gaborage/go-bricks/issues/874)) ([e194f38](https://github.com/gaborage/go-bricks/commit/e194f385e45182a89991ec3c7e0d7186a87a6146))
* **httpclient:** Build returns an error on unsafe transport composition ([#845](https://github.com/gaborage/go-bricks/issues/845)) ([3b887b6](https://github.com/gaborage/go-bricks/commit/3b887b65698ff3822b4115306064b8b03d5fd359))
* **httpclient:** compose WithTLSConfig onto an incumbent transport ([#843](https://github.com/gaborage/go-bricks/issues/843)) ([d7b23a5](https://github.com/gaborage/go-bricks/commit/d7b23a5703bf13fb4cc3280cadb9249a854e51b0))
* **httpclient:** do not seal a JOSE body onto bodyless requests ([#858](https://github.com/gaborage/go-bricks/issues/858)) ([fa8d331](https://github.com/gaborage/go-bricks/commit/fa8d3318f415cfd583b0f7c4b1d581b51ab24dc5))
* **httpclient:** stop reporting a discard the replacement cannot cause ([#844](https://github.com/gaborage/go-bricks/issues/844)) ([2626206](https://github.com/gaborage/go-bricks/commit/26262069176b430a3c0b945ec3ecbc4ad671e5d5))
* **jose:** require an issuer or explicit namespace for jti replay keys ([#826](https://github.com/gaborage/go-bricks/issues/826)) ([b234341](https://github.com/gaborage/go-bricks/commit/b234341dd257c0a7f068a890ccf92cbee5938e57))
* **keystore:** never echo the configured secret source in startup errors ([#825](https://github.com/gaborage/go-bricks/issues/825)) ([47beb7d](https://github.com/gaborage/go-bricks/commit/47beb7d5ea5af708b3bec270e49f4e43e2baf9cd))
* **messaging:** align the publish fake with amqp091 tag rollback ([#856](https://github.com/gaborage/go-bricks/issues/856)) ([4ad42b1](https://github.com/gaborage/go-bricks/commit/4ad42b1c66fc014c26c3258558fd62a26e67ec09))
* **messaging:** honor the caller's context on collapsed setup ([#837](https://github.com/gaborage/go-bricks/issues/837)) ([c856333](https://github.com/gaborage/go-bricks/commit/c856333ec3235c3e58a2120c091703e6eb8faf51))
* **messaging:** make the unbounded-publish retry test deterministic ([#851](https://github.com/gaborage/go-bricks/issues/851)) ([3fbf665](https://github.com/gaborage/go-bricks/commit/3fbf665ece6a526dec8b0236ac9d29fee704c4a1))
* **messaging:** merge compatible queue re-declarations ([#847](https://github.com/gaborage/go-bricks/issues/847)) ([76377d9](https://github.com/gaborage/go-bricks/commit/76377d9c8551e3bc56dd5222c9eb57c8ea87d414))
* **migration:** snapshot audit events instead of mutating the caller's struct ([#831](https://github.com/gaborage/go-bricks/issues/831)) ([759b223](https://github.com/gaborage/go-bricks/commit/759b223a739a60ba7ceff2cfdaa7e190ec2ffb67))
* **mutatediff:** scale the mutant timeout to the real suite cost ([#824](https://github.com/gaborage/go-bricks/issues/824)) ([5c183ac](https://github.com/gaborage/go-bricks/commit/5c183ac7ab345166538d46d125ab4c10a642f7d3))
* **mutate:** NOT COVERED sits outside mutants_total in gremlins ([#813](https://github.com/gaborage/go-bricks/issues/813)) ([ee5553e](https://github.com/gaborage/go-bricks/commit/ee5553efb614ebaba01852b1754f674020e47587))
* **resourcepool:** honor waiter contexts and join cleanup on close ([#832](https://github.com/gaborage/go-bricks/issues/832)) ([508799a](https://github.com/gaborage/go-bricks/commit/508799a46451700d38887bcff1a034bb1afc7337))
* **server:** check JOSE content type before reading the request body ([#857](https://github.com/gaborage/go-bricks/issues/857)) ([e264a3a](https://github.com/gaborage/go-bricks/commit/e264a3a9723ba172b987598a935607d9d69b0ec1))
* **server:** honor forwardedclientcert.require when enabled is false ([#838](https://github.com/gaborage/go-bricks/issues/838)) ([35edd1e](https://github.com/gaborage/go-bricks/commit/35edd1e0089d9019d7f819076f541bccd218f9f3))
* **server:** reject duplicated X-Amzn-Mtls headers as absent identity ([#828](https://github.com/gaborage/go-bricks/issues/828)) ([6e56e2d](https://github.com/gaborage/go-bricks/commit/6e56e2d468d52944d8d3f83e6d3b4835e3ea16aa))


### Changed

* **server:** share validator construction with messaging ([#848](https://github.com/gaborage/go-bricks/issues/848)) ([631ec41](https://github.com/gaborage/go-bricks/commit/631ec41578b1dbdc825254fb2bd4fcc9ac8ae91f))

## [0.55.0](https://github.com/gaborage/go-bricks/compare/v0.54.0...v0.55.0) (2026-07-27)


### Added

* **database:** add Execute query/exec helpers with typed errors ([#772](https://github.com/gaborage/go-bricks/issues/772)) ([#774](https://github.com/gaborage/go-bricks/issues/774)) ([aeb1363](https://github.com/gaborage/go-bricks/commit/aeb13633fa9da28c222c5a9038a9e16b11b7af01))
* **httpclient:** config-driven client TLS and client certificates ([#790](https://github.com/gaborage/go-bricks/issues/790)) ([78dcb62](https://github.com/gaborage/go-bricks/commit/78dcb62d58a091b54ff12fa4a0deac3cc8b02d54))
* **server,config:** first-class TLS listener for the HTTP server ([#798](https://github.com/gaborage/go-bricks/issues/798)) ([826fc55](https://github.com/gaborage/go-bricks/commit/826fc55b075ca23036c64e3d0829524db70ea2ea))


### Fixed

* **deps:** update module github.com/gaborage/go-bricks to v0.54.0 ([#770](https://github.com/gaborage/go-bricks/issues/770)) ([9a46cfa](https://github.com/gaborage/go-bricks/commit/9a46cfab8b4c359a50b692c6fc8572f183be550e))
* **httpclient:** apply transport wrappers in layer order at Build ([#773](https://github.com/gaborage/go-bricks/issues/773)) ([c196d76](https://github.com/gaborage/go-bricks/commit/c196d76b0b1e39a8202e6f1b5eb8d1c425d7d699))
* **httpclient:** close req.Body on JOSETransport's pre-read error return ([#792](https://github.com/gaborage/go-bricks/issues/792)) ([3c44db1](https://github.com/gaborage/go-bricks/commit/3c44db193f87f34d6635f5fadf7b691f3155f7fb))
* **httpclient:** reject a nil logger at the construction seam ([#775](https://github.com/gaborage/go-bricks/issues/775)) ([c7e44de](https://github.com/gaborage/go-bricks/commit/c7e44de074ea957b9880eec5a15f17ec49b2b0ab))
* **httpclient:** require DER shape before rejecting a path as key material ([#793](https://github.com/gaborage/go-bricks/issues/793)) ([343faf9](https://github.com/gaborage/go-bricks/commit/343faf95d3960dbb48cb5111548a424bb04da06e))
* **httpclient:** warn when filling the base slot discards its holder ([#797](https://github.com/gaborage/go-bricks/issues/797)) ([9bd1b42](https://github.com/gaborage/go-bricks/commit/9bd1b4270c203db252bffea3ee2aba29aa7ce5a5))
* **keystore:** stop echoing mis-filed key material into startup errors ([#794](https://github.com/gaborage/go-bricks/issues/794)) ([bce1a50](https://github.com/gaborage/go-bricks/commit/bce1a5097c063ab1970375458702ec1d5f46228d))

## [0.54.0](https://github.com/gaborage/go-bricks/compare/v0.53.0...v0.54.0) (2026-07-24)


### Added

* **outbox,inbox:** shared control-plane ledger tenancy for dynamic multi-tenant deployments ([#763](https://github.com/gaborage/go-bricks/issues/763)) ([d21ac0f](https://github.com/gaborage/go-bricks/commit/d21ac0f2650c4a01d45d83eacfc31abd594e48cc))
* **server:** fail fast on duplicate route registration ([#761](https://github.com/gaborage/go-bricks/issues/761)) ([7dbc7e0](https://github.com/gaborage/go-bricks/commit/7dbc7e0f19b3088e3a5cd3681e2599e54ade024b))


### Fixed

* **deps:** update aws-sdk-go-v2 monorepo ([#754](https://github.com/gaborage/go-bricks/issues/754)) ([7fe3c4d](https://github.com/gaborage/go-bricks/commit/7fe3c4d08d8e745077c745dc000ea445f716f606))
* **deps:** update module github.com/gaborage/go-bricks to v0.53.0 ([#755](https://github.com/gaborage/go-bricks/issues/755)) ([ca56b08](https://github.com/gaborage/go-bricks/commit/ca56b08aa4edf266fee27b89d030b5d347856da2))
* **deps:** update module github.com/labstack/echo/v5 to v5.3.1 ([#753](https://github.com/gaborage/go-bricks/issues/753)) ([fc1eb0d](https://github.com/gaborage/go-bricks/commit/fc1eb0d04ffa67eabd7b81a3b887ec045c875c85))

## [0.53.0](https://github.com/gaborage/go-bricks/compare/v0.52.0...v0.53.0) (2026-07-21)


### Added

* **messaging:** declarative dead-letter opt-in via DeclareQueueWithDLQ ([#741](https://github.com/gaborage/go-bricks/issues/741)) ([754328d](https://github.com/gaborage/go-bricks/commit/754328dc168bb9c935859e2a61b7864677301f26))
* **migrate:** add --timeout to go-bricks-migrate to override per-tenant Flyway timeout ([#739](https://github.com/gaborage/go-bricks/issues/739)) ([d55b690](https://github.com/gaborage/go-bricks/commit/d55b69047fb281810cf168427996daef7128cb17))
* **migration:** optional NameFor hook for suffix-style secret-name grammars ([#734](https://github.com/gaborage/go-bricks/issues/734)) ([9b99252](https://github.com/gaborage/go-bricks/commit/9b992522f15b48a7dd76e315d9db4783b0adf3c6))


### Fixed

* **app:** warn when debug endpoints register with no allowlist and no token ([#737](https://github.com/gaborage/go-bricks/issues/737)) ([7418f18](https://github.com/gaborage/go-bricks/commit/7418f18582b4586eafc734ec603b14dbf8cd5f1c))
* **ci:** apidiff gate fails only on the break a PR introduces (delta vs origin/main) ([#723](https://github.com/gaborage/go-bricks/issues/723)) ([dcfa173](https://github.com/gaborage/go-bricks/commit/dcfa173d54ec5b6cb9b2a6f9777cd290648f1478))
* **database:** log the DB error type, not the raw driver message (PII/PAN redaction) ([#738](https://github.com/gaborage/go-bricks/issues/738)) ([0bb571e](https://github.com/gaborage/go-bricks/commit/0bb571ebc0d510463faab36e35af20393eb5f1b5))
* **deps:** update module github.com/gaborage/go-bricks to v0.52.0 ([#725](https://github.com/gaborage/go-bricks/issues/725)) ([37c5302](https://github.com/gaborage/go-bricks/commit/37c530202c06bdb619e017209519286f86927c1f))
* **deps:** update module github.com/rabbitmq/amqp091-go to v1.13.0 ([#749](https://github.com/gaborage/go-bricks/issues/749)) ([b9ea8c5](https://github.com/gaborage/go-bricks/commit/b9ea8c5417d45631026975839493fcab0ad28586))
* **messaging:** run lazy infra setup on a bounded budget detached from the request deadline ([#740](https://github.com/gaborage/go-bricks/issues/740)) ([af1aebb](https://github.com/gaborage/go-bricks/commit/af1aebbf09ff050b517f2f9c589c974d8f4b9128))
* **migration:** pass -schemas/-defaultSchema when the target DatabaseConfig carries a PostgreSQL schema ([#730](https://github.com/gaborage/go-bricks/issues/730)) ([ae4b6ae](https://github.com/gaborage/go-bricks/commit/ae4b6aecbcee642e50a2678b3c062fc297a0c197))
* **migration:** serialize per-tenant provisioning and make CREATE ROLE idempotent under races ([#745](https://github.com/gaborage/go-bricks/issues/745)) ([475f49f](https://github.com/gaborage/go-bricks/commit/475f49fab77e83464490d0359ca391c02b1d5791))
* **migration:** set provisioned roles' default search_path to the tenant schema ([#735](https://github.com/gaborage/go-bricks/issues/735)) ([938d11a](https://github.com/gaborage/go-bricks/commit/938d11a44b0d6f5e15547b6700ac8b6f3d6db660))
* **scheduler:** emit action-log summary on job panic path so 100% sampling holds ([#736](https://github.com/gaborage/go-bricks/issues/736)) ([4fc83fa](https://github.com/gaborage/go-bricks/commit/4fc83fa61e91266b7fe93c3c3231c443e36d4781))
* **server:** log tenant/IP-preguard rejections so denied requests are auditable ([#731](https://github.com/gaborage/go-bricks/issues/731)) ([3152635](https://github.com/gaborage/go-bricks/commit/3152635cfa98cd1da6e40f36798cf866b5f57a03))


### Changed

* **cache:** extract generic internal/resourcepool and rewire CacheManager onto it ([#748](https://github.com/gaborage/go-bricks/issues/748)) ([6db7683](https://github.com/gaborage/go-bricks/commit/6db76833c2763c887dbea97ff12c62fa791f88c7))
* **database:** rewire DbManager onto internal/resourcepool (closes F22-db) ([#750](https://github.com/gaborage/go-bricks/issues/750)) ([e6dacaa](https://github.com/gaborage/go-bricks/commit/e6dacaa69c7c58e9a388532c7cc6a90356cc3dd2))
* **messaging:** rewire the publisher pool onto internal/resourcepool (closes F22-msg) ([#751](https://github.com/gaborage/go-bricks/issues/751)) ([d76de47](https://github.com/gaborage/go-bricks/commit/d76de47781a68cd78f33c2c7d7b12a67a46a1636))
* **migration:** cut parseFlywayJSON cognitive complexity below the gate ([#747](https://github.com/gaborage/go-bricks/issues/747)) ([9fc26cb](https://github.com/gaborage/go-bricks/commit/9fc26cb3b9279fde085a0b14c0cc16d14eab885c))
* **server:** precompute the per-type tag-binding plan at route registration ([#746](https://github.com/gaborage/go-bricks/issues/746)) ([25f748c](https://github.com/gaborage/go-bricks/commit/25f748cca47799f10accc1719f63661348fbbca2))

## [0.52.0](https://github.com/gaborage/go-bricks/compare/v0.51.0...v0.52.0) (2026-07-19)


### ⚠ BREAKING CHANGES

* **messaging:** honor queue/exchange/binding declaration Args at the broker ([#714](https://github.com/gaborage/go-bricks/issues/714))

### Added

* **messaging:** honor queue/exchange/binding declaration Args at the broker ([#714](https://github.com/gaborage/go-bricks/issues/714)) ([b4fdcc2](https://github.com/gaborage/go-bricks/commit/b4fdcc27093322dcbf53b161b60ff38bbd76a937))


### Fixed

* **deps:** update aws-sdk-go-v2 monorepo ([#701](https://github.com/gaborage/go-bricks/issues/701)) ([3ba133d](https://github.com/gaborage/go-bricks/commit/3ba133da50d7e4b11475e2b1d7e25f4425fa2daa))
* **deps:** update module github.com/gaborage/go-bricks to v0.51.0 ([#700](https://github.com/gaborage/go-bricks/issues/700)) ([0d71e99](https://github.com/gaborage/go-bricks/commit/0d71e99718ae18e1f140c1a88a1194c91ff0b7d0))
* **deps:** update module google.golang.org/grpc to v1.82.1 ([#712](https://github.com/gaborage/go-bricks/issues/712)) ([9d41c1a](https://github.com/gaborage/go-bricks/commit/9d41c1a6b9ef67f03d43504f62770a9384c10b3c))

## [0.51.0](https://github.com/gaborage/go-bricks/compare/v0.50.0...v0.51.0) (2026-07-15)


### Added

* **server:** configurable body limit + group-404 guard hardening for echo v5.3.0 ([#711](https://github.com/gaborage/go-bricks/issues/711)) ([3e6f201](https://github.com/gaborage/go-bricks/commit/3e6f201b1dfcb6a860205ccdd05e2e6fa1d6c174))


### Fixed

* **app:** propagate module Shutdown errors so Run reports an unclean shutdown ([#707](https://github.com/gaborage/go-bricks/issues/707)) ([c968ee2](https://github.com/gaborage/go-bricks/commit/c968ee244035df4703d8444db9f1c9f5b8f5e3fd))
* **messaging:** mask AMQP URL query string in redacted log output ([#706](https://github.com/gaborage/go-bricks/issues/706)) ([55bacb6](https://github.com/gaborage/go-bricks/commit/55bacb63cd8750d23bc1da52a91eaf4152ac6597))
* **migration:** signal schema-state-unknown on parent-cancel kill (not only deadline) ([#708](https://github.com/gaborage/go-bricks/issues/708)) ([2ebd6ee](https://github.com/gaborage/go-bricks/commit/2ebd6eeb9579d98924e63c3ae25840daa8dc4415))

## [0.50.0](https://github.com/gaborage/go-bricks/compare/v0.49.1...v0.50.0) (2026-07-14)


### ⚠ BREAKING CHANGES

* **multitenant:** require an explicit composite resolver order (ADR-039) ([#702](https://github.com/gaborage/go-bricks/issues/702))
* **server:** require CORS_DEV_WILDCARD opt-in for dev wildcard CORS (ADR-038) ([#698](https://github.com/gaborage/go-bricks/issues/698))

### Fixed

* **migration:** enforce Flyway timeout with WaitDelay and process-group kill ([#704](https://github.com/gaborage/go-bricks/issues/704)) ([58192fc](https://github.com/gaborage/go-bricks/commit/58192fc373450b750d354a981f4265bee60dbaaa))
* **multitenant:** require an explicit composite resolver order (ADR-039) ([#702](https://github.com/gaborage/go-bricks/issues/702)) ([cafd189](https://github.com/gaborage/go-bricks/commit/cafd1896e568d9133966e1ca12362e6c616ca14b))
* **server:** require CORS_DEV_WILDCARD opt-in for dev wildcard CORS (ADR-038) ([#698](https://github.com/gaborage/go-bricks/issues/698)) ([a7841b4](https://github.com/gaborage/go-bricks/commit/a7841b41ca6fe6847bc0722c07764c587e5a2d0b))

## [0.49.1](https://github.com/gaborage/go-bricks/compare/v0.49.0...v0.49.1) (2026-07-13)


### Fixed

* **database:** apply pool defaults to dynamic-tenant DB configs ([#690](https://github.com/gaborage/go-bricks/issues/690)) ([8f4ce60](https://github.com/gaborage/go-bricks/commit/8f4ce6096b168c8db06677a278400b393236ecee))
* **database:** redact driver error class on DB spans instead of raw message ([#684](https://github.com/gaborage/go-bricks/issues/684)) ([a2a3353](https://github.com/gaborage/go-bricks/commit/a2a33532fa74e2c09248c6242f6f2f11b4da859c))
* **deps:** update module github.com/gaborage/go-bricks to v0.49.0 ([#681](https://github.com/gaborage/go-bricks/issues/681)) ([97ac301](https://github.com/gaborage/go-bricks/commit/97ac301a79e80f7c7e775a2c6609a1357da2a108))
* **docs:** harden pre-push gates to ordered /simplify -&gt; /security-audit -&gt; /code-review ([#697](https://github.com/gaborage/go-bricks/issues/697)) ([df02ba6](https://github.com/gaborage/go-bricks/commit/df02ba69c667ae68643de3924db041f53a219749))
* **docs:** update PR review workflow to clarify SonarCloud issue handling ([#693](https://github.com/gaborage/go-bricks/issues/693)) ([49d1224](https://github.com/gaborage/go-bricks/commit/49d12243f184bef3f030d4fa3219eb89befa7230))
* **httpclient:** shallow-copy caller-provided client before setting Transport ([#689](https://github.com/gaborage/go-bricks/issues/689)) ([4ae82cd](https://github.com/gaborage/go-bricks/commit/4ae82cd5301497daeb688afba862fa63f8806880))
* **inbox,outbox:** initialize store/table per tenant in multi-tenant mode ([#694](https://github.com/gaborage/go-bricks/issues/694)) ([9103c9f](https://github.com/gaborage/go-bricks/commit/9103c9f0c837d66bfcc26bd4071b86195deaa5cc))
* **logger:** fully mask URL-valued sensitive fields ([#683](https://github.com/gaborage/go-bricks/issues/683)) ([d3e61a8](https://github.com/gaborage/go-bricks/commit/d3e61a87bf5e1962e8274dffae07110c2c6889c8))
* **migration:** recover panics in the audit-sink consumer goroutine ([#686](https://github.com/gaborage/go-bricks/issues/686)) ([e0b377f](https://github.com/gaborage/go-bricks/commit/e0b377fc0bf813c03cb78722d4a361aa94118c76))
* **migration:** select the Flyway JSON envelope, not the first brace in noise ([#695](https://github.com/gaborage/go-bricks/issues/695)) ([5aa5c4c](https://github.com/gaborage/go-bricks/commit/5aa5c4ca331fe47a7e4ed92ffa02fdc0c214c60c))
* **scheduler:** register manual job in-flight before tryLock and re-check shutdown ([#687](https://github.com/gaborage/go-bricks/issues/687)) ([49a207b](https://github.com/gaborage/go-bricks/commit/49a207b6fe9d1d7941415f94e76790efccf41e1c))
* **scheduler:** register manual trigger under m.mu before spawn to close Add-after-Wait race ([#688](https://github.com/gaborage/go-bricks/issues/688)) ([b65dd05](https://github.com/gaborage/go-bricks/commit/b65dd05080bbef652b1f7d59565c56feec08a3f6))
* **server:** route unhandled 5xx logs through the filtered framework logger ([#682](https://github.com/gaborage/go-bricks/issues/682)) ([d93dc74](https://github.com/gaborage/go-bricks/commit/d93dc743dd4fb857f0df2cd7ca0e806b78c41999))
* **server:** warn when CORS reflects any origin with credentials (unset APP_ENV) ([#696](https://github.com/gaborage/go-bricks/issues/696)) ([2f32f5b](https://github.com/gaborage/go-bricks/commit/2f32f5baa20fa668c1ec93f0fa13fac34e74a95e))

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

> **Note:** The `openapi` entries below never shipped in a tagged release — the tool was removed in #504 (2026-05-31), before v0.38.0 was cut, and now lives in [`gaborage/go-bricks-openapi`](https://github.com/gaborage/go-bricks-openapi).

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
