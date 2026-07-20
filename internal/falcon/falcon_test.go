package falcon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDetectStockCamundaNotDetected(t *testing.T) {
	// Stock Camunda topology carries no "nano" field.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"brokers":[{"host":"localhost","port":26500}],"gatewayVersion":"8.10.0"}`))
	}))
	defer srv.Close()

	caps, ok := Detect(context.Background(), srv.URL, srv.Client())
	if ok || caps != nil {
		t.Fatalf("stock Camunda must not be detected as nanobpmn; got ok=%v caps=%v", ok, caps)
	}
}

func TestDetectNanobpmnProbesTopologyAndBuildsEndpoints(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{
			"nano": {"falconPath": "/falcon"},
			"brokers": [
				{"host": "0.0.0.0", "port": 26500},
				{"host": "broker-2", "port": 26501}
			]
		}`))
	}))
	defer srv.Close()

	caps, ok := Detect(context.Background(), srv.URL, srv.Client())
	if !ok || caps == nil {
		t.Fatalf("nanobpmn gateway must be detected; got ok=%v", ok)
	}
	if gotPath != "/topology" {
		t.Errorf("Detect probed %q, want /topology", gotPath)
	}
	// Configured address + the 0.0.0.0 self-placeholder (host replaced) +
	// broker-2. All plaintext ws:// (httptest is http), all under /falcon.
	if len(caps.Endpoints) != 3 {
		t.Fatalf("expected 3 endpoints, got %d: %v", len(caps.Endpoints), caps.Endpoints)
	}
	for _, ep := range caps.Endpoints {
		if !strings.HasPrefix(ep, "ws://") {
			t.Errorf("endpoint %q should use ws:// against an http gateway", ep)
		}
		if !strings.HasSuffix(ep, "/falcon") {
			t.Errorf("endpoint %q should end with the command-stream path", ep)
		}
		if strings.Contains(ep, "0.0.0.0") {
			t.Errorf("endpoint %q must not contain the 0.0.0.0 self placeholder", ep)
		}
	}
	if !containsSuffix(caps.Endpoints, "broker-2:26501/falcon") {
		t.Errorf("expected a broker-2 endpoint; got %v", caps.Endpoints)
	}
}

func TestDetectDefaultsCommandStreamPath(t *testing.T) {
	// "nano" present but without falconPath => DefaultCommandStreamPath.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"nano":{},"brokers":[]}`))
	}))
	defer srv.Close()

	caps, ok := Detect(context.Background(), srv.URL, srv.Client())
	if !ok || caps == nil {
		t.Fatalf("nanobpmn gateway must be detected; got ok=%v", ok)
	}
	if len(caps.Endpoints) != 1 || !strings.HasSuffix(caps.Endpoints[0], DefaultCommandStreamPath) {
		t.Fatalf("expected a single endpoint on the default path %q, got %v", DefaultCommandStreamPath, caps.Endpoints)
	}
}

func TestDetectNon2xxNotDetected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if _, ok := Detect(context.Background(), srv.URL, srv.Client()); ok {
		t.Fatal("a non-2xx topology response must not be detected as nanobpmn")
	}
}

func TestDetectMalformedBodyNotDetected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	if _, ok := Detect(context.Background(), srv.URL, srv.Client()); ok {
		t.Fatal("a malformed topology body must not be detected as nanobpmn")
	}
}

func TestDetectUnreachableNotDetected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // now unreachable

	if _, ok := Detect(context.Background(), url, http.DefaultClient); ok {
		t.Fatal("an unreachable gateway must not be detected as nanobpmn")
	}
}

func TestEndpointsFromTopologyDerivesWSSForHTTPS(t *testing.T) {
	body := topology{}
	body.Brokers = []struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}{{Host: "node-a", Port: 26500}}

	eps := endpointsFromTopology("https://cluster.example.com:8080/v2", "/falcon", body)
	if len(eps) != 2 {
		t.Fatalf("expected 2 endpoints, got %v", eps)
	}
	for _, ep := range eps {
		if !strings.HasPrefix(ep, "wss://") {
			t.Errorf("https cluster must yield wss:// endpoints, got %q", ep)
		}
	}
}

func TestEndpointsFromTopologyDedupesAndSkipsZeroPort(t *testing.T) {
	body := topology{}
	body.Brokers = []struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}{
		{Host: "localhost", Port: 8080}, // duplicates the configured address
		{Host: "node-b", Port: 0},       // no port => skipped
	}

	eps := endpointsFromTopology("http://localhost:8080/v2", "/falcon", body)
	if len(eps) != 1 {
		t.Fatalf("expected duplicate collapsed and zero-port skipped, got %v", eps)
	}
	if eps[0] != "ws://localhost:8080/falcon" {
		t.Errorf("unexpected endpoint %q", eps[0])
	}
}

func containsSuffix(ss []string, suffix string) bool {
	for _, s := range ss {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}
