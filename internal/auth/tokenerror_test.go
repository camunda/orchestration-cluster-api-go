package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sentinelSecret is a recognisable value configured as the OAuth client secret so
// a leak into an error message is unambiguous rather than coincidental.
const sentinelSecret = "CAMUNDA-SENTINEL-DO-NOT-LOG-9f3a"

// sentinelBody is a recognisable token-endpoint response body. An identity
// provider is free to put anything in an error body — including submitted
// parameters or, in a partially-successful response, token material — so the SDK
// treats the whole body as potentially credential-bearing.
const sentinelBody = "CAMUNDA-SENTINEL-RESPONSE-BODY-4c71"

// TestTokenFetchErrorsDoNotEchoResponseBody covers the defect class rather than
// the single reported instance: *no* failure branch of TokenSource.fetch may put
// token-endpoint response content, or the configured client secret, into the
// error it returns. The returned error reaches the job worker's log on every poll
// (worker.go "activate jobs failed"), so anything in the message is written to
// the SDK log.
func TestTokenFetchErrorsDoNotEchoResponseBody(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "non-2xx with body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"`+sentinelBody+`"}`)
			},
		},
		{
			name: "non-2xx echoing the submitted form",
			handler: func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				w.WriteHeader(http.StatusUnauthorized)
				// Some identity providers echo the submitted parameters back, which
				// puts the client secret itself in the response body.
				_, _ = w.Write(body)
			},
		},
		{
			name: "server error with body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, sentinelBody)
			},
		},
		{
			name: "2xx with an undecodable body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, "not json "+sentinelBody)
			},
		},
		{
			name: "2xx with an empty access_token",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"access_token":"","error":"`+sentinelBody+`"}`)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			ts := NewTokenSource(OAuthConfig{
				TokenURL:     srv.URL,
				ClientID:     "sentinel-client-id",
				ClientSecret: sentinelSecret,
			}, &settableClock{t: time.Unix(1_000_000, 0)})

			tok, err := ts.Token(context.Background())
			if err == nil {
				t.Fatalf("Token() succeeded with token %q, want an error", tok)
			}
			if tok != "" {
				t.Errorf("Token() returned %q alongside an error, want an empty token", tok)
			}
			msg := err.Error()
			if strings.Contains(msg, sentinelBody) {
				t.Errorf("error echoes the token-endpoint response body: %q", msg)
			}
			if strings.Contains(msg, sentinelSecret) {
				t.Errorf("error leaks the configured client secret: %q", msg)
			}
		})
	}
}

// TestTokenFetchErrorStillIdentifiesTheStatus guards the other half of the
// change: dropping the body must not drop the diagnostic. The status code is
// what tells an operator whether the credentials were rejected (401/400) or the
// identity provider is unhealthy (5xx).
func TestTokenFetchErrorStillIdentifiesTheStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, sentinelBody)
	}))
	defer srv.Close()

	ts := NewTokenSource(OAuthConfig{
		TokenURL:     srv.URL,
		ClientID:     "id",
		ClientSecret: sentinelSecret,
	}, &settableClock{t: time.Unix(1_000_000, 0)})

	_, err := ts.Token(context.Background())
	if err == nil {
		t.Fatal("Token() succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error %q does not report the HTTP status", err)
	}
}
