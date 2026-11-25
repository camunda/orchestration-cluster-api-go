package camunda

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
	"github.com/camunda/orchestration-cluster-api-go/v11/internal/backpressure"
	"github.com/camunda/orchestration-cluster-api-go/v11/internal/transport"
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
		if got := v2BaseURL(in, false); got != want {
			t.Errorf("v2BaseURL(%q, false) = %q, want %q", in, got, want)
		}
	}
}

func TestV2BaseURLExact(t *testing.T) {
	cases := map[string]string{
		"http://localhost:8080":        "http://localhost:8080",
		"http://localhost:8080/":       "http://localhost:8080",
		"https://gw.example/camunda":   "https://gw.example/camunda",
		"https://gw.example/camunda/":  "https://gw.example/camunda",
		"https://gw.example/api/rest/": "https://gw.example/api/rest",
		"https://cluster.example/v2":   "https://cluster.example/v2",
	}
	for in, want := range cases {
		if got := v2BaseURL(in, true); got != want {
			t.Errorf("v2BaseURL(%q, true) = %q, want %q", in, got, want)
		}
	}
}

func TestExemptDrainOps(t *testing.T) {
	exempt := []string{
		"http://x/v2/jobs/123/completion",
		"http://x/v2/jobs/123/failure",
		"http://x/v2/jobs/123/error",
		"http://x/v2/user-tasks/456/completion",
		// A probe answer finishes work a worker already activated, and activation
		// delivers it. createProbe and an activation long poll hold permits while
		// they wait, so gating either could starve the other at one permit.
		"http://x/v2/probes/2251799813685249/answer",
		"http://x/v2/probes/activation",
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
		"http://x/v2/probes", // createProbe is the initiating call
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
	mapped := c.wrapError(&http.Response{StatusCode: 404}, &camundaapi.GenericOpenAPIError{})
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

// gateClock keeps the limiter off real time.
type gateClock struct{}

func (gateClock) Now() time.Time                                   { return time.Unix(1_000_000, 0) }
func (gateClock) Sleep(ctx context.Context, d time.Duration) error { return ctx.Err() }

// routedRT answers each request immediately, except those to a path in hold: those wait on
// the path's channel, and signal on entered once they have reached this transport. Reaching
// it means the backpressure gate in front of it has already granted the request its permit,
// so a test can wait on entered instead of guessing how long that takes.
type routedRT struct {
	hold    map[string]chan struct{}
	entered chan string
}

func (r routedRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if ch, ok := r.hold[req.URL.Path]; ok {
		r.entered <- req.URL.Path
		select {
		case <-ch:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header), Request: req}, nil
}

// At the limiter's one-permit floor, createProbe waits for a worker's answer while holding
// its permit, and an activation long poll holds one while it waits for a probe. Neither may
// be able to starve the other, or an answer, on the same client: a probe could then never
// complete, and createProbe would only end when its own request timed out.
func TestProbeOperationsDoNotBlockEachOtherAtOnePermit(t *testing.T) {
	cappedAtOne := func(t *testing.T) *backpressure.Manager {
		t.Helper()
		mgr := backpressure.New(backpressure.Balanced, gateClock{})
		for i := 0; i < 50; i++ {
			mgr.RecordBackpressure()
		}
		if st := mgr.State(); st.PermitsMax == nil || *st.PermitsMax != 1 {
			t.Fatalf("limiter not at its one-permit floor: %+v", st)
		}
		return mgr
	}
	send := func(rt http.RoundTripper, path string) <-chan error {
		done := make(chan error, 1)
		go func() {
			req, _ := http.NewRequest(http.MethodPost, "http://x/v2"+path, nil)
			resp, err := rt.RoundTrip(req)
			if err == nil {
				_ = resp.Body.Close()
			}
			done <- err
		}()
		return done
	}
	finishesWithin := func(t *testing.T, done <-chan error, what string) {
		t.Helper()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s failed: %v", what, err)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%s was blocked behind the single permit", what)
		}
	}

	for _, held := range []string{"/probes", "/probes/activation"} {
		t.Run("held "+held, func(t *testing.T) {
			release := make(chan struct{})
			entered := make(chan string, 1)
			rt := &transport.BackpressureTransport{
				Base:   routedRT{hold: map[string]chan struct{}{"/v2" + held: release}, entered: entered},
				Mgr:    cappedAtOne(t),
				Exempt: exemptDrainOps,
			}
			holder := send(rt, held)
			// Only once the held request is inside the transport does it hold the permit; sending
			// the others earlier could pass without the gate ever being under test.
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatalf("POST %s never reached the transport", held)
			}

			for _, other := range []string{"/probes", "/probes/activation", "/probes/2251799813685249/answer"} {
				if other == held {
					continue
				}
				finishesWithin(t, send(rt, other), "POST "+other)
			}
			close(release)
			finishesWithin(t, holder, "POST "+held)
		})
	}
}
