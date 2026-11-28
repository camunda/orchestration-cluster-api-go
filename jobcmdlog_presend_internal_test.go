package camunda

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/camunda/orchestration-cluster-api-go/v10/internal/auth"
	"github.com/camunda/orchestration-cluster-api-go/v10/internal/retry"
	"github.com/camunda/orchestration-cluster-api-go/v10/internal/transport"
)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Unix(1_700_000_000, 0) }

type zeroRand struct{}

func (zeroRand) Float64() float64 { return 0 }

type noSleepClock struct{}

func (noSleepClock) Sleep(context.Context, time.Duration) error { return nil }

var errTestHandshake = errors.New("simulated handshake failure")

// A job command whose OAuth token fetch is rejected was never sent: it is a
// definite failure, not an unknown outcome, and it is not sent to the server.
func TestJobCommandTokenFetchRejectionIsFailed(t *testing.T) {
	var commands atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		commands.Add(1)
	}))
	defer api.Close()
	for name, handler := range map[string]http.HandlerFunc{
		"401":       func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
		"malformed": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{not json")) },
	} {
		t.Run(name, func(t *testing.T) {
			idp := httptest.NewServer(handler)
			defer idp.Close()
			ts := auth.NewTokenSource(auth.OAuthConfig{TokenURL: idp.URL, ClientID: "c", ClientSecret: "s"}, fixedClock{})
			rt := &auth.Transport{Strategy: auth.OAuth, TokenSource: ts}
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, api.URL+"/v2/jobs/1/completion", strings.NewReader("{}"))
			_, err := rt.RoundTrip(req)
			if err == nil {
				t.Fatal("expected token error")
			}
			if got := classifyRESTJobCommandErr(err); got != jobCommandFailed {
				t.Fatalf("got %d, want jobCommandFailed (%v)", got, err)
			}
		})
	}
	if commands.Load() != 0 {
		t.Fatalf("job command reached the server %d times", commands.Load())
	}
}

// A failed TLS handshake proves the command was not sent: a strict job command
// is retried and succeeds on the next attempt.
func TestJobCommandRetriesFailedTLSHandshake(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	var dials atomic.Int32
	good := srv.Client().Transport.(*http.Transport).TLSClientConfig
	base := &http.Transport{DisableKeepAlives: true}
	base.TLSClientConfig = good.Clone()
	base.TLSClientConfig.VerifyConnection = func(tls.ConnectionState) error {
		if dials.Add(1) == 1 {
			return errTestHandshake
		}
		return nil
	}
	rt := transport.New(transport.Options{
		Base:          base,
		Retry:         retry.Config{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
		NonIdempotent: func(*http.Request) bool { return true },
	}, noSleepClock{}, zeroRand{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v2/jobs/1/completion", strings.NewReader("{}"))
	req.GetBody = func() (r io.ReadCloser, err error) { return io.NopCloser(strings.NewReader("{}")), nil }
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	_ = resp.Body.Close()
	if dials.Load() != 2 || calls.Load() != 1 {
		t.Fatalf("handshakes=%d calls=%d, want 2 and 1", dials.Load(), calls.Load())
	}
}
