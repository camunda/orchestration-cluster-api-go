package camunda

import (
	"net/http"
	"net/url"
	"testing"
)

func TestIsJobCommandPreservedThroughRedirect(t *testing.T) {
	orig := &http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/v2/jobs/1/completion"}}
	redirected := &http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/dispatch/completion"},
		Response: &http.Response{StatusCode: http.StatusTemporaryRedirect, Request: orig}}
	if !isJobCommand(redirected) {
		t.Fatal("redirected job command lost its classification")
	}
	other := &http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/v2/process-instances"},
		Response: &http.Response{StatusCode: http.StatusTemporaryRedirect,
			Request: &http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/v2/process-instances/x"}}}}
	if isJobCommand(other) {
		t.Fatal("non-job-command misclassified")
	}
}
