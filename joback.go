package camunda

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"

	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
)

// This file is the REST job-worker acknowledgement hot path (complete, fail,
// throw-error). Every handled job issues exactly one of these, so they run at
// the worker's full throughput. The generated JobAPIService path allocates
// per-call header and query maps, builds the path with strings.Replace, routes
// the body through a reflection-driven ToMap, and buffers the response into a
// fresh reader even for 204 No Content. The requests built here are
// byte-for-byte equivalent on the wire (method, URL, headers, JSON body) and
// still go through the configured *http.Client, i.e. the SDK transport chain
// (backpressure -> retry -> auth -> sized transport) or a user-supplied client.

// jobCompletionBody is the wire form of JobCompletionRequest for the fields the
// worker sets.
type jobCompletionBody struct {
	Variables     map[string]any        `json:"variables,omitempty"`
	Result        *camundaapi.JobResult `json:"result,omitempty"`
	JobLeaseToken string                `json:"jobLeaseToken,omitempty"`
}

// jobFailBody is the wire form of JobFailRequest for the fields the worker sets.
type jobFailBody struct {
	Retries      int32   `json:"retries"`
	ErrorMessage *string `json:"errorMessage,omitempty"`
	// RetryBackOff is always sent as 0, as NewJobFailRequest defaults it.
	RetryBackOff  int64  `json:"retryBackOff"`
	JobLeaseToken string `json:"jobLeaseToken,omitempty"`
}

// jobErrorBody is the wire form of JobErrorRequest for the fields the worker sets.
type jobErrorBody struct {
	ErrorCode     string         `json:"errorCode"`
	ErrorMessage  *string        `json:"errorMessage,omitempty"`
	Variables     map[string]any `json:"variables,omitempty"`
	JobLeaseToken string         `json:"jobLeaseToken,omitempty"`
}

// emptyObject is the body of a completion with no variables, result, or lease.
var emptyObject = []byte("{}")

// postJobAck POSTs body to <base>/jobs/<jobKey>/<suffix>, where base is the
// server configured for the generated operation op. A non-2xx response returns
// an *APIError carrying the status and the problem body; a 2xx response body is
// drained and closed so the connection is reused.
func (c *CamundaClient) postJobAck(ctx context.Context, op, jobKey, suffix string, body []byte) error {
	cfg := c.raw.GetConfig()
	base, err := cfg.ServerURLWithContext(ctx, op)
	if err != nil {
		return err
	}
	u := base + "/jobs/" + url.PathEscape(jobKey) + "/" + suffix
	if cfg.Host != "" || cfg.Scheme != "" {
		pu, err := url.Parse(u)
		if err != nil {
			return err
		}
		if cfg.Host != "" {
			pu.Host = cfg.Host
		}
		if cfg.Scheme != "" {
			pu.Scheme = cfg.Scheme
		}
		u = pu.String()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	h := req.Header
	h["Content-Type"] = []string{"application/json"}
	h["Accept"] = []string{"application/problem+json"}
	h.Add("User-Agent", cfg.UserAgent)
	if a, ok := ctx.Value(camundaapi.ContextBasicAuth).(camundaapi.BasicAuth); ok {
		req.SetBasicAuth(a.UserName, a.Password)
	}
	if tok, ok := ctx.Value(camundaapi.ContextAccessToken).(string); ok {
		h.Add("Authorization", "Bearer "+tok)
	}
	for k, v := range cfg.DefaultHeader {
		h.Add(k, v)
	}
	resp, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return &APIError{Status: resp.StatusCode, Body: string(b)}
	}
	if resp.ContentLength != 0 {
		// Propagate drain errors (truncated body, deadline expiry) exactly as
		// the generated path's io.ReadAll would.
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			return err
		}
	}
	return nil
}

// useGeneratedAck reports whether the generated client must be used for acks:
// its Debug mode dumps requests and responses, which the light path does not.
func (c *CamundaClient) useGeneratedAck() bool {
	return c.raw.GetConfig().Debug
}
