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

## Installation

```sh
go get github.com/camunda/orchestration-cluster-api-go
```

Requires Go 1.25+. During Technical Preview the module path has no version suffix
(see [versioning](#versioning)).

## Quick start

Construct a client (configuration comes from `CAMUNDA_*` environment variables)
and call an ergonomic facade method:

<!-- snippet-source: examples/readme.go | regions: QuickStart -->
```go
// Configuration is resolved from CAMUNDA_* environment variables (with ZEEBE_*
// fallbacks) and validated fail-fast at construction.
client, err := camunda.New()
if err != nil {
	return err
}

ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

topology, err := client.GetTopology(ctx)
if err != nil {
	return err
}
fmt.Printf("Camunda 8 %s — %d broker(s), %d partition(s)\n",
	topology.GetGatewayVersion(), len(topology.GetBrokers()), topology.GetPartitionsCount())
```

For production-shaped, runnable workflows, see the
[advanced examples](examples/advanced/README.md): bounded load with adaptive
backpressure, resilient job handling, and idempotent message correlation.

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
- **Job workers** — a REST activate-jobs worker (`NewJobWorker`) and a gRPC
  `StreamActivatedJobs` streaming worker (`NewStreamJobWorker`). Both share one
  `JobHandler` contract: returning variables completes the job, returning a
  `*BpmnError` throws a BPMN error, and returning any other error fails the job
  (decrementing its retries). The streaming worker also runs a low-frequency REST
  sidecar poll (a safety net for jobs re-queued after a timeout or a brief
  reconnect); poll-activated jobs are acknowledged over REST, streamed jobs over
  gRPC. Set `WithStreamPollInterval` to tune or disable it.
- **FALCON command stream** — an opt-in upgrade for
  [nanobpmn](https://github.com/jwulf/nano-bpm) gateways (an API/behaviour superset
  of Camunda 8). The gateway is probed once via `GET /v2/topology`; when it
  advertises the command stream, `CreateProcessInstance` is routed over a
  credit-metered WebSocket (a flood of creates queues on the submission-credit
  window instead of being shed with 503s) and `NewJobWorker` receives *pushed*
  jobs over the same stream instead of long-polling. The link fails over across
  cluster nodes and supports both `ws://` and `wss://` (deriving TLS from the
  cluster address). Against stock Camunda — or if the stream cannot be established
  — the SDK stays on its byte-identical REST path. Enabled by default; disable with
  `CAMUNDA_FALCON=false` / `WithFalcon(false)`, or force pure REST (e.g. behind a
  WebSocket-blocking proxy) with `CAMUNDA_FORCE_REST=1` / `WithForceREST(true)`.

## Configuration

Every setting is resolved from the environment and overridable with functional
options. Options take precedence over environment variables:

<!-- snippet-source: examples/readme.go | regions: Configuration -->
```go
// Functional options override the environment. Here: OAuth 2.0
// client-credentials against a SaaS cluster.
client, err := camunda.New(
	camunda.WithRestAddress("https://my-cluster.region.camunda.io"),
	camunda.WithOAuth(
		"my-client-id",
		"my-client-secret",
		"https://login.cloud.camunda.io/oauth/token",
	),
	camunda.WithOAuthAudience("zeebe.camunda.io"),
)
```

## Authentication

Three strategies are supported: OAuth 2.0 client credentials, HTTP Basic, and
none. The strategy is inferred from the credentials you supply, or set explicitly
with `CAMUNDA_AUTH_STRATEGY=OAUTH|BASIC|NONE`.

<!-- snippet-source: examples/readme.go | regions: Authentication -->
```go
// OAuth 2.0 client credentials. Tokens are cached in memory and on disk, and
// refreshed before expiry; concurrent refreshes are collapsed into one.
oauthClient, err := camunda.New(
	camunda.WithOAuth(
		"my-client-id",
		"my-client-secret",
		"https://login.cloud.camunda.io/oauth/token",
	),
	camunda.WithOAuthAudience("zeebe.camunda.io"),
	camunda.WithOAuthScope("camunda:read"),
	camunda.WithOAuthCacheDir("/var/cache/camunda"),
)

// HTTP Basic — typical for a Self-Managed cluster behind basic auth.
basicClient, err := camunda.New(
	camunda.WithBasicAuth("demo", "demo"),
)

// No authentication — a local development cluster with auth disabled.
openClient, err := camunda.New(camunda.WithNoAuth())
```

The OAuth token cache is two-tier: an in-memory cache backed by an on-disk cache
(`CAMUNDA_OAUTH_CACHE_DIR`), so short-lived processes and CLI invocations reuse a
valid token instead of re-authenticating on every start. Concurrent refreshes are
collapsed into a single in-flight request.

## Configuration reference

`CAMUNDA_*` variables are canonical; the `ZEEBE_*` names are accepted as
fallbacks for compatibility with older tooling. Functional options take
precedence over both.

### Connection

| Variable | Default | Description |
| --- | --- | --- |
| `CAMUNDA_REST_ADDRESS` / `ZEEBE_REST_ADDRESS` | `http://localhost:8080` | Orchestration Cluster REST base address. |
| `CAMUNDA_GRPC_ADDRESS` / `ZEEBE_GRPC_ADDRESS` | `localhost:26500` | Zeebe gRPC gateway address (`host:port`) for the streaming job worker. |
| `CAMUNDA_DEFAULT_TENANT_ID` / `CAMUNDA_TENANT_ID` | — | Default tenant id applied to operations that accept one. |

### Authentication

| Variable | Default | Description |
| --- | --- | --- |
| `CAMUNDA_AUTH_STRATEGY` | inferred | `OAUTH`, `BASIC`, or `NONE`. Inferred from the supplied credentials when unset. |
| `CAMUNDA_CLIENT_ID` / `ZEEBE_CLIENT_ID` | — | OAuth 2.0 client id (client-credentials grant). |
| `CAMUNDA_CLIENT_SECRET` / `ZEEBE_CLIENT_SECRET` | — | OAuth 2.0 client secret. |
| `CAMUNDA_OAUTH_URL` / `ZEEBE_AUTHORIZATION_SERVER_URL` | — | OAuth 2.0 token endpoint URL. |
| `CAMUNDA_TOKEN_AUDIENCE` | — | OAuth token audience. |
| `CAMUNDA_TOKEN_SCOPE` | — | OAuth token scope. |
| `CAMUNDA_OAUTH_CACHE_DIR` | — | Directory for the on-disk OAuth token cache. |
| `CAMUNDA_BASIC_AUTH_USERNAME` | — | HTTP Basic auth username. |
| `CAMUNDA_BASIC_AUTH_PASSWORD` | — | HTTP Basic auth password. |

### Reliability

| Variable | Default | Description |
| --- | --- | --- |
| `CAMUNDA_SDK_BACKPRESSURE_PROFILE` | `BALANCED` | `BALANCED` (gates) or `LEGACY` (observe-only). |
| `CAMUNDA_SDK_HTTP_RETRY_MAX_ATTEMPTS` | — | Max transient-error retry attempts. |
| `CAMUNDA_SDK_HTTP_RETRY_BASE_DELAY_MS` | — | Base backoff delay for retries, in milliseconds. |
| `CAMUNDA_SDK_HTTP_RETRY_MAX_DELAY_MS` | — | Max backoff delay for retries, in milliseconds. |
| `CAMUNDA_SDK_EVENTUAL_POLL_DEFAULT_MS` | — | Default eventual-consistency poll timeout, in milliseconds. |
| `CAMUNDA_SDK_LOG_LEVEL` | `info` | `off`, `error`, `warn`, `info`, `debug`, or `trace`. |

### Job workers

| Variable | Default | Description |
| --- | --- | --- |
| `CAMUNDA_WORKER_NAME` | hostname-derived | Default worker name. |
| `CAMUNDA_WORKER_TIMEOUT` | — | Default job activation timeout, in milliseconds. |
| `CAMUNDA_WORKER_MAX_CONCURRENT_JOBS` / `CAMUNDA_WORKER_MAX_JOBS_ACTIVE` | — | Default max concurrently-activated jobs per worker. |
| `CAMUNDA_WORKER_REQUEST_TIMEOUT` | — | Default activate-jobs request timeout, in milliseconds. |
| `CAMUNDA_WORKER_STARTUP_JITTER_MAX_SECONDS` | — | Max random startup delay for workers, in seconds. |

### Transport

| Variable | Default | Description |
| --- | --- | --- |
| `CAMUNDA_FALCON` | `true` | Enable the FALCON command-stream transport upgrade when the gateway advertises it. |
| `CAMUNDA_FORCE_REST` | — | Force the pure-REST path even when the gateway advertises FALCON. |

Invalid values are rejected at construction with a configuration error rather
than being silently coerced, so a typo in a deployment manifest fails the process
at startup instead of at first request.

## TLS and mutual TLS

TLS is derived from the scheme of `CAMUNDA_REST_ADDRESS`. Mutual TLS and custom
certificate authorities — for a Self-Managed cluster behind a private CA, or one
requiring client certificates — are configured by environment variable. Inline
PEM values take precedence over the corresponding `*_PATH` file locations.

| Variable | Description |
| --- | --- |
| `CAMUNDA_MTLS_CERT` | Inline client certificate PEM. |
| `CAMUNDA_MTLS_KEY` | Inline client private key PEM. |
| `CAMUNDA_MTLS_CA` | Inline CA certificate PEM for verifying the server. |
| `CAMUNDA_MTLS_CERT_PATH` | Path to the client certificate PEM. |
| `CAMUNDA_MTLS_KEY_PATH` | Path to the client private key PEM. |
| `CAMUNDA_MTLS_CA_PATH` | Path to the CA certificate PEM. |
| `CAMUNDA_MTLS_KEY_PASSPHRASE` | Recognised but **not supported yet** — setting it fails client construction. Supply an unencrypted client key. |

The same material is applied to both the REST transport and the gRPC streaming
worker, so a single configuration covers every connection the SDK opens.

## Job workers

Register a REST activate-jobs worker with `NewJobWorker`. The handler's return
value decides the job outcome:

<!-- snippet-source: examples/readme.go | regions: JobWorker -->
```go
// One JobHandler contract for both workers: returning variables completes the
// job; returning a *camunda.BpmnError throws a BPMN error; returning any other
// error fails the job (decrementing its retries).
worker := client.NewJobWorker("greet",
	func(ctx context.Context, job *camunda.Job) (map[string]any, error) {
		var in struct {
			Name string `json:"name"`
		}
		if err := job.Variables(&in); err != nil {
			return nil, err
		}
		return map[string]any{"greeting": "Hello, " + in.Name + "!"}, nil
	},
	camunda.WithMaxConcurrentJobs(10),
	camunda.WithPollInterval(500*time.Millisecond),
)

ctx, cancel := context.WithCancel(context.Background())
defer cancel()

// Run blocks until ctx is cancelled, draining in-flight jobs on shutdown.
if err := worker.Run(ctx); err != nil {
	fmt.Println("worker stopped:", err)
}
```

For high-throughput, low-latency work, use the gRPC streaming worker
(`NewStreamJobWorker`) — a capability unique to this SDK among the Camunda
Orchestration Cluster SDKs:

<!-- snippet-source: examples/readme.go | regions: StreamWorker -->
```go
// The gRPC streaming worker activates jobs over a StreamActivatedJobs stream
// and acknowledges them over gRPC. A low-frequency REST sidecar poll backs it
// up (a safety net for jobs re-queued after a timeout or brief reconnect).
worker := client.NewStreamJobWorker("greet",
	func(ctx context.Context, job *camunda.Job) (map[string]any, error) {
		return map[string]any{"greeting": "Hello!"}, nil
	},
	camunda.WithStreamPollInterval(30*time.Second), // -1 disables the sidecar poll
)

ctx, cancel := context.WithCancel(context.Background())
defer cancel()

if err := worker.Run(ctx); err != nil {
	fmt.Println("stream worker stopped:", err)
}
```

Pass `camunda.WithStreamJobLease(true)` to activate jobs with a lease. Each job
then carries a lease token that the worker sends back when it completes, fails,
or throws — so if the job timed out and another worker picked it up, the stale
acknowledgement is rejected instead of racing the newer activation. It covers
both the gRPC stream and the REST sidecar poll. The REST worker takes the same
option as `camunda.WithJobLease(true)`. Leases are off by default (matching the
engine) and need an engine that supports them; older gateways ignore the flag and
keep handing out unleased jobs.

## Deploying and starting processes

The facade exposes request bodies as first-class parameters; the `Raw()` escape
hatch covers anything the facade doesn't (such as multipart resource upload):

<!-- snippet-source: examples/readme.go | regions: DeployAndStart -->
```go
// Deploy a BPMN process. Multipart resource upload goes through the Raw()
// generated client (the escape hatch for anything the facade doesn't cover).
f, err := os.Open("greet.bpmn")
if err != nil {
	return err
}
defer func() { _ = f.Close() }()

if _, _, err := client.Raw().ResourceAPI.CreateDeployment(ctx).
	Resources([]*os.File{f}).
	Execute(); err != nil {
	return err
}

// Start an instance by process id. The request body is a first-class facade
// parameter — no Raw() needed.
byID := openapi.NewProcessInstanceCreationInstructionById("demo-process")
byID.SetVariables(map[string]any{"name": "Camunda"})
instruction := openapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(byID)

instance, err := client.CreateProcessInstance(ctx, instruction)
if err != nil {
	return err
}
fmt.Printf("started process instance %v\n", instance.GetProcessInstanceKey())
```

## Backpressure

A Camunda broker sheds load by rejecting commands (HTTP 429 / 503,
`RESOURCE_EXHAUSTED`). Rather than surfacing those to your code as a wall of
errors, the SDK gates outbound requests through an AIMD concurrency limiter —
the same additive-increase/multiplicative-decrease shape TCP uses. Throughput
rises while the cluster is healthy and backs off the moment it pushes back, so a
burst queues client-side instead of being shed:

<!-- snippet-source: examples/readme.go | regions: Backpressure -->
```go
// BALANCED (the default) gates outbound requests through an AIMD concurrency
// limiter that reacts to broker backpressure (HTTP 429/503). LEGACY observes
// and reports, but never gates — use it to compare against older SDKs.
client, err := camunda.New(
	camunda.WithBackpressureProfile(camunda.ProfileLegacy),
)
```

`BALANCED` is the default and is what you want in production. `LEGACY` keeps the
controller's observability but never gates, which is useful when comparing
behaviour against an older SDK or when an external system already governs
concurrency.

## Transient retry

Retries are layered *below* backpressure in the `http.RoundTripper` chain, so a
retried request is still subject to the concurrency limiter and cannot amplify a
load spike:

<!-- snippet-source: examples/readme.go | regions: TransientRetry -->
```go
// Transient failures (429, 502, 503, 504 and network errors) are retried with
// exponential backoff and full jitter. Non-transient 4xx are never retried.
client, err := camunda.New(
	camunda.WithRetry(camunda.RetryConfig{
		MaxAttempts: 5,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    5 * time.Second,
	}),
)
```

Only transient failures are retried. A `400`, `403`, or `404` is returned to you
immediately — retrying a request the server has definitively rejected wastes time
and hides the real error.

## Eventual consistency

Reads are served from the cluster's secondary storage and are eventually
consistent. `Poll` retries a read (by default while it returns 404) until the
entity is visible or a timeout elapses:

<!-- snippet-source: examples/readme.go | regions: EventualConsistency -->
```go
// Reads are eventually consistent: a just-created entity may briefly 404.
// Poll retries 404s until the entity is visible or the timeout elapses.
key := openapi.MustProcessInstanceKey("2251799813685249")

instance, err := camunda.Poll(ctx, func(ctx context.Context) (*openapi.ProcessInstanceResult, error) {
	return client.GetProcessInstance(ctx, key)
}, camunda.WithPollTimeout(10*time.Second))
if err != nil {
	return err
}
fmt.Printf("instance state: %v\n", instance.GetState())
```

## Semantic keys

Identifier types (`JobKey`, `ProcessInstanceKey`, …) are validated named string
types rather than bare strings:

<!-- snippet-source: examples/readme.go | regions: SemanticKeys -->
```go
// Semantic key types validate their format at construction.
key, err := openapi.NewJobKey("2251799813685424") // validates pattern & length
if err != nil {
	return err
}
fmt.Println(key.String())

// Side-load a key you already trust, without validation:
loose := openapi.MustJobKey("2251799813685424")
_ = loose
```

## Error handling

A server-side 4xx/5xx surfaces as a typed `*APIError` carrying the HTTP status
and response body; anything else is a transport-level error:

<!-- snippet-source: examples/readme.go | regions: ErrorHandling -->
```go
_, err := client.GetTopology(ctx)
var apiErr *camunda.APIError
if errors.As(err, &apiErr) {
	// The server returned a 4xx/5xx — inspect the status and response body.
	fmt.Printf("API error: HTTP %d — %s\n", apiErr.Status, apiErr.Body)
} else if err != nil {
	// Transport-level failure (DNS, TLS, connection refused, ...).
	fmt.Println("request failed:", err)
}
```

Two helpers cover the common classifications without unwrapping by hand:

<!-- snippet-source: examples/readme.go | regions: ErrorClassification -->
```go
key := openapi.MustProcessInstanceKey("2251799813685249")

_, err := client.GetProcessInstance(ctx, key)

// IsNotFound is the idiomatic 404 check — the common case when reading an
// entity that has not yet propagated to secondary storage.
if camunda.IsNotFound(err) {
	fmt.Println("not visible yet")
	return nil
}

// StatusCode reports the HTTP status for any server-side error, and
// ok == false for transport-level failures.
if status, ok := camunda.StatusCode(err); ok {
	fmt.Printf("server rejected the request: HTTP %d\n", status)
}
```

Errors are values throughout: use `errors.Is` for sentinels and `errors.As` for
typed errors. A function that cannot honour its contract returns an error rather
than a zero value or a best-guess default.

## Logging

The SDK logs through a level-gated internal logger, off by default at anything
above `info`:

<!-- snippet-source: examples/readme.go | regions: Logging -->
```go
// Levels: LogOff, LogError, LogWarn, LogInfo (default), LogDebug, LogTrace.
// LogDebug reports auth-token refreshes, retries, and backpressure decisions;
// LogTrace adds per-request detail. Credentials are never logged.
client, err := camunda.New(camunda.WithLogLevel(camunda.LogDebug))
```

Set `CAMUNDA_SDK_LOG_LEVEL=debug` to get the same effect without a code change —
useful when diagnosing an authentication or backpressure problem in a deployed
service. Secrets (client secrets, passwords, bearer tokens, TLS key material) are
never written to the log at any level.

## The full API surface

`CamundaClient` exposes one ergonomic method per operation in the OpenAPI
specification, generated from the same spec as the low-level client so the two
can never diverge. Each facade method flattens the generated builder into
first-class parameters and returns the deserialised result.

When you need something the facade deliberately does not model — multipart
uploads, unusual query-parameter combinations, or the raw `*http.Response` —
`Raw()` hands you the generated client directly:

<!-- snippet-source: examples/readme.go | regions: RawEscapeHatch -->
```go
// Raw() exposes the generated client: every operation, with the full builder
// surface. Use it for anything the facade does not cover, and for access to
// the raw *http.Response.
result, resp, err := client.Raw().ProcessDefinitionAPI.
	SearchProcessDefinitions(ctx).
	Execute()
if err != nil {
	return err
}
fmt.Printf("HTTP %d — %d process definition(s)\n", resp.StatusCode, len(result.GetItems()))
```

Requests made through `Raw()` still traverse the full runtime — backpressure,
retry, and authentication all apply, because those are `http.RoundTripper` layers
on the transport rather than facade-level wrappers.

Full API documentation is published on
[pkg.go.dev](https://pkg.go.dev/github.com/camunda/orchestration-cluster-api-go).

<!-- docs:cut:start -->

## Regenerating the client

`client/`, `pb/`, and `facade_generated.go` are generated. Never hand-edit them —
change the generator instead, under `scripts/hooks/` (post-processing) or
`cmd/facadegen/` (the facade generator).

```sh
make bundle        # re-bundle the upstream OpenAPI spec, then regenerate
make fetch-proto   # fetch gateway.proto
make generate      # regenerate from the already-fetched inputs
make check         # full local gate: fmt, vet, build, test, examples, snippet sync
```

When a change modifies the generator *and* its output, commit them separately —
the generator change first, the regenerated output second. See
[AGENTS.md](./AGENTS.md) for the full contributor workflow.

All README examples are compiled from [`examples/readme.go`](./examples/readme.go)
by `make sync-readme`, so they cannot drift from the real API. Edit the region in
the Go source, run `make sync-readme`, and commit both files — never edit a fenced
code block in this README by hand.

## Documentation site

The guide sections of this README and a `go/doc`-derived API reference are
rendered into Docusaurus markdown for https://docs.camunda.io:

```sh
make docs-json    # cmd/docgen -> docs-json/*.json
make docs-md      # docs-json + README -> docs-md/
```

Content between `<!-- docs:cut:start -->` and `<!-- docs:cut:end -->` markers is
maintainer-only and is excluded from the published pages. Both output
directories are generated and gitignored; a scheduled workflow in `camunda-docs`
runs `make docs-md` and opens the update PR.

<!-- docs:cut:end -->

## Versioning

The SDK major version tracks the Camunda server minor version (server 8.9 → SDK
9.x). Per Go conventions, majors ≥ 2 use a `/vN` module-path suffix
(`.../orchestration-cluster-api-go/v9`). During Technical Preview the module
stays at `v0`.

## License

Apache-2.0. See [LICENSE](./LICENSE).
