package camunda

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/camunda/orchestration-cluster-api-go/v10/internal/diag"
)

// The REST job-command paths must classify HTTP failures by status: the generated
// client returns them as GenericOpenAPIError, which must be mapped to *APIError
// first, or 404 is logged as "outcome unknown" and 400 as a warning.
func TestRESTJobCommandsClassifyHTTPStatus(t *testing.T) {
	cmds := map[string]func(c *CamundaClient, j *Job){
		"complete job":    func(c *CamundaClient, j *Job) { c.restCompleteJob(context.Background(), j, nil) },
		"fail job":        func(c *CamundaClient, j *Job) { c.restFailJob(context.Background(), j, nil) },
		"throw job error": func(c *CamundaClient, j *Job) { c.restThrowError(context.Background(), j, &BpmnError{Code: "E"}) },
	}
	for name, run := range cmds {
		for status, want := range map[int]string{
			http.StatusNotFound:   "job not found",
			http.StatusBadRequest: "[ERROR]",
		} {
			t.Run(name+"/"+http.StatusText(status), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(status)
				}))
				defer srv.Close()
				c, err := New(WithRestAddress(srv.URL), WithLogLevel(LogOff))
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				var buf bytes.Buffer
				c.logger = diag.New(diag.LevelDebug, &buf, fixedClock{})
				run(c, &Job{key: "1"})
				out := buf.String()
				if !strings.Contains(out, want) || strings.Contains(out, "outcome unknown") {
					t.Errorf("log %q: want %q and no 'outcome unknown'", out, want)
				}
			})
		}
	}
}
