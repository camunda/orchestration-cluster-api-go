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
