// Secret operations: resolve connector secret references to their values.
package examples

import (
	"context"
	"fmt"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	openapi "github.com/camunda/orchestration-cluster-api-go/client"
)

func resolveSecretsExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region ResolveSecrets
	req := openapi.NewSecretResolveRequest([]string{"MY_API_KEY", "MY_TOKEN"})

	result, err := client.ResolveSecrets(ctx, *req)
	if err != nil {
		return err
	}
	for _, secret := range result.GetResolved() {
		fmt.Printf("%v = %v\n", secret.GetReference(), secret.GetValue())
	}
	// endregion ResolveSecrets
	return nil
}
