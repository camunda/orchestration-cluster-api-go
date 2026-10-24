package camunda_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	camunda "github.com/camunda/orchestration-cluster-api-go/v11"
	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
)

// TestOperationServersHonorRestAddress is the regression test for
// https://github.com/camunda/orchestration-cluster-api-go/issues/77: operations
// whose spec carries an operation-level server (the cluster-admin /cluster/v2/*
// operations) must resolve scheme/host/port from the configured REST address —
// not from the generator's template defaults (http://localhost:8080).
func TestOperationServersHonorRestAddress(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		// Minimal body satisfying the response model's required properties.
		_, _ = io.WriteString(w, `{"brokers":[],"clusterId":null,"clusterSize":0,"gatewayVersion":null,"physicalTenants":[]}`)
	}))
	defer srv.Close()

	client, err := camunda.New(camunda.WithRestAddress(srv.URL), camunda.WithLogLevel(camunda.LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := client.GetClusterTopology(context.Background()); err != nil {
		t.Fatalf("GetClusterTopology: %v", err)
	}

	// The operation-level server template is "{schema}://{host}:{port}" with no
	// path, so the request path is exactly the spec path — no /v2 prefix.
	if want := "/cluster/v2/topology"; gotPath != want {
		t.Errorf("request path = %q, want %q", gotPath, want)
	}
}

// TestOperationServersCoverEveryOperationServerEndpoint guards the whole defect
// class: every endpoint the generator registered in OperationServers (Backup /
// Cluster / Exporting / Recovery cluster-admin operations) must resolve to the
// configured REST origin when no context overrides are supplied, so a future
// spec change cannot silently reintroduce a template-default endpoint. The
// templates themselves are retained ("{schema}://{host}:{port}") so
// context-based server variables keep working; only their defaults are rebased.
func TestOperationServersCoverEveryOperationServerEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	client, err := camunda.New(camunda.WithRestAddress(srv.URL), camunda.WithLogLevel(camunda.LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rawCfg := client.Raw().GetConfig()
	if len(rawCfg.OperationServers) == 0 {
		t.Fatal("generated client registered no operation servers; test is vacuous")
	}
	for endpoint, servers := range rawCfg.OperationServers {
		for i := range servers {
			got, err := servers.URL(i, nil)
			if err != nil {
				t.Errorf("OperationServers[%q][%d]: %v", endpoint, i, err)
				continue
			}
			if got != srv.URL {
				t.Errorf("OperationServers[%q][%d] = %q, want configured REST origin %q", endpoint, i, got, srv.URL)
			}
		}
	}
}

// TestOperationServersContextServerVariablesStillApply is the regression test
// for the review finding on PR #80: rebasing operation servers onto the
// configured REST address must not disable ContextServerVariables. A caller
// that supplies schema/host/port via context must still override the rebased
// defaults at request time.
func TestOperationServersContextServerVariablesStillApply(t *testing.T) {
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"brokers":[],"clusterId":null,"clusterSize":0,"gatewayVersion":null,"physicalTenants":[]}`)
	}))
	defer srv.Close()

	// Configure the client against an origin that is NOT the test server; the
	// context variables below must win over it.
	client, err := camunda.New(camunda.WithRestAddress("http://configured-origin:1111"), camunda.WithLogLevel(camunda.LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	override, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	ctx := context.WithValue(context.Background(), camundaapi.ContextServerVariables, map[string]string{
		"schema": override.Scheme,
		"host":   override.Hostname(),
		"port":   override.Port(),
	})

	if _, err := client.GetClusterTopology(ctx); err != nil {
		t.Fatalf("GetClusterTopology: %v", err)
	}
	if gotHost != override.Host {
		t.Errorf("request host = %q, want context override %q", gotHost, override.Host)
	}
}

// TestOperationServersContextOperationServerVariablesStillApply covers the
// per-operation variant of the same defect class:
// ContextOperationServerVariables must override the rebased defaults for the
// named endpoint only.
func TestOperationServersContextOperationServerVariablesStillApply(t *testing.T) {
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"brokers":[],"clusterId":null,"clusterSize":0,"gatewayVersion":null,"physicalTenants":[]}`)
	}))
	defer srv.Close()

	client, err := camunda.New(camunda.WithRestAddress("http://configured-origin:1111"), camunda.WithLogLevel(camunda.LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	override, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	ctx := context.WithValue(context.Background(), camundaapi.ContextOperationServerVariables, map[string]map[string]string{
		"ClusterAPIService.GetClusterTopology": {
			"schema": override.Scheme,
			"host":   override.Hostname(),
			"port":   override.Port(),
		},
	})

	if _, err := client.GetClusterTopology(ctx); err != nil {
		t.Fatalf("GetClusterTopology: %v", err)
	}
	if gotHost != override.Host {
		t.Errorf("request host = %q, want operation context override %q", gotHost, override.Host)
	}
}

// TestRebaseOperationServersRejectsForeignTemplate pins the fail-closed
// contract: an operation-server entry whose URL template does not declare the
// expected {schema}/{host}/{port} variables is a configuration error, never
// silently left pointing at the spec defaults (localhost:8080).
func TestRebaseOperationServersRejectsForeignTemplate(t *testing.T) {
	cfg := camundaapi.NewConfiguration()
	cfg.OperationServers["ForeignAPIService.DoThings"] = camundaapi.ServerConfigurations{
		{URL: "https://hardcoded.example:9443"},
	}
	if _, err := camunda.RebaseOperationServersForTesting(cfg, "http://my-cluster:9999", false); err == nil {
		t.Fatal("expected an error for a template without variables, got nil")
	} else if !errors.Is(err, camunda.ErrConfig) {
		t.Errorf("error = %v, want it to wrap ErrConfig", err)
	}
}

// TestOperationServerOriginOmitsDefaultPort documents the origin shape that
// feeds the rebased variable defaults: an explicit :443 on an https address
// (or :80 on http) survives as-is, and the template default fills in the
// standard port when the address omits one.
func TestOperationServerOriginOmitsDefaultPort(t *testing.T) {
	got, err := camunda.OperationServerOrigin("https://cluster.example:443/v2", false)
	if err != nil {
		t.Fatalf("OperationServerOrigin: %v", err)
	}
	if want := "https://cluster.example:443"; got != want {
		t.Errorf("OperationServerOrigin = %q, want %q", got, want)
	}
}

// TestOperationServerOriginFromRestAddress covers origin derivation for the
// REST-address shapes the SDK accepts: a plain host:port, an address that
// already ends in /v2, and an exact (gateway-fronted) address whose base path
// must not leak into the operation-server origin.
func TestOperationServerOriginFromRestAddress(t *testing.T) {
	cases := []struct {
		name  string
		addr  string
		exact bool
		want  string
	}{
		{"plain", "http://my-cluster:9999", false, "http://my-cluster:9999"},
		{"v2 suffix", "https://cluster.example:443/v2", false, "https://cluster.example:443"},
		{"base path", "https://gw.example/camunda", false, "https://gw.example"},
		{"exact base path", "https://gw.example/camunda", true, "https://gw.example"},
		{"trailing slash", "http://my-cluster:9999/", false, "http://my-cluster:9999"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := camunda.OperationServerOrigin(tc.addr, tc.exact)
			if err != nil {
				t.Fatalf("OperationServerOrigin(%q, %v): %v", tc.addr, tc.exact, err)
			}
			if got != tc.want {
				t.Errorf("OperationServerOrigin(%q, %v) = %q, want %q", tc.addr, tc.exact, got, tc.want)
			}
		})
	}
}

// TestOperationServerOriginRejectsInvalidAddress pins the no-silent-failure
// contract: an address that is not a valid http(s) URL is an error, never a
// best-guess origin.
func TestOperationServerOriginRejectsInvalidAddress(t *testing.T) {
	for _, addr := range []string{"", "not-a-url", "ftp://cluster.example", "http://"} {
		if _, err := camunda.OperationServerOrigin(addr, false); err == nil {
			t.Errorf("OperationServerOrigin(%q, false): expected an error, got nil", addr)
		} else if !errors.Is(err, camunda.ErrConfig) {
			t.Errorf("OperationServerOrigin(%q, false) error = %v, want it to wrap ErrConfig", addr, err)
		}
	}
}
