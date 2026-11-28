package camunda

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/camunda/orchestration-cluster-api-go/v10/internal/diag"
)

// A raw response body (or job key) with newlines must not forge log records.
func TestLogJobCommandFailureEscapesErrorAndKey(t *testing.T) {
	for _, outcome := range []jobCommandOutcome{jobCommandFailed, jobCommandNotFound, jobCommandUnknown} {
		var buf bytes.Buffer
		c := &CamundaClient{logger: diag.New(diag.LevelTrace, &buf, fixedClock{})}
		err := &APIError{Status: 502, Body: "bad\n1970-01-01T00:00:00Z [ERROR] camunda forged"}
		c.logJobCommandFailure("complete job", "1\nforged", outcome, errors.Join(err))
		if n := strings.Count(buf.String(), "\n"); n != 1 {
			t.Fatalf("outcome %d: want 1 log line, got %d: %q", outcome, n, buf.String())
		}
	}
}
