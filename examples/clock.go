// Clock operations: control the cluster clock (test/CI environments).
package examples

import (
	"context"
	"fmt"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go/v11"
)

func getClockExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region GetClock
	// Read the cluster clock. While the clock is pinned this reports the pinned
	// instant, so a test can assert that a pin took effect before driving time.
	clock, err := client.GetClock(ctx)
	if err != nil {
		return err
	}
	clusterNow := clock.GetTimestamp()
	skew := time.Since(clusterNow).Round(time.Millisecond)
	fmt.Printf("cluster clock: %s (skew vs local: %s)\n", clusterNow.Format(time.RFC3339), skew)
	// endregion GetClock
	return nil
}

func pinClockExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region PinClock
	// Pin the cluster clock to a fixed instant (epoch milliseconds).
	pinned := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	return client.PinClock(ctx, *camunda.NewClockPinRequest(pinned.UnixMilli()))
	// endregion PinClock
}

func resetClockExample(ctx context.Context, client *camunda.CamundaClient) error {
	// region ResetClock
	// Release a previously pinned clock back to system time.
	return client.ResetClock(ctx)
	// endregion ResetClock
}
