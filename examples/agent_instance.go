// Agent instance operations: create, read, update, search, and history.
package examples

import (
	"context"
	"fmt"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	camundaapi "github.com/camunda/orchestration-cluster-api-go/client"
)

func createAgentInstanceExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region CreateAgentInstance
	systemPrompt := []camundaapi.AgentInstanceMessageContent{
		camundaapi.AgentInstanceTextContentAsAgentInstanceMessageContent(
			camundaapi.NewAgentInstanceTextContent("TEXT", "You are a helpful assistant.")),
	}
	configItem := camundaapi.NewAgentInstanceHistoryItem(
		"config-1", camundaapi.MustLoopIterationId(1), camundaapi.AGENTINSTANCEHISTORYROLEENUM_CONFIGURATION, nil, time.Now())
	configItem.SetModel("gpt-4o")
	configItem.SetProvider("openai")
	configItem.SetSystemPrompt(systemPrompt)

	req := camundaapi.NewAgentInstanceCreationRequest(
		camundaapi.ElementInstanceKey("2251799813685360"), // elementInstanceKey
		camundaapi.JobKey("2251799813685424"),             // jobKey
		"lease-token",
		[]camundaapi.AgentInstanceHistoryItem{*configItem}, // history
	)

	result, err := client.CreateAgentInstance(ctx, *req)
	if err != nil {
		return err
	}
	fmt.Printf("%v\n", result)
	// endregion CreateAgentInstance
	return nil
}

func getAgentInstanceExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region GetAgentInstance
	agent, err := client.GetAgentInstance(ctx, camundaapi.MustAgentInstanceKey("2251799813685370"))
	if err != nil {
		return err
	}
	fmt.Printf("%v\n", agent)
	// endregion GetAgentInstance
	return nil
}

func updateAgentInstanceExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region UpdateAgentInstance
	req := camundaapi.NewAgentInstanceUpdateRequest(
		camundaapi.ElementInstanceKey("2251799813685360"), // elementInstanceKey
		camundaapi.JobKey("2251799813685424"),             // jobKey
		"lease-token",
	)

	result, err := client.UpdateAgentInstance(ctx, camundaapi.MustAgentInstanceKey("2251799813685370"), *req)
	if err != nil {
		return err
	}
	fmt.Printf("%v\n", result)
	// endregion UpdateAgentInstance
	return nil
}

func searchAgentInstancesExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region SearchAgentInstances
	result, err := client.SearchAgentInstances(ctx, *camundaapi.NewAgentInstanceSearchQuery())
	if err != nil {
		return err
	}
	for _, a := range result.GetItems() {
		fmt.Printf("%v\n", a)
	}
	// endregion SearchAgentInstances
	return nil
}

func searchAgentInstanceHistoryExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region SearchAgentInstanceHistory
	result, err := client.SearchAgentInstanceHistory(ctx,
		camundaapi.MustAgentInstanceKey("2251799813685370"),
		*camundaapi.NewAgentInstanceHistorySearchQuery())
	if err != nil {
		return err
	}
	fmt.Printf("%v\n", result)
	// endregion SearchAgentInstanceHistory
	return nil
}
