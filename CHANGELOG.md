# Changelog

## [0.61.0](https://github.com/gaborage/go-bricks/compare/v0.60.0...v0.61.0) (2026-08-31)


### ⚠ BREAKING CHANGES

* **app:** make the streams lane opt-in at the build graph ([#1268](https://github.com/gaborage/go-bricks/issues/1268))
* **app,cache:** address a tenant's cache config error to that tenant ([#1248](https://github.com/gaborage/go-bricks/issues/1248))
* **outbox:** drain each ledger key-ordered under one relay leader ([#1245](https://github.com/gaborage/go-bricks/issues/1245))
* **outbox:** order the ledger by a sequence and mark each row's lane ([#1241](https://github.com/gaborage/go-bricks/issues/1241))
* **config:** reject section names unreachable by env var ([#1243](https://github.com/gaborage/go-bricks/issues/1243))
* **database:** one acceptance rule for BuildUpsert column keys ([#1221](https://github.com/gaborage/go-bricks/issues/1221))
* **trace:** validate a pre-set traceparent before re-emitting it ([#1220](https://github.com/gaborage/go-bricks/issues/1220))
* **migration:** build the PostgreSQL Flyway JDBC URL in the framework ([#1188](https://github.com/gaborage/go-bricks/issues/1188))
* **database:** validate RawExpression aliases against the grammar ([#1203](https://github.com/gaborage/go-bricks/issues/1203))
* **observability:** record span errors by type at every sink ([#1189](https://github.com/gaborage/go-bricks/issues/1189))
* **server:** stop echoing request input in 400 error details ([#1181](https://github.com/gaborage/go-bricks/issues/1181))

### Added

* **app:** shared messaging tenancy on the classic lane ([#1244](https://github.com/gaborage/go-bricks/issues/1244)) ([80e3d71](https://github.com/gaborage/go-bricks/commit/80e3d71d82b7d0d8601f767cc33e9d83e46daf90))
* **config:** give the messaging kind a tenancy ([#1240](https://github.com/gaborage/go-bricks/issues/1240)) ([cbd2b26](https://github.com/gaborage/go-bricks/commit/cbd2b26b3f4cbfcb70b4f92dbf4218cbb98e0a6d))
* **database:** Having accepts qb.Expr and joins the annotation rule ([#1195](https://github.com/gaborage/go-bricks/issues/1195)) ([3093719](https://github.com/gaborage/go-bricks/commit/3093719cccc96bfba0597ded9ea6fbbc6341f856))
* **inbox:** drain the per-tenant hold and publish its backlog ([#1264](https://github.com/gaborage/go-bricks/issues/1264)) ([4665b64](https://github.com/gaborage/go-bricks/commit/4665b64b336bb936edb89bb9756cc43fc670ac73))
* **inbox:** park a failed stream delivery in a per-tenant hold ([#1253](https://github.com/gaborage/go-bricks/issues/1253)) ([479552d](https://github.com/gaborage/go-bricks/commit/479552dcd086b6bc4eca6f696b2433de5a146c1b))
* **logger:** add ErrorRedactor hook at the LogEvent.Err seam ([#1183](https://github.com/gaborage/go-bricks/issues/1183)) ([02b3ba0](https://github.com/gaborage/go-bricks/commit/02b3ba020b6b2f258e56895904dcb2ae6fdbc3e6))
* **messaging:** record settlement outcome on both lanes ([#1269](https://github.com/gaborage/go-bricks/issues/1269)) ([8c5aef1](https://github.com/gaborage/go-bricks/commit/8c5aef1f4cd9b3b851038e3090b03566b59e45b0)), closes [#1064](https://github.com/gaborage/go-bricks/issues/1064)
* **messaging:** retry a failed stream delivery in place within a bound ([#1242](https://github.com/gaborage/go-bricks/issues/1242)) ([da4a5ee](https://github.com/gaborage/go-bricks/commit/da4a5ee4d3dba902955fb2ad7515793acc65efcd))
* **messaging:** shared messaging tenancy on the streams lane ([#1246](https://github.com/gaborage/go-bricks/issues/1246)) ([94c7b85](https://github.com/gaborage/go-bricks/commit/94c7b854c120930e58c71a09c4cf76a1938c6bb8))
* **outbox:** drain each ledger key-ordered under one relay leader ([#1245](https://github.com/gaborage/go-bricks/issues/1245)) ([723e9a0](https://github.com/gaborage/go-bricks/commit/723e9a05b385333fb5dabcbd79c544c4516ae4d0))
* **outbox:** order the ledger by a sequence and mark each row's lane ([#1241](https://github.com/gaborage/go-bricks/issues/1241)) ([8ac578e](https://github.com/gaborage/go-bricks/commit/8ac578e1fcf9869b294af7848ef34b62d4c474bd))
* **outbox:** publish stream-lane rows through the native streams lane ([#1254](https://github.com/gaborage/go-bricks/issues/1254)) ([8690827](https://github.com/gaborage/go-bricks/commit/869082793ae9feb7d9bb24ca07c4ef0cc89ed032))


### Fixed

* **app,cache:** address a tenant's cache config error to that tenant ([#1248](https://github.com/gaborage/go-bricks/issues/1248)) ([70434ed](https://github.com/gaborage/go-bricks/commit/70434edb706c46b5c045f127df4796cdc2756358))
* **app:** make the streams lane opt-in at the build graph ([#1268](https://github.com/gaborage/go-bricks/issues/1268)) ([a8068d2](https://github.com/gaborage/go-bricks/commit/a8068d29852c0ab9e7ce6b9756f23618b3bdef2c)), closes [#1169](https://github.com/gaborage/go-bricks/issues/1169)
* **app:** recover a panicking create inside the resource pool ([#1237](https://github.com/gaborage/go-bricks/issues/1237)) ([8ab1216](https://github.com/gaborage/go-bricks/commit/8ab1216d9d972d79cc80e246e837145f9147d97d))
* **app:** stop folding a telemetry sink outage into shutdown ([#1235](https://github.com/gaborage/go-bricks/issues/1235)) ([b0204cf](https://github.com/gaborage/go-bricks/commit/b0204cfbebfcff971b1cca8b9410d8e24b2d690b))
* **config:** reject section names unreachable by env var ([#1243](https://github.com/gaborage/go-bricks/issues/1243)) ([d8d61e2](https://github.com/gaborage/go-bricks/commit/d8d61e217d96e729dee50e78f162c17b14be3a41))
* **database:** first deferred error wins; sort struct-driven columns ([#1199](https://github.com/gaborage/go-bricks/issues/1199)) ([44c0145](https://github.com/gaborage/go-bricks/commit/44c0145899ded00e97b84d93317621af2688bd63))
* **database:** identifier validators return the normalized identifier ([#1198](https://github.com/gaborage/go-bricks/issues/1198)) ([51def4b](https://github.com/gaborage/go-bricks/commit/51def4ba45249672dac8446c6f84dd3bec91d0eb))
* **database:** jf.Eq renders nil and slices like f.Eq ([#1204](https://github.com/gaborage/go-bricks/issues/1204)) ([c18870d](https://github.com/gaborage/go-bricks/commit/c18870d3e60748ae5869c33185659173f5045df7))
* **database:** one acceptance rule for BuildUpsert column keys ([#1221](https://github.com/gaborage/go-bricks/issues/1221)) ([b156dd8](https://github.com/gaborage/go-bricks/commit/b156dd8ade3494979ee36fce0ef799bd061f8df2))
* **database:** quote only the identifier in Oracle ORDER BY and From ([#1194](https://github.com/gaborage/go-bricks/issues/1194)) ([e781676](https://github.com/gaborage/go-bricks/commit/e7816760599f3f81c4bcca7cd56bbc457a3dfb8c))
* **database:** quote reserved-word columns on Insert Columns and SetMap ([#1186](https://github.com/gaborage/go-bricks/issues/1186)) ([4c4bed9](https://github.com/gaborage/go-bricks/commit/4c4bed927b2d38e31547aacd77308c07954bec99))
* **database:** quote-aware dot split; drop the function pass-through ([#1191](https://github.com/gaborage/go-bricks/issues/1191)) ([db5bb11](https://github.com/gaborage/go-bricks/commit/db5bb1104c4661352d4923cc9bcedf4d8df6515f))
* **database:** reject a nil config from a DBConfigProvider ([#1234](https://github.com/gaborage/go-bricks/issues/1234)) ([1453c3c](https://github.com/gaborage/go-bricks/commit/1453c3c7ec1dd45c46eab0c859a7a63a1f446a55))
* **database:** resolve nil operands at every filter door ([#1222](https://github.com/gaborage/go-bricks/issues/1222)) ([3fbdc7b](https://github.com/gaborage/go-bricks/commit/3fbdc7b714ee6b52ba47000a85f83b21335beae9))
* **database:** validate RawExpression aliases against the grammar ([#1203](https://github.com/gaborage/go-bricks/issues/1203)) ([7d04ef9](https://github.com/gaborage/go-bricks/commit/7d04ef93202e5c92a83254d5702007dd6c9c5613))
* **deps:** update aws-sdk-go-v2 monorepo ([#1103](https://github.com/gaborage/go-bricks/issues/1103)) ([813d537](https://github.com/gaborage/go-bricks/commit/813d53740d9f28e874e7397464fe2d17250f1cc2))
* **deps:** update aws-sdk-go-v2 monorepo ([#1215](https://github.com/gaborage/go-bricks/issues/1215)) ([0cec61d](https://github.com/gaborage/go-bricks/commit/0cec61d8d6eb0a7a056ee9bc9c5ae432126989df))
* **deps:** update module github.com/stretchr/testify to v1.12.1 ([#1086](https://github.com/gaborage/go-bricks/issues/1086)) ([bdf5583](https://github.com/gaborage/go-bricks/commit/bdf5583f00a71b37f68c224f5de281b90cf6c562))
* **deps:** update module go.opentelemetry.io/contrib/instrumentation/runtime to v0.71.0 ([#1214](https://github.com/gaborage/go-bricks/issues/1214)) ([46a242a](https://github.com/gaborage/go-bricks/commit/46a242a17e874131afecdc891797fd98fa4f2cf2))
* **deps:** update module google.golang.org/grpc to v1.83.2 ([#1210](https://github.com/gaborage/go-bricks/issues/1210)) ([a1e5d86](https://github.com/gaborage/go-bricks/commit/a1e5d8669ebecb30d11385d3cfdae7b146bf96fb))
* **deps:** update opentelemetry-go monorepo ([#1211](https://github.com/gaborage/go-bricks/issues/1211)) ([bd71d94](https://github.com/gaborage/go-bricks/commit/bd71d94e7a56bf951df9db09714bf73f26b3e5a6))
* **logger:** mask and redact the error field at the Err seam ([#1223](https://github.com/gaborage/go-bricks/issues/1223)) ([c719edf](https://github.com/gaborage/go-bricks/commit/c719edf4803659309a611c03aa397edd6d9289f2))
* **logger:** mask inside opaque payloads ([#1227](https://github.com/gaborage/go-bricks/issues/1227)) ([55e5fad](https://github.com/gaborage/go-bricks/commit/55e5fad04df3aa36989ef6c1a1fe382118464834))
* **messaging:** bound the publish frame's caller-supplied shortstrs ([#1228](https://github.com/gaborage/go-bricks/issues/1228)) ([dc03ecb](https://github.com/gaborage/go-bricks/commit/dc03ecb52e399f543a64b7f2378bd9353aea4f67))
* **messaging:** gate the decode summary's field path on the payload type ([#1176](https://github.com/gaborage/go-bricks/issues/1176)) ([de4d94d](https://github.com/gaborage/go-bricks/commit/de4d94d6d340d11de01aaf33f88d17e429bec930))
* **messaging:** skip a publish attempt the deadline cannot finish ([#1267](https://github.com/gaborage/go-bricks/issues/1267)) ([753de22](https://github.com/gaborage/go-bricks/commit/753de221b8b369593f76b719e3820c4d4cac615a)), closes [#1137](https://github.com/gaborage/go-bricks/issues/1137)
* **migration:** build the PostgreSQL Flyway JDBC URL in the framework ([#1188](https://github.com/gaborage/go-bricks/issues/1188)) ([e31d290](https://github.com/gaborage/go-bricks/commit/e31d2904c9faee4874a5d2c9f0d828ffc917fceb))
* **observability:** pin the OTLP exporter tests against the shutdown race ([#1226](https://github.com/gaborage/go-bricks/issues/1226)) ([ac2eb3d](https://github.com/gaborage/go-bricks/commit/ac2eb3d53844aac631ec564f233fd4923a82fca9))
* **observability:** record span errors by type at every sink ([#1189](https://github.com/gaborage/go-bricks/issues/1189)) ([9267634](https://github.com/gaborage/go-bricks/commit/926763495c4829db67902c8c599032b5e80ef181))
* **outbox:** dead-letter a destination the broker can never accept ([#1238](https://github.com/gaborage/go-bricks/issues/1238)) ([3c9606e](https://github.com/gaborage/go-bricks/commit/3c9606e5dc1d8081b5de9cbf8463e39af27c9d58))
* **server:** catch panics thrown outside Echo's Recover ([#1224](https://github.com/gaborage/go-bricks/issues/1224)) ([9693566](https://github.com/gaborage/go-bricks/commit/9693566b64af65894131c1865f36dd3b13fc06fe))
* **server:** re-pin the alloc tripwires for the Go 1.27 toolchain ([#1180](https://github.com/gaborage/go-bricks/issues/1180)) ([64d38da](https://github.com/gaborage/go-bricks/commit/64d38da8a6718732a40e1003fe463efff732a797))
* **server:** stop echoing request input in 400 error details ([#1181](https://github.com/gaborage/go-bricks/issues/1181)) ([19d5413](https://github.com/gaborage/go-bricks/commit/19d54132cb282126e41da05416561a210f310656))
* **trace:** validate a pre-set traceparent before re-emitting it ([#1220](https://github.com/gaborage/go-bricks/issues/1220)) ([aad091b](https://github.com/gaborage/go-bricks/commit/aad091b7e0b9a13d26a5108864e2cd9908156ae2))


### Changed

* **database:** collapse the f./jf. twin predicate shells ([#1274](https://github.com/gaborage/go-bricks/issues/1274)) ([d4a5a52](https://github.com/gaborage/go-bricks/commit/d4a5a522991277227e18dffca97b2683feb81bfd))
* **database:** one oracle quoting module in sqllex ([#1271](https://github.com/gaborage/go-bricks/issues/1271)) ([cfd0d55](https://github.com/gaborage/go-bricks/commit/cfd0d5577da47cfc1a2ec80d680ee7ee697401af)), closes [#1257](https://github.com/gaborage/go-bricks/issues/1257)
* **database:** spell the fast-path test without comparison literals ([#1273](https://github.com/gaborage/go-bricks/issues/1273)) ([f6993c2](https://github.com/gaborage/go-bricks/commit/f6993c204bba300d64bf124aa378403fbcf1e4b2))
* **messaging:** drop the recording logger's stored context ([#1239](https://github.com/gaborage/go-bricks/issues/1239)) ([c95c839](https://github.com/gaborage/go-bricks/commit/c95c8391ad297ac4ade0f87f68ab87e655bba043))
* **messaging:** extract one saturating backoff helper ([#1266](https://github.com/gaborage/go-bricks/issues/1266)) ([ec55f8a](https://github.com/gaborage/go-bricks/commit/ec55f8a68b625fd7d6cf9c14b41cd09851b79987)), closes [#1249](https://github.com/gaborage/go-bricks/issues/1249)

## [0.60.0](https://github.com/gaborage/go-bricks/compare/v0.59.0...v0.60.0) (2026-08-24)


### ⚠ BREAKING CHANGES

* **database:** validate the alias handed to Columns.As ([#1166](https://github.com/gaborage/go-bricks/issues/1166))
* **database:** validate RawExpression where it is consumed ([#1165](https://github.com/gaborage/go-bricks/issues/1165))
* **database:** validate every Filter and JoinFilter column identifier ([#1159](https://github.com/gaborage/go-bricks/issues/1159))
* **database:** validate SELECT and INSERT column identifiers ([#1155](https://github.com/gaborage/go-bricks/issues/1155))
* **database:** escape interior quotes and validate every table argument ([#1152](https://github.com/gaborage/go-bricks/issues/1152))
* **server,messaging,scheduler:** report a recovered panic by type ([#1136](https://github.com/gaborage/go-bricks/issues/1136))
* **logger,app:** walk JSON arrays without comparing uncomparable values ([#1131](https://github.com/gaborage/go-bricks/issues/1131))
* **server,config:** derive the client IP only from observed hops ([#1135](https://github.com/gaborage/go-bricks/issues/1135))
* **config:** a delivered-empty debug.allowedips fails configuration resolution ([#1130](https://github.com/gaborage/go-bricks/issues/1130))
* **server,messaging,trace:** validate trace identifiers at every door ([#1128](https://github.com/gaborage/go-bricks/issues/1128))
* **config,database:** address tenant-tree errors and hints to their section ([#1127](https://github.com/gaborage/go-bricks/issues/1127))
* **config:** a delivered-empty bool config value fails startup ([#1122](https://github.com/gaborage/go-bricks/issues/1122))
* **config,app:** a delivered-empty numeric config value fails startup ([#1112](https://github.com/gaborage/go-bricks/issues/1112))
* **config:** delete the unused TestKey* constant surface ([#1108](https://github.com/gaborage/go-bricks/issues/1108))
* **logger:** name key material instead of a bare "key" needle ([#1106](https://github.com/gaborage/go-bricks/issues/1106))
* **config:** address a database section's errors to that section ([#1115](https://github.com/gaborage/go-bricks/issues/1115))
* **database:** upsert column sets must name each column once ([#1105](https://github.com/gaborage/go-bricks/issues/1105))
* **config,scheduler:** one default per scheduler timeout key ([#1107](https://github.com/gaborage/go-bricks/issues/1107))
* **streams:** run the streams lane on the delivery pipeline ([#1082](https://github.com/gaborage/go-bricks/issues/1082))
* **trace:** validate inbound trace identifiers at one seam ([#1081](https://github.com/gaborage/go-bricks/issues/1081))
* **messaging:** the pipeline contains its own tail and invokes settlement ([#1080](https://github.com/gaborage/go-bricks/issues/1080))
* **messaging:** run the AMQP lane on the delivery pipeline ([#1058](https://github.com/gaborage/go-bricks/issues/1058))
* **app:** idle-cleanup maintenance moves into the managers ([#1055](https://github.com/gaborage/go-bricks/issues/1055))
* **app:** fold the dead lifecycle helpers into App and unexport the debug JSON types ([#1045](https://github.com/gaborage/go-bricks/issues/1045))

### Added

* **messaging:** add the delivery pipeline both consume lanes will share ([#1053](https://github.com/gaborage/go-bricks/issues/1053)) ([92d63e0](https://github.com/gaborage/go-bricks/commit/92d63e0e4ad0197ad1ec3bc4ad853020e7876f4e))
* **messaging:** add the fixture that drives the lane contract ([#1073](https://github.com/gaborage/go-bricks/issues/1073)) ([7776004](https://github.com/gaborage/go-bricks/commit/77760045d3a0dce1f44be8ee47301105ac745a56))
* **messaging:** add the identity family and its counter-example lane ([#1075](https://github.com/gaborage/go-bricks/issues/1075)) ([5695f6f](https://github.com/gaborage/go-bricks/commit/5695f6f8047bb4c0724d9d5d641d3a0968f5fc1e))
* **messaging:** add the telemetry family to the lane contract ([#1076](https://github.com/gaborage/go-bricks/issues/1076)) ([9924b1f](https://github.com/gaborage/go-bricks/commit/9924b1f346382ae4346dec0e87459bed2953e21b))
* **messaging:** declare the lane contract both messaging lanes must satisfy ([#1072](https://github.com/gaborage/go-bricks/issues/1072)) ([4ab8fdb](https://github.com/gaborage/go-bricks/commit/4ab8fdb05740f15bbfc13f6e836d1ae28fbd1be8))
* **messaging:** the pipeline contains its own tail and invokes settlement ([#1080](https://github.com/gaborage/go-bricks/issues/1080)) ([c508779](https://github.com/gaborage/go-bricks/commit/c508779a617329208896696d89df91bda4779ac3))


### Fixed

* **config,app:** a delivered-empty numeric config value fails startup ([#1112](https://github.com/gaborage/go-bricks/issues/1112)) ([daefb32](https://github.com/gaborage/go-bricks/commit/daefb32832970dff3b7417b14c5543bf6a738556))
* **config,database:** address tenant-tree errors and hints to their section ([#1127](https://github.com/gaborage/go-bricks/issues/1127)) ([0cd0f68](https://github.com/gaborage/go-bricks/commit/0cd0f68758af7f63cc7aa1526a6b42029aa1d097))
* **config,scheduler:** one default per scheduler timeout key ([#1107](https://github.com/gaborage/go-bricks/issues/1107)) ([9e2fc87](https://github.com/gaborage/go-bricks/commit/9e2fc871f968292f664dd330d2439b31d8c9b313))
* **config:** a delivered-empty bool config value fails startup ([#1122](https://github.com/gaborage/go-bricks/issues/1122)) ([1cede38](https://github.com/gaborage/go-bricks/commit/1cede3896fbffd7fdd134e19e0999c58d155f954))
* **config:** a delivered-empty debug.allowedips fails configuration resolution ([#1130](https://github.com/gaborage/go-bricks/issues/1130)) ([aa97155](https://github.com/gaborage/go-bricks/commit/aa971558b0c6c2a58b1a828635833ddf354aa063))
* **config:** address a database section's errors to that section ([#1115](https://github.com/gaborage/go-bricks/issues/1115)) ([c383588](https://github.com/gaborage/go-bricks/commit/c383588b2a81e81364c94b25e22b0ccc069e193d))
* **config:** delete the unused TestKey* constant surface ([#1108](https://github.com/gaborage/go-bricks/issues/1108)) ([f6b8016](https://github.com/gaborage/go-bricks/commit/f6b80161dfda5ba31ea56beaa83d3ec96e849f9b))
* **database:** escape interior quotes and validate every table argument ([#1152](https://github.com/gaborage/go-bricks/issues/1152)) ([a7cde6c](https://github.com/gaborage/go-bricks/commit/a7cde6c499c51b10dcca18662cff3912964213d3))
* **database:** upsert column sets must name each column once ([#1105](https://github.com/gaborage/go-bricks/issues/1105)) ([443d12b](https://github.com/gaborage/go-bricks/commit/443d12b6d672409207a0b5f80403e56d6bbfed1e))
* **database:** validate every Filter and JoinFilter column identifier ([#1159](https://github.com/gaborage/go-bricks/issues/1159)) ([b8e3941](https://github.com/gaborage/go-bricks/commit/b8e3941902f7414e0c8948ede42ad25cc85ed4b8))
* **database:** validate RawExpression where it is consumed ([#1165](https://github.com/gaborage/go-bricks/issues/1165)) ([f8525f6](https://github.com/gaborage/go-bricks/commit/f8525f64a1d2ace123abf3ba49c64fb49e07dcac))
* **database:** validate SELECT and INSERT column identifiers ([#1155](https://github.com/gaborage/go-bricks/issues/1155)) ([c3eb940](https://github.com/gaborage/go-bricks/commit/c3eb940002a47aa5b01c7a1724761f364b6a4068))
* **database:** validate the alias handed to Columns.As ([#1166](https://github.com/gaborage/go-bricks/issues/1166)) ([0005e97](https://github.com/gaborage/go-bricks/commit/0005e97fcfef0637a367d35f46496d2e3da0bb89))
* **deps:** update module github.com/fxamacker/cbor/v2 to v2.9.3 ([#1060](https://github.com/gaborage/go-bricks/issues/1060)) ([e996b13](https://github.com/gaborage/go-bricks/commit/e996b131ee0bf1edc30316c6d36fd0b8b58e8ee8))
* **deps:** update module github.com/rabbitmq/amqp091-go to v1.14.0 ([#1062](https://github.com/gaborage/go-bricks/issues/1062)) ([0848188](https://github.com/gaborage/go-bricks/commit/084818802d6f36738dbb8241b2be3b1541f34188))
* **deps:** update module github.com/stretchr/testify to v1.12.0 ([#1046](https://github.com/gaborage/go-bricks/issues/1046)) ([9243568](https://github.com/gaborage/go-bricks/commit/924356892409fa4613e8e034e2cbd3aa2cfdb2f1))
* **deps:** update module google.golang.org/grpc to v1.83.1 ([#1085](https://github.com/gaborage/go-bricks/issues/1085)) ([53f3408](https://github.com/gaborage/go-bricks/commit/53f3408b8964293998816954d6e3d69c8d7dd2ce))
* **logger,app:** walk JSON arrays without comparing uncomparable values ([#1131](https://github.com/gaborage/go-bricks/issues/1131)) ([71c1255](https://github.com/gaborage/go-bricks/commit/71c1255157f96814620daf99a5d448d667ce9cc1))
* **logger:** name key material instead of a bare "key" needle ([#1106](https://github.com/gaborage/go-bricks/issues/1106)) ([71a7df9](https://github.com/gaborage/go-bricks/commit/71a7df997b5da6adaa0b0ba065da9a0fcb1738bd))
* **messaging:** align published CorrelationId with X-Request-ID ([#1099](https://github.com/gaborage/go-bricks/issues/1099)) ([db54b50](https://github.com/gaborage/go-bricks/commit/db54b507840e4a44843f468ddb42c465f60d580f))
* **messaging:** re-bind the trace ID, share one outcome-log spine ([#1068](https://github.com/gaborage/go-bricks/issues/1068)) ([03e7c5f](https://github.com/gaborage/go-bricks/commit/03e7c5f9e1f8e430728a400fb7075b72a956b78f))
* **migration:** validate and forward database.tls in the migrate CLI ([#1041](https://github.com/gaborage/go-bricks/issues/1041)) ([a8df861](https://github.com/gaborage/go-bricks/commit/a8df86165127faeb7466b8ab3873e01bfd2f76b4))
* **outbox:** bound the error text the relay persists ([#1084](https://github.com/gaborage/go-bricks/issues/1084)) ([cbd2d9e](https://github.com/gaborage/go-bricks/commit/cbd2d9eb2aff2767cfa4a8b09ee86252d680764b))
* **scheduler:** record job metrics under the traced context ([#1102](https://github.com/gaborage/go-bricks/issues/1102)) ([8c0afcd](https://github.com/gaborage/go-bricks/commit/8c0afcdc26e95b7689b54e88efeff3bac03c7ba2))
* **server,config:** derive the client IP only from observed hops ([#1135](https://github.com/gaborage/go-bricks/issues/1135)) ([da02570](https://github.com/gaborage/go-bricks/commit/da0257063321e9c7590950c38e8cb3201f110ee9))
* **server,messaging,scheduler:** report a recovered panic by type ([#1136](https://github.com/gaborage/go-bricks/issues/1136)) ([e7aef8c](https://github.com/gaborage/go-bricks/commit/e7aef8cc637756be716e5481541f9fa7e94ae20f))
* **server,messaging,trace:** validate trace identifiers at every door ([#1128](https://github.com/gaborage/go-bricks/issues/1128)) ([d715a7c](https://github.com/gaborage/go-bricks/commit/d715a7c5aab9fd0e03b87aa5c0e1036f492a8e94))
* **server:** gate response error detail on debug and development ([#1161](https://github.com/gaborage/go-bricks/issues/1161)) ([dfd77d1](https://github.com/gaborage/go-bricks/commit/dfd77d1789a5b892e13b942b311f49113d6fa082))
* **trace:** validate inbound trace identifiers at one seam ([#1081](https://github.com/gaborage/go-bricks/issues/1081)) ([27acbf6](https://github.com/gaborage/go-bricks/commit/27acbf6ddf051aac5bf2dbdc9f1f9de870f8335e))


### Changed

* **app:** each resource kind owns a slot for probe, pre-init and close ([#1048](https://github.com/gaborage/go-bricks/issues/1048)) ([ccb6889](https://github.com/gaborage/go-bricks/commit/ccb6889d1c13bc77b7f434f81ece75ba88f0c669))
* **app:** fold the dead lifecycle helpers into App and unexport the debug JSON types ([#1045](https://github.com/gaborage/go-bricks/issues/1045)) ([fceb4b8](https://github.com/gaborage/go-bricks/commit/fceb4b8ac6101a4292e9ae2726b4255d515c6660))
* **app:** idle-cleanup maintenance moves into the managers ([#1055](https://github.com/gaborage/go-bricks/issues/1055)) ([db8dfe7](https://github.com/gaborage/go-bricks/commit/db8dfe716619553ac90511cc318d903ae640bbe9))
* **app:** judge every readiness kind from one probe description ([#1042](https://github.com/gaborage/go-bricks/issues/1042)) ([65e243b](https://github.com/gaborage/go-bricks/commit/65e243b7857d9f62ab24c28d8a229ae2651fcaac))
* **app:** render /ready and the debug view from the readiness module ([#1043](https://github.com/gaborage/go-bricks/issues/1043)) ([c3fb714](https://github.com/gaborage/go-bricks/commit/c3fb7144b3e59b5f1408b1dd367c9373e1afb35e))
* **app:** run the start and stop phases through the slots ([#1052](https://github.com/gaborage/go-bricks/issues/1052)) ([5e3d951](https://github.com/gaborage/go-bricks/commit/5e3d951c95410542b405e3e5ca4842aa15398451))
* **config:** derive the owned koanf defaults from normalize ([#1116](https://github.com/gaborage/go-bricks/issues/1116)) ([4c94c8f](https://github.com/gaborage/go-bricks/commit/4c94c8f20b44c8124b9d005fd74dd0c2d266e0c5))
* **config:** fold scheduler timeout defaults into the derived set ([#1117](https://github.com/gaborage/go-bricks/issues/1117)) ([c0104aa](https://github.com/gaborage/go-bricks/commit/c0104aac62595ce8f9965222f765a37675c71d13))
* **messaging:** one consume recorder for both lanes ([#1050](https://github.com/gaborage/go-bricks/issues/1050)) ([41d919f](https://github.com/gaborage/go-bricks/commit/41d919fc4641e6fe0d442d77f029037109001b83))
* **messaging:** run the AMQP lane on the delivery pipeline ([#1058](https://github.com/gaborage/go-bricks/issues/1058)) ([5d6f100](https://github.com/gaborage/go-bricks/commit/5d6f100c59d783876309b54c01ec729c002f3e27))
* **streams:** route the manager through an unexported Environment port ([#1049](https://github.com/gaborage/go-bricks/issues/1049)) ([d8c3b63](https://github.com/gaborage/go-bricks/commit/d8c3b63fb57ff530561e189a2c35c99df9c69fe6))
* **streams:** run the streams lane on the delivery pipeline ([#1082](https://github.com/gaborage/go-bricks/issues/1082)) ([ef35baa](https://github.com/gaborage/go-bricks/commit/ef35baad9ff32dd3f2ab6eafabbce5cc64234039))

## [0.59.0](https://github.com/gaborage/go-bricks/compare/v0.58.1...v0.59.0) (2026-08-17)


### ⚠ BREAKING CHANGES

* **config,keystore:** keystore.secretminlength is a tri-state pointer ([#1039](https://github.com/gaborage/go-bricks/issues/1039))
* **app:** validate the config on every construction path ([#1019](https://github.com/gaborage/go-bricks/issues/1019))
* **config:** fail closed on database.tls misconfiguration ([#1005](https://github.com/gaborage/go-bricks/issues/1005))
* **database:** require upsert conflict columns in the insert set ([#996](https://github.com/gaborage/go-bricks/issues/996))
* **config:** infer type and validate vendor rules on dynamic configs ([#1002](https://github.com/gaborage/go-bricks/issues/1002))
* **database:** reject conflict columns in the upsert update set on both vendors ([#991](https://github.com/gaborage/go-bricks/issues/991))
* **migration:** redact role passwords before the newline split ([#989](https://github.com/gaborage/go-bricks/issues/989))
* **cache:** add CompareAndDelete for safe conditional release ([#977](https://github.com/gaborage/go-bricks/issues/977))
* **messaging:** consume RabbitMQ stream queues over AMQP 0.9.1 ([#967](https://github.com/gaborage/go-bricks/issues/967))
* **server:** derive client IP through trusted proxies, not raw XFF ([#965](https://github.com/gaborage/go-bricks/issues/965))

### Added

* **app:** start native stream consumers during prepareRuntime ([#973](https://github.com/gaborage/go-bricks/issues/973)) ([371236a](https://github.com/gaborage/go-bricks/commit/371236afe467e151e58fb15fc8d0609badd7018e))
* **cache:** add CompareAndDelete for safe conditional release ([#977](https://github.com/gaborage/go-bricks/issues/977)) ([fae813f](https://github.com/gaborage/go-bricks/commit/fae813f115e66c1aa98b6ad8976df65c5a33a856))
* **config:** add the messaging.streams configuration block ([#972](https://github.com/gaborage/go-bricks/issues/972)) ([058a3a3](https://github.com/gaborage/go-bricks/commit/058a3a37e3d75827319e95e4fe96297dd6254660))
* **messaging:** add stream declarations and offset positions ([#970](https://github.com/gaborage/go-bricks/issues/970)) ([4d16e36](https://github.com/gaborage/go-bricks/commit/4d16e36be0a0bc85b14da2fcb1bcabc290bc9373))
* **messaging:** add the native stream consumption manager ([#971](https://github.com/gaborage/go-bricks/issues/971)) ([03c88b2](https://github.com/gaborage/go-bricks/commit/03c88b2bbd156fc5ad5032c976664b5ae43e3750))
* **messaging:** consume RabbitMQ stream queues over AMQP 0.9.1 ([#967](https://github.com/gaborage/go-bricks/issues/967)) ([574bac6](https://github.com/gaborage/go-bricks/commit/574bac6c36093234f7ed8bca66a3a744866b9533))
* **streams:** add confirmed native publishing to plain streams ([#1007](https://github.com/gaborage/go-bricks/issues/1007)) ([dcfd3bc](https://github.com/gaborage/go-bricks/commit/dcfd3bc67cf13d3e3e0c030e829913a887ec5548))
* **streams:** consume super streams as a partitioned group ([#986](https://github.com/gaborage/go-bricks/issues/986)) ([59a3876](https://github.com/gaborage/go-bricks/commit/59a3876e3cb9133d83b4888e15dbd6ec89988b5c))
* **streams:** declare super streams and their consumers ([#984](https://github.com/gaborage/go-bricks/issues/984)) ([59feb6b](https://github.com/gaborage/go-bricks/commit/59feb6b3a19e794f7b3215c7119b719b09a0326c))
* **streams:** publish to super streams by routing key ([#1010](https://github.com/gaborage/go-bricks/issues/1010)) ([acbc8d8](https://github.com/gaborage/go-bricks/commit/acbc8d87ab7ddc14def90f6f285f6215c5ef3d7b))


### Fixed

* **app:** validate the config on every construction path ([#1019](https://github.com/gaborage/go-bricks/issues/1019)) ([6d9733a](https://github.com/gaborage/go-bricks/commit/6d9733a26ba047b6ba91272ef3b05fa6a5c51155))
* **config,keystore:** keystore.secretminlength is a tri-state pointer ([#1039](https://github.com/gaborage/go-bricks/issues/1039)) ([b6efcea](https://github.com/gaborage/go-bricks/commit/b6efceafffb81aae0117b96b6e66d136eaf8f03f))
* **config:** fail closed on database.tls misconfiguration ([#1005](https://github.com/gaborage/go-bricks/issues/1005)) ([f16c35d](https://github.com/gaborage/go-bricks/commit/f16c35d8f331c200ff45c343c9ac028a40120653))
* **config:** infer type and validate vendor rules on dynamic configs ([#1002](https://github.com/gaborage/go-bricks/issues/1002)) ([b30c2f5](https://github.com/gaborage/go-bricks/commit/b30c2f5f8b9055229ffeafa8fd8a7127f322d559))
* **database:** match upsert conflict columns by vendor identity ([#1000](https://github.com/gaborage/go-bricks/issues/1000)) ([42ac212](https://github.com/gaborage/go-bricks/commit/42ac212190f5da9953af4ebc81f31979704f7fa3))
* **database:** reject conflict columns in the upsert update set on both vendors ([#991](https://github.com/gaborage/go-bricks/issues/991)) ([c5ba4df](https://github.com/gaborage/go-bricks/commit/c5ba4df8213ddd9a5a1eb46b4c0e21383d1556ef))
* **database:** require upsert conflict columns in the insert set ([#996](https://github.com/gaborage/go-bricks/issues/996)) ([d554696](https://github.com/gaborage/go-bricks/commit/d554696ce504d69c556e8598a494c84b0007507b))
* **deps:** update aws-sdk-go-v2 monorepo ([#961](https://github.com/gaborage/go-bricks/issues/961)) ([510d359](https://github.com/gaborage/go-bricks/commit/510d3594298c9dc4edd598386edf30d9611cccaf))
* **deps:** update aws-sdk-go-v2 monorepo ([#990](https://github.com/gaborage/go-bricks/issues/990)) ([ee3bac8](https://github.com/gaborage/go-bricks/commit/ee3bac84a0052885ae44c8af6be94111d9ce71e8))
* **deps:** update module github.com/gaborage/go-bricks to v0.58.1 ([#960](https://github.com/gaborage/go-bricks/issues/960)) ([569eea0](https://github.com/gaborage/go-bricks/commit/569eea0c11e3590eb7198e449a18702b8267b896))
* **deps:** upgrade integration images and Renovate-manage the pins ([#993](https://github.com/gaborage/go-bricks/issues/993)) ([be321fe](https://github.com/gaborage/go-bricks/commit/be321fe23574b953212842615487c96476172093))
* **migration:** redact role passwords before the newline split ([#989](https://github.com/gaborage/go-bricks/issues/989)) ([6a0e8d5](https://github.com/gaborage/go-bricks/commit/6a0e8d52fcc34a2e792d9c4d15437a0da23561fa))
* **mutatediff:** exclude the integration-only containers package ([#969](https://github.com/gaborage/go-bricks/issues/969)) ([93d005b](https://github.com/gaborage/go-bricks/commit/93d005bbc69a6339ebc5257abfeac216bd6ea19e))
* **mutate:** sandbox gate builds away from shared GOCACHE ([#1008](https://github.com/gaborage/go-bricks/issues/1008)) ([ed76b9a](https://github.com/gaborage/go-bricks/commit/ed76b9a08d35e76e2545d95c969ed82d6c2f071f))
* **mutate:** skip packages whose dry run yields no report ([#1017](https://github.com/gaborage/go-bricks/issues/1017)) ([f566861](https://github.com/gaborage/go-bricks/commit/f5668617034c0b8e658c968da26701729a735e9e))
* **server:** derive client IP through trusted proxies, not raw XFF ([#965](https://github.com/gaborage/go-bricks/issues/965)) ([925665d](https://github.com/gaborage/go-bricks/commit/925665de08b6fed3a0696dd968c95154e26a35ab))
* **streams:** resume from a known position when an offset query fails ([#985](https://github.com/gaborage/go-bricks/issues/985)) ([f2a17aa](https://github.com/gaborage/go-bricks/commit/f2a17aaebdbf572a2a42ffb840fa91679f896dc7))
* **testing:** permit transient non-exclusive queues in RabbitMQ testcontainer ([#999](https://github.com/gaborage/go-bricks/issues/999)) ([d1943fe](https://github.com/gaborage/go-bricks/commit/d1943feb4b2137b89a7ec08444a5afe2cc936695))


### Changed

* **config,app:** delete the mirrored defaults the bypass required ([#1021](https://github.com/gaborage/go-bricks/issues/1021)) ([2112a3a](https://github.com/gaborage/go-bricks/commit/2112a3aeafb9651e021a217b937cab35fc720af1))
* **config:** one database-section normalization module behind two doors ([#1016](https://github.com/gaborage/go-bricks/issues/1016)) ([0862124](https://github.com/gaborage/go-bricks/commit/0862124247ef8a32162982c89f3280dc5271dc44))
* **config:** presence step heads normalize; split multitenant and databases ([#1033](https://github.com/gaborage/go-bricks/issues/1033)) ([482db21](https://github.com/gaborage/go-bricks/commit/482db210a235859a4f1d018ca0e2c02e11d00096))
* **config:** split cache and messaging into normalize and check phases ([#1035](https://github.com/gaborage/go-bricks/issues/1035)) ([8f0b232](https://github.com/gaborage/go-bricks/commit/8f0b232aba307f1873e147316731f5b0ff8ab5fc))
* **config:** split Validate into normalize and check phases ([#1032](https://github.com/gaborage/go-bricks/issues/1032)) ([3646020](https://github.com/gaborage/go-bricks/commit/3646020146e38f55a15ecfcc4df38954772258e9))

## [0.58.1](https://github.com/gaborage/go-bricks/compare/v0.58.0...v0.58.1) (2026-08-10)


### Fixed

* **app:** request the per-goroutine pprof dump the analyzer parses ([#941](https://github.com/gaborage/go-bricks/issues/941)) ([0859fb1](https://github.com/gaborage/go-bricks/commit/0859fb13e3891369a2891bbb4dd104752e64fbaa))
* **app:** stop doomed cache probe leases and messaging body contradiction ([#947](https://github.com/gaborage/go-bricks/issues/947)) ([c0f23a5](https://github.com/gaborage/go-bricks/commit/c0f23a548df9712ae3082e603b5c49e58d8e4fe0))
* **build:** restore make test-integration package list ([#944](https://github.com/gaborage/go-bricks/issues/944)) ([b4d47ea](https://github.com/gaborage/go-bricks/commit/b4d47ea9167da55efd2207f160d016fa2775b8fd))
* **cache:** clamp sub-millisecond CompareAndSet TTLs to 1ms ([#946](https://github.com/gaborage/go-bricks/issues/946)) ([90207cd](https://github.com/gaborage/go-bricks/commit/90207cd6df94da2ed405f396d2fe1be116be5e68))
* **cache:** preserve sub-second time.Time precision in CBOR ([#937](https://github.com/gaborage/go-bricks/issues/937)) ([b2cf8b9](https://github.com/gaborage/go-bricks/commit/b2cf8b9f11b43115838289db774766b4ee0d0950))
* **cache:** register the documented cache.manager.* metrics ([#936](https://github.com/gaborage/go-bricks/issues/936)) ([595bb58](https://github.com/gaborage/go-bricks/commit/595bb58f7f4b43d72ed2f25c4c648d4fe76ae6f9))
* **config:** reject NaN trace sampling rate ([#953](https://github.com/gaborage/go-bricks/issues/953)) ([f63747b](https://github.com/gaborage/go-bricks/commit/f63747b80298413877cfaec7d21a3788351fdc0e))
* **database:** keep the PostgreSQL DSN out of ParseConfig startup errors ([#945](https://github.com/gaborage/go-bricks/issues/945)) ([9d15521](https://github.com/gaborage/go-bricks/commit/9d15521c33237dbcd086f71d2c12b3843f9acca7))
* **deps:** update aws-sdk-go-v2 monorepo ([#891](https://github.com/gaborage/go-bricks/issues/891)) ([fa5f791](https://github.com/gaborage/go-bricks/commit/fa5f7918862b4151fff2ce2bf65c71850144a506))
* **deps:** update module github.com/gaborage/go-bricks to v0.58.0 ([#930](https://github.com/gaborage/go-bricks/issues/930)) ([f59ef0f](https://github.com/gaborage/go-bricks/commit/f59ef0f9791ccc69976a30ab300527d2c3f98820))
* **deps:** update testcontainers-go monorepo to v0.44.0 ([#910](https://github.com/gaborage/go-bricks/issues/910)) ([0920464](https://github.com/gaborage/go-bricks/commit/09204641fac63053e2223bcef444d8ecf98b694a))
* **messaging:** EnsureConsumers fails closed after manager Close ([#950](https://github.com/gaborage/go-bricks/issues/950)) ([06ac6e5](https://github.com/gaborage/go-bricks/commit/06ac6e5ebd335e9879d6e7a971a9a36e572d5d2b))
* **messaging:** record consume metrics on success and start a receive span ([#940](https://github.com/gaborage/go-bricks/issues/940)) ([799641f](https://github.com/gaborage/go-bricks/commit/799641f3fe3d8549d7249caf4e9b83509c3deb55))
* **migration:** guard tenant-source redirects and cap response reads ([#942](https://github.com/gaborage/go-bricks/issues/942)) ([1e808cb](https://github.com/gaborage/go-bricks/commit/1e808cb54bf4ebba1832d2ceb056c2fae6ed8e67))
* **observability:** sample logs at 0.01% resolution, not whole percent ([#949](https://github.com/gaborage/go-bricks/issues/949)) ([7ab1b32](https://github.com/gaborage/go-bricks/commit/7ab1b32bbab05356f268ad8504ae2459dee455dd))
* **resourcepool:** Close defers leased entries to final release ([#952](https://github.com/gaborage/go-bricks/issues/952)) ([7b8fa84](https://github.com/gaborage/go-bricks/commit/7b8fa846620690413dc29f5e8fc33ca0e2d7fb00))
* **server:** bind header slices of named string types ([#939](https://github.com/gaborage/go-bricks/issues/939)) ([e609408](https://github.com/gaborage/go-bricks/commit/e60940833c692d1485028f58b3949f5936951c06))


### Changed

* **logger:** first-byte dispatch for sensitive-field matching ([#948](https://github.com/gaborage/go-bricks/issues/948)) ([b55194e](https://github.com/gaborage/go-bricks/commit/b55194e75baacf5097f6d638b1e96c0d87eb6f49))
* **messaging/httpclient/database:** guard debug log sites and fold SQL prefix matching ([#954](https://github.com/gaborage/go-bricks/issues/954)) ([89ae7a9](https://github.com/gaborage/go-bricks/commit/89ae7a93940cfab48a54d2b939bf17141b6a11a1))
* **messaging:** drop the per-delivery WithFields logger layer ([#951](https://github.com/gaborage/go-bricks/issues/951)) ([7259011](https://github.com/gaborage/go-bricks/commit/7259011c7a16572f029136290309eacd0aef711e))
* **observability:** single-walk log routing; drop bridge re-walk ([#955](https://github.com/gaborage/go-bricks/issues/955)) ([8c1c017](https://github.com/gaborage/go-bricks/commit/8c1c017c1d01f21ca35b502b649a5f79662b5e8d))

## [0.58.0](https://github.com/gaborage/go-bricks/compare/v0.57.0...v0.58.0) (2026-08-08)


### ⚠ BREAKING CHANGES

* **observability:** log records stop duplicating resource identity ([#929](https://github.com/gaborage/go-bricks/issues/929))
* **logger:** reserve resource-identity namespaces in OTel bridge ([#920](https://github.com/gaborage/go-bricks/issues/920))
* **jose:** remove dead PolicyRegistry from the public API ([#916](https://github.com/gaborage/go-bricks/issues/916))
* **app:** fail startup when the cache manager cannot be built ([#919](https://github.com/gaborage/go-bricks/issues/919))
* **server:** remove unreferenced Test*Timeout constants ([#917](https://github.com/gaborage/go-bricks/issues/917))

### Fixed

* **app:** fail startup when the cache manager cannot be built ([#919](https://github.com/gaborage/go-bricks/issues/919)) ([a0e3ceb](https://github.com/gaborage/go-bricks/commit/a0e3ceb44e12754da644f8b2cbcffccf4b7894d7))
* **database:** keep Oracle pagination above math.MaxInt ([#922](https://github.com/gaborage/go-bricks/issues/922)) ([378e4ba](https://github.com/gaborage/go-bricks/commit/378e4bad95299bebe6b9e5a6cbf6603c45f0554a))
* **deps:** update module github.com/gaborage/go-bricks to v0.57.0 ([#911](https://github.com/gaborage/go-bricks/issues/911)) ([b0627b2](https://github.com/gaborage/go-bricks/commit/b0627b264626f0cb7dfe4fdb93513cc6ad97ec3b))
* **jose:** remove dead PolicyRegistry from the public API ([#916](https://github.com/gaborage/go-bricks/issues/916)) ([3f90ddc](https://github.com/gaborage/go-bricks/commit/3f90ddc3436ceedc925457a3d350fe302bc16364))
* **logger:** reserve resource-identity namespaces in OTel bridge ([#920](https://github.com/gaborage/go-bricks/issues/920)) ([099cd40](https://github.com/gaborage/go-bricks/commit/099cd40ddf2629a423e80c7f8afe41cd884cf48e))
* **observability:** log records stop duplicating resource identity ([#929](https://github.com/gaborage/go-bricks/issues/929)) ([eca4ee1](https://github.com/gaborage/go-bricks/commit/eca4ee1e45968a5aa8089d16a4f7a66ca46e1367))
* **server:** remove unreferenced Test*Timeout constants ([#917](https://github.com/gaborage/go-bricks/issues/917)) ([b633b17](https://github.com/gaborage/go-bricks/commit/b633b17cd0aa7e8c688839b2ef96085f64ebf101))


### Changed

* **mutatediff:** cache clean per-package mutation results ([#923](https://github.com/gaborage/go-bricks/issues/923)) ([6e7717e](https://github.com/gaborage/go-bricks/commit/6e7717e886338bdd9f799a0162b6b76f8bf8bcce))
* **observability:** stop allocating per log record on export ([#918](https://github.com/gaborage/go-bricks/issues/918)) ([8e20490](https://github.com/gaborage/go-bricks/commit/8e2049094fb421a0910d605fded11ebefdbfdb33))

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
