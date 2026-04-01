// Package main demonstrates basic usage of the Camunda Go client.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	camunda "github.com/amanyadav/camunda-go-client/pkg/camunda"
)

func main() {
	// Create a client pointing to a local Camunda 8 cluster.
	// For C8 Run or Docker Compose defaults, no auth is needed.
	client, err := camunda.NewClientWithResponses("http://localhost:8080/v2")
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	ctx := context.Background()

	// ----- Get Cluster Topology -----
	fmt.Println("=== Cluster Topology ===")
	topologyResp, err := client.GetTopologyWithResponse(ctx)
	if err != nil {
		log.Fatalf("Failed to get topology: %v", err)
	}
	if topologyResp.JSON200 != nil {
		t := topologyResp.JSON200
		fmt.Printf("Gateway version: %s\n", t.GatewayVersion)
		fmt.Printf("Cluster size:    %d\n", t.ClusterSize)
		fmt.Printf("Partitions:      %d\n", t.PartitionsCount)
	} else {
		fmt.Printf("Unexpected status: %s\n", topologyResp.Status())
	}

	// ----- Example: Search Process Definitions -----
	fmt.Println("\n=== Process Definitions ===")
	// Create a search request with no filters (list all).
	searchResp, err := client.SearchProcessDefinitionsWithResponse(ctx, camunda.SearchProcessDefinitionsJSONRequestBody{})
	if err != nil {
		log.Fatalf("Failed to search process definitions: %v", err)
	}
	if searchResp.JSON200 != nil {
		fmt.Printf("Found process definitions (total: check items length)\n")
	} else {
		fmt.Printf("Status: %s\n", searchResp.Status())
	}

	// ----- Example with Bearer Auth -----
	fmt.Println("\n=== Example with Bearer Auth ===")
	token := "your-access-token"
	authClient, err := camunda.NewClientWithResponses(
		"https://region.zeebe.camunda.io/cluster-id/v2",
		camunda.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+token)
			return nil
		}),
	)
	if err != nil {
		log.Fatalf("Failed to create auth client: %v", err)
	}
	_ = authClient // Use authClient for SaaS cluster calls
	fmt.Println("Auth client created successfully (not calling SaaS in example)")
}

// ptrVal safely dereferences a pointer, returning the zero value if nil.
func ptrVal[T any](p *T) T {
	if p != nil {
		return *p
	}
	var zero T
	return zero
}
