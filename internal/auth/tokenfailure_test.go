package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// erroringRoundTripper fails every request, standing in for a token endpoint
// that cannot be reached at all (DNS failure, refused connection, TLS error).
type erroringRoundTripper struct{ calls atomic.Int32 }

func (e *erroringRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	e.calls.Add(1)
	return nil, errors.New("dial tcp: simulated transport failure")
}

// countingRoundTripper records whether it was reached. It stands in for the next
// RoundTripper in the chain — the one that would carry the actual API request.
type countingRoundTripper struct{ calls atomic.Int32 }

func (c *countingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header), Request: r}, nil
}

// TestTokenSourceFailures covers the failure branches of TokenSource.fetch, none
// of which had a test: every existing test in this package drives a 200 from the
// stub endpoint. Each case asserts the error is returned rather than swallowed,
// and — the part that matters for an auth component — that nothing is left
// cached in memory or on disk for a later call to hand out.
func TestTokenSourceFailures(t *testing.T) {
	tests := []struct {
		name string
		// handler serves the token endpoint. nil means the endpoint is
		// unreachable (a transport-level failure instead of an HTTP response).
		handler     http.HandlerFunc
		wantErrText string
	}{
		{
			name: "http 400",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_client"}`)
			},
			wantErrText: "400",
		},
		{
			name: "http 401",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			wantErrText: "401",
		},
		{
			name: "http 500",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantErrText: "500",
		},
		{
			name: "http 503",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
			wantErrText: "503",
		},
		{
			name: "redirect without a location is not success",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				// A 3xx the client cannot follow is outside 2xx and must be
				// treated as a failure rather than as an empty success. No
				// Location header, so this stays hermetic.
				w.WriteHeader(http.StatusFound)
			},
			wantErrText: "302",
		},
		{
			name:        "transport failure",
			handler:     nil,
			wantErrText: "token request failed",
		},
		{
			name: "undecodable body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, "{not json")
			},
			wantErrText: "decoding token response",
		},
		{
			name: "json body of the wrong shape",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `["an","array"]`)
			},
			wantErrText: "decoding token response",
		},
		{
			name: "empty body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
			},
			wantErrText: "decoding token response",
		},
		{
			name: "empty access_token",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"access_token":"","expires_in":3600}`)
			},
			wantErrText: "empty access_token",
		},
		{
			name: "access_token absent",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"expires_in":3600}`)
			},
			wantErrText: "empty access_token",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := OAuthConfig{ClientID: "id", ClientSecret: "sec", CacheDir: dir}

			// attempts counts how many times the token endpoint is actually
			// reached, so the test can tell a genuine retry from a replayed
			// cached error.
			var attempts atomic.Int32
			erroring := &erroringRoundTripper{}
			if tc.handler != nil {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempts.Add(1)
					tc.handler(w, r)
				}))
				defer srv.Close()
				cfg.TokenURL = srv.URL
			} else {
				cfg.TokenURL = "http://token.invalid/oauth/token"
				cfg.HTTPClient = &http.Client{Transport: erroring}
			}
			fetchCount := func() int32 {
				if tc.handler != nil {
					return attempts.Load()
				}
				return erroring.calls.Load()
			}

			ts := NewTokenSource(cfg, &settableClock{t: time.Unix(1_000_000, 0)})

			tok, err := ts.Token(context.Background())
			if err == nil {
				t.Fatalf("Token() = %q, nil; want an error", tok)
			}
			if tok != "" {
				t.Errorf("Token() returned %q alongside an error; want an empty token", tok)
			}
			if !strings.Contains(err.Error(), tc.wantErrText) {
				t.Errorf("error %q does not mention %q", err, tc.wantErrText)
			}

			// A failed fetch must leave no token behind for a later call to serve
			// from cache — in memory...
			ts.mu.Lock()
			cached := ts.token
			ts.mu.Unlock()
			if cached != "" {
				t.Errorf("a failed fetch cached %q in memory", cached)
			}
			// ...or on disk.
			if entries, err := os.ReadDir(dir); err == nil && len(entries) != 0 {
				t.Errorf("a failed fetch wrote %d file(s) to the token cache dir", len(entries))
			}

			if got := fetchCount(); got != 1 {
				t.Errorf("first Token() made %d fetch attempts, want 1", got)
			}

			// And a second call must really go back to the endpoint, not replay
			// a cached failure: an implementation that memoised the first error
			// would never recover from a transient outage.
			if _, err := ts.Token(context.Background()); err == nil {
				t.Error("second Token() succeeded after a failed fetch")
			}
			if got := fetchCount(); got != 2 {
				t.Errorf("after two Token() calls the endpoint saw %d attempts, want 2; "+
					"a failure must not be cached", got)
			}
		})
	}
}

// TestTokenSourceMissingExpiresInUsesConservativeDefault pins the expiry
// boundary on the branch that had no test: an endpoint that omits expires_in
// silently selects the 60-second conservative default, so the token must be
// reused inside that window and refetched past it.
func TestTokenSourceMissingExpiresInUsesConservativeDefault(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"expires_in absent", `{"access_token":"tok-%d"}`},
		{"expires_in zero", `{"access_token":"tok-%d","expires_in":0}`},
		{"expires_in negative", `{"access_token":"tok-%d","expires_in":-1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n := atomic.AddInt32(&calls, 1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, strings.Replace(tc.body, "%d", string(rune('0'+n)), 1))
			}))
			defer srv.Close()

			start := time.Unix(1_000_000, 0)
			clk := &settableClock{t: start}
			ts := NewTokenSource(OAuthConfig{TokenURL: srv.URL, ClientID: "id", ClientSecret: "sec"}, clk)

			if _, err := ts.Token(context.Background()); err != nil {
				t.Fatalf("first Token: %v", err)
			}
			// 60s default, refreshed at 90% of the lifetime: still valid at 53s.
			clk.set(start.Add(53 * time.Second))
			if _, err := ts.Token(context.Background()); err != nil {
				t.Fatalf("second Token: %v", err)
			}
			if got := atomic.LoadInt32(&calls); got != 1 {
				t.Errorf("made %d fetches inside the 60s default window, want 1", got)
			}
			// Past 54s the token must be refetched.
			clk.set(start.Add(55 * time.Second))
			if _, err := ts.Token(context.Background()); err != nil {
				t.Fatalf("third Token: %v", err)
			}
			if got := atomic.LoadInt32(&calls); got != 2 {
				t.Errorf("made %d fetches after the 60s default window, want 2", got)
			}
		})
	}
}

// TestTransportFailsClosedOnTokenError is the case worth having most: it is the
// difference between a failed call and a silently unauthenticated one. When the
// token cannot be obtained, RoundTrip must return the error and must not pass the
// request to the next RoundTripper in the chain.
func TestTransportFailsClosedOnTokenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	next := &countingRoundTripper{}
	tr := &Transport{
		Base:     next,
		Strategy: OAuth,
		TokenSource: NewTokenSource(OAuthConfig{
			TokenURL: srv.URL, ClientID: "id", ClientSecret: "sec",
		}, &settableClock{t: time.Unix(1_000_000, 0)}),
	}

	req, err := http.NewRequest(http.MethodGet, "https://cluster.example/v2/topology", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := tr.RoundTrip(req)
	if err == nil {
		t.Fatal("RoundTrip succeeded despite a failing token endpoint")
	}
	if resp != nil {
		t.Errorf("RoundTrip returned a response alongside an error: %+v", resp)
	}
	if got := next.calls.Load(); got != 0 {
		t.Errorf("the request reached the next RoundTripper %d time(s); it must not go out unauthenticated", got)
	}
}

// TestTransportOAuthWithoutTokenSourceFails covers the misconfiguration branch:
// OAuth selected with no token source must fail rather than send the request
// with no Authorization header.
func TestTransportOAuthWithoutTokenSourceFails(t *testing.T) {
	next := &countingRoundTripper{}
	tr := &Transport{Base: next, Strategy: OAuth}

	req, err := http.NewRequest(http.MethodGet, "https://cluster.example/v2/topology", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("RoundTrip succeeded with no token source configured")
	}
	if got := next.calls.Load(); got != 0 {
		t.Errorf("the request reached the next RoundTripper %d time(s); it must not go out unauthenticated", got)
	}
}

// TestTokenSourceContextCancellation covers the cancellation baseline case on
// the auth path: a canceled context must surface as an error rather than a wait
// or an empty token.
func TestTokenSourceContextCancellation(t *testing.T) {
	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-released
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"tok","expires_in":3600}`)
	}))
	defer srv.Close()
	defer close(released)

	ts := NewTokenSource(OAuthConfig{
		TokenURL: srv.URL, ClientID: "id", ClientSecret: "sec",
	}, &settableClock{t: time.Unix(1_000_000, 0)})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tok, err := ts.Token(ctx)
	if err == nil {
		t.Fatalf("Token() = %q, nil; want a cancellation error", tok)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v does not wrap context.Canceled", err)
	}
}
