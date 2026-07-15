package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func tokenServer(t *testing.T, counter *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(counter, 1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "client_credentials" {
			t.Errorf("grant_type = %q, want client_credentials", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600}`, atomic.LoadInt32(counter))
	}))
}

func TestTokenSourceCachesInMemory(t *testing.T) {
	var calls int32
	srv := tokenServer(t, &calls)
	defer srv.Close()

	ts := NewTokenSource(OAuthConfig{TokenURL: srv.URL, ClientID: "id", ClientSecret: "sec"})
	for i := 0; i < 3; i++ {
		tok, err := ts.Token(context.Background())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if tok != "tok-1" {
			t.Errorf("expected cached tok-1, got %q", tok)
		}
	}
	if calls != 1 {
		t.Errorf("expected a single token fetch, got %d", calls)
	}
}

func TestTokenSourceRefreshesAfterExpiry(t *testing.T) {
	var calls int32
	srv := tokenServer(t, &calls)
	defer srv.Close()

	current := time.Unix(1_000_000, 0)
	ts := NewTokenSource(OAuthConfig{TokenURL: srv.URL, ClientID: "id", ClientSecret: "sec"})
	ts.cfg.now = func() time.Time { return current }

	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatalf("first Token: %v", err)
	}
	// Advance beyond the refresh window (3600s * 0.9 = 3240s).
	current = current.Add(4000 * time.Second)
	tok, err := ts.Token(context.Background())
	if err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if tok != "tok-2" {
		t.Errorf("expected refreshed tok-2, got %q", tok)
	}
	if calls != 2 {
		t.Errorf("expected 2 fetches across expiry, got %d", calls)
	}
}

func TestTokenSourceDiskCacheSharedAcrossInstances(t *testing.T) {
	var calls int32
	srv := tokenServer(t, &calls)
	defer srv.Close()
	dir := t.TempDir()

	first := NewTokenSource(OAuthConfig{TokenURL: srv.URL, ClientID: "id", ClientSecret: "sec", CacheDir: dir})
	if _, err := first.Token(context.Background()); err != nil {
		t.Fatalf("first instance Token: %v", err)
	}
	// A fresh instance (simulating a process restart) should reuse the disk token.
	second := NewTokenSource(OAuthConfig{TokenURL: srv.URL, ClientID: "id", ClientSecret: "sec", CacheDir: dir})
	tok, err := second.Token(context.Background())
	if err != nil {
		t.Fatalf("second instance Token: %v", err)
	}
	if tok != "tok-1" {
		t.Errorf("expected disk-cached tok-1, got %q", tok)
	}
	if calls != 1 {
		t.Errorf("expected disk cache to avoid a second fetch, got %d fetches", calls)
	}
}

// captureRT records the request it saw and returns 200.
type captureRT struct{ last *http.Request }

func (c *captureRT) RoundTrip(r *http.Request) (*http.Response, error) {
	c.last = r
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header), Request: r}, nil
}

func TestTransportBasicAndNone(t *testing.T) {
	cap := &captureRT{}
	tr := &Transport{Base: cap, Strategy: Basic, BasicUsername: "u", BasicPassword: "p"}
	req, _ := http.NewRequest(http.MethodGet, "http://example/v2/topology", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if u, p, ok := cap.last.BasicAuth(); !ok || u != "u" || p != "p" {
		t.Errorf("expected basic auth u/p, got ok=%v u=%q p=%q", ok, u, p)
	}
	// The original request must not have been mutated.
	if req.Header.Get("Authorization") != "" {
		t.Error("original request was mutated with an Authorization header")
	}

	capNone := &captureRT{}
	trNone := &Transport{Base: capNone, Strategy: None}
	req2, _ := http.NewRequest(http.MethodGet, "http://example/v2/topology", nil)
	if _, err := trNone.RoundTrip(req2); err != nil {
		t.Fatal(err)
	}
	if capNone.last.Header.Get("Authorization") != "" {
		t.Error("None strategy should not set an Authorization header")
	}
}

func TestTransportOAuthBearer(t *testing.T) {
	var calls int32
	srv := tokenServer(t, &calls)
	defer srv.Close()
	ts := NewTokenSource(OAuthConfig{TokenURL: srv.URL, ClientID: "id", ClientSecret: "sec"})

	cap := &captureRT{}
	tr := &Transport{Base: cap, Strategy: OAuth, TokenSource: ts}
	req, _ := http.NewRequest(http.MethodGet, "http://example/v2/topology", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got := cap.last.Header.Get("Authorization"); got != "Bearer tok-1" {
		t.Errorf("expected bearer token header, got %q", got)
	}
}
