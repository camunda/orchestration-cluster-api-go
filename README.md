# Camunda Go Client

Auto-generated Go client for the [Camunda 8 Orchestration Cluster REST API](https://docs.camunda.io/docs/apis-tools/camunda-api-rest/camunda-api-rest-overview/) using [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen/), with a built-in **API changelog tool** that detects breaking changes between versions using Go type introspection.

## Features

- Full API coverage for the Camunda 8 v2 REST API
- Type-safe request/response models generated from the [official OpenAPI specification](https://github.com/camunda/camunda)
- Client with `net/http` — works with any HTTP middleware
- **Multi-version type packages** — versioned clients for 8.5, 8.6, 8.7, 8.8, 8.9
- **Structural API changelog** — detects breaking changes, new types, enum diffs, and field-level changes between versions using `go/ast` introspection

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

## API Changelog

The changelog tool compares generated Go type packages across versions and produces a categorized report of every structural change. It uses `go/ast` to introspect struct fields, enum const groups, type aliases, and JSON tags — detecting changes that are invisible to the Go compiler's binary pass/fail type conversion.

### What it detects

| Category | Example |
|---|---|
| **Struct field added/removed** | `AuthorizationRequest.OwnerId` removed |
| **Field type changed** | `IncidentFilter.State`: `*IncidentFilterState` → `*IncidentStateFilterProperty` |
| **Field became optional/required** | `UserTaskResult.State`: `*UserTaskStateEnum` → `UserTaskStateEnum` |
| **Enum member added/removed** | `IncidentSearchQuerySortRequestField` lost `"errorMessage"` |
| **Enum type added/removed** | `BatchOperationResponseState` removed |
| **Type alias target changed** | `ScopeKey`: `LongKey` → `string` |
| **JSON tag changed** | Wire-level field rename detection |

### Severity classification

Changes are classified with role-aware severity (request types vs response types):

| Severity | Meaning |
|---|---|
| **breaking** | Removed types/fields, incompatible type changes, new required request fields |
| **warning** | Response field became nullable, optional request field removed |
| **additive** | New types, fields, enum members, aliases |
| **info** | Fields became optional/required (informational) |

### Usage

```bash
# Compare two versions
make changelog OLD_VERSION=8.8 NEW_VERSION=8.9

# Compare all consecutive version pairs
make changelog-all

# With options
go run ./cmd/changelog --old-version 8.8 --new-version 8.9 --format json --output report.json

# Compare by file path
go run ./cmd/changelog --old pkg/camunda/8.8/client.gen.go --new pkg/camunda/8.9/client.gen.go
```

### Sample output

```
# API Changelog: 8.8 → 8.9

> **63** breaking, **6** warning, **329** additive, **235** info — **633** total changes

## 🔴 Breaking Changes

### `AuthorizationRequest` `[request]`

- ❌ `AuthorizationRequest.OwnerId` removed (was `string`)
- ❌ `AuthorizationRequest.OwnerType` removed (was `OwnerTypeEnum`)
- ❌ `AuthorizationRequest.PermissionTypes` removed (was `[]PermissionTypeEnum`)
...
```

## Regenerating the Client

### Prerequisites

- Go 1.24+
- A local clone of the [camunda/camunda](https://github.com/camunda/camunda) monorepo
- Node.js (for `npx @redocly/cli` to bundle the spec)
- [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen/) v2

### Steps

```bash
# Install oapi-codegen
go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest

# Generate client for a specific version
make generate CAMUNDA_VERSION=8.9

# Generate all versions (8.5–8.9 + main)
make generate-all

# Build everything
make build-all
```

### Makefile targets

| Target | Description |
|---|---|
| `make spec CAMUNDA_VERSION=8.9` | Extract OpenAPI spec from the Camunda monorepo |
| `make generate CAMUNDA_VERSION=8.9` | Generate Go client for a version |
| `make spec-all` | Extract specs for all versions |
| `make generate-all` | Generate clients for all versions |
| `make build-all` | Tidy + build everything |
| `make compare` | Side-by-side comparison of generated clients |
| `make changelog OLD_VERSION=8.8 NEW_VERSION=8.9` | API changelog between two versions |
| `make changelog-all` | Changelog for all consecutive version pairs |
| `make clean` | Remove generated files |

## Project Structure

```
.
├── Makefile                        # Build automation
├── oapi-codegen.yaml               # oapi-codegen configuration
├── cmd/
│   └── changelog/
│       └── main.go                 # CLI for API changelog tool
├── internal/
│   └── changelog/
│       ├── parse.go                # go/ast type introspection
│       ├── diff.go                 # Structural comparison engine
│       ├── classify.go             # Request/response role classification
│       └── report.go              # Markdown and JSON report generation
├── pkg/
│   └── camunda/
│       ├── client.gen.go           # Generated client (latest/main)
│       ├── generate.go             # go:generate directive
│       ├── 8.5/client.gen.go       # Generated types for 8.5
│       ├── 8.6/client.gen.go       # Generated types for 8.6
│       ├── 8.7/client.gen.go       # Generated types for 8.7
│       ├── 8.8/client.gen.go       # Generated types for 8.8
│       └── 8.9/client.gen.go       # Generated types for 8.9
├── scripts/
│   ├── extract-spec.sh             # Spec extraction from Camunda monorepo
│   └── fix-generated.py            # Post-generation fixes
├── spec/
│   ├── 8.5/ ... 8.9/              # Bundled OpenAPI specs per version
│   └── *.yaml                      # Individual spec partials
└── examples/
    └── basic/
        └── main.go                 # Example usage
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

## How the Changelog Tool Works

Unlike the [tsc-api-changelog](https://github.com/camunda/orchestration-cluster-api-js) which uses TypeScript's structural type system as a diff engine (generating `const _: Current.Foo = {} as any as Stable.Foo` assignments and parsing compiler errors), this tool uses direct `go/ast` introspection because Go's nominal type system lacks the structural assignability checks needed for the tsc approach.

The tool:

1. **Parses** both generated `client.gen.go` files using `go/ast` and `go/parser`
2. **Extracts** all exported structs (with field names, types, JSON tags, pointer-ness), enum types (typed strings + const groups), and type aliases
3. **Classifies** each type as request, response, or unknown based on `oapi-codegen` naming conventions (`*Filter` → request, `*Result` → response, etc.)
4. **Diffs** field-by-field, with role-aware severity: removing a required request field is breaking, but removing an optional response field is a warning
5. **Reports** in Markdown or JSON, grouped by severity and type name

This catches changes that Go's type conversion (`v89.Foo(v88Foo)`) cannot detect: enum member additions/removals, JSON tag renames, and per-field detail on what exactly changed.

## License

This client generator is provided as-is. The Camunda OpenAPI specification is subject to the [Camunda License Version 1.0](https://github.com/camunda/camunda/blob/main/licenses/CAMUNDA-LICENSE-1.0.txt).
