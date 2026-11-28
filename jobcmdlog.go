package camunda

import (
	"encoding/json"
	"errors"

	"github.com/camunda/orchestration-cluster-api-go/v10/internal/retry"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// jobCommandOutcome classifies why a job command (complete, fail, throw error)
// failed, so the worker can log it at the right level.
type jobCommandOutcome int

const (
	// jobCommandFailed: the server rejected the command; it was not applied.
	jobCommandFailed jobCommandOutcome = iota
	// jobCommandNotFound: the job no longer exists (already completed, failed
	// or timed out) — for example a replay of a command that was applied.
	jobCommandNotFound
	// jobCommandUnknown: the command may have been applied (504, 502, 500, or a
	// transport error after the request was sent). It is not retried and no
	// follow-up failJob is sent; if it was not applied, the job times out and is
	// re-activated.
	jobCommandUnknown
)

// classifyRESTJobCommandErr classifies the error of a REST job command.
func classifyRESTJobCommandErr(err error) jobCommandOutcome {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Status == 404:
			return jobCommandNotFound
		case apiErr.Status == 429 || apiErr.Status == 503:
			// Not applied (retries exhausted).
			return jobCommandFailed
		case apiErr.Status >= 500:
			return jobCommandUnknown
		}
		return jobCommandFailed
	}
	// A request body that could not be serialized was never sent.
	var unsupType *json.UnsupportedTypeError
	var unsupVal *json.UnsupportedValueError
	var marshalErr *json.MarshalerError
	if errors.As(err, &unsupType) || errors.As(err, &unsupVal) || errors.As(err, &marshalErr) {
		return jobCommandFailed
	}
	// A connection that was never established (connect refused, DNS, failed
	// TLS handshake) or a failed OAuth token fetch means
	// the command was not sent; any other transport error (timeout, reset,
	// truncated response) may have happened after it was applied.
	if retry.IsPreSendError(err) {
		return jobCommandFailed
	}
	return jobCommandUnknown
}

// classifyGRPCJobCommandErr classifies the error of a gRPC job command.
func classifyGRPCJobCommandErr(err error) jobCommandOutcome {
	st, ok := status.FromError(err)
	if !ok {
		return jobCommandUnknown
	}
	switch st.Code() {
	case codes.NotFound:
		return jobCommandNotFound
	case codes.DeadlineExceeded, codes.Unavailable, codes.Unknown, codes.Internal, codes.Canceled:
		return jobCommandUnknown
	}
	return jobCommandFailed
}

// logJobCommandFailure logs a failed job command. action is e.g. "complete job".
func (c *CamundaClient) logJobCommandFailure(action, jobKey string, outcome jobCommandOutcome, err error) {
	switch outcome {
	case jobCommandNotFound:
		c.logger.Warn(action+" failed: job not found (already completed, failed or timed out)", "job", jobKey, "error", err)
	case jobCommandUnknown:
		c.logger.Warn(action+" failed: outcome unknown; the job may have completed, otherwise it will time out and be re-activated", "job", jobKey, "error", err)
	default:
		c.logger.Error(action+" failed", "job", jobKey, "error", err)
	}
}
