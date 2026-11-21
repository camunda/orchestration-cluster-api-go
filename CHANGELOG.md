# [10.3.0](https://github.com/camunda/orchestration-cluster-api-go/compare/v10.2.0...v10.3.0) (2026-11-21)


### Features

* recycle job streams at a jittered max lifetime and on inactivity ([#215](https://github.com/camunda/orchestration-cluster-api-go/issues/215)) ([#219](https://github.com/camunda/orchestration-cluster-api-go/issues/219)) ([a5d252b](https://github.com/camunda/orchestration-cluster-api-go/commit/a5d252b3cda33d77cfcc92f6dab39d413804f4b7)), closes [#214](https://github.com/camunda/orchestration-cluster-api-go/issues/214)

# [10.2.0](https://github.com/camunda/orchestration-cluster-api-go/compare/v10.1.0...v10.2.0) (2026-11-20)


### Features

* back off the stream worker sidecar poll on empty results (backport [#213](https://github.com/camunda/orchestration-cluster-api-go/issues/213) to stable/10) ([#216](https://github.com/camunda/orchestration-cluster-api-go/issues/216)) ([24da3d4](https://github.com/camunda/orchestration-cluster-api-go/commit/24da3d4f0d4534d6b2ce0492e14df43053a0416d))

# [10.1.0](https://github.com/camunda/orchestration-cluster-api-go/compare/v10.0.8...v10.1.0) (2026-11-19)


### Features

* send attributable User-Agent on REST, OAuth and gRPC (backport [#208](https://github.com/camunda/orchestration-cluster-api-go/issues/208) to stable/10) ([#209](https://github.com/camunda/orchestration-cluster-api-go/issues/209)) ([f1a821c](https://github.com/camunda/orchestration-cluster-api-go/commit/f1a821cc320686e42bd80a71dd0ace3eeb4836b7))

## [10.0.8](https://github.com/camunda/orchestration-cluster-api-go/compare/v10.0.7...v10.0.8) (2026-11-19)


### Bug Fixes

* alias *ExactMatch filter branches to their key/enum base (stable/10) ([#189](https://github.com/camunda/orchestration-cluster-api-go/issues/189)) ([74969b2](https://github.com/camunda/orchestration-cluster-api-go/commit/74969b28622b22590844720eda1c05f5fffb7c32))

## [10.0.7](https://github.com/camunda/orchestration-cluster-api-go/compare/v10.0.6...v10.0.7) (2026-11-17)


### Bug Fixes

* size the HTTP idle pool from job workers' in-flight capacity (backport [#197](https://github.com/camunda/orchestration-cluster-api-go/issues/197) to stable/10) ([#198](https://github.com/camunda/orchestration-cluster-api-go/issues/198)) ([165e7d0](https://github.com/camunda/orchestration-cluster-api-go/commit/165e7d0d10d7cef32a73d69a55a9b381ed9b7b25)), closes [#163](https://github.com/camunda/orchestration-cluster-api-go/issues/163) [#164](https://github.com/camunda/orchestration-cluster-api-go/issues/164)

## [10.0.6](https://github.com/camunda/orchestration-cluster-api-go/compare/v10.0.5...v10.0.6) (2026-11-15)


### Bug Fixes

* **auth:** keep token-response content out of decoding errors ([#178](https://github.com/camunda/orchestration-cluster-api-go/issues/178)) ([#185](https://github.com/camunda/orchestration-cluster-api-go/issues/185)) ([dbab815](https://github.com/camunda/orchestration-cluster-api-go/commit/dbab8150d1b158f8407d75dd133d1a0f5b4acf67)), closes [#143](https://github.com/camunda/orchestration-cluster-api-go/issues/143) [#174](https://github.com/camunda/orchestration-cluster-api-go/issues/174)
* **config:** render HTTPClient by type in Config formatting ([#177](https://github.com/camunda/orchestration-cluster-api-go/issues/177)) ([#184](https://github.com/camunda/orchestration-cluster-api-go/issues/184)) ([446261c](https://github.com/camunda/orchestration-cluster-api-go/commit/446261cbe1a671606de913791a026b84dfc27903)), closes [#171](https://github.com/camunda/orchestration-cluster-api-go/issues/171)

## [10.0.5](https://github.com/camunda/orchestration-cluster-api-go/compare/v10.0.4...v10.0.5) (2026-11-15)


### Bug Fixes

* **auth:** stop echoing the token-endpoint response body in errors ([#143](https://github.com/camunda/orchestration-cluster-api-go/issues/143)) ([#174](https://github.com/camunda/orchestration-cluster-api-go/issues/174)) ([455dc2a](https://github.com/camunda/orchestration-cluster-api-go/commit/455dc2a5916bad8907a9570f89a9197fe63f82ab)), closes [camunda/sdk-infra#88](https://github.com/camunda/sdk-infra/issues/88)
* **auth:** write the oauth token cache atomically with owner-only permissions ([#145](https://github.com/camunda/orchestration-cluster-api-go/issues/145)) ([#176](https://github.com/camunda/orchestration-cluster-api-go/issues/176)) ([6121865](https://github.com/camunda/orchestration-cluster-api-go/commit/61218652576d02efa93335b080197f1404800f3b)), closes [camunda/sdk-infra#88](https://github.com/camunda/sdk-infra/issues/88)
* **config:** redact credentials in Config and TLSConfig formatting (backport ([#171](https://github.com/camunda/orchestration-cluster-api-go/issues/171)) ([f882dbd](https://github.com/camunda/orchestration-cluster-api-go/commit/f882dbdcdc790c1393dfb4ddfed43d076c29919f)), closes [#144](https://github.com/camunda/orchestration-cluster-api-go/issues/144) [camunda/sdk-infra#88](https://github.com/camunda/sdk-infra/issues/88) [#v](https://github.com/camunda/orchestration-cluster-api-go/issues/v)
* **diag:** drive environment snapshot redaction from ConfigSchema secrets (backport ([#173](https://github.com/camunda/orchestration-cluster-api-go/issues/173)) ([2693884](https://github.com/camunda/orchestration-cluster-api-go/commit/26938841a0cb733ab7eaed077abd16f5aa1337bc)), closes [#151](https://github.com/camunda/orchestration-cluster-api-go/issues/151) [#150](https://github.com/camunda/orchestration-cluster-api-go/issues/150) [#144](https://github.com/camunda/orchestration-cluster-api-go/issues/144)
* **job:** never return a nil map from RawVariables or CustomHeaders (backport ([#172](https://github.com/camunda/orchestration-cluster-api-go/issues/172)) ([e766a14](https://github.com/camunda/orchestration-cluster-api-go/commit/e766a14c805881b9207b119e964ac96669c5460a)), closes [#152](https://github.com/camunda/orchestration-cluster-api-go/issues/152) [#147](https://github.com/camunda/orchestration-cluster-api-go/issues/147)
* **worker:** recover a panicking job handler and fail the job ([#149](https://github.com/camunda/orchestration-cluster-api-go/issues/149)) ([#175](https://github.com/camunda/orchestration-cluster-api-go/issues/175)) ([003fa97](https://github.com/camunda/orchestration-cluster-api-go/commit/003fa9788d0b24667ca59983c202271c4d288b29))

## [10.0.4](https://github.com/camunda/orchestration-cluster-api-go/compare/v10.0.3...v10.0.4) (2026-11-14)


### Bug Fixes

* size the HTTP connection pool for job workers and allow user-supplied transports (backport to stable/10) ([#164](https://github.com/camunda/orchestration-cluster-api-go/issues/164)) ([6ced2ba](https://github.com/camunda/orchestration-cluster-api-go/commit/6ced2ba7a94074be4bbe48c7b7e523b7d190916f)), closes [#162](https://github.com/camunda/orchestration-cluster-api-go/issues/162)

## [10.0.3](https://github.com/camunda/orchestration-cluster-api-go/compare/v10.0.2...v10.0.3) (2026-11-12)


### Bug Fixes

* ignore unknown fields when decoding response models (backport to stable/10) ([#128](https://github.com/camunda/orchestration-cluster-api-go/issues/128)) ([e7b47bb](https://github.com/camunda/orchestration-cluster-api-go/commit/e7b47bb92117f61c041972e50d59d8331d385549)), closes [#126](https://github.com/camunda/orchestration-cluster-api-go/issues/126) [#124](https://github.com/camunda/orchestration-cluster-api-go/issues/124)

## [10.0.2](https://github.com/camunda/orchestration-cluster-api-go/compare/v10.0.1...v10.0.2) (2026-11-02)


### Bug Fixes

* **deps:** update module golang.org/x/net to v0.62.0 [security] ([#100](https://github.com/camunda/orchestration-cluster-api-go/issues/100)) ([fd527be](https://github.com/camunda/orchestration-cluster-api-go/commit/fd527bed145cd7e45bcadedb0bf612b4b1ba0a68))

## [10.0.1](https://github.com/camunda/orchestration-cluster-api-go/compare/v10.0.0...v10.0.1) (2026-11-01)


### Bug Fixes

* backport conformance fixes to stable/10 ([#84](https://github.com/camunda/orchestration-cluster-api-go/issues/84)) ([3b58d88](https://github.com/camunda/orchestration-cluster-api-go/commit/3b58d887c0307db0db353d9ca485789c0cd9d5f2)), closes [#80](https://github.com/camunda/orchestration-cluster-api-go/issues/80) [#83](https://github.com/camunda/orchestration-cluster-api-go/issues/83) [#77](https://github.com/camunda/orchestration-cluster-api-go/issues/77) [#81](https://github.com/camunda/orchestration-cluster-api-go/issues/81) [#78](https://github.com/camunda/orchestration-cluster-api-go/issues/78) [#82](https://github.com/camunda/orchestration-cluster-api-go/issues/82) [#79](https://github.com/camunda/orchestration-cluster-api-go/issues/79)
* inline path-local refs so error responses decode (stable/10) ([#87](https://github.com/camunda/orchestration-cluster-api-go/issues/87)) ([2190e74](https://github.com/camunda/orchestration-cluster-api-go/commit/2190e740deae855f17b41a4181bc752a349754bb))
