// Process instance operations: create (by id/key), search, read, cancel, delete,
// migrate, modify, incident resolution, statistics, and batch operations.
package examples

import (
	"context"
	"fmt"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	camundaapi "github.com/camunda/orchestration-cluster-api-go/client"
)

func createProcessInstanceByIdExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region CreateProcessInstanceById
	byID := camundaapi.NewProcessInstanceCreationInstructionById(camundaapi.ProcessDefinitionId("order-process"))
	byID.SetVariables(map[string]any{"orderId": "order-42"})

	result, err := client.CreateProcessInstance(ctx,
		camundaapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(byID))
	if err != nil {
		return err
	}
	fmt.Printf("started instance %v\n", result.GetProcessInstanceKey())
	// endregion CreateProcessInstanceById
	return nil
}

func createProcessInstanceByKeyExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region CreateProcessInstanceByKey
	// Use a specific process definition version by its key.
	byKey := camundaapi.NewProcessInstanceCreationInstructionByKey(camundaapi.ProcessDefinitionKey("2251799813685330"))
	byKey.SetVariables(map[string]any{"orderId": "order-42"})

	result, err := client.CreateProcessInstance(ctx,
		camundaapi.ProcessInstanceCreationInstructionByKeyAsProcessInstanceCreationInstruction(byKey))
	if err != nil {
		return err
	}
	fmt.Printf("started instance %v\n", result.GetProcessInstanceKey())
	// endregion CreateProcessInstanceByKey
	return nil
}

func searchProcessInstancesExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region SearchProcessInstances
	result, err := client.SearchProcessInstances(ctx, *camundaapi.NewProcessInstanceSearchQuery())
	if err != nil {
		return err
	}
	for _, pi := range result.GetItems() {
		fmt.Printf("%v: %v\n", pi.GetProcessInstanceKey(), pi.GetState())
	}
	// endregion SearchProcessInstances
	return nil
}

func getProcessInstanceExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region GetProcessInstance
	instance, err := client.GetProcessInstance(ctx, camundaapi.MustProcessInstanceKey("2251799813685340"))
	if err != nil {
		return err
	}
	fmt.Printf("state=%v definition=%q\n", instance.GetState(), instance.GetProcessDefinitionId())
	// endregion GetProcessInstance
	return nil
}

func cancelProcessInstanceExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region CancelProcessInstance
	return client.CancelProcessInstance(ctx,
		camundaapi.MustProcessInstanceKey("2251799813685340"),
		*camundaapi.NewCancelProcessInstanceRequest())
	// endregion CancelProcessInstance
}

func deleteProcessInstanceExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region DeleteProcessInstance
	return client.DeleteProcessInstance(ctx,
		camundaapi.MustProcessInstanceKey("2251799813685340"),
		*camundaapi.NewDeleteProcessInstanceRequest())
	// endregion DeleteProcessInstance
}

func getProcessInstanceCallHierarchyExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region GetProcessInstanceCallHierarchy
	hierarchy, err := client.GetProcessInstanceCallHierarchy(ctx, camundaapi.MustProcessInstanceKey("2251799813685340"))
	if err != nil {
		return err
	}
	for _, entry := range hierarchy {
		fmt.Printf("%v\n", entry)
	}
	// endregion GetProcessInstanceCallHierarchy
	return nil
}

func getProcessInstanceSequenceFlowsExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region GetProcessInstanceSequenceFlows
	result, err := client.GetProcessInstanceSequenceFlows(ctx, camundaapi.MustProcessInstanceKey("2251799813685340"))
	if err != nil {
		return err
	}
	fmt.Printf("%v\n", result)
	// endregion GetProcessInstanceSequenceFlows
	return nil
}

func getProcessInstanceStatisticsExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region GetProcessInstanceStatistics
	result, err := client.GetProcessInstanceStatistics(ctx, camundaapi.MustProcessInstanceKey("2251799813685340"))
	if err != nil {
		return err
	}
	fmt.Printf("%v\n", result)
	// endregion GetProcessInstanceStatistics
	return nil
}

func getProcessInstanceWaitStateStatisticsExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region GetProcessInstanceWaitStateStatistics
	result, err := client.GetProcessInstanceWaitStateStatistics(ctx, camundaapi.MustProcessInstanceKey("2251799813685340"))
	if err != nil {
		return err
	}
	fmt.Printf("%v\n", result)
	// endregion GetProcessInstanceWaitStateStatistics
	return nil
}

func resolveProcessInstanceIncidentsExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region ResolveProcessInstanceIncidents
	result, err := client.ResolveProcessInstanceIncidents(ctx, camundaapi.MustProcessInstanceKey("2251799813685340"))
	if err != nil {
		return err
	}
	fmt.Printf("%v\n", result)
	// endregion ResolveProcessInstanceIncidents
	return nil
}

func searchProcessInstanceIncidentsExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region SearchProcessInstanceIncidents
	result, err := client.SearchProcessInstanceIncidents(ctx,
		camundaapi.MustProcessInstanceKey("2251799813685340"),
		*camundaapi.NewIncidentSearchQuery())
	if err != nil {
		return err
	}
	for _, inc := range result.GetItems() {
		fmt.Printf("%v\n", inc)
	}
	// endregion SearchProcessInstanceIncidents
	return nil
}

func migrateProcessInstanceExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region MigrateProcessInstance
	instruction := camundaapi.NewProcessInstanceMigrationInstruction(
		camundaapi.ProcessDefinitionKey("2251799813685399"),
		[]camundaapi.MigrateProcessInstanceMappingInstruction{
			*camundaapi.NewMigrateProcessInstanceMappingInstruction("review", "review-v2"),
		})

	return client.MigrateProcessInstance(ctx, camundaapi.MustProcessInstanceKey("2251799813685340"), *instruction)
	// endregion MigrateProcessInstance
}

func modifyProcessInstanceExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region ModifyProcessInstance
	return client.ModifyProcessInstance(ctx,
		camundaapi.MustProcessInstanceKey("2251799813685340"),
		*camundaapi.NewProcessInstanceModificationInstruction())
	// endregion ModifyProcessInstance
}

func cancelProcessInstancesBatchOperationExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region CancelProcessInstancesBatchOperation
	// Cancel every instance matching a filter in a single batch operation.
	req := camundaapi.NewProcessInstanceCancellationBatchOperationRequest(*camundaapi.NewProcessInstanceFilter())

	result, err := client.CancelProcessInstancesBatchOperation(ctx, *req)
	if err != nil {
		return err
	}
	fmt.Printf("created batch operation %v\n", result.GetBatchOperationKey())
	// endregion CancelProcessInstancesBatchOperation
	return nil
}

func deleteProcessInstancesBatchOperationExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region DeleteProcessInstancesBatchOperation
	req := camundaapi.NewProcessInstanceDeletionBatchOperationRequest(*camundaapi.NewProcessInstanceFilter())

	result, err := client.DeleteProcessInstancesBatchOperation(ctx, *req)
	if err != nil {
		return err
	}
	fmt.Printf("created batch operation %v\n", result.GetBatchOperationKey())
	// endregion DeleteProcessInstancesBatchOperation
	return nil
}

func resolveIncidentsBatchOperationExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region ResolveIncidentsBatchOperation
	req := camundaapi.NewProcessInstanceIncidentResolutionBatchOperationRequest(*camundaapi.NewProcessInstanceFilter())

	result, err := client.ResolveIncidentsBatchOperation(ctx, *req)
	if err != nil {
		return err
	}
	fmt.Printf("created batch operation %v\n", result.GetBatchOperationKey())
	// endregion ResolveIncidentsBatchOperation
	return nil
}

func migrateProcessInstancesBatchOperationExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region MigrateProcessInstancesBatchOperation
	plan := camundaapi.NewProcessInstanceMigrationBatchOperationPlan(
		camundaapi.ProcessDefinitionKey("2251799813685399"),
		[]camundaapi.MigrateProcessInstanceMappingInstruction{
			*camundaapi.NewMigrateProcessInstanceMappingInstruction("review", "review-v2"),
		})
	req := camundaapi.NewProcessInstanceMigrationBatchOperationRequest(*camundaapi.NewProcessInstanceFilter(), *plan)

	result, err := client.MigrateProcessInstancesBatchOperation(ctx, *req)
	if err != nil {
		return err
	}
	fmt.Printf("created batch operation %v\n", result.GetBatchOperationKey())
	// endregion MigrateProcessInstancesBatchOperation
	return nil
}

func modifyProcessInstancesBatchOperationExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region ModifyProcessInstancesBatchOperation
	req := camundaapi.NewProcessInstanceModificationBatchOperationRequest(
		*camundaapi.NewProcessInstanceFilter(),
		[]camundaapi.ProcessInstanceModificationMoveBatchOperationInstruction{
			*camundaapi.NewProcessInstanceModificationMoveBatchOperationInstruction("review", "approve"),
		})

	result, err := client.ModifyProcessInstancesBatchOperation(ctx, *req)
	if err != nil {
		return err
	}
	fmt.Printf("created batch operation %v\n", result.GetBatchOperationKey())
	// endregion ModifyProcessInstancesBatchOperation
	return nil
}

func suspendProcessInstanceExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region SuspendProcessInstance
	return client.SuspendProcessInstance(ctx,
		camundaapi.MustProcessInstanceKey("2251799813685340"),
		*camundaapi.NewSuspendProcessInstanceRequest())
	// endregion SuspendProcessInstance
}

func resumeProcessInstanceExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region ResumeProcessInstance
	return client.ResumeProcessInstance(ctx,
		camundaapi.MustProcessInstanceKey("2251799813685340"),
		*camundaapi.NewResumeProcessInstanceRequest())
	// endregion ResumeProcessInstance
}

func assignProcessInstanceBusinessIdExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region AssignProcessInstanceBusinessId
	return client.AssignProcessInstanceBusinessId(ctx,
		camundaapi.MustProcessInstanceKey("2251799813685340"),
		*camundaapi.NewProcessInstanceBusinessIdAssignmentInstruction("order-42"))
	// endregion AssignProcessInstanceBusinessId
}

func suspendProcessInstancesBatchOperationExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region SuspendProcessInstancesBatchOperation
	// Suspend every instance matching a filter in a single batch operation.
	req := camundaapi.NewProcessInstanceSuspensionBatchOperationRequest(*camundaapi.NewProcessInstanceFilter())

	result, err := client.SuspendProcessInstancesBatchOperation(ctx, *req)
	if err != nil {
		return err
	}
	fmt.Printf("created batch operation %v\n", result.GetBatchOperationKey())
	// endregion SuspendProcessInstancesBatchOperation
	return nil
}

func resumeProcessInstancesBatchOperationExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region ResumeProcessInstancesBatchOperation
	// Resume every previously-suspended instance matching a filter.
	req := camundaapi.NewProcessInstanceResumptionBatchOperationRequest(*camundaapi.NewProcessInstanceFilter())

	result, err := client.ResumeProcessInstancesBatchOperation(ctx, *req)
	if err != nil {
		return err
	}
	fmt.Printf("created batch operation %v\n", result.GetBatchOperationKey())
	// endregion ResumeProcessInstancesBatchOperation
	return nil
}
