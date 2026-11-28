package camunda

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClassifyJobCommandErrors(t *testing.T) {
	rest := map[error]jobCommandOutcome{
		&APIError{Status: 404}:   jobCommandNotFound,
		&APIError{Status: 504}:   jobCommandUnknown,
		&APIError{Status: 502}:   jobCommandUnknown,
		&APIError{Status: 500}:   jobCommandUnknown,
		&APIError{Status: 503}:   jobCommandFailed,
		&APIError{Status: 400}:   jobCommandFailed,
		errors.New("conn reset"): jobCommandUnknown,
	}
	for err, want := range rest {
		if got := classifyRESTJobCommandErr(err); got != want {
			t.Errorf("REST %v: got %d, want %d", err, got, want)
		}
	}
	grpc := map[codes.Code]jobCommandOutcome{
		codes.NotFound:          jobCommandNotFound,
		codes.DeadlineExceeded:  jobCommandUnknown,
		codes.Unavailable:       jobCommandUnknown,
		codes.InvalidArgument:   jobCommandFailed,
		codes.ResourceExhausted: jobCommandFailed,
	}
	for c, want := range grpc {
		if got := classifyGRPCJobCommandErr(status.Error(c, "x")); got != want {
			t.Errorf("gRPC %v: got %d, want %d", c, got, want)
		}
	}
}
