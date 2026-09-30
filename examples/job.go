// Job operations: direct activation/completion, search, update, and statistics.
// For long-running work prefer the NewJobWorker / NewStreamJobWorker helpers
// (see the README) over calling ActivateJobs directly.
package examples

import (
	"context"
	"fmt"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	camundaapi "github.com/camunda/orchestration-cluster-api-go/client"
)

func activateJobsExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region ActivateJobs
	// Activate up to 10 "greet" jobs with a 60s activation timeout.
	req := camundaapi.NewJobActivationRequest("greet", 60_000, 10)
	req.SetWorker("greet-worker")

	result, err := client.ActivateJobs(ctx, *req)
	if err != nil {
		return err
	}
	for _, job := range result.GetJobs() {
		fmt.Printf("activated job %v\n", job.GetJobKey())
	}
	// endregion ActivateJobs
	return nil
}

func completeJobExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region CompleteJob
	req := camundaapi.NewJobCompletionRequest()
	req.SetVariables(map[string]any{"greeting": "Hello!"})

	return client.CompleteJob(ctx, camundaapi.MustJobKey("2251799813685424"), *req)
	// endregion CompleteJob
}

func failJobExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region FailJob
	req := camundaapi.NewJobFailRequest()
	req.SetRetries(2)
	req.SetErrorMessage("inventory service unavailable")

	return client.FailJob(ctx, camundaapi.MustJobKey("2251799813685424"), *req)
	// endregion FailJob
}

func throwJobErrorExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region ThrowJobError
	req := camundaapi.NewJobErrorRequest("OUT_OF_STOCK")
	req.SetErrorMessage("item is out of stock")

	return client.ThrowJobError(ctx, camundaapi.MustJobKey("2251799813685424"), *req)
	// endregion ThrowJobError
}

func updateJobExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region UpdateJob
	changeset := camundaapi.NewJobChangeset()
	changeset.SetRetries(3)

	return client.UpdateJob(ctx, camundaapi.MustJobKey("2251799813685424"), *camundaapi.NewJobUpdateRequest(*changeset))
	// endregion UpdateJob
}

func searchJobsExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region SearchJobs
	result, err := client.SearchJobs(ctx, *camundaapi.NewJobSearchQuery())
	if err != nil {
		return err
	}
	for _, job := range result.GetItems() {
		fmt.Printf("%v\n", job)
	}
	// endregion SearchJobs
	return nil
}

func updateJobsBatchOperationExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region UpdateJobsBatchOperation
	changeset := camundaapi.NewJobChangeset()
	changeset.SetRetries(3)
	req := camundaapi.NewJobBatchUpdateRequest(*camundaapi.NewJobFilter(), *changeset)

	result, err := client.UpdateJobsBatchOperation(ctx, *req)
	if err != nil {
		return err
	}
	fmt.Printf("created batch operation %v\n", result.GetBatchOperationKey())
	// endregion UpdateJobsBatchOperation
	return nil
}

func getGlobalJobStatisticsExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region GetGlobalJobStatistics
	result, err := client.GetGlobalJobStatistics(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%v\n", result)
	// endregion GetGlobalJobStatistics
	return nil
}

func getJobTypeStatisticsExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region GetJobTypeStatistics
	result, err := client.GetJobTypeStatistics(ctx, *camundaapi.NewJobTypeStatisticsQuery())
	if err != nil {
		return err
	}
	fmt.Printf("%v\n", result)
	// endregion GetJobTypeStatistics
	return nil
}

func getJobWorkerStatisticsExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region GetJobWorkerStatistics
	from, to := time.Now().Add(-24*time.Hour), time.Now()
	query := camundaapi.NewJobWorkerStatisticsQuery(*camundaapi.NewJobWorkerStatisticsFilter(from, to, "greet"))

	result, err := client.GetJobWorkerStatistics(ctx, *query)
	if err != nil {
		return err
	}
	fmt.Printf("%v\n", result)
	// endregion GetJobWorkerStatistics
	return nil
}

func getJobTimeSeriesStatisticsExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region GetJobTimeSeriesStatistics
	from, to := time.Now().Add(-24*time.Hour), time.Now()
	query := camundaapi.NewJobTimeSeriesStatisticsQuery(*camundaapi.NewJobTimeSeriesStatisticsFilter(from, to, "greet"))

	result, err := client.GetJobTimeSeriesStatistics(ctx, *query)
	if err != nil {
		return err
	}
	fmt.Printf("%v\n", result)
	// endregion GetJobTimeSeriesStatistics
	return nil
}

func getJobErrorStatisticsExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region GetJobErrorStatistics
	from, to := time.Now().Add(-24*time.Hour), time.Now()
	query := camundaapi.NewJobErrorStatisticsQuery(*camundaapi.NewJobErrorStatisticsFilter(from, to, "greet"))

	result, err := client.GetJobErrorStatistics(ctx, *query)
	if err != nil {
		return err
	}
	fmt.Printf("%v\n", result)
	// endregion GetJobErrorStatistics
	return nil
}
