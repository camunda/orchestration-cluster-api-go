// Recovery operations: change cluster mode and restore from backup.
package examples

import (
	"context"
	"fmt"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	openapi "github.com/camunda/orchestration-cluster-api-go/client"
)

func changeClusterModeExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region ChangeClusterMode
	result, err := client.ChangeClusterMode(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%v\n", result)
	// endregion ChangeClusterMode
	return nil
}

func restoreExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region Restore
	result, err := client.Restore(ctx, *openapi.NewRestoreRequest())
	if err != nil {
		return err
	}
	fmt.Printf("%v\n", result)
	// endregion Restore
	return nil
}
