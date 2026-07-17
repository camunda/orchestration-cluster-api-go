package exampleutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	openapi "github.com/camunda/orchestration-cluster-api-go/client"
)

var errProcessActive = errors.New("process instance is still active")

// NewClient targets a local c8run cluster. Production applications should source
// credentials from a secret store and select OAuth instead of using these defaults.
func NewClient() (*camunda.CamundaClient, error) {
	return camunda.New(
		camunda.WithRestAddress(envOr("CAMUNDA_REST_ADDRESS", "http://localhost:8080")),
		camunda.WithBasicAuth(
			envOr("CAMUNDA_BASIC_AUTH_USERNAME", "demo"),
			envOr("CAMUNDA_BASIC_AUTH_PASSWORD", "demo"),
		),
		camunda.WithBackpressureProfile(camunda.ProfileBalanced),
		camunda.WithRetry(camunda.RetryConfig{
			MaxAttempts: 5,
			BaseDelay:   100 * time.Millisecond,
			MaxDelay:    3 * time.Second,
		}),
		camunda.WithLogLevel(camunda.LogWarn),
	)
}

func Deploy(ctx context.Context, client *camunda.CamundaClient, name string, model []byte) error {
	dir, err := os.MkdirTemp("", "camunda-go-example-*")
	if err != nil {
		return fmt.Errorf("create deployment directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, model, 0o600); err != nil {
		return fmt.Errorf("write BPMN model: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open BPMN model: %w", err)
	}
	defer func() { _ = file.Close() }()

	if _, _, err := client.Raw().ResourceAPI.CreateDeployment(ctx).
		Resources([]*os.File{file}).
		Execute(); err != nil {
		return fmt.Errorf("deploy %s: %w", name, err)
	}
	return nil
}

func StartProcess(
	ctx context.Context,
	client *camunda.CamundaClient,
	processID string,
	businessID string,
	variables map[string]any,
) (openapi.ProcessInstanceKey, error) {
	byID := openapi.NewProcessInstanceCreationInstructionById(processID)
	byID.SetBusinessId(businessID)
	byID.SetTags([]string{"go-sdk-example"})
	byID.SetVariables(variables)

	result, err := client.CreateProcessInstance(ctx,
		openapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(byID))
	if err != nil {
		return "", fmt.Errorf("start process %q for %q: %w", processID, businessID, err)
	}
	return openapi.MustProcessInstanceKey(string(result.GetProcessInstanceKey())), nil
}

func WaitForCompletion(
	ctx context.Context,
	client *camunda.CamundaClient,
	key openapi.ProcessInstanceKey,
) (*openapi.ProcessInstanceResult, error) {
	result, err := camunda.Poll(ctx, func(ctx context.Context) (*openapi.ProcessInstanceResult, error) {
		instance, err := client.GetProcessInstance(ctx, key)
		if err != nil {
			return nil, err
		}
		switch instance.GetState() {
		case openapi.PROCESSINSTANCESTATEENUM_COMPLETED:
			return instance, nil
		case openapi.PROCESSINSTANCESTATEENUM_TERMINATED:
			return nil, fmt.Errorf("process instance %s was terminated", key)
		default:
			return nil, errProcessActive
		}
	},
		camunda.WithPollTimeout(30*time.Second),
		camunda.WithPollRetryInterval(250*time.Millisecond),
		camunda.WithRetryOn(func(err error) bool {
			return errors.Is(err, errProcessActive) || camunda.IsNotFound(err)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("wait for process instance %s: %w", key, err)
	}
	return result, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
