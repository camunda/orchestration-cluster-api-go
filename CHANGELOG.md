# [11.0.0-alpha.23](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.22...v11.0.0-alpha.23) (2026-11-25)


### Features

* add example coverage for createProbe, activateProbes, answerProbe ([#237](https://github.com/camunda/orchestration-cluster-api-go/issues/237)) ([74b8fc4](https://github.com/camunda/orchestration-cluster-api-go/commit/74b8fc49ee326ce0cf1a00711eaed20154f4c049)), closes [#233](https://github.com/camunda/orchestration-cluster-api-go/issues/233)

# [11.0.0-alpha.22](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.21...v11.0.0-alpha.22) (2026-11-25)


### Performance Improvements

* **worker:** event-driven slot wake-up and lighter REST ack hot path ([#235](https://github.com/camunda/orchestration-cluster-api-go/issues/235)) ([39594d7](https://github.com/camunda/orchestration-cluster-api-go/commit/39594d7effc8264c3bb34c3f7f72763f7851dbf5)), closes [#234](https://github.com/camunda/orchestration-cluster-api-go/issues/234)

# [11.0.0-alpha.21](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.20...v11.0.0-alpha.21) (2026-11-24)


### Features

* add example coverage for searchVariableHistory ([#231](https://github.com/camunda/orchestration-cluster-api-go/issues/231)) ([e5a2a35](https://github.com/camunda/orchestration-cluster-api-go/commit/e5a2a35994a3a76e1609c9bf865200e88bb71185))

# [11.0.0-alpha.20](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.19...v11.0.0-alpha.20) (2026-11-22)


### Bug Fixes

* keep x-clearable dates as time.Time and detect them in hook 09 ([#129](https://github.com/camunda/orchestration-cluster-api-go/issues/129)) ([d321976](https://github.com/camunda/orchestration-cluster-api-go/commit/d321976ced156c52923565ff246739563441d7ea)), closes [camunda/camunda#68710](https://github.com/camunda/camunda/issues/68710)

# [11.0.0-alpha.19](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.18...v11.0.0-alpha.19) (2026-11-21)


### Bug Fixes

* inject default tenant on suspend/resumeProcessDefinitions by id, derived from body shape ([#218](https://github.com/camunda/orchestration-cluster-api-go/issues/218)) ([12fb485](https://github.com/camunda/orchestration-cluster-api-go/commit/12fb4850d7adb376c9b62e92bbfafeb82a42ac02)), closes [#217](https://github.com/camunda/orchestration-cluster-api-go/issues/217) [#217](https://github.com/camunda/orchestration-cluster-api-go/issues/217)

# [11.0.0-alpha.18](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.17...v11.0.0-alpha.18) (2026-11-20)


### Features

* recycle job streams at a jittered max lifetime and on inactivity ([#215](https://github.com/camunda/orchestration-cluster-api-go/issues/215)) ([5501207](https://github.com/camunda/orchestration-cluster-api-go/commit/5501207d9bb1156bcc453fb1fdbab586640f29a5)), closes [#214](https://github.com/camunda/orchestration-cluster-api-go/issues/214)

# [11.0.0-alpha.17](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.16...v11.0.0-alpha.17) (2026-11-20)


### Features

* send the default tenant (<default> unless configured) on tenant-scoped commands ([#169](https://github.com/camunda/orchestration-cluster-api-go/issues/169)) ([da18278](https://github.com/camunda/orchestration-cluster-api-go/commit/da182783bf4adffa59aa492da33768c7f738f104)), closes [#168](https://github.com/camunda/orchestration-cluster-api-go/issues/168)

# [11.0.0-alpha.16](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.15...v11.0.0-alpha.16) (2026-11-20)


### Features

* back off the stream worker sidecar poll on empty results ([#213](https://github.com/camunda/orchestration-cluster-api-go/issues/213)) ([825df0f](https://github.com/camunda/orchestration-cluster-api-go/commit/825df0f54bc7c1d5e64e638d9808f8e7ed16f4fb)), closes [#212](https://github.com/camunda/orchestration-cluster-api-go/issues/212)

# [11.0.0-alpha.15](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.14...v11.0.0-alpha.15) (2026-11-19)


### Features

* send attributable User-Agent on REST, OAuth and gRPC ([#208](https://github.com/camunda/orchestration-cluster-api-go/issues/208)) ([aca7251](https://github.com/camunda/orchestration-cluster-api-go/commit/aca72510de7a0b966345f19f44d43550f03188fa)), closes [#207](https://github.com/camunda/orchestration-cluster-api-go/issues/207)

# [11.0.0-alpha.14](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.13...v11.0.0-alpha.14) (2026-11-17)


### Bug Fixes

* size the HTTP idle pool from job workers' in-flight capacity ([#197](https://github.com/camunda/orchestration-cluster-api-go/issues/197)) ([58ff322](https://github.com/camunda/orchestration-cluster-api-go/commit/58ff322058e6eabbf74d94d0bfd93dd8804ca9c1)), closes [#196](https://github.com/camunda/orchestration-cluster-api-go/issues/196)

# [11.0.0-alpha.13](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.12...v11.0.0-alpha.13) (2026-11-15)


### Bug Fixes

* adapt job result corrections to the ClearableDateTime shape ([#166](https://github.com/camunda/orchestration-cluster-api-go/issues/166)) ([6738313](https://github.com/camunda/orchestration-cluster-api-go/commit/6738313842ef8143154ecc317cf0ca329fce2920)), closes [#165](https://github.com/camunda/orchestration-cluster-api-go/issues/165)

# [11.0.0-alpha.12](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.11...v11.0.0-alpha.12) (2026-11-15)


### Bug Fixes

* **auth:** keep token-response content out of decoding errors ([#178](https://github.com/camunda/orchestration-cluster-api-go/issues/178)) ([73f9ce2](https://github.com/camunda/orchestration-cluster-api-go/commit/73f9ce27db965e7fb1c2ece856c5a963dcc34a55)), closes [#143](https://github.com/camunda/orchestration-cluster-api-go/issues/143) [#174](https://github.com/camunda/orchestration-cluster-api-go/issues/174)

# [11.0.0-alpha.11](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.10...v11.0.0-alpha.11) (2026-11-15)


### Bug Fixes

* **config:** render HTTPClient by type in Config formatting ([#177](https://github.com/camunda/orchestration-cluster-api-go/issues/177)) ([bfdb790](https://github.com/camunda/orchestration-cluster-api-go/commit/bfdb790eb9ab5fcfc23e94c1cde34f60272c602a)), closes [#171](https://github.com/camunda/orchestration-cluster-api-go/issues/171)

# [11.0.0-alpha.10](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.9...v11.0.0-alpha.10) (2026-11-15)


### Bug Fixes

* **auth:** stop echoing the token-endpoint response body in errors ([#143](https://github.com/camunda/orchestration-cluster-api-go/issues/143)) ([e93832c](https://github.com/camunda/orchestration-cluster-api-go/commit/e93832cffc46aae944d7120eebf492c96d993c93)), closes [camunda/sdk-infra#88](https://github.com/camunda/sdk-infra/issues/88)
* **auth:** write the oauth token cache atomically with owner-only permissions ([#145](https://github.com/camunda/orchestration-cluster-api-go/issues/145)) ([73c20aa](https://github.com/camunda/orchestration-cluster-api-go/commit/73c20aa53da38e2328a992f5d7c6671d6d36a010)), closes [camunda/sdk-infra#88](https://github.com/camunda/sdk-infra/issues/88)
* **diag:** drive environment snapshot redaction from ConfigSchema secrets ([#151](https://github.com/camunda/orchestration-cluster-api-go/issues/151)) ([05e8a7d](https://github.com/camunda/orchestration-cluster-api-go/commit/05e8a7d590065822abaac491b62bc76340c5f52a)), closes [#150](https://github.com/camunda/orchestration-cluster-api-go/issues/150) [#144](https://github.com/camunda/orchestration-cluster-api-go/issues/144)
* **job:** never return a nil map from RawVariables or CustomHeaders ([#152](https://github.com/camunda/orchestration-cluster-api-go/issues/152)) ([906d817](https://github.com/camunda/orchestration-cluster-api-go/commit/906d817103d21185bf6af249380e9eb717a38a57)), closes [#147](https://github.com/camunda/orchestration-cluster-api-go/issues/147)
* **worker:** recover a panicking job handler and fail the job ([#149](https://github.com/camunda/orchestration-cluster-api-go/issues/149)) ([9945420](https://github.com/camunda/orchestration-cluster-api-go/commit/9945420c426e5ac9325eee4ea8f7c07ac4611a03)), closes [camunda/sdk-infra#89](https://github.com/camunda/sdk-infra/issues/89) [camunda/sdk-infra#90](https://github.com/camunda/sdk-infra/issues/90)

# [11.0.0-alpha.9](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.8...v11.0.0-alpha.9) (2026-11-15)


### Bug Fixes

* **config:** redact credentials in Config and TLSConfig formatting ([#144](https://github.com/camunda/orchestration-cluster-api-go/issues/144)) ([39a6a35](https://github.com/camunda/orchestration-cluster-api-go/commit/39a6a35b1ffad796b496664f2201bc2b36423fb6)), closes [camunda/sdk-infra#88](https://github.com/camunda/sdk-infra/issues/88) [#v](https://github.com/camunda/orchestration-cluster-api-go/issues/v)

# [11.0.0-alpha.8](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.7...v11.0.0-alpha.8) (2026-11-14)


### Bug Fixes

* size the HTTP connection pool for job workers and allow user-supplied transports ([#163](https://github.com/camunda/orchestration-cluster-api-go/issues/163)) ([281a6cb](https://github.com/camunda/orchestration-cluster-api-go/commit/281a6cb394319cdb41a31274eb3f25976b5a93f7)), closes [#162](https://github.com/camunda/orchestration-cluster-api-go/issues/162)

# [11.0.0-alpha.7](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.6...v11.0.0-alpha.7) (2026-11-13)


### Bug Fixes

* **deps:** update grpc to v1.83.2 and clear dev tooling alerts ([#131](https://github.com/camunda/orchestration-cluster-api-go/issues/131)) ([c6c8552](https://github.com/camunda/orchestration-cluster-api-go/commit/c6c85524e4e8688a9973e16e168baa64ef543ba9))

# [11.0.0-alpha.6](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.5...v11.0.0-alpha.6) (2026-11-11)


### Bug Fixes

* ignore unknown fields when decoding response models ([#126](https://github.com/camunda/orchestration-cluster-api-go/issues/126)) ([190f456](https://github.com/camunda/orchestration-cluster-api-go/commit/190f4566c09c3d49fdfab0e4b183e865d924affe)), closes [#124](https://github.com/camunda/orchestration-cluster-api-go/issues/124)

# [11.0.0-alpha.5](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.4...v11.0.0-alpha.5) (2026-11-09)


### Features

* send job results from job workers ([#122](https://github.com/camunda/orchestration-cluster-api-go/issues/122)) ([93d8025](https://github.com/camunda/orchestration-cluster-api-go/commit/93d802517be5bb5af5892523c5a066efb54a98f7))

# [11.0.0-alpha.4](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.3...v11.0.0-alpha.4) (2026-11-04)


### Features

* support lookupUserTask and typed expression tenantId ([#119](https://github.com/camunda/orchestration-cluster-api-go/issues/119)) ([3a1bfe8](https://github.com/camunda/orchestration-cluster-api-go/commit/3a1bfe8d43c93fdcf75d4e5465e61c4c244e57c3)), closes [camunda/camunda#67949](https://github.com/camunda/camunda/issues/67949)

# [11.0.0-alpha.3](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.2...v11.0.0-alpha.3) (2026-11-03)


### Bug Fixes

* **deps:** update module golang.org/x/net to v0.62.0 [security] ([#97](https://github.com/camunda/orchestration-cluster-api-go/issues/97)) ([bc08021](https://github.com/camunda/orchestration-cluster-api-go/commit/bc080211e48a1bafbb5bfe87dd4f059eec4b6877))

# [11.0.0-alpha.2](https://github.com/camunda/orchestration-cluster-api-go/compare/v11.0.0-alpha.1...v11.0.0-alpha.2) (2026-11-02)


### Bug Fixes

* **deps:** update module golang.org/x/text to v0.41.0 [security] ([#98](https://github.com/camunda/orchestration-cluster-api-go/issues/98)) ([83e0ca3](https://github.com/camunda/orchestration-cluster-api-go/commit/83e0ca31ab46cceacfd34974ad93943fd5903aa3))
