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
- **Job workers** — a REST activate-jobs worker (`NewJobWorker`) and a gRPC
  `StreamActivatedJobs` streaming worker (`NewStreamJobWorker`). Both share one
  `JobHandler` contract: returning variables completes the job, returning a
  `*BpmnError` throws a BPMN error, and returning any other error fails the job
  (decrementing its retries). The streaming worker also runs a low-frequency REST
  sidecar poll (a safety net for jobs re-queued after a timeout or a brief
  reconnect); poll-activated jobs are acknowledged over REST, streamed jobs over
  gRPC. Set `WithStreamPollInterval` to tune or disable it.

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

## Eventual consistency

Reads are served from the cluster's secondary storage and are eventually
consistent. `Poll` retries a read (by default while it returns 404) until the
entity is visible or a timeout elapses:

<!-- snippet-source: examples/readme.go | regions: EventualConsistency -->
```go
// Reads are eventually consistent: a just-created entity may briefly 404.
// Poll retries 404s until the entity is visible or the timeout elapses.
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

All README examples are compiled from [`examples/readme.go`](./examples/readme.go)
by `make sync-readme`, so they cannot drift from the real API.

## Versioning

The SDK major version tracks the Camunda server minor version (server 8.9 → SDK
9.x). Per Go conventions, majors ≥ 2 use a `/vN` module-path suffix
(`.../orchestration-cluster-api-go/v9`). During Technical Preview the module
stays at `v0`.

## License

Apache-2.0. See [LICENSE](./LICENSE).
