package camunda

import (
	"errors"
	"net/http"
	"testing"

	openapi "github.com/camunda/orchestration-cluster-api-go/client"
)

func TestNewBasicAuthBuildsClient(t *testing.T) {
	c, err := New(WithRestAddress("http://localhost:8080"), WithBasicAuth("u", "p"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Raw() == nil {
		t.Error("expected a non-nil raw client")
	}
	if c.Config().AuthStrategy != AuthBasic {
		t.Errorf("AuthStrategy = %v, want AuthBasic", c.Config().AuthStrategy)
	}
}

func TestNewOAuthMissingCredentialsFails(t *testing.T) {
	_, err := New(WithOAuth("", "", ""))
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("expected ErrConfig for OAuth without credentials, got %v", err)
	}
}

func TestV2BaseURL(t *testing.T) {
	cases := map[string]string{
		"http://localhost:8080":       "http://localhost:8080/v2",
		"http://localhost:8080/":      "http://localhost:8080/v2",
		"https://cluster.example/v2":  "https://cluster.example/v2",
		"https://cluster.example/v2/": "https://cluster.example/v2",
	}
	for in, want := range cases {
		if got := v2BaseURL(in); got != want {
			t.Errorf("v2BaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExemptDrainOps(t *testing.T) {
	exempt := []string{
		"http://x/v2/jobs/123/completion",
		"http://x/v2/jobs/123/failure",
		"http://x/v2/jobs/123/error",
		"http://x/v2/user-tasks/456/completion",
	}
	for _, u := range exempt {
		req, _ := http.NewRequest(http.MethodPost, u, nil)
		if !exemptDrainOps(req) {
			t.Errorf("expected %q to be exempt from backpressure gating", u)
		}
	}
	gated := []string{
		"http://x/v2/process-instances/search",
		"http://x/v2/jobs/activation",
	}
	for _, u := range gated {
		req, _ := http.NewRequest(http.MethodPost, u, nil)
		if exemptDrainOps(req) {
			t.Errorf("expected %q to be gated (not exempt)", u)
		}
	}
}

func TestWrapError(t *testing.T) {
	c := &CamundaClient{}

	if err := c.wrapError(nil, nil); err != nil {
		t.Errorf("nil error should map to nil, got %v", err)
	}

	// A non-success HTTP response maps to *APIError carrying the status.
	mapped := c.wrapError(&http.Response{StatusCode: 404}, &openapi.GenericOpenAPIError{})
	var apiErr *APIError
	if !errors.As(mapped, &apiErr) {
		t.Fatalf("expected *APIError, got %T (%v)", mapped, mapped)
	}
	if apiErr.Status != 404 {
		t.Errorf("APIError.Status = %d, want 404", apiErr.Status)
	}

	// A transport-level error (no response) is passed through unchanged.
	netErr := errors.New("connection refused")
	if got := c.wrapError(nil, netErr); !errors.Is(got, netErr) {
		t.Errorf("expected transport error to pass through, got %v", got)
	}
}
