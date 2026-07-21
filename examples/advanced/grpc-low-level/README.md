# Low-level generated gRPC client

This focused example calls the generated `pb.GatewayClient.Topology` RPC. It
shows the work that direct stub users own: transport security, authentication
metadata, request construction, deadlines, gRPC status handling, and connection
cleanup.

Topology is intentionally representative rather than exhaustive. Most
Orchestration Cluster operations should use the SDK's REST facade, and streamed
job handling should use
[`NewStreamJobWorker`](../grpc-stream-worker).

## Run it locally

Start a Camunda 8.10 c8run cluster, then run from the repository root:

```sh
CAMUNDA_GRPC_INSECURE=true \
  go run ./examples/advanced/grpc-low-level
```

`CAMUNDA_GRPC_ADDRESS` defaults to `localhost:26500`. Plaintext transport is an
explicit local-development opt-in. The example refuses to send a bearer token
over plaintext.

## Run it against a TLS and OAuth-secured cluster

Obtain an access token using your identity provider's client-credentials flow,
then pass it to the example:

```sh
CAMUNDA_GRPC_ADDRESS='your-cluster.example.com:443' \
CAMUNDA_ACCESS_TOKEN="$ACCESS_TOKEN" \
  go run ./examples/advanced/grpc-low-level
```

The static environment token keeps the example focused on the generated gRPC
surface. A real direct-stub application must obtain, cache, refresh, and protect
OAuth tokens itself. It may also need custom root CAs, mutual TLS, interceptors,
retry policy, observability, and graceful connection lifecycle management.

## Choosing this API

Direct `pb` use is an escape hatch for representative gateway RPCs that have no
handwritten SDK abstraction. The generated types mirror `gateway.proto` and
have a lower-level stability contract than the root `camunda` package. Proto
changes can therefore require application changes after regeneration.

Prefer:

- the root SDK facade for REST operations;
- `NewStreamJobWorker` for gRPC job streaming;
- direct `pb.GatewayClient` only when the required gateway RPC is otherwise
  unavailable and the extra lifecycle and compatibility responsibilities are
  acceptable.

This topology call does not execute a process, so it has no BPMN model. The
[streaming worker example](../grpc-stream-worker) includes a complete,
deployable parcel-delivery model.
