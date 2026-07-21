# gRPC streaming parcel worker

This example deploys `parcel-delivery.bpmn`, starts three parcel-delivery
instances, and handles their service jobs with `NewStreamJobWorker`:

1. a normal dispatch completes with a tracking ID;
2. an invalid address throws the modeled `ADDRESS_REJECTED` BPMN error and
   follows the manual-review path;
3. a temporary carrier outage fails once, consumes a technical retry, and then
   completes.

The worker activates and acknowledges streamed jobs over gRPC. A low-frequency REST
sidecar poll also runs as a safety net for jobs re-queued during a stream
reconnect. The example uses a short 10-second sidecar interval so that recovery
is observable during one run; production applications should choose a
lower-frequency interval from their recovery objective and REST traffic budget.

## Run it

Start a Camunda 8.10 c8run cluster, then run from the repository root:

```sh
go run ./examples/advanced/grpc-stream-worker
```

The local defaults are:

```text
CAMUNDA_REST_ADDRESS=http://localhost:8080
CAMUNDA_GRPC_ADDRESS=localhost:26500
CAMUNDA_BASIC_AUTH_USERNAME=demo
CAMUNDA_BASIC_AUTH_PASSWORD=demo
```

For an OAuth-secured cluster, configure `CAMUNDA_REST_ADDRESS`,
`CAMUNDA_GRPC_ADDRESS`, `CAMUNDA_CLIENT_ID`, `CAMUNDA_CLIENT_SECRET`,
`CAMUNDA_OAUTH_URL`, and, when required, `CAMUNDA_TOKEN_AUDIENCE` and
`CAMUNDA_TOKEN_SCOPE`. The example passes those values into the SDK's OAuth
configuration, which acquires and refreshes tokens for REST and gRPC calls.

## Why use the high-level worker

`NewStreamJobWorker` is the supported gRPC abstraction for SDK applications. It
owns connection setup, OAuth token refresh, stream reconnection, bounded
concurrency, job decoding, and acknowledgement. The handler communicates the
outcome through its return values:

- variables and `nil` complete the job;
- an ordinary error fails the job and decrements retries;
- `*camunda.BpmnError` throws a modeled business error.

The process listens for `SIGINT` and `SIGTERM`. Cancellation stops activation,
waits for in-flight handlers, and gives their acknowledgements a bounded
shutdown window.

Production handlers must make downstream side effects idempotent because job
execution is at least once. Set concurrency from downstream capacity, set the
job timeout above worst-case handler and acknowledgement latency, and request
only variables the handler owns. This example uses 15 seconds because its
simulated carrier call returns immediately; real integrations usually need a
larger measured lock budget.

Use the [low-level generated client example](../grpc-low-level) only when an RPC
is not exposed by the SDK's handwritten API and accepting direct stub
responsibilities is justified.
