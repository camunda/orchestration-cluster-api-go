# Camunda Go Client

Auto-generated Go client for the [Camunda 8 Orchestration Cluster REST API](https://docs.camunda.io/docs/apis-tools/camunda-api-rest/camunda-api-rest-overview/) using [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen/).

## Features

- Full API coverage for the Camunda 8 v2 REST API
- Type-safe request/response models
- Client with `net/http` — works with any HTTP middleware
- Generated from the [official OpenAPI specification](https://github.com/camunda/camunda/tree/main/zeebe/gateway-protocol/src/main/proto/v2)

## Installation

```bash
go get github.com/amanyadav/camunda-go-client
```

## Quick Start

```go
package main

import (
	"context"
	"fmt"
	"log"

	camunda "github.com/amanyadav/camunda-go-client/pkg/camunda"
)

func main() {
	// Create a client pointing to your Camunda 8 cluster
	client, err := camunda.NewClientWithResponses("http://localhost:8080/v2")
	if err != nil {
		log.Fatal(err)
	}

	// Get cluster topology
	resp, err := client.GetTopologyWithResponse(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	if resp.JSON200 != nil {
		fmt.Printf("Cluster version: %s\n", *resp.JSON200.GatewayVersion)
	}
}
```

## Regenerating the Client

### Prerequisites

- Go 1.24+
- Node.js (for `npx @redocly/cli` to bundle the spec)
- [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen/) v2

### Steps

```bash
# Install oapi-codegen
go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest

# Download spec, bundle, and generate
make generate

# Or step by step:
make spec       # Download and bundle the OpenAPI spec
make generate   # Generate Go client code
make build      # Verify it compiles
```

## Project Structure

```
.
├── Makefile                  # Build automation
├── oapi-codegen.yaml         # oapi-codegen configuration
├── pkg/
│   └── camunda/
│       ├── client.gen.go     # Generated client (types + HTTP client)
│       └── generate.go       # go:generate directive
├── scripts/
│   └── fix-generated.py      # Post-generation fixes
├── spec/
│   ├── rest-api.yaml         # Main OpenAPI spec (multi-file)
│   ├── *.yaml                # Individual spec partials
│   └── bundled-api.yaml      # Bundled single-file spec
└── examples/
    └── basic/
        └── main.go           # Example usage
```

## Authentication

The Camunda 8 REST API supports multiple authentication methods. For Bearer token auth:

```go
client, err := camunda.NewClientWithResponses(
    "http://localhost:8080/v2",
    camunda.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
        req.Header.Set("Authorization", "Bearer "+token)
        return nil
    }),
)
```

## License

This client generator is provided as-is. The Camunda OpenAPI specification is subject to the [Camunda License Version 1.0](https://github.com/camunda/camunda/blob/main/licenses/CAMUNDA-LICENSE-1.0.txt).
