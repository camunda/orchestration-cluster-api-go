package retry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// An HTTPS proxy that rejects CONNECT fails the request before it was sent.
func TestPreSendTracerMarksProxyConnectRejection(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	tr := &PreSendTracer{Base: &http.Transport{Proxy: http.ProxyURL(pu)}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://camunda.invalid/v2/jobs/1/completion", nil)
	_, err := tr.RoundTrip(req)
	if err == nil || !IsPreSendError(err) {
		t.Fatalf("want pre-send error, got %v", err)
	}
}

type errRT struct{}

func (errRT) RoundTrip(*http.Request) (*http.Response, error) { return nil, errors.New("reset") }

// An untraced custom transport stays conservative.
func TestPreSendTracerUntracedTransportNotPreSend(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://x/", nil)
	_, err := (&PreSendTracer{Base: errRT{}}).RoundTrip(req)
	if IsPreSendError(err) {
		t.Fatal("untraced error must not be pre-send")
	}
}
