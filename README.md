# Camunda 8 Orchestration Cluster API — Go SDK

> **Technical Preview.** APIs may change before a stable `v1`/`v9` release. See
> [versioning](#versioning).

An idiomatic Go client for the [Camunda 8](https://camunda.io) Orchestration
Cluster REST API, with a gRPC job-streaming worker. It pairs a **generated
low-level REST client** (produced from the upstream OpenAPI specification) with a
hand-written **ergonomic runtime** that handles the concerns real integrations
need: configuration, authentication, adaptive backpressure, transient retry, and
eventual-consistency handling.

This is a sibling of the [Rust](https://github.com/camunda/orchestration-cluster-api-rust),
[TypeScript](https://github.com/camunda/orchestration-cluster-api-js),
[Python](https://github.com/camunda/orchestration-cluster-api-python), and
[C#](https://github.com/camunda/orchestration-cluster-api-csharp) SDKs and follows
the same two-layer architecture.

## Architecture

```
OpenAPI spec ──▶ openapi-generator ──▶ client/  (generated REST client, never hand-edited)
gateway.proto ──▶ buf              ──▶ pb/      (generated gRPC stubs, never hand-edited)
                                          │
                     ergonomic runtime ───┤  config · auth · backpressure · retry ·
                     (hand-written)        │  eventual consistency · job workers
                                          ▼
                                   CamundaClient  (the facade you use)
```

Cross-cutting concerns are implemented as a composable `http.RoundTripper` chain
(`backpressure → retry → auth → base`) injected into the generated client, so the
generated code stays pure and regenerable.

- **Configuration** — resolved from `CAMUNDA_*` environment variables (with
  `ZEEBE_*` fallbacks) and overridable via functional options. Validated
  fail-fast at construction.
- **Authentication** — OAuth 2.0 client-credentials (with in-memory + on-disk
  token cache), HTTP Basic, or None.
- **Adaptive backpressure** — an AIMD concurrency limiter that reacts to broker
  backpressure (HTTP 429 / 503 / `RESOURCE_EXHAUSTED`). `BALANCED` (default) gates;
  `LEGACY` observes only.
- **Transient retry** — exponential backoff with full jitter on 429/502/503/504
  and network errors.
- **Job workers** — a REST activate-jobs worker and a gRPC `StreamActivatedJobs`
  streaming worker (with a REST sidecar-poll safety net).

## Status of the build

This repository is under active construction. The ergonomic runtime
(configuration, authentication, backpressure, retry, transport chain) is
implemented and tested. The generated REST client, gRPC stubs, ergonomic facade,
and job workers are produced/added by the generation pipeline and subsequent
milestones — see [`AGENTS.md`](./AGENTS.md) and `make help`.

## Versioning

The SDK major version tracks the Camunda server minor version (server 8.9 → SDK
9.x). Per Go conventions, majors ≥ 2 use a `/vN` module-path suffix
(`.../orchestration-cluster-api-go/v9`). During Technical Preview the module
stays at `v0`.

## License

Apache-2.0. See [LICENSE](./LICENSE).
