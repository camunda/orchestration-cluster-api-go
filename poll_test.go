package camunda

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPollSucceedsImmediately(t *testing.T) {
	calls := 0
	got, err := Poll(context.Background(), func(context.Context) (int, error) {
		calls++
		return 42, nil
	})
	if err != nil || got != 42 {
		t.Fatalf("Poll = (%d, %v), want (42, nil)", got, err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestPollRetriesOn404ThenSucceeds(t *testing.T) {
	calls := 0
	got, err := Poll(context.Background(), func(context.Context) (string, error) {
		calls++
		if calls < 3 {
			return "", &APIError{Status: 404}
		}
		return "ready", nil
	}, WithPollRetryInterval(time.Millisecond), WithPollTimeout(2*time.Second))
	if err != nil || got != "ready" {
		t.Fatalf("Poll = (%q, %v), want (ready, nil)", got, err)
	}
	if calls != 3 {
		t.Errorf("expected 3 attempts, got %d", calls)
	}
}

func TestPollTimesOut(t *testing.T) {
	_, err := Poll(context.Background(), func(context.Context) (int, error) {
		return 0, &APIError{Status: 404}
	}, WithPollRetryInterval(5*time.Millisecond), WithPollTimeout(40*time.Millisecond))
	if !errors.Is(err, ErrEventualConsistencyTimeout) {
		t.Fatalf("expected ErrEventualConsistencyTimeout, got %v", err)
	}
}

func TestPollDoesNotRetryNonRetryableError(t *testing.T) {
	calls := 0
	_, err := Poll(context.Background(), func(context.Context) (int, error) {
		calls++
		return 0, &APIError{Status: 500}
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 500 {
		t.Fatalf("expected the 500 APIError to pass through, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected no retries for a non-404 error, got %d attempts", calls)
	}
}

func TestPollRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Poll(ctx, func(context.Context) (int, error) {
		return 0, &APIError{Status: 404}
	}, WithPollRetryInterval(time.Second))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestIsNotFound(t *testing.T) {
	if !IsNotFound(&APIError{Status: 404}) {
		t.Error("404 APIError should be NotFound")
	}
	if IsNotFound(&APIError{Status: 409}) {
		t.Error("409 APIError should not be NotFound")
	}
	if IsNotFound(errors.New("plain")) {
		t.Error("plain error should not be NotFound")
	}
}
