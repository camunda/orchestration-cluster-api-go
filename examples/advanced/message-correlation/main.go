package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	openapi "github.com/camunda/orchestration-cluster-api-go/client"
	"github.com/camunda/orchestration-cluster-api-go/examples/advanced/internal/exampleutil"
)

const processID = "go-example-payment"

//go:embed payment.bpmn
var processModel []byte

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "message correlation example:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, err := exampleutil.NewClient()
	if err != nil {
		return err
	}
	if err := exampleutil.Deploy(ctx, client, "payment.bpmn", processModel); err != nil {
		return err
	}

	// A run-scoped suffix keeps business IDs and provider event IDs stable within
	// retries while avoiding collisions when the example is executed repeatedly.
	runID := time.Now().UTC().Format("20060102T150405.000000000")
	orderIDs := []string{
		"payment-2001-" + runID,
		"payment-2002-" + runID,
		"payment-2003-" + runID,
	}
	keys := make([]openapi.ProcessInstanceKey, 0, len(orderIDs))
	for _, orderID := range orderIDs {
		// Publish immediately after starting the process on purpose. The message TTL
		// must cover the interval before the subscription becomes available.
		key, err := exampleutil.StartProcess(ctx, client, processID, orderID,
			map[string]any{"orderId": orderID, "amount": 49.90})
		if err != nil {
			return err
		}
		keys = append(keys, key)

		// Prefer publish-with-TTL for integration events. The broker buffers the
		// event if the subscription is not open yet, removing a race between the
		// process start and message delivery.
		messageID := "payment-provider-event-" + orderID
		if err := publishPayment(ctx, client, orderID, messageID); err != nil {
			return err
		}

		// A stable message ID makes redelivery by an at-least-once producer safe
		// during the TTL deduplication window.
		if err := publishPayment(ctx, client, orderID, messageID); err != nil {
			return fmt.Errorf("redeliver payment event: %w", err)
		}
	}

	// Completion is read from eventually consistent secondary storage, so use the
	// SDK polling helper rather than assuming immediate visibility.
	for i, key := range keys {
		if _, err := exampleutil.WaitForCompletion(ctx, client, key); err != nil {
			return err
		}
		fmt.Printf("%s correlated and completed\n", orderIDs[i])
	}
	return nil
}

func publishPayment(
	ctx context.Context,
	client *camunda.CamundaClient,
	orderID string,
	messageID string,
) error {
	request := openapi.NewMessagePublicationRequest("payment-received")
	request.SetCorrelationKey(orderID)
	request.SetMessageId(messageID)
	// TTL defines both the buffering window and duplicate-ID protection window.
	// Set it from real producer redelivery and process-start timing in production.
	request.SetTimeToLive((10 * time.Minute).Milliseconds())
	request.SetVariables(map[string]any{
		"paymentStatus": "captured",
		"paymentId":     "pay-" + orderID,
	})
	if _, err := client.PublishMessage(ctx, *request); err != nil {
		if status, ok := camunda.StatusCode(err); ok && status == 409 {
			// The broker has already accepted this idempotency key. Treat that
			// specific conflict as successful redelivery, not as a retry signal.
			return nil
		}
		return fmt.Errorf("publish payment for %s: %w", orderID, err)
	}
	return nil
}
